package query

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/aekis-dev/goql/models"
)

// whereClause recursively builds a SQL WHERE clause from a condition tree.
//
// Column references are qualified through the statement's alias map, so one tree renders
// correctly whether the enclosing statement aliases its tables or names them in full.
// Placeholders are drawn from the statement in emission order.
func (d *Dialect) whereClause(node *ParseNode, s *stmt) (string, []any, error) {
	if node.IsJoin() {
		return d.whereClause(node.JoinScope, s)
	}
	if node.IsLeaf() {
		return d.leafClause(node, s)
	}
	if node.LogicalOp == LogicalNot {
		sql, vals, err := d.whereClause(node.Children[0], s)
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("NOT (%s)", sql), vals, nil
	}

	var parts []string
	var values []any
	for _, child := range node.Children {
		sql, vals, err := d.whereClause(child, s)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, fmt.Sprintf("(%s)", sql))
		values = append(values, vals...)
	}
	return strings.Join(parts, fmt.Sprintf(" %s ", node.LogicalOp)), values, nil
}

func (d *Dialect) leafClause(node *ParseNode, s *stmt) (string, []any, error) {
	// A relation filter is a correlated EXISTS with no left-hand side.
	if node.Exists != nil {
		return d.existsClause(node.Exists, s)
	}

	// A nested query standing on its own is an EXISTS term with no left-hand side.
	if node.IsSubOnly() {
		return d.subQuery(node.Sub, s)
	}

	col := node.RawColumn
	var args []any
	switch {
	case node.Agg != nil:
		// A condition over an aggregate: SUM(total) > 1000, emitted in HAVING.
		expr, aggArgs, err := d.selectExpr(node.Agg, "", s)
		if err != nil {
			return "", nil, err
		}
		col, args = expr, aggArgs

	case node.LeftValue != nil:
		// A computed left-hand side: o.Price * o.Qty > 100.
		expr, leftArgs, err := d.valueSQL(node.LeftValue, s)
		if err != nil {
			return "", nil, err
		}
		col, args = expr, leftArgs

	case col == "":
		col = s.column(node.Left)
	}

	// Operators that take no right-hand side at all.
	if IsNullaryOperator(node.Operator) {
		return fmt.Sprintf("%s %s", col, node.Operator), args, nil
	}

	// A nested query as the right-hand side: IN (SELECT …) or a scalar comparison. Checked
	// before the list branch, since IN is a list operator but takes no bound values here.
	if node.Sub != nil {
		sql, subArgs, err := d.subQuery(node.Sub, s)
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("%s %s %s", col, node.Operator, sql), append(args, subArgs...), nil
	}

	// Operators that take a list.
	if IsListOperator(node.Operator) {
		values := make([]any, len(node.Values))
		for i, value := range node.Values {
			values[i] = value.Value
		}
		return fmt.Sprintf("%s %s (%s)", col, node.Operator, s.marks(len(values))), append(args, values...), nil
	}

	if node.Right == nil {
		// Defensive: a single-value operator without a value would emit invalid SQL.
		return fmt.Sprintf("%s %s %s", col, node.Operator, s.mark()), append(args, nil), nil
	}
	right, rightArgs, err := d.valueSQL(node.Right, s)
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("%s %s %s", col, node.Operator, right), append(args, rightArgs...), nil
}

// CollectJoins walks a condition tree and returns every relation hop it traverses, outermost
// first so a hop is always rendered after the one it starts from.
//
// Hops are keyed by their path rather than by field name: o.Customer.Company and
// o.Tag.Company both end at "Company" but are different rows, and collapsing them would emit
// one join where two are needed.
func CollectJoins(node *ParseNode, seen map[string]bool) []FieldHop {
	if node == nil {
		return nil
	}

	if node.IsBranch() {
		var hops []FieldHop
		for _, child := range node.Children {
			hops = append(hops, CollectJoins(child, seen)...)
		}
		return hops
	}

	// Every position a field reference can appear in contributes its path. Restricting this
	// to the left-hand side would leave a path on the right of a comparison unjoined.
	var refs []*FieldRef
	refs = append(refs, node.Left, node.JoinField)
	if node.Agg != nil {
		refs = append(refs, node.Agg.Field)
	}
	refs = append(refs, valueRefFields(node.Right)...)
	refs = append(refs, valueRefFields(node.LeftValue)...)
	for _, v := range node.Values {
		refs = append(refs, valueRefFields(v)...)
	}

	var hops []FieldHop
	for _, ref := range refs {
		if ref == nil {
			continue
		}
		for _, hop := range ref.Hops() {
			if seen[hop.TargetPath] {
				continue
			}
			seen[hop.TargetPath] = true
			hops = append(hops, hop)
		}
	}
	return hops
}

// valueRefFields collects the field references a value carries, walking into an expression's
// operands so a path used in arithmetic is joined too.
func valueRefFields(v *ValueRef) []*FieldRef {
	if v == nil {
		return nil
	}
	if v.Expr != nil {
		return append(valueRefFields(v.Expr.Left), valueRefFields(v.Expr.Right)...)
	}
	if v.IsColumn {
		return []*FieldRef{v.Field}
	}
	return nil
}

// joinClauses collects and renders every JOIN a condition tree requires.
func (d *Dialect) joinClauses(cond *ParseNode, s *stmt) ([]string, error) {
	var clauses []string
	for _, hop := range CollectJoins(cond, make(map[string]bool)) {
		clause, err := d.joinClause(hop, s)
		if err != nil {
			return nil, err
		}
		clauses = append(clauses, clause)
	}
	return clauses, nil
}

// explicitJoins renders the joins a lambda declared with a *goql.Join carrier, in
// declaration order, and returns the values bound by their ON conditions.
//
// Unlike a join derived from a relation, the condition is whatever the caller wrote — which
// is what makes joining unrelated models, and choosing the kind, expressible at all.
func (d *Dialect) explicitJoins(opts *Options, s *stmt) (string, []any, error) {
	if opts == nil || len(opts.Joins) == 0 {
		return "", nil, nil
	}

	var clauses []string
	var args []any
	for _, join := range opts.Joins {
		kind := join.Type
		if kind == "" {
			kind = "INNER"
		}
		if !d.SupportsJoinType(kind) {
			return "", nil, fmt.Errorf("%s does not support %s JOIN", d.Name(), kind)
		}

		// A join declared by relation path renders one JOIN per hop, related by the foreign
		// keys the models declare. An extra ON condition, if given, joins the last hop —
		// which for an outer join is the only place a filter can go without turning it back
		// into an inner one.
		if len(join.Hops) > 0 {
			hopClauses, hopArgs, err := d.pathJoin(join, kind, s)
			if err != nil {
				return "", nil, err
			}
			clauses = append(clauses, hopClauses)
			args = append(args, hopArgs...)
			continue
		}

		on, onArgs, err := d.whereClause(join.On, s)
		if err != nil {
			return "", nil, fmt.Errorf("join on %s: %w", join.Table, err)
		}
		clauses = append(clauses, fmt.Sprintf("%s JOIN %s ON %s", kind, s.alias.From(join.Table), on))
		args = append(args, onArgs...)
	}
	return strings.Join(clauses, " "), args, nil
}

// pathJoin renders a join declared by a relation path: one JOIN per hop, every one of them
// of the declared kind. Mixing kinds would defeat the point — a LEFT followed by an INNER
// drops exactly the rows the LEFT existed to keep.
func (d *Dialect) pathJoin(join JoinSpec, kind string, s *stmt) (string, []any, error) {
	var clauses []string
	var args []any

	for i, hop := range join.Hops {
		clause, err := d.joinClauseOf(hop, kind, s)
		if err != nil {
			return "", nil, err
		}

		// The caller's own condition belongs to the far end of the path.
		if i == len(join.Hops)-1 && join.On != nil {
			on, onArgs, err := d.whereClause(join.On, s)
			if err != nil {
				return "", nil, fmt.Errorf("join on %s: %w", join.Table, err)
			}
			clause += " AND " + on
			args = append(args, onArgs...)
		}
		clauses = append(clauses, clause)
	}
	return strings.Join(clauses, " "), args, nil
}

// RelationTargetSchema resolves the schema of a relation field's target model.
func RelationTargetSchema(field *models.Field) (*models.Model, error) {
	targetType := field.TargetModel()
	target := reflect.New(targetType).Interface()
	entity, ok := target.(models.Entity)
	if !ok {
		return nil, fmt.Errorf("query: relation target %v does not implement models.Entity", targetType)
	}
	schema, err := models.GetModel(entity)
	if err != nil {
		return nil, fmt.Errorf("query: schema not found for relation target %v: %w", targetType, err)
	}
	return schema, nil
}

// joinClause builds the SQL JOIN string for a join node.
func (d *Dialect) joinClause(hop FieldHop, s *stmt) (string, error) {
	return d.joinClauseOf(hop, "INNER", s)
}

// joinClauseOf renders one relation hop with an explicit join kind. A many2many hop emits
// two JOINs — the bridge and the target — and both take the kind, which is why the kind is
// threaded through rather than patched into the rendered string afterwards.
func (d *Dialect) joinClauseOf(hop FieldHop, kind string, s *stmt) (string, error) {
	sourceSchema := hop.Field.TableSchema
	source := s.alias.AliasFor(hop.SourcePath, sourceSchema.TableName)

	switch hop.Field.RelationKind() {
	case models.M2O:
		targetSchema, err := RelationTargetSchema(hop.Field)
		if err != nil {
			return "", err
		}
		target := s.alias.AliasFor(hop.TargetPath, targetSchema.TableName)
		return fmt.Sprintf("%s JOIN %s ON %s.%s = %s.%s", kind,
			s.alias.FromFor(hop.TargetPath, targetSchema.TableName),
			source, d.QuoteIdent(hop.Field.GetFKColumn()),
			target, d.primaryKey(targetSchema)), nil

	case models.O2M:
		targetSchema, err := RelationTargetSchema(hop.Field)
		if err != nil {
			return "", err
		}
		target := s.alias.AliasFor(hop.TargetPath, targetSchema.TableName)
		return fmt.Sprintf("%s JOIN %s ON %s.%s = %s.%s", kind,
			s.alias.FromFor(hop.TargetPath, targetSchema.TableName),
			target, d.QuoteIdent(hop.Field.OneToMany.Ref),
			source, d.primaryKey(sourceSchema)), nil

	case models.M2M:
		m := hop.Field.ManyToMany
		targetSchema, err := RelationTargetSchema(hop.Field)
		if err != nil {
			return "", err
		}
		// The bridge belongs to this hop, so it is keyed under the hop's path too.
		bridgePath := hop.TargetPath + ".@bridge"
		bridge := s.alias.AliasFor(bridgePath, m.Table)
		target := s.alias.AliasFor(hop.TargetPath, targetSchema.TableName)
		return fmt.Sprintf("%s JOIN %s ON %s.%s = %s.%s %s JOIN %s ON %s.%s = %s.%s",
			kind, s.alias.FromFor(bridgePath, m.Table),
			bridge, d.QuoteIdent(m.Column),
			source, d.primaryKey(sourceSchema),
			kind, s.alias.FromFor(hop.TargetPath, targetSchema.TableName),
			target, d.primaryKey(targetSchema),
			bridge, d.QuoteIdent(m.Ref)), nil
	}

	return "", fmt.Errorf("query: cannot build a JOIN for non-relation field %s", hop.Field.Name)
}

// updateFromClause builds the FROM table and join condition for an UPDATE that reaches
// through a relation.
func (d *Dialect) updateFromClause(hop FieldHop, s *stmt) (fromTable, joinCondition string, err error) {
	sourceSchema := hop.Field.TableSchema
	source := s.alias.AliasFor(hop.SourcePath, sourceSchema.TableName)

	targetSchema, err := RelationTargetSchema(hop.Field)
	if err != nil {
		return "", "", err
	}
	target := s.alias.AliasFor(hop.TargetPath, targetSchema.TableName)

	switch hop.Field.RelationKind() {
	case models.M2O:
		return s.alias.FromFor(hop.TargetPath, targetSchema.TableName),
			fmt.Sprintf("%s.%s = %s.%s",
				source, d.QuoteIdent(hop.Field.GetFKColumn()),
				target, d.primaryKey(targetSchema)),
			nil

	case models.O2M:
		return s.alias.FromFor(hop.TargetPath, targetSchema.TableName),
			fmt.Sprintf("%s.%s = %s.%s",
				target, d.QuoteIdent(hop.Field.OneToMany.Ref),
				source, d.primaryKey(sourceSchema)),
			nil
	}

	return "", "", fmt.Errorf("query: cannot build an UPDATE FROM clause for field %s", hop.Field.Name)
}

// JoinSelect builds SELECT ref FROM join_table WHERE col = ?
func (d *Dialect) JoinSelect(m *models.ManyToMany) *Query {
	s := d.newStmt()
	return &Query{
		SQL: fmt.Sprintf("SELECT %s FROM %s WHERE %s = %s",
			d.QuoteIdent(m.Ref), d.QuoteIdent(m.Table), d.QuoteIdent(m.Column), s.mark()),
	}
}

// JoinInsert builds an insert into a join table that skips rows already linked.
func (d *Dialect) JoinInsert(m *models.ManyToMany) *Query {
	s := d.newStmt()
	marks := []string{s.mark(), s.mark()}
	return &Query{
		SQL: d.InsertIgnore(d.QuoteIdent(m.Table),
			[]string{d.QuoteIdent(m.Column), d.QuoteIdent(m.Ref)}, marks),
	}
}

// JoinDelete builds DELETE FROM join_table WHERE col = ? AND ref = ?
func (d *Dialect) JoinDelete(m *models.ManyToMany) *Query {
	s := d.newStmt()
	return &Query{
		SQL: fmt.Sprintf("DELETE FROM %s WHERE %s = %s AND %s = %s",
			d.QuoteIdent(m.Table), d.QuoteIdent(m.Column), s.mark(),
			d.QuoteIdent(m.Ref), s.mark()),
	}
}

// O2MUpdate builds UPDATE targetTable SET fkCol = ? WHERE pkCol = ?
func (d *Dialect) O2MUpdate(targetSchema *models.Model, fkCol string) *Query {
	s := d.newStmt()
	return &Query{
		SQL: fmt.Sprintf("UPDATE %s SET %s = %s WHERE %s = %s",
			d.table(targetSchema), d.QuoteIdent(fkCol), s.mark(),
			d.primaryKey(targetSchema), s.mark()),
	}
}

// SelectPKsWhere builds `SELECT pk FROM table [joins] WHERE …` for a parsed condition, or
// an unconditional select when cond is nil.
func (d *Dialect) SelectPKsWhere(schema *models.Model, cond *ParseNode) (*Query, error) {
	s := d.newStmt()
	alias := s.alias.Alias(schema.TableName)
	pk := d.primaryKey(schema)

	if cond == nil {
		return &Query{
			SQL: fmt.Sprintf("SELECT %s.%s FROM %s", alias, pk, s.alias.From(schema.TableName)),
		}, nil
	}

	joins, err := d.joinClauses(cond, s)
	if err != nil {
		return nil, err
	}
	where, args, err := d.whereClause(cond, s)
	if err != nil {
		return nil, err
	}

	sql := fmt.Sprintf("SELECT %s.%s FROM %s", alias, pk, s.alias.From(schema.TableName))
	if len(joins) > 0 {
		sql += " " + strings.Join(joins, " ")
	}
	sql += " WHERE " + where

	return &Query{SQL: sql, Args: args}, nil
}

// EntityDeleteBatch builds DELETE FROM table WHERE pk IN (?, ?, ...)
func (d *Dialect) EntityDeleteBatch(schema *models.Model, count int) *Query {
	s := d.newStmt()
	return &Query{
		SQL: fmt.Sprintf("DELETE FROM %s WHERE %s IN (%s)",
			d.table(schema), d.primaryKey(schema), s.marks(count)),
	}
}
