package tests

import (
	"testing"

	"github.com/aekis-dev/goql"
)

// call runs a function, so a closure can be written nested inside another closure.
func call(fn func()) { fn() }

// A lambda is located by the runtime's funcN index, which the compiler assigns to the
// literals written *directly* in the enclosing function — 1..n in source order. A closure
// nested inside another one continues the same counter but is named under its parent
// (Outer.func1.func3), so it never takes a sibling's number.
//
// goql used to count every literal flat. The nested closure below therefore consumed
// index 2, and the goql lambda that the runtime calls func2 was looked up as index 3 —
// resolving to the wrong body, or to none.
func TestParse_NestedClosureDoesNotShiftNumbering(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()
	seedData(t, ctx, e)

	// Literal #1 in this function, with a closure nested inside it.
	call(func() {
		call(func() {})
	})

	// Literal #2: the compiler names it func2 regardless of the nested one above.
	canada, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return c.Country == "Canada"
	})
	if err != nil {
		t.Fatalf("the nested closure shifted this lambda's index: %v", err)
	}
	if len(canada) != 1 || canada[0].Country != "Canada" {
		t.Fatalf("expected the Canada customer, got %+v — another lambda's body was used", canada)
	}
}
