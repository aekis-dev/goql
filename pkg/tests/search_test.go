package tests

import (
	"testing"

	"github.com/aekis/goql/pkg/models"
	"github.com/aekis/goql/pkg/orm"
	"github.com/aekis/goql/pkg/query"
)

// --- SQL generation tests (no DB) ---

func TestSearchSQL_EntityByPK(t *testing.T) {
	schema, _ := models.GetModel(&Customer{})
	var entities []models.Entity
	entity := Customer{
		Model: orm.Model{ID: 1},
	}
	entities = []models.Entity{&entity}
	q, err := query.EntitySearch(entities, schema)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "SELECT customers.* FROM customers WHERE id = ?", q.SQL)
	assertEqual(t, []any{int64(1)}, q.Args)
}

func TestSearchSQL_LambdaSimple(t *testing.T) {
	executor := &orm.DebugExecutor{}
	body, err := executor.ParseBodyFromSource(`func(c Customer) bool {
		return c.Country == "USA"
	}`)
	if err != nil {
		t.Fatal(err)
	}

	schema, _ := models.GetModel(&Customer{})
	q, err := query.LambdaSearch(body, schema)
	if err != nil {
		t.Fatal(err)
	}

	assertEqual(t, "SELECT c.* FROM customers c WHERE c.country = ?", q.SQL)
	assertEqual(t, []any{"USA"}, q.Args)
}

func TestSearchSQL_LambdaWithM2OJoin(t *testing.T) {
	executor := &orm.DebugExecutor{}
	body, err := executor.ParseBodyFromSource(`func(o Order) bool {
		return o.Customer.Country == "USA"
	}`)
	if err != nil {
		t.Fatal(err)
	}

	schema, _ := models.GetModel(&Order{})
	q, err := query.LambdaSearch(body, schema)
	if err != nil {
		t.Fatal(err)
	}

	assertEqual(t, "SELECT * FROM orders INNER JOIN customers ON orders.customer_id = customers.id WHERE customers.country = ?", q.SQL)
	assertEqual(t, []any{"USA"}, q.Args)
}

func TestSearchSQL_LambdaWithO2MJoin(t *testing.T) {
	executor := &orm.DebugExecutor{}
	body, err := executor.ParseBodyFromSource(`func(c Customer) bool {
		for _, o := range c.Orders {
			if o.Total > 1000 {
				return true
			}
		}
		return false
	}`)
	if err != nil {
		t.Fatal(err)
	}

	schema, _ := models.GetModel(&Customer{})
	q, err := query.LambdaSearch(body, schema)
	if err != nil {
		t.Fatal(err)
	}

	assertEqual(t, "SELECT * FROM customers INNER JOIN orders ON orders.customer_id = customers.id WHERE orders.total_amount > ?", q.SQL)
	assertEqual(t, []any{int64(1000)}, q.Args)
}

func TestSearchSQL_LambdaWithM2MJoin(t *testing.T) {
	executor := &orm.DebugExecutor{}
	body, err := executor.ParseBodyFromSource(`func(o Order) bool {
		for _, t := range o.Tags {
			if t.Name == "urgent" {
				return true
			}
		}
		return false
	}`)
	if err != nil {
		t.Fatal(err)
	}

	schema, _ := models.GetModel(&Order{})
	q, err := query.LambdaSearch(body, schema)
	if err != nil {
		t.Fatal(err)
	}

	assertEqual(t, "SELECT * FROM orders INNER JOIN order_tags ON order_tags.order_id = orders.id INNER JOIN tags ON tags.id = order_tags.tag_id WHERE tags.name = ?", q.SQL)
	assertEqual(t, []any{"urgent"}, q.Args)
}

func TestSearchSQL_LambdaSentinel(t *testing.T) {
	executor := &orm.DebugExecutor{}
	body, err := executor.ParseBodyFromSource(`func(o Order) bool {
		urgent_tag := false
		for _, t := range o.Tags {
			if t.Name == "urgent" {
				urgent_tag = true
				break
			}
		}
		return o.Priority == "High" && urgent_tag == true
	}`)
	if err != nil {
		t.Fatal(err)
	}

	schema, _ := models.GetModel(&Order{})
	q, err := query.LambdaSearch(body, schema)
	if err != nil {
		t.Fatal(err)
	}

	assertContains(t, q.SQL, "INNER JOIN order_tags")
	assertContains(t, q.SQL, "INNER JOIN tags")
	assertContains(t, q.SQL, "orders.priority = ?")
	assertContains(t, q.SQL, "tags.name = ?")
}

// --- Execution tests (with DB) ---

func TestSearch_EntityByPK(t *testing.T) {
	ctx, cleanup := setupDB(t)
	defer cleanup()
	customers, _, _ := seedData(t, ctx)

	alice := customers[0].(*Customer)
	results, err := ctx.Search(&Customer{Model: orm.Model{ID: alice.ID}})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(results))
	assertEqual(t, "Alice", results[0].(*Customer).Name)
}

func TestSearch_EntityByField(t *testing.T) {
	ctx, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx)

	results, err := ctx.Search(&Customer{Country: "USA"})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(results))
	assertEqual(t, "Alice", results[0].(*Customer).Name)
}

func TestSearch_LambdaSimple(t *testing.T) {
	ctx, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx)

	results, err := ctx.Search(func(c Customer) bool {
		return c.Country == "USA"
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(results))
	assertEqual(t, "Alice", results[0].(*Customer).Name)
}

func TestSearch_LambdaM2OJoin(t *testing.T) {
	ctx, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx)

	results, err := ctx.Search(func(o Order) bool {
		return o.Customer.Country == "USA"
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(results))
	assertEqual(t, float64(1500), results[0].(*Order).Total)
}

func TestSearch_LambdaO2MJoin(t *testing.T) {
	ctx, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx)

	results, err := ctx.Search(func(c Customer) bool {
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
	assertEqual(t, "Alice", results[0].(*Customer).Name)
}

func TestSearch_LambdaM2MJoin(t *testing.T) {
	ctx, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx)

	results, err := ctx.Search(func(o Order) bool {
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
