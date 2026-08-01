package tests

import (
	"errors"
	"testing"

	"github.com/aekis-dev/goql"
)

// --- Struct-based path: options as trailing values ---

func TestOptions_StructSortAndLimit(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	// Alice is 40, Bob is 41.
	desc, err := goql.Search(ctx, e, Customer{}, goql.Sort{By: "Age", Desc: true})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 2, len(desc))
	assertEqual(t, "Bob", desc[0].Name)

	asc, err := goql.Search(ctx, e, Customer{}, goql.Sort{By: "Age"})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "Alice", asc[0].Name)

	limited, err := goql.Search(ctx, e, Customer{}, goql.Sort{By: "Age"}, goql.Limit{Value: 1})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(limited))
	assertEqual(t, "Alice", limited[0].Name)
}

func TestOptions_StructOffsetWithoutLimit(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	// Offset alone must still work: goql emits an open-ended LIMIT for engines that
	// require one before OFFSET.
	rest, err := goql.Search(ctx, e, Customer{}, goql.Sort{By: "Age"}, goql.Offset{Value: 1})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rest))
	assertEqual(t, "Bob", rest[0].Name)
}

func TestOptions_Projection(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Search(ctx, e, Customer{Country: "USA"}, goql.Fields{Names: []string{"Name"}})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))

	// The requested field and the primary key are populated; everything else comes back
	// as a Go zero value.
	assertEqual(t, "Alice", rows[0].Name)
	if rows[0].ID == 0 {
		t.Error("primary key should always be selected")
	}
	assertEqual(t, "", rows[0].Country)
	assertEqual(t, 0, rows[0].Age)
}

func TestOptions_UnknownFieldIsRejected(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	_, err := goql.Search(ctx, e, Customer{}, goql.Sort{By: "NoSuchField"})
	assertError(t, err)
	assertContains(t, err.Error(), "NoSuchField")
}

func TestOptions_NonOptionValueIsRejected(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	_, err := goql.Search(ctx, e, Customer{}, "not an option")
	assertError(t, err)
	if !errors.Is(err, goql.ErrInvalidOption) {
		t.Fatalf("expected ErrInvalidOption, got %v", err)
	}
}

// --- Lambda path: options declared as extra parameters ---

func TestOptions_LambdaSortAndLimit(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Select[Customer](ctx, e,
		func(c *Customer, sort *goql.Sort, limit *goql.Limit) bool {
			sort.By = "Age"
			sort.Desc = true
			limit.Value = 1
			return c.Status == "Active"
		})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))
	assertEqual(t, "Bob", rows[0].Name)
}

// Parameters are classified by type, so their declaration order does not matter.
func TestOptions_LambdaParamOrderDoesNotMatter(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Select[Customer](ctx, e,
		func(c *Customer, limit *goql.Limit, sort *goql.Sort) bool {
			limit.Value = 1
			sort.By = "Age"
			return c.Status == "Active"
		})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))
	assertEqual(t, "Alice", rows[0].Name)
}

// Several *Sort parameters compose as a multi-column ORDER BY, in declaration order.
func TestOptions_LambdaMultiColumnSort(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Select[Customer](ctx, e,
		func(c *Customer, first *goql.Sort, second *goql.Sort) bool {
			first.By = "Country"
			second.By = "Age"
			second.Desc = true
			return c.Status == "Active"
		})
	if err != nil {
		t.Fatal(err)
	}
	// Canada sorts before USA.
	assertEqual(t, 2, len(rows))
	assertEqual(t, "Bob", rows[0].Name)
	assertEqual(t, "Alice", rows[1].Name)
}

func TestOptions_LambdaProjection(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Select[Customer](ctx, e,
		func(c *Customer, fields *goql.Fields) bool {
			fields.Names = []string{"Name", "Country"}
			return c.Country == "USA"
		})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))
	assertEqual(t, "Alice", rows[0].Name)
	assertEqual(t, "USA", rows[0].Country)
	assertEqual(t, 0, rows[0].Age)
}

// Options describe the whole query, so setting one inside a branch is an error rather
// than silently applying to every row.
func TestOptions_LambdaOptionInsideBranchIsRejected(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	_, err := goql.Select[Customer](ctx, e,
		func(c *Customer, limit *goql.Limit) bool {
			if c.Country == "USA" {
				limit.Value = 1
				return true
			}
			return false
		})
	assertError(t, err)
	if !errors.Is(err, goql.ErrInvalidOption) {
		t.Fatalf("expected ErrInvalidOption, got %v", err)
	}
}

// An extra parameter that is not an option carrier is a params struct, so declaring one
// and supplying nothing is a missing-params error rather than an unknown-option one.
func TestOptions_NonCarrierParamIsTreatedAsParams(t *testing.T) {
	type Filters struct{ MinAge int }

	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	_, err := goql.Select[Customer](ctx, e, func(c *Customer, f Filters) bool {
		return c.Age > f.MinAge
	})
	assertError(t, err)
	if !errors.Is(err, goql.ErrMissingParams) {
		t.Fatalf("expected ErrMissingParams, got %v", err)
	}
}
