package tests

import (
	"testing"

	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/models"
)

// --- SQL generation tests (no DB) ---

func TestSearchSQL_EntityByPK(t *testing.T) {
	schema, _ := models.GetModel(&Customer{})
	var entities []models.Entity
	entity := Customer{
		Model: goql.Model{ID: 1},
	}
	entities = []models.Entity{&entity}
	q, err := dialect.EntitySearch(entities, schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, `SELECT "customers".* FROM "customers" WHERE "id" = ?`, q.SQL)
	assertEqual(t, []any{int64(1)}, q.Args)
}

func TestSearchSQL_LambdaSimple(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(c *Customer) bool {
		return c.Country == "USA"
	}`, "Select")
	if err != nil {
		t.Fatal(err)
	}

	q, err := dialect.LambdaSearch(body, nil)
	if err != nil {
		t.Fatal(err)
	}

	assertEqual(t, `SELECT c.* FROM "customers" c WHERE c."country" = ?`, q.SQL)
	assertEqual(t, []any{"USA"}, q.Args)
}

func TestSearchSQL_LambdaWithM2OJoin(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(o *Order) bool {
		return o.Customer.Country == "USA"
	}`, "Select")
	if err != nil {
		t.Fatal(err)
	}

	q, err := dialect.LambdaSearch(body, nil)
	if err != nil {
		t.Fatal(err)
	}

	assertEqual(t, `SELECT o.* FROM "orders" o INNER JOIN "customers" c ON o."customer_id" = c."id" WHERE c."country" = ?`, q.SQL)
	assertEqual(t, []any{"USA"}, q.Args)
}

func TestSearchSQL_LambdaWithO2MJoin(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(c *Customer) bool {
		for _, o := range c.Orders {
			if o.Total > 1000 {
				return true
			}
		}
		return false
	}`, "Select")
	if err != nil {
		t.Fatal(err)
	}

	q, err := dialect.LambdaSearch(body, nil)
	if err != nil {
		t.Fatal(err)
	}

	assertEqual(t, `SELECT c.* FROM "customers" c INNER JOIN "orders" o ON o."customer_id" = c."id" WHERE o."total_amount" > ?`, q.SQL)
	assertEqual(t, []any{int64(1000)}, q.Args)
}

func TestSearchSQL_LambdaWithM2MJoin(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(o *Order) bool {
		for _, t := range o.Tags {
			if t.Name == "urgent" {
				return true
			}
		}
		return false
	}`, "Select")
	if err != nil {
		t.Fatal(err)
	}

	q, err := dialect.LambdaSearch(body, nil)
	if err != nil {
		t.Fatal(err)
	}

	// order_tags collides with orders on the preferred alias "o", so it becomes "o2".
	assertEqual(t, `SELECT o.* FROM "orders" o INNER JOIN "order_tags" o2 ON o2."order_id" = o."id" INNER JOIN "tags" t ON t."id" = o2."tag_id" WHERE t."name" = ?`, q.SQL)
	assertEqual(t, []any{"urgent"}, q.Args)
}

func TestSearchSQL_LambdaSentinel(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(o *Order) bool {
		urgent_tag := false
		for _, t := range o.Tags {
			if t.Name == "urgent" {
				urgent_tag = true
				break
			}
		}
		return o.Priority == "High" && urgent_tag == true
	}`, "Select")
	if err != nil {
		t.Fatal(err)
	}

	q, err := dialect.LambdaSearch(body, nil)
	if err != nil {
		t.Fatal(err)
	}

	assertContains(t, q.SQL, `INNER JOIN "order_tags" o2`)
	assertContains(t, q.SQL, `INNER JOIN "tags" t`)
	assertContains(t, q.SQL, `o."priority" = ?`)
	assertContains(t, q.SQL, `t."name" = ?`)
}

// --- Execution tests (with DB) ---

func TestSearch_EntityByPK(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	customers, _, _ := seedData(t, ctx, e)

	alice := customers[0]
	results, err := goql.Search(ctx, e, Customer{Model: goql.Model{ID: alice.ID}})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(results))
	assertEqual(t, "Alice", results[0].Name)
}

func TestSearch_EntityByField(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	results, err := goql.Search(ctx, e, Customer{Country: "USA"})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(results))
	assertEqual(t, "Alice", results[0].Name)
}

func TestSearch_LambdaSimple(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	results, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return c.Country == "USA"
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(results))
	assertEqual(t, "Alice", results[0].Name)
}

func TestSearch_LambdaM2OJoin(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	results, err := goql.Select[Order](ctx, e, func(o *Order) bool {
		return o.Customer.Country == "USA"
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(results))
	assertEqual(t, float64(1500), results[0].Total)
}

func TestSearch_LambdaO2MJoin(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	results, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		for _, o := range c.Orders {
			if o.Total > 1000 {
				return true
			}
		}
		return false
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(results))
	assertEqual(t, "Alice", results[0].Name)
}

func TestSearch_LambdaM2MJoin(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	results, err := goql.Select[Order](ctx, e, func(o *Order) bool {
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
	assertEqual(t, 1, len(results))
}
