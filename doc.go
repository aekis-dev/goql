// Package goql is an ORM whose queries are written as ordinary Go functions.
//
// # Lambda bodies are inspected, not executed
//
// This is the single most important thing to know about goql. When you write:
//
//	goql.Update[Order](ctx, e, func(o *Order) {
//	    if o.Total > 1000 {
//	        o.Priority = "High"
//	    }
//	})
//
// that function is never called. goql locates its source, parses it with go/ast and
// compiles the statements into SQL — here, one UPDATE with a WHERE clause. The
// assignment describes what the statement should do; it does not perform it. Nothing
// you write inside a lambda has any runtime effect, and the parameter is a pointer
// only so the body reads as the mutation the generated statement performs.
//
// Two consequences follow from this, and they explain most of the API:
//
//   - A lambda may not reference variables from its surrounding scope. There is no way
//     to read a closure's captured values by reflection, so an unresolved identifier is
//     a parse error rather than a silently wrong query. Call-time values are passed
//     explicitly through a params struct — see [Select].
//
//   - Only expressions goql can parse are allowed. Comparison and logical operators,
//     if/else and switch, ranging over a relation, and [Condition] for operators Go has
//     no syntax for (LIKE, IN, IS NULL).
//
// A goql call written inside a body is parsed as well, and becomes a subquery — see
// [Unwrap].
//
// # Two ways to say each thing
//
// Struct-based calls describe rows by example; lambda-based calls describe them by
// predicate. They are separate functions so that per-query options attach unambiguously:
//
//	create   Create   Insert (INSERT … SELECT)
//	read     Search   Select, Exists
//	update   Write    Update
//	delete   Remove   Delete
//
// One model and one operation per call. Results are typed: every read returns []*T,
// with no casts at the call site.
//
// # Execution modes
//
// In development, bodies are parsed from source at runtime and cached. Because a
// released binary has no source to read, a build tagged "prod" instead consults a
// registry generated ahead of time by the same parser — see the generator package.
// Run go generate before every -tags prod build; the registry is keyed positionally,
// so a stale one can resolve to the wrong lambda.
//
// # Getting started
//
//	e := goql.New(db, goql.SQLite{})
//	orders, err := goql.Select[Order](ctx, e, func(o *Order) bool {
//	    return o.Priority == "High"
//	})
//
// Schemas are declared imperatively in each model's init() via models.AddModel.
// See the models package for field metadata and relations, the query package for the
// SQL builders and dialects, and examples/demo for a program exercising the API.
package goql
