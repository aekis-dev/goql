package tests

import (
	"testing"

	"github.com/aekis-dev/goql"
	"time"

	"github.com/aekis-dev/goql/models"
)

func TestCreateSQL(t *testing.T) {
	schema, _ := models.GetModel(&Customer{})
	q, err := dialect.EntityCreate(&Customer{
		Name:    "Alice",
		Age:     40,
		Country: "USA",
	},
		schema,
	)
	if err != nil {
		t.Fatal(err)
	}

	// Columns are emitted in sorted Go-field-name order, so the SQL is stable.
	// NotNull columns are included even when their Go value is the zero value, which
	// is why login/number and the (unset) timestamps appear here.
	assertEqual(t,
		`INSERT INTO "customers" ("age", "country", "goql_created", "login", "name", "number", "goql_updated") VALUES (?, ?, ?, ?, ?, ?, ?)`,
		q.SQL)
	assertEqual(t, []any{40, "USA", time.Time{}, "", "Alice", 0, time.Time{}}, q.Args)
}

func TestCreate_Single(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	results, err := goql.Create(ctx, e, []Customer{
		{Name: "Alice", Age: 30, Number: 99, Country: "USA", Status: "Active", Login: "alice99"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(results))

	alice := results[0]
	if alice.ID == 0 {
		t.Error("expected ID to be set after create")
	}
	assertEqual(t, "Alice", alice.Name)
}

func TestCreate_WithM2MTags(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	tags, err := goql.Create(ctx, e, []Tag{{Name: "urgent"}, {Name: "vip"}})
	if err != nil {
		t.Fatal(err)
	}

	customers, err := goql.Create(ctx, e, []Customer{
		{Name: "Alice", Age: 30, Number: 99, Country: "USA", Status: "Active", Login: "alice99"},
	})
	if err != nil {
		t.Fatal(err)
	}

	orders, err := goql.Create(ctx, e, []Order{
		{
			Total:          1500.00,
			Priority:       "Normal",
			ShippingMethod: "Standard",
			Customer:       customers[0],
			Tags:           []Tag{*tags[0], *tags[1]},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(orders))

	// Verify tags were associated
	tagged, err := goql.Select[Order](ctx, e, func(o *Order) bool {
		for _, t := range o.Tags {
			if t.Name == "urgent" {
				return true
			}
		}
		return false
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(tagged))
}
