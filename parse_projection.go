//go:build !prod

package goql

import (
	"fmt"
	"go/ast"
	"go/types"
	"reflect"

	"github.com/aekis-dev/goql/models"
	"github.com/aekis-dev/goql/query"
)

// A query whose result type is not a model projects an explicit list of columns, written as
// assignments to the result:
//
//	goql.Select[OrderTotals](ctx, e, func(t *OrderTotals, o *Order, from *goql.From) bool {
//	    from.Model = o
//	    t.Customer = o.Customer
//	    t.Total    = goql.Sum(o.Total)
//	    return o.Priority == "High"
//	})
//
// Each assignment is one output column; the ones that are not aggregates are the GROUP BY,
// which is SQL's own rule rather than a second declaration.

// aggregateMarkers are the parse-only functions that may appear on the right of a projection
// assignment, mapped to the SQL they render as. They take a column rather than a lambda, so
// they are not the nestable subquery functions.
var aggregateMarkers = map[string]string{
	"Sum":   "Sum",
	"Avg":   "Avg",
	"Min":   "Min",
	"Max":   "Max",
	"Count": "Count",
}

// setFromModel handles `from.Model = o`, which names the model a projection query reads
// from. The right-hand side is one of the lambda's own model parameters, so the declaration
// and the query cannot disagree.
func (e *DebugExecutor) setFromModel(field string, rhs ast.Expr, pctx *parseContext) error {
	if field != "Model" {
		return fmt.Errorf("%w: From has no field %s", ErrInvalidOption, field)
	}

	name := baseIdentName(rhs)
	schema, declared := pctx.participants[name]
	if !declared {
		return fmt.Errorf(
			"%w: from.Model must be one of the lambda's model parameters, got %s",
			ErrInvalidLambda, types.ExprString(rhs))
	}

	pctx.schema = schema
	pctx.paramName = name
	return nil
}

// tryParseProjection recognises an assignment to the result type — `t.Total = goql.Sum(o.Total)`
// or `t.Customer = o.Customer` — and records it as one projected column.
func (e *DebugExecutor) tryParseProjection(s *ast.AssignStmt, pctx *parseContext) (*query.ParseSelect, error) {
	if pctx.resultName == "" {
		return nil, nil
	}
	if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
		return nil, nil
	}
	sel, ok := s.Lhs[0].(*ast.SelectorExpr)
	if !ok {
		return nil, nil
	}
	base, ok := sel.X.(*ast.Ident)
	if !ok || base.Name != pctx.resultName {
		return nil, nil
	}

	into := sel.Sel.Name
	if pctx.resultType != nil {
		if _, found := pctx.resultType.FieldByName(into); !found {
			return nil, fmt.Errorf("%w: %s has no field %s",
				ErrInvalidLambda, pctx.resultType.Name(), into)
		}
	}

	// An aggregate marker on the right; anything else must be a plain column.
	if call, isCall := s.Rhs[0].(*ast.CallExpr); isCall {
		fn, isMarker := aggregateMarkers[calleeName(call.Fun)]
		if !isMarker {
			return nil, fmt.Errorf(
				"%w: %s is not something a projection can select — use a column or an "+
					"aggregate (goql.Sum, goql.Avg, goql.Min, goql.Max, goql.Count)",
				ErrUnsupportedExpr, types.ExprString(call.Fun))
		}
		return e.projectionFromMarker(fn, call, into, pctx)
	}

	ref, err := e.resolveFieldRefIn(s.Rhs[0], pctx)
	if err != nil {
		return nil, fmt.Errorf("projected column %s: %w", into, err)
	}
	return &query.ParseSelect{Field: ref, Into: into}, nil
}

// projectionFromMarker turns goql.Sum(o.Total) into a projected aggregate.
func (e *DebugExecutor) projectionFromMarker(fn string, call *ast.CallExpr, into string, pctx *parseContext) (*query.ParseSelect, error) {
	switch len(call.Args) {
	case 0:
		// Only Count means anything without a column: it counts rows.
		if fn != "Count" {
			return nil, fmt.Errorf("%w: goql.%s needs the column to aggregate",
				ErrInvalidLambda, fn)
		}
		return &query.ParseSelect{Func: fn, Into: into}, nil

	case 1:
		ref, err := e.resolveFieldRefIn(call.Args[0], pctx)
		if err != nil {
			return nil, fmt.Errorf("goql.%s: %w", fn, err)
		}
		if err := checkAggregateColumn(fn, ref); err != nil {
			return nil, err
		}
		return &query.ParseSelect{Func: fn, Field: ref, Into: into}, nil

	default:
		return nil, fmt.Errorf("%w: goql.%s takes one column, got %d",
			ErrInvalidLambda, fn, len(call.Args))
	}
}

// checkAggregateColumn refuses arithmetic over a column that holds no number. SQLite would
// quietly answer 0 where Postgres errors — the same code silently wrong on one engine and
// broken on the other.
func checkAggregateColumn(fn string, ref *query.FieldRef) error {
	switch fn {
	case "Sum", "Avg":
	default:
		return nil
	}
	field := ref.Field
	if ref.Nested != nil {
		field = ref.Nested.Field
	}
	if !field.LogicalType().IsNumeric() {
		return fmt.Errorf("%w: goql.%s needs a numeric column, but %s is %s",
			ErrInvalidLambda, fn, field.ColumnName(), field.LogicalType())
	}
	return nil
}

// parseProjectionLambda parses a lambda whose result type is not a model. The model comes
// from a `from.Model = …` statement in the body rather than from the first parameter, which
// here is the result being built.
func (e *DebugExecutor) parseProjectionLambda(fn any, funcType reflect.Type, funcLit *ast.FuncLit, params []lambdaParam, id runtimeLambdaID) (*query.ParseQuery, error) {
	resultType := funcType.In(0)
	if resultType.Kind() != reflect.Ptr {
		return nil, fmt.Errorf(
			"%w: the result parameter must be a pointer (func(r *%s, …)), got %s",
			ErrInvalidLambda, resultType.Name(), resultType)
	}
	resultType = resultType.Elem()

	// The models the body may read from are the lambda's other model parameters; which one
	// the query reads from is stated by from.Model.
	pctx := newParseContext(nil, "")
	pctx.resultName = paramNameAt(params, 0)
	pctx.resultType = resultType

	for i := 1; i < funcType.NumIn(); i++ {
		if !isEntityParam(funcType, i) {
			continue
		}
		schema, err := schemaOfParam(funcType, i)
		if err != nil {
			return nil, err
		}
		pctx.addParticipant(paramNameAt(params, i), schema)
	}
	if err := pctx.classifyParams(funcLit); err != nil {
		return nil, err
	}
	var err error
	if pctx.paramsType, err = paramsTypeFrom(funcType, 1); err != nil {
		return nil, err
	}

	body, err := e.parseBody(funcLit, pctx)
	if err != nil {
		return nil, err
	}

	// A set operation is the query: its branches carry the models and the projections, so the
	// combining lambda declares neither.
	if body.Set == nil {
		if pctx.schema == nil {
			return nil, fmt.Errorf(
				"%w: %s does not say which model to read from — assign one of the lambda's model "+
					"parameters to a *goql.From, e.g. from.Model = o",
				ErrInvalidLambda, resultType.Name())
		}
		if len(body.Select) == 0 {
			return nil, fmt.Errorf(
				"%w: %s selects nothing — assign its fields from columns or aggregates",
				ErrInvalidLambda, resultType.Name())
		}
	}

	parsed := &query.ParseQuery{Model: modelName(pctx.schema), Body: body}
	if err := query.ValidateQuery(parsed, false); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidLambda, err)
	}

	e.storeBody(id.cacheKey(), parsed)
	return parsed, nil
}

// parseProjectionSource is parseProjectionLambda for the generator, which has source text
// rather than runtime types. The result type's fields cannot be checked here — the Go
// compiler has already done that at the call site.
func (e *DebugExecutor) parseProjectionSource(funcLit *ast.FuncLit, params []lambdaParam) (*query.ParseQuery, error) {
	pctx := newParseContext(nil, "")
	pctx.resultName = paramNameAt(params, 0)

	for i := 1; i < len(params); i++ {
		if schema := sourceModelFromParam(params, i); schema != nil {
			pctx.addParticipant(params[i].name, schema)
		}
	}
	if err := pctx.classifyParams(funcLit); err != nil {
		return nil, err
	}

	body, err := e.parseBody(funcLit, pctx)
	if err != nil {
		return nil, err
	}

	if body.Set == nil {
		if pctx.schema == nil {
			return nil, fmt.Errorf(
				"%w: the query does not say which model to read from — assign one of the lambda's "+
					"model parameters to a *goql.From, e.g. from.Model = o", ErrInvalidLambda)
		}
		if len(body.Select) == 0 {
			return nil, fmt.Errorf("%w: the projection selects nothing", ErrInvalidLambda)
		}
	}

	parsed := &query.ParseQuery{Model: modelName(pctx.schema), Body: body}
	if err := query.ValidateQuery(parsed, false); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidLambda, err)
	}
	return parsed, nil
}

// aggregateOperand recognises an aggregate marker used as one side of a comparison —
// goql.Sum(o.Total) > 1000 — which filters groups rather than rows. Returns nil when the
// expression is something else.
func (e *DebugExecutor) aggregateOperand(expr ast.Expr, pctx *parseContext) (*query.ParseSelect, error) {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return nil, nil
	}
	fn, isMarker := aggregateMarkers[calleeName(call.Fun)]
	if !isMarker {
		return nil, nil
	}
	return e.projectionFromMarker(fn, call, "", pctx)
}

// modelName is the schema's type name, or empty for a query that has no model of its own —
// a set operation, whose branches carry theirs.
func modelName(schema *models.Model) string {
	if schema == nil {
		return ""
	}
	return schema.Type.Name()
}
