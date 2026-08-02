//go:build !prod

package goql

import (
	"fmt"
	"go/ast"
	"go/types"

	"github.com/aekis-dev/goql/models"
	"github.com/aekis-dev/goql/query"
)

// filterCall parses goql.Filter(o.Tags, func(t *Tag) bool { … }) into a correlated EXISTS.
//
// Reported as (nil, false, nil) when the call is not a Filter, so the caller can go on to try
// the other call shapes a condition may take.
func (e *DebugExecutor) filterCall(call *ast.CallExpr, pctx *parseContext) (*query.ParseNode, bool, error) {
	if calleeName(call.Fun) != "Filter" {
		return nil, false, nil
	}
	if len(call.Args) != 2 {
		return nil, true, fmt.Errorf(
			"%w: goql.Filter takes a collection and a predicate", ErrUnsupportedExpr)
	}

	relationRef, err := e.resolveFieldRefIn(call.Args[0], pctx)
	if err != nil {
		return nil, true, fmt.Errorf("goql.Filter collection: %w", err)
	}
	if relationRef.Nested != nil {
		return nil, true, fmt.Errorf(
			"%w: goql.Filter takes a collection declared on the model being queried; "+
				"reaching one through another relation is not supported", ErrUnsupportedExpr)
	}
	kind := relationRef.Field.RelationKind()
	if kind != models.O2M && kind != models.M2M {
		return nil, true, fmt.Errorf(
			"%w: goql.Filter needs a one2many or many2many field, and %s is not one",
			ErrUnsupportedExpr, relationRef.Field.Name)
	}

	funcLit, ok := call.Args[1].(*ast.FuncLit)
	if !ok {
		return nil, true, fmt.Errorf(
			"%w: goql.Filter needs a predicate written at the call site", ErrInvalidLambda)
	}

	targetSchema, err := query.RelationTargetSchema(relationRef.Field)
	if err != nil {
		return nil, true, err
	}

	body, err := e.parseSubBody(funcLit, targetSchema, pctx)
	if err != nil {
		return nil, true, fmt.Errorf("goql.Filter predicate: %w", err)
	}
	if len(body.WriteBranches()) > 0 {
		return nil, true, fmt.Errorf(
			"%w: a goql.Filter predicate selects rows and cannot assign", ErrInvalidLambda)
	}

	return &query.ParseNode{Exists: &query.RelationExists{
		Relation:  relationRef,
		Condition: body.SelectCondition(),
	}}, true, nil
}

// rangeRetired reports a relation traversal written as a range loop, which goql used to
// compile to a JOIN.
//
// The loop was removed rather than re-pointed at EXISTS because it is a *statement*: it can
// only appear where statements can, so it could never sit in an `if` condition (making a
// relation-conditioned Update inexpressible) nor on one side of || or !.
func rangeRetired(rangeStmt *ast.RangeStmt) error {
	collection := types.ExprString(rangeStmt.X)
	item := "x"
	if ident, ok := rangeStmt.Value.(*ast.Ident); ok && ident.Name != "_" {
		item = ident.Name
	}

	return fmt.Errorf(
		"%w: ranging over %s is no longer supported — it compiled to a JOIN, which changes "+
			"the row count and cannot be combined with || or !. Use goql.Filter:\n"+
			"\n    goql.Filter(%s, func(%s *T) bool { return %s.Field == value })\n",
		ErrUnsupportedExpr, collection, collection, item, item)
}
