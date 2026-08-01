package goql

import (
	"fmt"
	"reflect"

	"github.com/aekis-dev/goql/models"
	"github.com/aekis-dev/goql/query"
)

// preloadRelations loads the named relations for a set of just-scanned rows.
//
// Each relation costs a fixed number of batched queries regardless of how many rows came
// back, so results never fan out into a query per row. rows holds entity pointers as
// returned by scanRows.
func (ctx *Engine) preloadRelations(rows []any, schema *models.Model, relations []string) error {
	if len(rows) == 0 || len(relations) == 0 {
		return nil
	}

	for _, name := range relations {
		field, ok := schema.Fields[name]
		if !ok {
			return fmt.Errorf("%w: %s on %s", models.ErrFieldNotFound, name, schema.TableName)
		}
		if !field.IsRelation() {
			return fmt.Errorf("%w: %s is not a relation, so it cannot be preloaded",
				ErrInvalidOption, name)
		}

		var err error
		switch field.RelationKind() {
		case models.M2O:
			err = ctx.preloadM2O(rows, field)
		case models.O2M:
			err = ctx.preloadO2M(rows, schema, field)
		case models.M2M:
			err = ctx.preloadM2M(rows, schema, field)
		}
		if err != nil {
			return fmt.Errorf("preload %s: %w", name, err)
		}
	}

	return nil
}

// preloadM2O loads the parents referenced by a foreign key: one query for every distinct
// key across the rows, then each row is pointed at its parent.
func (ctx *Engine) preloadM2O(rows []any, field *models.Field) error {
	targetSchema, err := query.RelationTargetSchema(field)
	if err != nil {
		return err
	}

	parentKeys, byRow := gatherPrimaryKeys(rows)
	if len(parentKeys) == 0 {
		return nil
	}

	// Read the foreign keys back from the table: the scanned entity deliberately leaves
	// the relation field nil, so there is nothing to read them from.
	sourceSchema := field.TableSchema
	pk := ctx.dialect.QuoteIdent(sourceSchema.PrimaryKey.ColumnName())
	pq := ctx.dialect.SelectKeyPairsIn(sourceSchema, ctx.dialect.QuoteIdent(field.GetFKColumn()), pk, len(parentKeys))
	pairs, err := ctx.query(pq.SQL, parentKeys...)
	if err != nil {
		return err
	}

	fkByParent := make(map[any]any, len(rows))
	var targetKeys []any
	seen := make(map[any]bool)

	for pairs.Next() {
		var parentKey, fk any
		if err := pairs.Scan(&parentKey, &fk); err != nil {
			pairs.Close()
			return err
		}
		if fk == nil {
			continue
		}
		pk, target := normalizeKey(parentKey), normalizeKey(fk)
		fkByParent[pk] = target
		if !seen[target] {
			seen[target] = true
			targetKeys = append(targetKeys, target)
		}
	}
	pairs.Close()
	if err := pairs.Err(); err != nil {
		return err
	}
	if len(targetKeys) == 0 {
		return nil
	}

	loaded, err := ctx.loadByColumn(targetSchema, ctx.dialect.QuoteIdent(targetSchema.PrimaryKey.ColumnName()), targetKeys)
	if err != nil {
		return err
	}
	index := indexByPrimaryKey(loaded)

	for i, row := range rows {
		parentKey, ok := byRow[i]
		if !ok {
			continue
		}
		related, ok := index[fkByParent[parentKey]]
		if !ok {
			continue
		}
		if err := assignRelated(row, field.Name, []any{related}, false); err != nil {
			return err
		}
	}
	return nil
}

// preloadO2M loads the children pointing back at each row through a foreign key: one
// query for all parent keys, then the children are grouped by that key.
func (ctx *Engine) preloadO2M(rows []any, schema *models.Model, field *models.Field) error {
	targetSchema, err := query.RelationTargetSchema(field)
	if err != nil {
		return err
	}

	parentKeys, byRow := gatherPrimaryKeys(rows)
	if len(parentKeys) == 0 {
		return nil
	}

	fkColumn := ctx.dialect.QuoteIdent(field.OneToMany.Ref)

	// Map child key → parent key first, since the child's own relation field is nil.
	pairs, err := ctx.query(
		ctx.dialect.SelectKeyPairsIn(targetSchema, fkColumn, fkColumn, len(parentKeys)).SQL,
		parentKeys...)
	if err != nil {
		return err
	}

	parentOfChild := make(map[any]any)
	var childKeys []any
	for pairs.Next() {
		var childKey, parentKey any
		if err := pairs.Scan(&childKey, &parentKey); err != nil {
			pairs.Close()
			return err
		}
		if parentKey == nil {
			continue
		}
		ck := normalizeKey(childKey)
		parentOfChild[ck] = normalizeKey(parentKey)
		childKeys = append(childKeys, ck)
	}
	pairs.Close()
	if err := pairs.Err(); err != nil {
		return err
	}
	if len(childKeys) == 0 {
		return nil
	}

	children, err := ctx.loadByColumn(targetSchema, ctx.dialect.QuoteIdent(targetSchema.PrimaryKey.ColumnName()), childKeys)
	if err != nil {
		return err
	}

	grouped := make(map[any][]any, len(rows))
	for _, child := range children {
		entity, ok := child.(models.Entity)
		if !ok {
			continue
		}
		_, childKey := entity.PrimaryKey()
		parentKey, ok := parentOfChild[normalizeKey(childKey)]
		if !ok {
			continue
		}
		grouped[parentKey] = append(grouped[parentKey], child)
	}

	for i, row := range rows {
		key, ok := byRow[i]
		if !ok {
			continue
		}
		if err := assignRelated(row, field.Name, grouped[normalizeKey(key)], true); err != nil {
			return err
		}
	}
	return nil
}

// preloadM2M loads a many2many relation with two batched queries: the join rows for every
// parent key, then the targets for every distinct related key.
func (ctx *Engine) preloadM2M(rows []any, schema *models.Model, field *models.Field) error {
	targetSchema, err := query.RelationTargetSchema(field)
	if err != nil {
		return err
	}

	parentKeys, byRow := gatherPrimaryKeys(rows)
	if len(parentKeys) == 0 {
		return nil
	}

	m := field.ManyToMany
	jq := ctx.dialect.JoinRowsIn(m, len(parentKeys))
	joinRows, err := ctx.query(jq.SQL, parentKeys...)
	if err != nil {
		return err
	}

	// parent key → related keys, and the distinct set of related keys to fetch.
	links := make(map[any][]any)
	var targetKeys []any
	seen := make(map[any]bool)

	for joinRows.Next() {
		var parentKey, relatedKey any
		if err := joinRows.Scan(&parentKey, &relatedKey); err != nil {
			joinRows.Close()
			return err
		}
		pk, rk := normalizeKey(parentKey), normalizeKey(relatedKey)
		links[pk] = append(links[pk], rk)
		if !seen[rk] {
			seen[rk] = true
			targetKeys = append(targetKeys, rk)
		}
	}
	joinRows.Close()
	if err := joinRows.Err(); err != nil {
		return err
	}
	if len(targetKeys) == 0 {
		return nil
	}

	loaded, err := ctx.loadByColumn(targetSchema, ctx.dialect.QuoteIdent(targetSchema.PrimaryKey.ColumnName()), targetKeys)
	if err != nil {
		return err
	}
	index := indexByPrimaryKey(loaded)

	for i, row := range rows {
		key, ok := byRow[i]
		if !ok {
			continue
		}
		var related []any
		for _, rk := range links[normalizeKey(key)] {
			if target, ok := index[rk]; ok {
				related = append(related, target)
			}
		}
		if err := assignRelated(row, field.Name, related, true); err != nil {
			return err
		}
	}
	return nil
}

// loadByColumn runs one batched SELECT for the given key set.
func (ctx *Engine) loadByColumn(schema *models.Model, column string, keys []any) ([]any, error) {
	q := ctx.dialect.SelectWhereIn(schema, column, len(keys))
	rows, err := ctx.query(q.SQL, keys...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows, schema.Type)
}

// gatherPrimaryKeys collects the distinct primary keys of rows, plus each row's own key.
func gatherPrimaryKeys(rows []any) ([]any, map[int]any) {
	var keys []any
	seen := make(map[any]bool)
	byRow := make(map[int]any, len(rows))

	for i, row := range rows {
		entity, ok := row.(models.Entity)
		if !ok {
			continue
		}
		_, value := entity.PrimaryKey()
		if value == nil {
			continue
		}
		key := normalizeKey(value)
		byRow[i] = key
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return keys, byRow
}

// gatherColumnValues collects the distinct non-zero values of one column across rows.
func gatherColumnValues(rows []any, column string) ([]any, map[int]any) {
	var keys []any
	seen := make(map[any]bool)
	byRow := make(map[int]any, len(rows))

	for i, row := range rows {
		value, ok := readColumnValue(row, column)
		if !ok {
			continue
		}
		key := normalizeKey(value)
		byRow[i] = key
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return keys, byRow
}

// readColumnValue reads the Go field backing a database column, skipping zero values so a
// missing foreign key does not become a lookup for key 0.
func readColumnValue(row any, column string) (any, bool) {
	entity, ok := row.(models.Entity)
	if !ok {
		return nil, false
	}
	schema, err := models.GetModel(entity)
	if err != nil {
		return nil, false
	}
	field, ok := schema.FieldsByDB[column]
	if !ok {
		return nil, false
	}

	v := reflect.ValueOf(row)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	fieldValue, found := getFieldValue(v, field.Name)
	if !found || !fieldValue.IsValid() {
		return nil, false
	}

	// A many2one field holds the related entity, not the raw key.
	if field.RelationKind() == models.M2O {
		if fieldValue.Kind() == reflect.Ptr && !fieldValue.IsNil() {
			if related, ok := fieldValue.Interface().(models.Entity); ok {
				_, key := related.PrimaryKey()
				return key, key != nil
			}
		}
		return nil, false
	}

	if isZeroValue(fieldValue) {
		return nil, false
	}
	return fieldValue.Interface(), true
}

// indexByPrimaryKey indexes loaded entities by their primary key.
func indexByPrimaryKey(loaded []any) map[any]any {
	index := make(map[any]any, len(loaded))
	for _, item := range loaded {
		entity, ok := item.(models.Entity)
		if !ok {
			continue
		}
		_, key := entity.PrimaryKey()
		if key == nil {
			continue
		}
		index[normalizeKey(key)] = item
	}
	return index
}

// normalizeKey makes keys comparable across drivers, which may report the same integer
// key as int64, int32 or int depending on the column and platform.
func normalizeKey(key any) any {
	switch v := key.(type) {
	case int:
		return int64(v)
	case int8:
		return int64(v)
	case int16:
		return int64(v)
	case int32:
		return int64(v)
	case int64:
		return v
	case uint:
		return int64(v)
	case uint8:
		return int64(v)
	case uint16:
		return int64(v)
	case uint32:
		return int64(v)
	case uint64:
		return int64(v)
	case []byte:
		return string(v)
	default:
		return key
	}
}

// assignRelated writes loaded entities into a relation field, matching the field's own
// shape: a pointer for a single relation, a slice (of values or pointers) for a collection.
func assignRelated(row any, fieldName string, related []any, collection bool) error {
	v := reflect.ValueOf(row)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	target, found := getFieldValue(v, fieldName)
	if !found || !target.CanSet() {
		return fmt.Errorf("cannot set field %s", fieldName)
	}

	if !collection {
		if len(related) == 0 {
			return nil
		}
		value := reflect.ValueOf(related[0])
		switch {
		case target.Kind() == reflect.Ptr && value.Type().AssignableTo(target.Type()):
			target.Set(value)
		case value.Kind() == reflect.Ptr && value.Elem().Type().AssignableTo(target.Type()):
			target.Set(value.Elem())
		default:
			return fmt.Errorf("cannot assign %s to field %s of type %s",
				value.Type(), fieldName, target.Type())
		}
		return nil
	}

	if target.Kind() != reflect.Slice {
		return fmt.Errorf("field %s is not a slice", fieldName)
	}
	elemType := target.Type().Elem()
	slice := reflect.MakeSlice(target.Type(), 0, len(related))

	for _, item := range related {
		value := reflect.ValueOf(item)
		switch {
		case value.Type().AssignableTo(elemType):
			slice = reflect.Append(slice, value)
		case value.Kind() == reflect.Ptr && value.Elem().Type().AssignableTo(elemType):
			slice = reflect.Append(slice, value.Elem())
		default:
			return fmt.Errorf("cannot append %s to field %s of type %s",
				value.Type(), fieldName, target.Type())
		}
	}

	target.Set(slice)
	return nil
}

// effectivePreload decides which relations to load for a query.
//
// The model's `Preload: true` fields are the default. A query that names any relations
// replaces those defaults outright, so passing an empty Preload is how a caller asks for
// none.
func effectivePreload(opts *query.Options, schema *models.Model) []string {
	if opts != nil && opts.PreloadSet {
		return opts.Preload
	}
	return schema.PreloadFields()
}

// syncO2M points the listed rows at the parent and clears the foreign key of any row that
// previously pointed at it but is no longer listed.
//
// Only adding links left stale ones behind: a row dropped from the slice kept pointing at
// the parent. many2many already diffed in both directions; this brings one2many in line.
func (ctx *Engine) syncO2M(targetSchema *models.Model, fkColumn string, parentPK any, relatedPKs []any) error {
	for _, relatedPK := range relatedPKs {
		q := ctx.dialect.O2MUpdate(targetSchema, fkColumn)
		if _, err := ctx.exec(q.SQL, parentPK, relatedPK); err != nil {
			return err
		}
	}

	args := append([]any{parentPK}, relatedPKs...)

	// A NOT NULL foreign key cannot be cleared, so report the conflict rather than
	// leaving a stale link or letting the driver fail with a constraint violation.
	if field, ok := targetSchema.FieldsByDB[fkColumn]; ok && field.NotNull {
		sq := ctx.dialect.O2MStale(targetSchema, fkColumn, len(relatedPKs))
		rows, err := ctx.query(sq.SQL, args...)
		if err != nil {
			return err
		}
		stale := rows.Next()
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if stale {
			return fmt.Errorf(
				"%w: %s.%s is NOT NULL, so rows dropped from this relation cannot be "+
					"disassociated — reassign or delete them explicitly",
				ErrRelationConstraint, targetSchema.TableName, fkColumn)
		}
		return nil
	}

	dq := ctx.dialect.O2MDisassociate(targetSchema, fkColumn, len(relatedPKs))
	_, err := ctx.exec(dq.SQL, args...)
	return err
}
