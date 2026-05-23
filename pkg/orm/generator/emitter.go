//go:build ignore

package main

import (
	"fmt"
	"strings"

	"github.com/aekis/goql/src/orm"
)

func indent(n int) string {
	return strings.Repeat("\t", n)
}

func emitConditionNode(node *orm.ConditionNode, depth int) string {
	if node == nil {
		return "nil"
	}

	ind := indent(depth)
	ind1 := indent(depth + 1)

	if node.IsJoin() {
		return fmt.Sprintf("&orm.ConditionNode{\n%sJoinField: %s,\n%sJoinScope: %s,\n%s}",
			ind1, emitFieldRef(node.JoinField, depth+1),
			ind1, emitConditionNode(node.JoinScope, depth+1),
			ind)
	}

	if node.IsBranch() {
		children := make([]string, len(node.Children))
		for i, child := range node.Children {
			children[i] = emitConditionNode(child, depth+1)
		}
		return fmt.Sprintf(
			"&orm.ConditionNode{\n%sLogicalOp: %q,\n%sChildren: []*orm.ConditionNode{\n%s%s,\n%s},\n%s}",
			ind1, node.LogicalOp,
			ind1,
			ind1, strings.Join(children, fmt.Sprintf(",\n%s", ind1)),
			ind1,
			ind)
	}

	// Leaf
	return fmt.Sprintf(
		"&orm.ConditionNode{\n%sLeft: %s,\n%sOperator: %q,\n%sRight: %s,\n%s}",
		ind1, emitFieldRef(node.Left, depth+1),
		ind1, node.Operator,
		ind1, emitValueRef(node.Right, depth+1),
		ind)
}

func emitFieldRef(ref *orm.FieldRef, depth int) string {
	if ref == nil {
		return "nil"
	}

	tableName := ref.Field.TableSchema.TableName
	fieldName := ref.Field.Name

	if ref.Nested != nil {
		return fmt.Sprintf(
			"&orm.FieldRef{Field: orm.ResolveField(%q, %q), Nested: %s}",
			tableName, fieldName,
			emitFieldRef(ref.Nested, depth))
	}
	return fmt.Sprintf(
		"&orm.FieldRef{Field: orm.ResolveField(%q, %q)}",
		tableName, fieldName)
}

func emitValueRef(ref *orm.ValueRef, depth int) string {
	if ref == nil {
		return "nil"
	}
	if ref.IsColumn {
		return fmt.Sprintf(
			"&orm.ValueRef{IsColumn: true, Field: %s}",
			emitFieldRef(ref.Field, depth))
	}
	return fmt.Sprintf("&orm.ValueRef{Value: %#v}", ref.Value)
}

func emitParsedBody(body *orm.ParseBody, depth int) string {
	ind := indent(depth)
	ind1 := indent(depth + 1)

	condStr := "nil"
	if body.Condition != nil {
		condStr = emitConditionNode(body.Condition, depth+1)
	}

	assignStr := "nil"
	if len(body.Assignments) > 0 {
		parts := make([]string, len(body.Assignments))
		for i, a := range body.Assignments {
			parts[i] = fmt.Sprintf(
				"&orm.ParseAssign{Field: %s, Value: %s}",
				emitFieldRef(a.Field, depth+2),
				emitValueRef(a.Value, depth+2))
		}
		assignStr = fmt.Sprintf("[]*orm.ParseAssign{%s}", strings.Join(parts, ", "))
	}

	relAssignStr := "nil"
	if len(body.RelationAssignments) > 0 {
		parts := make([]string, len(body.RelationAssignments))
		for i, ra := range body.RelationAssignments {
			pks := make([]string, len(ra.RelatedPKs))
			for j, pk := range ra.RelatedPKs {
				pks[j] = fmt.Sprintf("%#v", pk)
			}
			parts[i] = fmt.Sprintf(
				"&orm.RelationAssignment{Field: %s, RelatedPKs: []any{%s}}",
				emitFieldRef(ra.Field, depth+2),
				strings.Join(pks, ", "))
		}
		relAssignStr = fmt.Sprintf("[]*orm.RelationAssignment{%s}", strings.Join(parts, ", "))
	}

	return fmt.Sprintf(
		"&orm.ParseBody{\n%sCondition: %s,\n%sAssignments: %s,\n%sRelationAssignments: %s,\n%s}",
		ind1, condStr,
		ind1, assignStr,
		ind1, relAssignStr,
		ind)
}
