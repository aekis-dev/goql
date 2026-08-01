package tests

import (
	"errors"
	"strings"
	"testing"

	"github.com/aekis-dev/goql"
)

// A goql call inside a lambda body is parsed like everything else, and compiles to a
// subquery. Named form: the result can be referenced more than once.
func TestSubquery_NamedInCondition(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()
	seedData(t, ctx, e)

	orders, err := goql.Select[Order](ctx, e, func(o *Order) bool {
		usa, _ := goql.Select[Customer](ctx, e, func(c *Customer) bool {
			return c.Country == "USA"
		})
		return goql.Condition(o.Customer, "IN", usa)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 {
		t.Fatalf("expected the 1 order of the USA customer, got %d", len(orders))
	}
	if orders[0].Total != 1500 {
		t.Errorf("wrong order came back: %+v", orders[0])
	}
}

// Nested directly, via Unwrap.
func TestSubquery_NestedDirectly(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()
	seedData(t, ctx, e)

	orders, err := goql.Select[Order](ctx, e, func(o *Order) bool {
		return goql.Condition(o.Customer, "IN",
			goql.Unwrap(goql.Select[Customer](ctx, e, func(c *Customer) bool {
				return c.Country == "Canada"
			})))
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 || orders[0].Total != 700 {
		t.Fatalf("expected the Canada customer's order, got %+v", orders)
	}
}

// goql.Fields inside the nested lambda chooses the projected column.
func TestSubquery_ProjectsNamedField(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()
	seedData(t, ctx, e)

	// Match orders whose shipping method equals some customer's country — nonsense data,
	// but it proves the projection is the named column rather than the primary key.
	rows, err := goql.Select[Order](ctx, e, func(o *Order) bool {
		return goql.Condition(o.ShippingMethod, "IN",
			goql.Unwrap(goql.Select[Customer](ctx, e, func(c *Customer, f *goql.Fields) bool {
				f.Names = []string{"Country"}
				return c.Country == "USA"
			})))
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected no matches, got %d", len(rows))
	}
}

// A nested Exists is a condition on its own, and correlates with the outer row.
func TestSubquery_CorrelatedExists(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()
	seedData(t, ctx, e)

	rows, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return goql.Unwrap(goql.Exists[Order](ctx, e, func(o *Order) bool {
			return o.Customer == c && o.Total > 1000
		}))
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Country != "USA" {
		t.Fatalf("expected only the customer with a big order, got %+v", rows)
	}
}

// Only predicate-shaped calls can nest: a write is not a value.
func TestSubquery_RejectsNestedWrite(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()

	_, err := goql.Select[Order](ctx, e, func(o *Order) bool {
		return goql.Condition(o.Customer, "IN",
			goql.Unwrap(goql.Select[Customer](ctx, e, func(c *Customer) bool {
				c.Status = "mutated"
				return true
			})))
	})
	if !errors.Is(err, goql.ErrInvalidLambda) {
		t.Fatalf("expected ErrInvalidLambda for an assigning sub-lambda, got %v", err)
	}
}

// The enclosing query already uses the table, so both would render with one alias.
func TestSubquery_RejectsSameTable(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()
	seedData(t, ctx, e)

	_, err := goql.Select[Order](ctx, e, func(o *Order) bool {
		return goql.Condition(o.Priority, "IN",
			goql.Unwrap(goql.Select[Order](ctx, e, func(x *Order) bool {
				return x.Total > 1000
			})))
	})
	if err == nil || !strings.Contains(err.Error(), "same alias") {
		t.Fatalf("expected a clear alias error, got %v", err)
	}
}

// Nothing inside a lambda is executed, so a nested call has no error to inspect. Writing
// the habitual `if err != nil` used to fail with "field err not found in models orders";
// it now says what is actually wrong.
func TestSubquery_RejectsInspectingNestedError(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()
	seedData(t, ctx, e)

	_, err := goql.Select[Order](ctx, e, func(o *Order) bool {
		usa, subErr := goql.Select[Customer](ctx, e, func(c *Customer) bool {
			return c.Country == "USA"
		})
		if subErr != nil {
			return false
		}
		return goql.Condition(o.Customer, "IN", usa)
	})
	if !errors.Is(err, goql.ErrInvalidLambda) {
		t.Fatalf("expected ErrInvalidLambda, got %v", err)
	}
	if !strings.Contains(err.Error(), "never executed") {
		t.Errorf("error should explain why there is no error to check, got %q", err)
	}
}

// A failure inside a subquery is reported through the enclosing call — there is nowhere
// else for it to go.
func TestSubquery_ErrorsReachTheCaller(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()
	seedData(t, ctx, e)

	// The nested lambda assigns, which is not a predicate.
	_, err := goql.Select[Order](ctx, e, func(o *Order) bool {
		bad, _ := goql.Select[Customer](ctx, e, func(c *Customer) bool {
			c.Status = "x"
			return true
		})
		return goql.Condition(o.Customer, "IN", bad)
	})
	if !errors.Is(err, goql.ErrInvalidLambda) {
		t.Fatalf("a broken subquery must surface through the outer call, got %v", err)
	}
}
