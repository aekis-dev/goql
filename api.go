package goql

import (
	"context"
	"fmt"
	"reflect"

	"github.com/aekis-dev/goql/models"
)

// The public API is split by how a query is expressed, and every call operates on
// exactly one model and one operation.
//
// Struct-based, where the values come from entity structs you already have:
//
//	Create[T]  insert            Search[T]  select by example
//	Write[T]   update by diff    Remove[T]  delete by primary key
//	Get[T]     select by primary key
//
// Lambda-based, named after the SQL they compile into:
//
//	Select[T]     select by predicate
//	Insert[D]     insert … select, from another model
//	Update[T]     update by predicate
//	Delete[T]     delete by predicate
//
// Lambda bodies are **parsed from source, never executed**. Their statements describe
// the SQL to generate; assigning to a field does not mutate anything at runtime. The
// entity parameter must be a pointer so the body reads as the mutation it describes.

// Create inserts entities, filling in generated primary keys.
//
// The returned pointers alias the slice you passed, so primary keys are visible through
// either one.
func Create[T any](ctx context.Context, e *Engine, entities []T) ([]*T, error) {
	if len(entities) == 0 {
		return nil, nil
	}
	boxed, ptrs, err := entityPointers(entities)
	if err != nil {
		return nil, err
	}
	if _, err := e.withCall(ctx, nil).createAny(boxed); err != nil {
		return nil, err
	}
	return ptrs, nil
}

// Search finds rows matching a non-zero-valued example ("query by example"). An example
// with its primary key set matches on the key alone.
func Search[T any](ctx context.Context, e *Engine, example T, opts ...any) ([]*T, error) {
	if _, err := entityOf[T](); err != nil {
		return nil, err
	}
	options, err := buildOptions(opts)
	if err != nil {
		return nil, err
	}
	results, err := e.withCall(ctx, nil).searchAny(&example, options)
	if err != nil {
		return nil, err
	}
	return typedResults[T](results)
}

// Get finds rows by primary key.
//
// ids is either a single key or a slice of them, so both readings stay clean:
//
//	user, err := goql.Get[User](ctx, e, userID)
//	users, err := goql.Get[User](ctx, e, []int64{1, 2, 3})
//
// One key emits an equality and several an IN list. A key that matches nothing is simply
// absent from the result, so a miss is an empty slice rather than an error, and the
// results are in whatever order the engine returns them — pass goql.Sort to fix one.
func Get[T any](ctx context.Context, e *Engine, ids any, opts ...any) ([]*T, error) {
	entity, err := entityOf[T]()
	if err != nil {
		return nil, err
	}
	keys, err := keyList(ids)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, nil
	}
	options, err := buildOptions(opts)
	if err != nil {
		return nil, err
	}
	results, err := e.withCall(ctx, nil).getByKeys(entity, keys, options)
	if err != nil {
		return nil, err
	}
	return typedResults[T](results)
}

// keyList normalises Get's ids argument into the keys to bind. A slice or array is its
// elements; anything else is one key. []byte is a single key, since that is how a binary
// primary key arrives rather than a list of bytes.
func keyList(ids any) ([]any, error) {
	if ids == nil {
		return nil, fmt.Errorf("%w: Get needs a primary key, got nil", ErrInvalidParams)
	}

	v := reflect.ValueOf(ids)
	switch v.Kind() {
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			break
		}
		keys := make([]any, v.Len())
		for i := range keys {
			keys[i] = v.Index(i).Interface()
		}
		return keys, nil
	}
	return []any{ids}, nil
}

// Write updates entities by diffing them against the values they were loaded with, and
// reports the number of rows affected.
func Write[T any](ctx context.Context, e *Engine, entities []T) (int64, error) {
	if len(entities) == 0 {
		return 0, nil
	}
	boxed, _, err := entityPointers(entities)
	if err != nil {
		return 0, err
	}
	return e.withCall(ctx, nil).writeAny(boxed)
}

// Remove deletes entities by primary key and reports the number of rows affected.
func Remove[T any](ctx context.Context, e *Engine, entities []T) (int64, error) {
	if len(entities) == 0 {
		return 0, nil
	}
	boxed, _, err := entityPointers(entities)
	if err != nil {
		return 0, err
	}
	return e.withCall(ctx, nil).deleteAny(boxed)
}

// Select finds rows matching a predicate lambda, for example:
//
//	goql.Select[Customer](ctx, e, func(c *Customer) bool {
//	    return c.Country == "USA" && c.Age > 40
//	})
//
// The predicate is passed as any rather than func(*T) bool because option and parameter
// carriers may be declared as extra parameters; its shape is validated here.
func Select[T any](ctx context.Context, e *Engine, pred any, params ...any) ([]*T, error) {
	value, err := lambdaParams[T](pred, true, params)
	if err != nil {
		return nil, err
	}
	scoped := e.withCall(ctx, value)

	// A result type that is not a model means the lambda projects an explicit list of
	// columns — grouping keys and aggregates — which scans into T rather than into rows.
	if _, isModel := any(new(T)).(models.Entity); !isModel {
		return selectProjected[T](scoped, pred)
	}

	// Query modifiers are declared as extra parameters of the lambda itself, so only the
	// params struct crosses the call boundary here.
	results, err := scoped.searchAny(pred, nil)
	if err != nil {
		return nil, err
	}
	return typedResults[T](results)
}

// Update applies the assignments in a lambda to every row matching its conditions, and
// reports the number of rows affected. An if/else or switch in the body produces one
// UPDATE per arm, each scoped to its own mutually exclusive condition.
func Update[T any](ctx context.Context, e *Engine, mutate any, params ...any) (int64, error) {
	value, err := lambdaParams[T](mutate, false, params)
	if err != nil {
		return 0, err
	}
	return e.withCall(ctx, value).writeAny(mutate)
}

// Insert copies rows from one model into another with INSERT … SELECT, and reports how many
// rows were inserted. The type parameter names the model being written; the lambda's first
// parameter is that destination and its second is the source read from:
//
//	goql.Insert[OrderArchive](ctx, e, func(a *OrderArchive, o *Order) {
//	    if o.Total > 1000 {
//	        a.Total  = o.Total
//	        a.Reason = "high value"
//	    }
//	})
//
// The source needs no type parameter: it is named by the lambda, which is the only place it
// could be checked against anyway.
//
// Each assignment supplies both halves of the statement: the left side names a destination
// column, the right side an expression selected from the source — a source field, a literal,
// or a params-struct value. Conditions filter the SELECT, and an if/else or switch produces
// one statement per arm, as it does for Update. Nothing is executed; the body is parsed.
//
// The rows are made by the database, so no entities come back: recovering generated keys is
// only portable where INSERT … RETURNING exists.
func Insert[D any](ctx context.Context, e *Engine, mutate any, params ...any) (int64, error) {
	value, err := insertLambdaParams[D](mutate, params)
	if err != nil {
		return 0, err
	}
	return e.withCall(ctx, value).insertAny(mutate)
}

// Delete removes every row matching a predicate lambda and reports the number of rows
// affected.
func Delete[T any](ctx context.Context, e *Engine, pred any, params ...any) (int64, error) {
	value, err := lambdaParams[T](pred, true, params)
	if err != nil {
		return 0, err
	}
	return e.withCall(ctx, value).deleteAny(pred)
}

// entityOf returns a zero *T as an Entity, failing with a clear message when the model
// does not embed goql.Model.
func entityOf[T any]() (models.Entity, error) {
	entity, ok := any(new(T)).(models.Entity)
	if !ok {
		return nil, fmt.Errorf("%w: *%s — embed goql.Model in the struct",
			ErrNotEntity, reflect.TypeFor[T]().Name())
	}
	return entity, nil
}

// entityPointers boxes a slice of values as Entities. The pointers alias the caller's
// slice, so values written back during Create (primary keys, timestamps) are visible to
// the caller too.
func entityPointers[T any](entities []T) ([]models.Entity, []*T, error) {
	boxed := make([]models.Entity, 0, len(entities))
	ptrs := make([]*T, 0, len(entities))

	for i := range entities {
		ptr := &entities[i]
		entity, ok := any(ptr).(models.Entity)
		if !ok {
			return nil, nil, fmt.Errorf("%w: *%s — embed goql.Model in the struct",
				ErrNotEntity, reflect.TypeFor[T]().Name())
		}
		boxed = append(boxed, entity)
		ptrs = append(ptrs, ptr)
	}
	return boxed, ptrs, nil
}

// typedResults converts scanned entities back into the caller's concrete type.
func typedResults[T any](results []any) ([]*T, error) {
	out := make([]*T, 0, len(results))
	for _, result := range results {
		typed, ok := result.(*T)
		if !ok {
			return nil, fmt.Errorf("goql: internal error: scanned %T where *%s was expected",
				result, reflect.TypeFor[T]().Name())
		}
		out = append(out, typed)
	}
	return out, nil
}

// validateLambda checks a lambda's shape before it is parsed.
//
// The entity parameter must be a pointer: `func(c Customer) { c.Status = "x" }` reads as
// dead code to any Go developer (and linters flag it), while the pointer form reads as
// the mutation the generated UPDATE performs. Both forms parsed identically before, so
// the one signal Go gives for mutation carried no meaning.
func validateLambda[T any](fn any, wantPredicate bool) error {
	fnType := reflect.TypeOf(fn)
	if fnType == nil || fnType.Kind() != reflect.Func {
		return fmt.Errorf("%w: expected a function, got %T", ErrInvalidLambda, fn)
	}

	want := reflect.TypeFor[T]()

	if fnType.NumIn() == 0 {
		return fmt.Errorf("%w: lambda must take *%s as its first parameter",
			ErrInvalidLambda, want.Name())
	}

	first := fnType.In(0)
	if first.Kind() != reflect.Ptr {
		// A leading []*T is the self handle of a recursive query. That form is only
		// meaningful for a query bound inside another lambda and read through goql.From,
		// because the rows it accumulates need an enclosing query to select from them.
		// Reporting it as "not a pointer" sends the reader to fix the wrong thing.
		if first.Kind() == reflect.Slice && first.Elem() == reflect.PointerTo(want) {
			return fmt.Errorf(
				"%w: a lambda taking []*%s is the self handle of a recursive query, which "+
					"has to be bound inside another lambda and read with from.Query — it "+
					"cannot be passed straight to this call",
				ErrInvalidLambda, want.Name())
		}
		return fmt.Errorf(
			"%w: first parameter must be *%s, not %s — the body describes the row being "+
				"matched, so it is written as a mutation through a pointer",
			ErrInvalidLambda, want.Name(), first.Name())
	}
	if first.Elem() != want {
		return fmt.Errorf("%w: lambda takes *%s but the call is for %s",
			ErrInvalidLambda, first.Elem().Name(), want.Name())
	}

	if wantPredicate {
		if fnType.NumOut() != 1 || fnType.Out(0).Kind() != reflect.Bool {
			return fmt.Errorf("%w: predicate must return bool", ErrInvalidLambda)
		}
		return nil
	}
	if fnType.NumOut() != 0 {
		return fmt.Errorf("%w: write lambda must not return a value", ErrInvalidLambda)
	}
	return nil
}

// optionTypes are the carrier types recognised as lambda parameters, mirroring
// optionNames but as reflect types so a lambda's signature can be inspected.
var optionTypes = map[reflect.Type]bool{
	reflect.TypeFor[Sort]():     true,
	reflect.TypeFor[Limit]():    true,
	reflect.TypeFor[Offset]():   true,
	reflect.TypeFor[Fields]():   true,
	reflect.TypeFor[Preload]():  true,
	reflect.TypeFor[Conflict](): true,
	reflect.TypeFor[From]():     true,
	reflect.TypeFor[Group]():    true,
	reflect.TypeFor[Join]():     true,
}

// lambdaParams validates a lambda and reconciles its params-struct parameter, if any,
// with the value supplied at the call site.
//
// Extra parameters are classified by type: a pointer to a known option carrier is a query
// modifier, and the one remaining kind is the params struct carrying call-time values.
func lambdaParams[T any](fn any, wantPredicate bool, supplied []any) (any, error) {
	if err := validateLambda[T](fn, wantPredicate); err != nil {
		return nil, err
	}

	return reconcileParams(reflect.TypeOf(fn), 1, supplied)
}

// reconcileParams matches a lambda's params-struct parameter, if it declares one, against
// the value supplied at the call site. start is the number of leading entity parameters.
func reconcileParams(fnType reflect.Type, start int, supplied []any) (any, error) {
	paramsType, err := paramsTypeFrom(fnType, start)
	if err != nil {
		return nil, err
	}

	switch {
	case paramsType == nil && len(supplied) == 0:
		return nil, nil

	case paramsType == nil:
		return nil, fmt.Errorf(
			"%w: %d value(s) supplied but the lambda declares no params struct",
			ErrInvalidParams, len(supplied))

	case len(supplied) == 0:
		return nil, fmt.Errorf("%w: the lambda declares %s but no value was supplied",
			ErrMissingParams, paramsType)

	case len(supplied) > 1:
		return nil, fmt.Errorf("%w: expected one params value, got %d",
			ErrInvalidParams, len(supplied))
	}

	value := supplied[0]
	if actual := reflect.TypeOf(value); actual != paramsType {
		return nil, fmt.Errorf("%w: the lambda declares %s but %s was supplied",
			ErrInvalidParams, paramsType, actual)
	}
	return value, nil
}

// insertLambdaParams validates an Insert lambda — destination first, source second — and
// reconciles its params struct with the value supplied at the call site.
//
// Only the destination is named by a type parameter. The source is whichever model the
// lambda declares second, so it is checked for being a model at all rather than against a
// type argument that could only ever restate it.
func insertLambdaParams[D any](fn any, supplied []any) (any, error) {
	fnType := reflect.TypeOf(fn)
	if fnType == nil || fnType.Kind() != reflect.Func {
		return nil, fmt.Errorf("%w: expected a function, got %T", ErrInvalidLambda, fn)
	}
	if fnType.NumIn() < 2 {
		return nil, fmt.Errorf(
			"%w: an Insert lambda takes the destination and the source: func(d *%s, s *Source)",
			ErrInvalidLambda, reflect.TypeFor[D]().Name())
	}
	if err := checkEntityParam[D](fnType, 0, "destination"); err != nil {
		return nil, err
	}
	if !isEntityParam(fnType, 1) {
		return nil, fmt.Errorf(
			"%w: the second parameter must be a pointer to the model rows are copied from, "+
				"got %s", ErrInvalidLambda, fnType.In(1))
	}
	if fnType.NumOut() != 0 {
		return nil, fmt.Errorf("%w: an Insert lambda must not return a value", ErrInvalidLambda)
	}
	return reconcileParams(fnType, 2, supplied)
}

// checkEntityParam verifies that a lambda's nth parameter is a pointer to the expected model.
func checkEntityParam[T any](fnType reflect.Type, index int, role string) error {
	want := reflect.TypeFor[T]()
	got := fnType.In(index)
	if got.Kind() != reflect.Ptr {
		return fmt.Errorf(
			"%w: the %s parameter must be *%s, not %s — the body describes the row being "+
				"built, so it is written as a mutation through a pointer",
			ErrInvalidLambda, role, want.Name(), got.Name())
	}
	if got.Elem() != want {
		return fmt.Errorf("%w: the %s parameter is *%s but the call is for %s",
			ErrInvalidLambda, role, got.Elem().Name(), want.Name())
	}
	return nil
}

// paramsTypeOf returns the type of a lambda's params-struct parameter, or nil when it
// declares none. Extra parameters are classified by type: a pointer to a known option
// carrier is a query modifier, anything else is the params struct.
func paramsTypeOf(fnType reflect.Type) (reflect.Type, error) {
	return paramsTypeFrom(fnType, 1)
}

// paramsTypeFrom is paramsTypeOf starting past a given number of entity parameters, which
// is two for an Insert.
func paramsTypeFrom(fnType reflect.Type, start int) (reflect.Type, error) {
	var paramsType reflect.Type
	for i := start; i < fnType.NumIn(); i++ {
		in := fnType.In(i)
		if in.Kind() == reflect.Ptr && optionTypes[in.Elem()] {
			continue
		}
		// A pointer to another model is a join participant, not the params struct.
		if isEntityParam(fnType, i) {
			continue
		}
		// A pointer to anything else stands for a row — of a CTE the body reads from.
		// A params struct is passed by value, which is what distinguishes them.
		if in.Kind() == reflect.Ptr {
			continue
		}
		if paramsType != nil {
			return nil, fmt.Errorf("%w: a lambda may declare at most one params struct",
				ErrInvalidParams)
		}
		paramsType = in
	}
	return paramsType, nil
}
