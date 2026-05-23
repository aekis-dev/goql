package tests

import (
	"testing"

	"github.com/aekis/goql/pkg/models"
	"github.com/aekis/goql/pkg/query"
)

func TestCreateSQL(t *testing.T) {
	schema, _ := models.GetModel(&Customer{})
	q, err := query.EntityCreate(&Customer{
		Name:    "Alice",
		Age:     40,
		Country: "USA",
	},
		schema,
	)
	if err != nil {
		t.Fatal(err)
	}

	assertEqual(t, "INSERT INTO customers (name, age, country) VALUES (?, ?, ?)", q.SQL)
	assertEqual(t, []any{"Alice", 40, "USA"}, q.Args)
}

func TestCreate_Single(t *testing.T) {
	ctx, cleanup := setupDB(t)
	defer cleanup()

	results, err := ctx.Create([]Customer{
		{Name: "Alice", Age: 30, Number: 99, Country: "USA", Status: "Active", Login: "alice99"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(results))

	alice := results[0].(*Customer)
	if alice.ID == 0 {
		t.Error("expected ID to be set after create")
	}
	assertEqual(t, "Alice", alice.Name)
}

func TestCreate_WithM2MTags(t *testing.T) {
	ctx, cleanup := setupDB(t)
	defer cleanup()

	tags, err := ctx.Create([]Tag{{Name: "urgent"}, {Name: "vip"}})
	if err != nil {
		t.Fatal(err)
	}

	customers, err := ctx.Create([]Customer{
		{Name: "Alice", Age: 30, Number: 99, Country: "USA", Status: "Active", Login: "alice99"},
	})
	if err != nil {
		t.Fatal(err)
	}

	orders, err := ctx.Create([]Order{
		{
			Total:          1500.00,
			Priority:       "Normal",
			ShippingMethod: "Standard",
			Customer:       customers[0].(*Customer),
			Tags:           []Tag{*tags[0].(*Tag), *tags[1].(*Tag)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(orders))

	// Verify tags were associated
	tagged, err := ctx.Search(func(o Order) bool {
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
