//go:build !prod

package goql

import (
	"github.com/aekis-dev/goql/query"
)

// A recursive CTE is one whose definition joins itself:
//
//	tree, _ := goql.Select[CatNode](ctx, e, func(t []*CatNode) bool {
//	    roots, _    := goql.Select[CatNode](ctx, e, /* the anchor  */)
//	    children, _ := goql.Select[CatNode](ctx, e, /* joins t     */)
//	    return goql.UnionAll(roots, children)
//	})
//
// RECURSIVE is derived from that join rather than declared, so there is nothing to state
// twice. The self-reference carries a placeholder name while its own definition is being
// parsed, because what names it — the Go binding — is only known afterwards.

// nameSelfReferences replaces the placeholder a self-reference carries with the name the CTE
// was bound to, and reports whether the query is recursive.
//
// It walks the whole tree: a self-reference appears as a joined table, and as the table of
// every column read through the joined row handle.
func nameSelfReferences(q *query.ParseQuery, name string) bool {
	if q == nil || q.Body == nil {
		return false
	}
	return renameInBody(q.Body, name)
}

func renameInBody(body *query.ParseBody, name string) bool {
	found := false

	if body.Options != nil {
		for i := range body.Options.Joins {
			join := &body.Options.Joins[i]
			if join.Table == selfCTEName {
				join.Table = name
				found = true
			}
			if renameInNode(join.On, name) {
				found = true
			}
		}
	}

	for _, branch := range body.Branches {
		if renameInNode(branch.Condition, name) {
			found = true
		}
		for _, assign := range branch.Assignments {
			if renameInField(assign.Field, name) || renameInValue(assign.Value, name) {
				found = true
			}
		}
	}

	for _, sel := range body.Select {
		if renameInField(sel.Field, name) || renameInValue(sel.Value, name) {
			found = true
		}
	}

	if body.Set != nil {
		for _, branch := range body.Set.Branches {
			if renameInBody(branch.Body, name) {
				found = true
			}
		}
	}

	for _, cte := range body.With {
		if renameInBody(cte.Query.Body, name) {
			found = true
		}
	}

	return found
}

func renameInNode(node *query.ParseNode, name string) bool {
	if node == nil {
		return false
	}
	found := renameInField(node.Left, name) || renameInValue(node.LeftValue, name) ||
		renameInValue(node.Right, name)
	for _, value := range node.Values {
		if renameInValue(value, name) {
			found = true
		}
	}
	if node.Agg != nil && renameInField(node.Agg.Field, name) {
		found = true
	}
	if renameInField(node.JoinField, name) {
		found = true
	}
	if renameInNode(node.JoinScope, name) {
		found = true
	}
	for _, child := range node.Children {
		if renameInNode(child, name) {
			found = true
		}
	}
	return found
}

func renameInField(ref *query.FieldRef, name string) bool {
	if ref == nil {
		return false
	}
	found := false
	if ref.CTETable == selfCTEName {
		ref.CTETable = name
		found = true
	}
	if renameInField(ref.Nested, name) {
		found = true
	}
	return found
}

func renameInValue(ref *query.ValueRef, name string) bool {
	if ref == nil {
		return false
	}
	found := renameInField(ref.Field, name)
	if ref.Expr != nil {
		if renameInValue(ref.Expr.Left, name) || renameInValue(ref.Expr.Right, name) {
			found = true
		}
	}
	return found
}
