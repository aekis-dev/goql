//go:build !prod

package goql

import (
	"fmt"
	"go/ast"

	"github.com/aekis-dev/goql/models"
	"github.com/aekis-dev/goql/query"
)

// A goql call written inside a lambda body is a subquery. Like the rest of the body it is
// parsed, never executed — the nested lambda is simply another body to parse.
//
// Two spellings, both of which are ordinary Go:
//
//	usa, _ := goql.Select[Customer](ctx, e, func(c *Customer) bool { … })
//	return goql.Condition(o.Customer, "IN", usa)
//
//	return goql.Condition(o.Customer, "IN",
//	    goql.Unwrap(goql.Select[Customer](ctx, e, func(c *Customer) bool { … })))
//
// The first names the subquery so it can be used more than once. The second nests directly;
// it needs Unwrap because Go does not allow a two-value call as one argument among others,
// and passing a call as a function's entire argument list is the one exception.

// nestableFuncs are the goql functions that may be written inside a lambda body, mapped to
// the Func a parsed query records. Only predicate-shaped calls qualify: Update and Insert
// emit one statement per branch, so they are not values.
//
// Select records an empty Func because it yields rows; the rest record their own name.
// Adding an aggregate is an entry here plus one in query.funcArity.
var nestableFuncs = map[string]string{
	"Select": "",
	"Exists": "Exists",
}

// tryParseSubqueryDecl recognises `name, _ := goql.Select[T](…)` and binds the name, so the
// same subquery can be referenced several times in one body.
func (e *DebugExecutor) tryParseSubqueryDecl(s *ast.AssignStmt, pctx *parseContext) (bool, error) {
	if len(s.Rhs) != 1 {
		return false, nil
	}
	call, ok := s.Rhs[0].(*ast.CallExpr)
	if !ok {
		return false, nil
	}
	if _, isSub := nestableFuncs[calleeName(call.Fun)]; !isSub {
		// Unwrap around a goql call is the nested form; assigning it is equally valid.
		if calleeName(call.Fun) != "Unwrap" {
			return false, nil
		}
	}

	sub, err := e.parseSubCall(call, pctx)
	if err != nil {
		return true, err
	}
	if sub == nil {
		return false, nil
	}

	// The first name receives the subquery; a second is the discarded error.
	name := ""
	if len(s.Lhs) > 0 {
		name = baseIdentName(s.Lhs[0])
	}
	if name == "" || name == "_" {
		return true, fmt.Errorf("%w: a nested goql call must be assigned to a name to be used",
			ErrInvalidLambda)
	}
	if pctx.subqueries == nil {
		pctx.subqueries = make(map[string]*query.ParseQuery)
	}
	pctx.subqueries[name] = sub

	// The first branch parsed inside a combining lambda is the anchor of a recursive query,
	// and its projection is the shape the self-reference presents — which is where SQL takes
	// a recursive CTE's column types from too.
	if pctx.selfHandle != "" && len(pctx.selfColumns) == 0 {
		for _, sel := range sub.Body.Select {
			pctx.selfColumns = append(pctx.selfColumns, sel.Into)
		}
	}

	// Go forces a bound error to be used, and the only honest use is discarding it, so a
	// named one is remembered to give a clear message if the body tests it.
	if len(s.Lhs) > 1 {
		if errName := baseIdentName(s.Lhs[1]); errName != "" && errName != "_" {
			if pctx.subErrors == nil {
				pctx.subErrors = make(map[string]bool)
			}
			pctx.subErrors[errName] = true
		}
	}
	return true, nil
}

// subqueryFor resolves an expression to a nested query: a name bound earlier in the body, a
// goql call wrapped in Unwrap, or a bare goql call. Returns nil when the expression is
// something else entirely.
func (e *DebugExecutor) subqueryFor(expr ast.Expr, pctx *parseContext) (*query.ParseQuery, error) {
	if ident, ok := expr.(*ast.Ident); ok {
		if sub, found := pctx.subqueries[ident.Name]; found {
			return sub, nil
		}
		return nil, nil
	}
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return nil, nil
	}
	return e.parseSubCall(call, pctx)
}

// parseSubCall parses a nested goql call, unwrapping goql.Unwrap(…) first. It returns nil
// when the call is not one of the nestable goql functions.
func (e *DebugExecutor) parseSubCall(call *ast.CallExpr, pctx *parseContext) (*query.ParseQuery, error) {
	if calleeName(call.Fun) == "Unwrap" {
		if len(call.Args) != 1 {
			return nil, fmt.Errorf("%w: Unwrap takes one call", ErrUnsupportedExpr)
		}
		inner, ok := call.Args[0].(*ast.CallExpr)
		if !ok {
			return nil, fmt.Errorf("%w: Unwrap must wrap a goql call", ErrUnsupportedExpr)
		}
		call = inner
	}

	fn, isSub := nestableFuncs[calleeName(call.Fun)]
	if !isSub {
		return nil, nil
	}

	// The model comes from the type argument: goql.Select[Customer](…).
	index, ok := call.Fun.(*ast.IndexExpr)
	if !ok {
		return nil, fmt.Errorf("%w: a nested %s needs its model as a type argument, "+
			"e.g. goql.%s[Customer](…)", ErrInvalidLambda,
			calleeName(call.Fun), calleeName(call.Fun))
	}
	funcLit := findArgFuncLit(call)
	if funcLit == nil {
		return nil, fmt.Errorf("%w: a nested %s needs its predicate written at the call site",
			ErrInvalidLambda, calleeName(call.Fun))
	}

	// A type argument that is not a model means the nested query projects into a result type
	// of its own — a branch of a set operation, say. It names the model it reads from in its
	// own body, like any other projection.
	typeName := baseTypeName(index.Index)
	schema := modelByTypeName(typeName)
	if schema == nil {
		return e.parseProjectionSource(funcLit, flatParams(funcLit), pctx)
	}

	body, err := e.parseSubBody(funcLit, schema, pctx)
	if err != nil {
		return nil, err
	}

	sub := &query.ParseQuery{Model: schema.Type.Name(), Func: fn, Body: body}

	// What the nested query yields is named by goql.Fields inside its own lambda; the
	// function decides how many fields that may be, of what type, and which options apply.
	if err := query.ValidateQuery(sub, true); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidOption, err)
	}

	for _, branch := range body.Branches {
		if len(branch.Assignments) > 0 || len(branch.RelationAssignments) > 0 {
			return nil, fmt.Errorf(
				"%w: a nested goql call must be a predicate — assignments describe a write, "+
					"which is not a value", ErrInvalidLambda)
		}
	}

	return sub, nil
}

// parseSubBody parses a nested lambda against its own model, inheriting the enclosing
// lambda's models so a condition may correlate with the outer row.
func (e *DebugExecutor) parseSubBody(funcLit *ast.FuncLit, schema *models.Model, outer *parseContext) (*query.ParseBody, error) {
	params := flatParams(funcLit)
	pctx := newParseContext(schema, paramNameAt(params, 0))

	// Correlation: the outer models stay resolvable by name inside the nested body, but
	// referencing one must not add it to the subquery's own FROM list — it is already in
	// the enclosing statement.
	for name, outerSchema := range outer.participants {
		if _, taken := pctx.participants[name]; taken {
			continue
		}
		pctx.participants[name] = outerSchema
		pctx.inherited(outerSchema.TableName)
	}
	pctx.paramsName = outer.paramsName
	pctx.paramsType = outer.paramsType

	if err := pctx.classifyParams(funcLit); err != nil {
		return nil, err
	}
	return e.parseBody(funcLit, pctx)
}

// findArgFuncLit returns the function literal among a call's arguments, ignoring the
// context and engine a nested call carries only to satisfy the compiler.
func findArgFuncLit(call *ast.CallExpr) *ast.FuncLit {
	for _, arg := range call.Args {
		if funcLit, ok := arg.(*ast.FuncLit); ok {
			return funcLit
		}
	}
	return nil
}

// baseTypeName renders a type argument's name, handling both Customer and models.Customer.
func baseTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

// displayFunc names a parsed query's function for an error message; an empty Func is a
// plain Select.
func displayFunc(name string) string {
	if name == "" {
		return "Select"
	}
	return name
}

// tryParseSetOperation recognises `return goql.Union(live, archived)` and its relatives. The
// branches are queries bound earlier in the body, or written inline through Unwrap.
func (e *DebugExecutor) tryParseSetOperation(expr ast.Expr, pctx *parseContext) (*query.ParseSet, error) {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return nil, nil
	}
	op, isSet := query.SetOp(calleeName(call.Fun))
	if !isSet {
		return nil, nil
	}

	if len(call.Args) < 2 {
		return nil, fmt.Errorf("%w: %s combines at least two queries, got %d",
			ErrInvalidLambda, calleeName(call.Fun), len(call.Args))
	}

	set := &query.ParseSet{Op: op}
	for i, arg := range call.Args {
		branch, err := e.subqueryFor(arg, pctx)
		if err != nil {
			return nil, err
		}
		if branch == nil {
			return nil, fmt.Errorf(
				"%w: branch %d of %s is not a goql query — bind one with "+
					"`name, _ := goql.Select[…](…)` or wrap it in goql.Unwrap",
				ErrInvalidLambda, i+1, calleeName(call.Fun))
		}
		if branch.Func != "" {
			return nil, fmt.Errorf("%w: %s combines row queries, but branch %d is a %s",
				ErrInvalidLambda, calleeName(call.Fun), i+1, branch.Func)
		}
		set.Branches = append(set.Branches, branch)
	}

	if err := checkBranchShapes(set); err != nil {
		return nil, err
	}
	return set, nil
}

// checkBranchShapes requires every branch to yield the same columns. The compiler already
// forces one result type across branches; this catches a branch that fills a different set
// of its fields, which SQL would either reject or line up by position.
func checkBranchShapes(set *query.ParseSet) error {
	first := projectedNames(set.Branches[0])
	for i, branch := range set.Branches[1:] {
		names := projectedNames(branch)
		if len(names) != len(first) {
			return fmt.Errorf(
				"%w: branch 1 selects %d column(s) but branch %d selects %d — every branch of "+
					"a set operation must yield the same columns",
				ErrInvalidLambda, len(first), i+2, len(names))
		}
		for _, name := range first {
			if !containsString(names, name) {
				return fmt.Errorf(
					"%w: branch %d does not select %s, which branch 1 does",
					ErrInvalidLambda, i+2, name)
			}
		}
	}
	return nil
}

func projectedNames(q *query.ParseQuery) []string {
	names := make([]string, 0, len(q.Body.Select))
	for _, sel := range q.Body.Select {
		names = append(names, sel.Into)
	}
	return names
}

func containsString(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}
