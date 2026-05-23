package query

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/aekis/goql/pkg/models"
)

// WhereClause recursively builds a SQL WHERE clause from a ConditionNode tree
func WhereClause(node *ParseNode) (string, []any) {
	if node.IsJoin() {
		return WhereClause(node.JoinScope)
	}
	if node.IsLeaf() {
		return buildLeafClause(node)
	}
	var parts []string
	var values []any
	for _, child := range node.Children {
		sql, vals := WhereClause(child)
		parts = append(parts, fmt.Sprintf("(%s)", sql))
		values = append(values, vals...)
	}
	return strings.Join(parts, fmt.Sprintf(" %s ", node.LogicalOp)), values
}

func buildLeafClause(node *ParseNode) (string, []any) {
	col := node.Left.FullColumn()
	if node.Right.IsColumn {
		return fmt.Sprintf("%s %s %s", col, node.Operator, node.Right.Field.FullColumn()), nil
	}
	return fmt.Sprintf("%s %s ?", col, node.Operator), []any{node.Right.Value}
}

// CollectJoins walks a ConditionNode tree and returns all nodes requiring a JOIN
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

// BuildJoinClause builds the SQL JOIN string for a join node
func BuildJoinClause(joinNode *ParseNode) string {
	ref := joinNode.JoinField
	switch ref.Field.RelationKind() {
	case models.M2O:
		return fmt.Sprintf("INNER JOIN %s ON %s.%s = %s.%s",
			ref.Nested.Field.TableSchema.TableName,
			ref.Field.TableSchema.TableName,
			ref.Field.GetFKColumn(),
			ref.Nested.Field.TableSchema.TableName,
			ref.Field.TableSchema.PrimaryKey.GetColumnName())

	case models.O2M:
		targetType := ref.Field.TargetModel()
		tempTarget := reflect.New(targetType).Interface()
		targetEntity, ok := tempTarget.(models.Entity)
		if !ok {
			panic(fmt.Sprintf("query: O2M target %v does not implement Entity", targetType))
		}
		targetSchema, err := models.GetModel(targetEntity)
		if err != nil {
			panic(fmt.Sprintf("query: schema not found for %v: %v", targetType, err))
		}
		return fmt.Sprintf("INNER JOIN %s ON %s.%s = %s.%s",
			targetSchema.TableName,
			targetSchema.TableName,
			ref.Field.OneToMany.Ref,
			ref.Field.TableSchema.TableName,
			ref.Field.TableSchema.PrimaryKey.GetColumnName())

	case models.M2M:
		m := ref.Field.ManyToMany
		targetType := ref.Field.TargetModel()
		tempTarget := reflect.New(targetType).Interface()
		targetEntity, ok := tempTarget.(models.Entity)
		if !ok {
			panic(fmt.Sprintf("query: M2M target %v does not implement Entity", targetType))
		}
		targetSchema, err := models.GetModel(targetEntity)
		if err != nil {
			panic(fmt.Sprintf("query: schema not found for %v: %v", targetType, err))
		}
		sourcePK := ref.Field.TableSchema.PrimaryKey.GetColumnName()
		targetPK := targetSchema.PrimaryKey.GetColumnName()
		return fmt.Sprintf(
			"INNER JOIN %s ON %s.%s = %s.%s INNER JOIN %s ON %s.%s = %s.%s",
			m.Table,
			m.Table, m.Column,
			ref.Field.TableSchema.TableName, sourcePK,
			targetSchema.TableName,
			targetSchema.TableName, targetPK,
			m.Table, m.Ref)
	}
	return ""
}

// BuildUpdateFromClause builds FROM and join condition for UPDATE with joins
func BuildUpdateFromClause(joinNode *ParseNode, sourceTable string) (fromTable string, joinCondition string) {
	ref := joinNode.JoinField
	switch ref.Field.RelationKind() {
	case models.M2O:
		fromTable = ref.Nested.Field.TableSchema.TableName
		joinCondition = fmt.Sprintf("%s.%s = %s.%s",
			sourceTable,
			ref.Field.GetFKColumn(),
			fromTable,
			ref.Field.TableSchema.PrimaryKey.GetColumnName())
	case models.O2M:
		targetType := ref.Field.TargetModel()
		tempTarget := reflect.New(targetType).Interface()
		targetEntity, ok := tempTarget.(models.Entity)
		if !ok {
			panic(fmt.Sprintf("query: O2M target %v does not implement Entity", targetType))
		}
		targetSchema, err := models.GetModel(targetEntity)
		if err != nil {
			panic(fmt.Sprintf("query: schema not found for %v: %v", targetType, err))
		}
		fromTable = targetSchema.TableName
		joinCondition = fmt.Sprintf("%s.%s = %s.%s",
			fromTable,
			ref.Field.OneToMany.Ref,
			sourceTable,
			ref.Field.TableSchema.PrimaryKey.GetColumnName())
	}
	return
}

// JoinSelect builds SELECT ref FROM join_table WHERE col = ?
func JoinSelect(m *models.ManyToMany) *Query {
	return &Query{
		SQL: fmt.Sprintf("SELECT %s FROM %s WHERE %s = ?", m.Ref, m.Table, m.Column),
	}
}

// JoinInsert builds INSERT OR IGNORE INTO join_table (col, ref) VALUES (?, ?)
func JoinInsert(m *models.ManyToMany) *Query {
	return &Query{
		SQL: fmt.Sprintf("INSERT OR IGNORE INTO %s (%s, %s) VALUES (?, ?)", m.Table, m.Column, m.Ref),
	}
}

// JoinDelete builds DELETE FROM join_table WHERE col = ? AND ref = ?
func JoinDelete(m *models.ManyToMany) *Query {
	return &Query{
		SQL: fmt.Sprintf("DELETE FROM %s WHERE %s = ? AND %s = ?", m.Table, m.Column, m.Ref),
	}
}

// O2MUpdate builds UPDATE targetTable SET fkCol = ? WHERE pkCol = ?
func O2MUpdate(targetSchema *models.Model, fkCol string) *Query {
	return &Query{
		SQL: fmt.Sprintf("UPDATE %s SET %s = ? WHERE %s = ?",
			targetSchema.TableName, fkCol, targetSchema.PrimaryKey.GetColumnName()),
	}
}

// SelectPKs builds SELECT pk FROM table WHERE ...
func SelectPKs(schema *models.Model, where string) *Query {
	return &Query{
		SQL: fmt.Sprintf("SELECT %s FROM %s WHERE %s",
			schema.PrimaryKey.GetColumnName(), schema.TableName, where),
	}
}

// EntityDeleteBatch builds DELETE FROM table WHERE pk IN (?, ?, ...)
func EntityDeleteBatch(schema *models.Model, pkColumn string, count int) *Query {
	placeholders := make([]string, count)
	for i := range placeholders {
		placeholders[i] = "?"
	}
	return &Query{
		SQL: fmt.Sprintf("DELETE FROM %s WHERE %s IN (%s)",
			schema.TableName, pkColumn, strings.Join(placeholders, ", ")),
	}
}
