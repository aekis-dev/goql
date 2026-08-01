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
	// A nested query standing on its own is an EXISTS term with no left-hand side.
	if node.IsSubOnly() {
		return d.subQuery(node.Sub, s)
	}

	col := node.RawColumn
	if node.Agg != nil {
		// A condition over an aggregate: SUM(total) > 1000, emitted in HAVING.
		expr, err := d.selectExpr(node.Agg, "", s)
		if err != nil {
			return "", nil, err
		}
		col = expr
	} else if col == "" {
		col = s.column(node.Left)
	}

	// Operators that take no right-hand side at all.
	if IsNullaryOperator(node.Operator) {
		return fmt.Sprintf("%s %s", col, node.Operator), nil, nil
	}

	// A nested query as the right-hand side: IN (SELECT …) or a scalar comparison. Checked
	// before the list branch, since IN is a list operator but takes no bound values here.
	if node.Sub != nil {
		sql, args, err := d.subQuery(node.Sub, s)
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("%s %s %s", col, node.Operator, sql), args, nil
	}

	// Operators that take a list.
	if IsListOperator(node.Operator) {
		args := make([]any, len(node.Values))
		for i, value := range node.Values {
			args[i] = value.Value
		}
		return fmt.Sprintf("%s %s (%s)", col, node.Operator, s.marks(len(args))), args, nil
	}

	if node.Right == nil {
		// Defensive: a single-value operator without a value would emit invalid SQL.
		return fmt.Sprintf("%s %s %s", col, node.Operator, s.mark()), []any{nil}, nil
	}
	if node.Right.IsColumn {
		return fmt.Sprintf("%s %s %s", col, node.Operator, s.column(node.Right.Field)), nil, nil
	}
	return fmt.Sprintf("%s %s %s", col, node.Operator, s.mark()), []any{node.Right.Value}, nil
}

// CollectJoins walks a condition tree and returns all nodes requiring a JOIN
func CollectJoins(node *ParseNode, seen map[string]bool) []*ParseNode {
	if node.IsJoin() {
		key := node.JoinField.Field.Name
		if !seen[key] {
			seen[key] = true
			return []*ParseNode{node}
		}
		return nil
	}
	if node.IsLeaf() {
		if node.Left != nil && node.Left.Nested != nil {
			key := node.Left.Field.Name
			if !seen[key] {
				seen[key] = true
				return []*ParseNode{{
					JoinField: node.Left,
					JoinScope: node,
				}}
			}
		}
		return nil
	}
	var joins []*ParseNode
	for _, child := range node.Children {
		joins = append(joins, CollectJoins(child, seen)...)
	}
	return joins
}

// joinClauses collects and renders every JOIN a condition tree requires.
func (d *Dialect) joinClauses(cond *ParseNode, s *stmt) ([]string, error) {
	var clauses []string
	for _, joinNode := range CollectJoins(cond, make(map[string]bool)) {
		clause, err := d.joinClause(joinNode, s)
		if err != nil {
			return nil, err
		}
		clauses = append(clauses, clause)
	}
	return clauses, nil
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
func (d *Dialect) joinClause(joinNode *ParseNode, s *stmt) (string, error) {
	ref := joinNode.JoinField
	sourceSchema := ref.Field.TableSchema

	switch ref.Field.RelationKind() {
	case models.M2O:
		targetSchema := ref.Nested.Field.TableSchema
		return fmt.Sprintf("INNER JOIN %s ON %s.%s = %s.%s",
			s.alias.From(targetSchema.TableName),
			s.alias.Alias(sourceSchema.TableName), d.QuoteIdent(ref.Field.GetFKColumn()),
			s.alias.Alias(targetSchema.TableName), d.primaryKey(targetSchema)), nil

	case models.O2M:
		targetSchema, err := RelationTargetSchema(ref.Field)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("INNER JOIN %s ON %s.%s = %s.%s",
			s.alias.From(targetSchema.TableName),
			s.alias.Alias(targetSchema.TableName), d.QuoteIdent(ref.Field.OneToMany.Ref),
			s.alias.Alias(sourceSchema.TableName), d.primaryKey(sourceSchema)), nil

	case models.M2M:
		m := ref.Field.ManyToMany
		targetSchema, err := RelationTargetSchema(ref.Field)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("INNER JOIN %s ON %s.%s = %s.%s INNER JOIN %s ON %s.%s = %s.%s",
			s.alias.From(m.Table),
			s.alias.Alias(m.Table), d.QuoteIdent(m.Column),
			s.alias.Alias(sourceSchema.TableName), d.primaryKey(sourceSchema),
			s.alias.From(targetSchema.TableName),
			s.alias.Alias(targetSchema.TableName), d.primaryKey(targetSchema),
			s.alias.Alias(m.Table), d.QuoteIdent(m.Ref)), nil
	}

	return "", fmt.Errorf("query: cannot build a JOIN for non-relation field %s", ref.Field.Name)
}

// updateFromClause builds the FROM table and join condition for an UPDATE that reaches
// through a relation.
func (d *Dialect) updateFromClause(joinNode *ParseNode, s *stmt) (fromTable, joinCondition string, err error) {
	ref := joinNode.JoinField
	sourceSchema := ref.Field.TableSchema

	switch ref.Field.RelationKind() {
	case models.M2O:
		targetSchema := ref.Nested.Field.TableSchema
		return s.alias.From(targetSchema.TableName),
			fmt.Sprintf("%s.%s = %s.%s",
				s.alias.Alias(sourceSchema.TableName), d.QuoteIdent(ref.Field.GetFKColumn()),
				s.alias.Alias(targetSchema.TableName), d.primaryKey(targetSchema)),
			nil

	case models.O2M:
		targetSchema, err := RelationTargetSchema(ref.Field)
		if err != nil {
			return "", "", err
		}
		return s.alias.From(targetSchema.TableName),
			fmt.Sprintf("%s.%s = %s.%s",
				s.alias.Alias(targetSchema.TableName), d.QuoteIdent(ref.Field.OneToMany.Ref),
				s.alias.Alias(sourceSchema.TableName), d.primaryKey(sourceSchema)),
			nil
	}

	return "", "", fmt.Errorf("query: cannot build an UPDATE FROM clause for field %s", ref.Field.Name)
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
