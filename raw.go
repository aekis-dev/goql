package goql

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"

	"github.com/aekis-dev/goql/query"
)

// Execute runs a statement written by hand, for whatever the lambda language cannot yet
// express. It is the same execution path the generated statements use, so it participates
// in the surrounding transaction and honours ctx.
//
// The real sql.Result is returned rather than a row count, so LastInsertId remains
// available.
func Execute(ctx context.Context, e *Engine, sqlText string, args ...any) (sql.Result, error) {
	return e.withCall(ctx, nil).exec(sqlText, args...)
}

// Bind runs a query written by hand and scans the rows into entities, using exactly the
// same column-mapping path as Select and Search — columns are matched to fields by their
// database name, so a projection narrower than the model is fine and the missing fields
// stay zero-valued.
func Bind[T any](ctx context.Context, e *Engine, sqlText string, args ...any) ([]*T, error) {
	if _, err := entityOf[T](); err != nil {
		return nil, err
	}

	rows, err := e.withCall(ctx, nil).query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results, err := scanRows(rows, reflect.TypeFor[T]())
	if err != nil {
		return nil, err
	}
	return typedResults[T](results)
}

// Exists reports whether any row matches a predicate lambda, stopping at the first match.
func Exists[T any](ctx context.Context, e *Engine, pred any, params ...any) (bool, error) {
	value, err := lambdaParams[T](pred, true, params)
	if err != nil {
		return false, err
	}

	scoped := e.withCall(ctx, value)
	parsed, err := scoped.parsePredicate(pred, "Exists")
	if err != nil {
		return false, err
	}

	q, err := scoped.dialect.LambdaExists(parsed)
	if err != nil {
		return false, err
	}

	rows, err := scoped.query(q.SQL, q.Args...)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	found := rows.Next()
	return found, rows.Err()
}

// parsePredicate parses a predicate lambda into a query.
func (ctx *Engine) parsePredicate(fn any, call string) (*query.ParseQuery, error) {
	fnType := reflect.TypeOf(fn)
	if fnType == nil || fnType.Kind() != reflect.Func || fnType.NumIn() == 0 {
		return nil, fmt.Errorf("%w: expected a lambda", ErrInvalidLambda)
	}
	return ctx.executor.ParseQuery(fn, call)
}

// Unwrap discards the error of a goql call so it can be nested inside a lambda:
//
//	goql.Condition(o.Customer, "IN",
//	    goql.Unwrap(goql.Select[Customer](ctx, e, func(c *Customer) bool { … })))
//
// It exists for the type checker, not for runtime: inside a lambda nothing is executed, and
// passing a call as a function's entire argument list is the one place Go allows a
// two-value call to be used as an argument. Calling it outside a lambda simply returns the
// value and drops the error, which is rarely what you want — handle the error instead.
func Unwrap[T any](value T, _ error) T { return value }

// Filter reports whether any element of a collection satisfies pred.
//
// Inside a lambda it is parsed, not executed, and compiles to a correlated EXISTS over the
// relation:
//
//	goql.Select[Order](ctx, e, func(o *Order) bool {
//	    return o.Total > 200 ||
//	        goql.Filter(o.Tags, func(t *Tag) bool { return t.Name == "urgent" })
//	})
//
// EXISTS rather than a JOIN because a filter is a *predicate*: it answers a question about
// the row under consideration without changing how many rows come back, which is what lets it
// appear inside ||, ! and a branch arm. Negate it with ! for NOT EXISTS.
//
// Unlike most of goql's lambda vocabulary this is a real function, so calling it on a loaded
// entity outside a query returns the answer you would expect.
func Filter[T any](collection []T, pred func(*T) bool) bool {
	for i := range collection {
		if pred(&collection[i]) {
			return true
		}
	}
	return false
}

// selectProjected runs a query whose result type is not a model, scanning each row into T by
// matching column aliases to field names. Column order therefore never matters, and an
// embedded model in T is filled like any other field.
func selectProjected[T any](e *Engine, pred any) ([]*T, error) {
	parsed, err := e.executor.ParseQuery(pred, "Select")
	if err != nil {
		return nil, fmt.Errorf("failed to parse predicate: %w", err)
	}

	q, err := e.dialect.LambdaSearch(parsed, parsed.Body.Options)
	if err != nil {
		return nil, fmt.Errorf("failed to build search query: %w", err)
	}

	rows, err := e.query(q.SQL, q.Args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var results []*T
	for rows.Next() {
		holders := make([]any, len(columns))
		for i := range holders {
			holders[i] = new(any)
		}
		if err := rows.Scan(holders...); err != nil {
			return nil, err
		}

		row := new(T)
		value := reflect.ValueOf(row).Elem()
		for i, column := range columns {
			field := value.FieldByName(column)
			if !field.IsValid() || !field.CanSet() {
				// A column with no matching field is not an error: the projection named it,
				// and the parser already checked every name it produced.
				continue
			}
			if err := setFieldValue(field, *(holders[i].(*any))); err != nil {
				return nil, fmt.Errorf("column %s: %w", column, err)
			}
		}
		results = append(results, row)
	}
	return results, rows.Err()
}
