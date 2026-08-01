package tests

import (
	"errors"
	"testing"

	"github.com/aekis-dev/goql"
)

type CustomerFilter struct {
	MinAge  int
	Country string
}

type StatusChange struct {
	Status string
	MinAge int
}

// The core of the feature: a value that is only known at the call site reaches the query.
func TestParams_SelectUsesCallTimeValues(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	// Alice is 40/USA, Bob is 41/Canada.
	rows, err := goql.Select[Customer](ctx, e, func(c *Customer, f CustomerFilter) bool {
		return c.Age > f.MinAge && c.Country == f.Country
	}, CustomerFilter{MinAge: 30, Country: "Canada"})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))
	assertEqual(t, "Bob", rows[0].Name)
}

// The parsed body is cached, so the second call must not reuse the first call's values.
func TestParams_CachedBodyIsReusedWithNewValues(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	pred := func(c *Customer, f CustomerFilter) bool {
		return c.Age > f.MinAge && c.Country == f.Country
	}

	canada, err := goql.Select[Customer](ctx, e, pred, CustomerFilter{MinAge: 30, Country: "Canada"})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(canada))
	assertEqual(t, "Bob", canada[0].Name)

	usa, err := goql.Select[Customer](ctx, e, pred, CustomerFilter{MinAge: 30, Country: "USA"})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(usa))
	assertEqual(t, "Alice", usa[0].Name)

	none, err := goql.Select[Customer](ctx, e, pred, CustomerFilter{MinAge: 99, Country: "USA"})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 0, len(none))
}

// Params supply both the SET value and the WHERE value of an update.
func TestParams_UpdateUsesCallTimeValues(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	customers, _, _ := seedData(t, ctx, e)

	rows, err := goql.Update[Customer](ctx, e, func(c *Customer, ch StatusChange) {
		if c.Age > ch.MinAge {
			c.Status = ch.Status
		}
	}, StatusChange{Status: "Senior", MinAge: 40})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, int64(1), rows)

	// Bob is 41 and matched; Alice is 40 and did not.
	assertEqual(t, "Senior", byID[Customer](t, ctx, e, customers[1].ID).Status)
	assertEqual(t, "Active", byID[Customer](t, ctx, e, customers[0].ID).Status)
}

func TestParams_DeleteUsesCallTimeValues(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	_, orders, _ := seedData(t, ctx, e)

	type Threshold struct{ Max float64 }

	rows, err := goql.Delete[Order](ctx, e, func(o *Order, th Threshold) bool {
		return o.Total < th.Max
	}, Threshold{Max: 1000})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, int64(1), rows)
	assertEqual(t, 0, countByID[Order](t, ctx, e, orders[1].ID))
	assertEqual(t, 1, countByID[Order](t, ctx, e, orders[0].ID))
}

// Params compose with query options, since both are just extra parameters.
func TestParams_ComposeWithOptions(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Select[Customer](ctx, e,
		func(c *Customer, f CustomerFilter, sort *goql.Sort, limit *goql.Limit) bool {
			sort.By = "Age"
			sort.Desc = true
			limit.Value = 1
			return c.Age > f.MinAge
		}, CustomerFilter{MinAge: 1})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))
	assertEqual(t, "Bob", rows[0].Name)
}

// Referencing a field the params struct does not have is caught by the Go compiler, since
// the lambda is ordinary Go code — no goql check is needed for that case.
//
// An unexported field does compile (within one package) but cannot be read by reflection,
// so goql rejects it while parsing rather than failing later.
type privateFilter struct{ minAge int }

func TestParams_UnexportedFieldIsRejectedAtParseTime(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	_, err := goql.Select[Customer](ctx, e, func(c *Customer, f privateFilter) bool {
		return c.Age > f.minAge
	}, privateFilter{minAge: 30})
	assertError(t, err)
	if !errors.Is(err, goql.ErrInvalidParams) {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}
	assertContains(t, err.Error(), "minAge")
}

func TestParams_MissingValueIsRejected(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	_, err := goql.Select[Customer](ctx, e, func(c *Customer, f CustomerFilter) bool {
		return c.Age > f.MinAge
	})
	assertError(t, err)
	if !errors.Is(err, goql.ErrMissingParams) {
		t.Fatalf("expected ErrMissingParams, got %v", err)
	}
}

func TestParams_WrongValueTypeIsRejected(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	_, err := goql.Select[Customer](ctx, e, func(c *Customer, f CustomerFilter) bool {
		return c.Age > f.MinAge
	}, StatusChange{Status: "x"})
	assertError(t, err)
	if !errors.Is(err, goql.ErrInvalidParams) {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}
}

// Supplying a value the lambda never declared is a mistake worth reporting.
func TestParams_UnexpectedValueIsRejected(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	_, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return c.Country == "USA"
	}, CustomerFilter{MinAge: 1})
	assertError(t, err)
	if !errors.Is(err, goql.ErrInvalidParams) {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}
}
