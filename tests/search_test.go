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
		return goql.Filter(c.Orders, func(o *Order) bool { return o.Total > 1000 })
	}`, "Select")
	if err != nil {
		t.Fatal(err)
	}

	q, err := dialect.LambdaSearch(body, nil)
	if err != nil {
		t.Fatal(err)
	}

	assertEqual(t, `SELECT c.* FROM "customers" c WHERE EXISTS (SELECT 1 FROM "orders" o WHERE o."customer_id" = c."id" AND (o."total_amount" > ?))`, q.SQL)
	assertEqual(t, []any{int64(1000)}, q.Args)
}

func TestSearchSQL_LambdaWithM2MJoin(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(o *Order) bool {
		return goql.Filter(o.Tags, func(t *Tag) bool { return t.Name == "urgent" })
	}`, "Select")
	if err != nil {
		t.Fatal(err)
	}

	q, err := dialect.LambdaSearch(body, nil)
	if err != nil {
		t.Fatal(err)
	}

	// order_tags collides with orders on the preferred alias "o", so it becomes "o2".
	assertEqual(t, `SELECT o.* FROM "orders" o WHERE EXISTS (SELECT 1 FROM "order_tags" o2 INNER JOIN "tags" t ON t."id" = o2."tag_id" WHERE o2."order_id" = o."id" AND (t."name" = ?))`, q.SQL)
	assertEqual(t, []any{"urgent"}, q.Args)
}

// A relation filter composes with a plain column condition. This shape used to need a
// sentinel variable assigned inside a range loop, because a loop is a statement and could
// not appear inside the && it was contributing to.
func TestSearchSQL_FilterComposesWithColumn(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(o *Order) bool {
		return o.Priority == "High" &&
			goql.Filter(o.Tags, func(t *Tag) bool { return t.Name == "urgent" })
	}`, "Select")
	if err != nil {
		t.Fatal(err)
	}

	q, err := dialect.LambdaSearch(body, nil)
	if err != nil {
		t.Fatal(err)
	}

	assertContains(t, q.SQL, `o."priority" = ?`)
	assertContains(t, q.SQL, `EXISTS (SELECT 1 FROM "order_tags" o2`)
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
		return goql.Filter(c.Orders, func(o *Order) bool { return o.Total > 1000 })
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
		return goql.Filter(o.Tags, func(t *Tag) bool { return t.Name == "urgent" })
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(results))
}
