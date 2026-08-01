package query

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/aekis-dev/goql/models"
)

// LambdaSearch builds a SELECT query from a parsed lambda predicate.
// opts may be nil when no projection, ordering or pagination is requested.
func (d *Dialect) LambdaSearch(q *ParseQuery, opts *Options) (*Query, error) {
	// A set operation is the query: its branches carry the models, so there is no schema of
	// its own to resolve.
	if q.Body != nil && q.Body.Set != nil {
		return d.lambdaSetSearch(q, opts)
	}

	return d.lambdaSearchIn(q, opts, d.newStmt())
}

// lambdaSearchIn builds a SELECT within an existing statement, so a set operation can render
// its branches into one placeholder sequence.
func (d *Dialect) lambdaSearchIn(q *ParseQuery, opts *Options, s *stmt) (*Query, error) {
	body := q.Body

	// A query may read from a common table expression rather than a table, in which case its
	// "schema" is that query's projection.
	schema, err := q.SchemaIn(body.With)
	if err != nil {
		return nil, err
	}

	// Definitions are rendered before the statement that reads them, so their placeholders
	// come first.
	with, withArgs, err := d.withClause(body, s)
	if err != nil {
		return nil, err
	}

	// Claim the primary table's alias first so it gets the preferred short form.
	alias := s.alias.Alias(schema.TableName)

	selectList, err := d.selectList(opts, schema, alias)
	if err != nil {
		return nil, err
	}
	// A projection replaces the model's columns entirely: it names what comes back, and
	// aliases each column so scanning does not depend on order.
	args := withArgs
	if body.Projection() {
		var selectArgs []any
		selectList, selectArgs, err = d.projectionList(body, schema, alias, s)
		if err != nil {
			return nil, err
		}
		// A computed column can bind a value, and the SELECT list is emitted first.
		args = append(args, selectArgs...)
	}

	from := s.fromList(body, schema)
	if q.From != "" && !d.SupportsCTE() {
		// No WITH on this engine: the definition is inlined where it is read.
		derived, derivedArgs, err := d.derivedTable(body, q.From, s)
		if err != nil {
			return nil, err
		}
		from, args = derived, append(args, derivedArgs...)
	}

	sql := with + fmt.Sprintf("SELECT %s FROM %s", selectList, from)

	// Explicit joins are rendered before anything derived from the predicate, so their bound
	// values are appended in the order their placeholders appear.
	explicit, explicitArgs, err := d.explicitJoins(body.Options, s)
	if err != nil {
		return nil, err
	}
	if explicit != "" {
		sql += " " + explicit
		args = append(args, explicitArgs...)
	}

	// Every selecting branch contributes; nil means unconditional. A condition over an
	// aggregate filters groups rather than rows, so it is split out into HAVING.
	var having *ParseNode
	if cond := body.SelectCondition(); cond != nil {
		rowCond, groupCond, err := SplitHaving(cond)
		if err != nil {
			return nil, err
		}
		having = groupCond

		joins, err := d.joinClauses(cond, s)
		if err != nil {
			return nil, err
		}
		if len(joins) > 0 {
			sql += " " + strings.Join(joins, " ")
		}
		if rowCond != nil {
			where, whereArgs, err := d.whereClause(rowCond, s)
			if err != nil {
				return nil, err
			}
			sql += " WHERE " + where
			args = append(args, whereArgs...)
		}
	}

	// Grouping follows the WHERE clause and precedes ordering.
	groupTerms, groupArgs, err := d.groupTerms(body, schema, alias, s)
	if err != nil {
		return nil, err
	}
	if len(groupTerms) > 0 {
		args = append(args, groupArgs...)
		sql += " GROUP BY " + strings.Join(groupTerms, ", ")
	}

	if having != nil {
		clause, havingArgs, err := d.whereClause(having, s)
		if err != nil {
			return nil, err
		}
		sql += " HAVING " + clause
		args = append(args, havingArgs...)
	}

	// Rendered last so its placeholders follow the WHERE clause's.
	tail, tailArgs, err := d.optionsTail(opts, schema, alias, s)
	if err != nil {
		return nil, err
	}

	return &Query{SQL: sql + tail, Args: append(args, tailArgs...)}, nil
}

// lambdaSetSearch renders UNION and its relatives.
//
// Each branch is an independent statement and gets its own aliases — otherwise a union of two
// queries over the same table would be refused, since the second would find the table already
// aliased. The placeholder counter is shared, because Postgres numbers parameters across the
// whole statement.
func (d *Dialect) lambdaSetSearch(q *ParseQuery, opts *Options) (*Query, error) {
	return d.lambdaSetSearchIn(q, opts, d.newStmt())
}

// lambdaSetSearchIn renders a set operation within an existing statement, so one nested in a
// CTE keeps the enclosing placeholder sequence.
func (d *Dialect) lambdaSetSearchIn(q *ParseQuery, opts *Options, s *stmt) (*Query, error) {
	set := q.Body.Set

	parts := make([]string, 0, len(set.Branches))
	var args []any

	for i, branch := range set.Branches {
		s.alias = NewAliasMap(d)
		sub, err := d.lambdaSearchIn(branch, branch.Body.Options, s)
		if err != nil {
			return nil, fmt.Errorf("%s branch %d: %w", set.Op, i+1, err)
		}
		parts = append(parts, sub.SQL)
		args = append(args, sub.Args...)
	}

	sql := strings.Join(parts, " "+set.Op+" ")

	// Ordering and pagination apply to the combined result, and name the projected columns
	// rather than a model's fields — there is no single model to resolve against.
	tail, tailArgs, err := d.setOptionsTail(set, opts, s)
	if err != nil {
		return nil, err
	}

	return &Query{SQL: sql + tail, Args: append(args, tailArgs...)}, nil
}

// setOptionsTail renders ORDER BY / LIMIT / OFFSET for a set operation, checking that a sort
// names a column the branches actually produce.
func (d *Dialect) setOptionsTail(set *ParseSet, opts *Options, s *stmt) (string, []any, error) {
	if opts == nil {
		return "", nil, nil
	}

	var sql strings.Builder
	var args []any

	if len(opts.Sorts) > 0 {
		produced := map[string]bool{}
		for _, sel := range set.Branches[0].Body.Select {
			produced[sel.Into] = true
		}

		terms := make([]string, 0, len(opts.Sorts))
		for _, sort := range opts.Sorts {
			if !produced[sort.By] {
				return "", nil, fmt.Errorf(
					"cannot order a %s by %s: the branches do not select it", set.Op, sort.By)
			}
			term := d.QuoteIdent(sort.By)
			if sort.Desc {
				term += " DESC"
			}
			terms = append(terms, term)
		}
		sql.WriteString(" ORDER BY " + strings.Join(terms, ", "))
	}

	if opts.Limit != nil {
		sql.WriteString(" LIMIT " + s.mark())
		args = append(args, *opts.Limit)
	}
	if opts.Offset != nil {
		if opts.Limit == nil {
			sql.WriteString(" " + d.OpenEndedLimit())
		}
		sql.WriteString(" OFFSET " + s.mark())
		args = append(args, *opts.Offset)
	}

	return sql.String(), args, nil
}

// groupTerms renders the GROUP BY, which is additive: the keys the lambda named explicitly,
// then every plain projected column that is not already among them. Projecting a column
// always groups by it, so a projection cannot be invalid on Postgres while quietly returning
// an arbitrary row's value on SQLite.
func (d *Dialect) groupTerms(body *ParseBody, schema *models.Model, alias string, s *stmt) ([]string, []any, error) {
	var terms []string
	var args []any
	seen := map[string]bool{}

	for _, name := range body.Options.groupKeys() {
		col, err := d.columnFor(schema, alias, name)
		if err != nil {
			return nil, nil, fmt.Errorf("group by: %w", err)
		}
		if !seen[col] {
			seen[col] = true
			terms = append(terms, col)
		}
	}

	// Explicit keys alone do not group anything unless something is aggregated.
	if len(terms) > 0 && !body.Aggregated() {
		return nil, nil, fmt.Errorf("group by names %d key(s) but the query aggregates nothing",
			len(terms))
	}

	for _, group := range body.GroupBy() {
		// A projected column may be computed, in which case the group term is the same
		// expression — SQL groups by the expression, not by an alias.
		col, groupArgs, err := d.selectExpr(group, "", s)
		if err != nil {
			return nil, nil, err
		}
		if !seen[col] {
			seen[col] = true
			terms = append(terms, col)
			args = append(args, groupArgs...)
		}
	}
	return terms, args, nil
}

// projectionList renders an explicit projection, aliasing every column to the result field
// it lands in.
func (d *Dialect) projectionList(body *ParseBody, schema *models.Model, alias string, s *stmt) (string, []any, error) {
	// A join multiplies rows, so counting them would count one entity several times. This is
	// the same rule the dedicated Count builder applied before projections existed.
	distinct := ""
	if joins := CollectJoins(orEmpty(body.SelectCondition()), map[string]bool{}); len(joins) > 0 || len(body.Joined) > 0 {
		if schema.PrimaryKey != nil {
			distinct = fmt.Sprintf("DISTINCT %s.%s", alias, d.primaryKey(schema))
		}
	}

	cols := make([]string, 0, len(body.Select))
	var args []any
	for _, sel := range body.Select {
		expr, exprArgs, err := d.selectExpr(sel, distinct, s)
		if err != nil {
			return "", nil, err
		}
		args = append(args, exprArgs...)
		cols = append(cols, fmt.Sprintf("%s AS %s", expr, d.QuoteIdent(sel.Into)))
	}
	return strings.Join(cols, ", "), args, nil
}

// orEmpty guards CollectJoins against a nil condition.
func orEmpty(node *ParseNode) *ParseNode {
	if node == nil {
		return &ParseNode{}
	}
	return node
}

// selectExpr renders one projected column: the column itself, or an aggregate over it.
// countDistinct is the expression COUNT(*) becomes when the query joins, so a row matched
// through several related rows is still counted once.
func (d *Dialect) selectExpr(sel *ParseSelect, countDistinct string, s *stmt) (string, []any, error) {
	if sel.Func == "" {
		// A computed column: an expression, or a literal placed straight into the result.
		if sel.Value != nil {
			return d.valueSQL(sel.Value, s)
		}
		if sel.Field == nil {
			return "", nil, fmt.Errorf("projected column %s has no field", sel.Into)
		}
		return s.column(sel.Field), nil, nil
	}
	if sel.Field == nil {
		if sel.Value != nil {
			expr, args, err := d.valueSQL(sel.Value, s)
			if err != nil {
				return "", nil, err
			}
			return fmt.Sprintf("%s(%s)", strings.ToUpper(sel.Func), expr), args, nil
		}
		// COUNT counts rows when given no column.
		if sel.Func != "Count" {
			return "", nil, fmt.Errorf("%s needs a column", sel.Func)
		}
		if countDistinct != "" {
			return fmt.Sprintf("COUNT(%s)", countDistinct), nil, nil
		}
		return "COUNT(*)", nil, nil
	}
	return fmt.Sprintf("%s(%s)", strings.ToUpper(sel.Func), s.column(sel.Field)), nil, nil
}

// EntitySearch builds a SELECT query from one or more entities.
// Non-zero fields are used as WHERE conditions.
// Multiple entities produce OR conditions (IN clauses per column).
// A single entity with PK set searches by PK only.
func (d *Dialect) EntitySearch(entities []models.Entity, schema *models.Model, opts *Options) (*Query, error) {
	s := d.newStmt()
	// Entity search names the table in full rather than aliasing it.
	qualifier := d.table(schema)
	s.alias.PinTableName(schema.TableName)

	selectList, err := d.selectList(opts, schema, qualifier)
	if err != nil {
		return nil, err
	}
	head := fmt.Sprintf("SELECT %s FROM %s", selectList, qualifier)

	var body string
	var args []any

	switch {
	case len(entities) == 0:
		// no filter

	default:
		if len(entities) == 1 {
			_, pkValue := entities[0].PrimaryKey()
			if pkValue != nil && !isZeroValue(reflect.ValueOf(pkValue)) {
				body = fmt.Sprintf(" WHERE %s = %s", d.primaryKey(schema), s.mark())
				args = []any{pkValue}
				break
			}
		}
		conditions, condArgs := d.entityConditions(entities, schema, s)
		if len(conditions) > 0 {
			// Multiple entities are alternatives; one entity's fields all apply.
			joiner := " AND "
			if len(entities) > 1 {
				joiner = " OR "
			}
			body = " WHERE " + strings.Join(conditions, joiner)
			args = condArgs
		}
	}

	tail, tailArgs, err := d.optionsTail(opts, schema, qualifier, s)
	if err != nil {
		return nil, err
	}

	return &Query{SQL: head + body + tail, Args: append(args, tailArgs...)}, nil
}

// entityConditions renders one condition per non-zero column across the given entities.
func (d *Dialect) entityConditions(entities []models.Entity, schema *models.Model, s *stmt) ([]string, []any) {
	// column → the distinct values seen for it
	columnValues := make(map[string][]any)

	for _, entity := range entities {
		ev := reflect.ValueOf(entity)
		if ev.Kind() == reflect.Ptr {
			ev = ev.Elem()
		}
		et := ev.Type()

		for _, fieldSchema := range schema.Fields {
			if fieldSchema.PrimaryKey {
				continue
			}
			fieldValue, found := getFieldValue(ev, et, fieldSchema.Name)
			if !found || isZeroValue(fieldValue) {
				continue
			}

			switch fieldSchema.RelationKind() {
			case models.O2M, models.M2M:
				continue

			case models.M2O:
				if fieldValue.Kind() == reflect.Ptr && !fieldValue.IsNil() {
					if related, ok := fieldValue.Interface().(models.Entity); ok {
						_, relPK := related.PrimaryKey()
						col := d.QuoteIdent(fieldSchema.GetFKColumn())
						if !containsValue(columnValues[col], relPK) {
							columnValues[col] = append(columnValues[col], relPK)
						}
					}
				}

			default:
				col := d.QuoteIdent(fieldSchema.ColumnName())
				val := fieldValue.Interface()
				if !containsValue(columnValues[col], val) {
					columnValues[col] = append(columnValues[col], val)
				}
			}
		}
	}

	// Sorted for deterministic condition order — columnValues is a map.
	cols := make([]string, 0, len(columnValues))
	for col := range columnValues {
		cols = append(cols, col)
	}
	sort.Strings(cols)

	var conditions []string
	var args []any

	for _, col := range cols {
		vals := columnValues[col]
		if len(vals) == 1 {
			// Entity-based search is always an exact match. Pattern matching is expressed
			// through the predicate language, not inferred from values.
			conditions = append(conditions, fmt.Sprintf("%s = %s", col, s.mark()))
			args = append(args, vals[0])
			continue
		}
		conditions = append(conditions, fmt.Sprintf("%s IN (%s)", col, s.marks(len(vals))))
		args = append(args, vals...)
	}

	return conditions, args
}
