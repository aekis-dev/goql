package tests

import (
	"testing"

	"github.com/aekis/goql/pkg/models"
	"github.com/aekis/goql/pkg/orm"
	"github.com/aekis/goql/pkg/query"
)

// --- SQL generation tests (no DB) ---

func TestWriteSQL_LambdaSimple(t *testing.T) {
	executor := &orm.DebugExecutor{}
	body, err := executor.ParseBodyFromSource(`func(c Customer) {
		c.Status = "Premium"
	}`)
	if err != nil {
		t.Fatal(err)
	}

	schema, _ := models.GetModel(&Customer{})
	q, err := query.LambdaWrite(body, schema)
	if err != nil {
		t.Fatal(err)
	}

	assertContains(t, q.SQL, "UPDATE customers SET")
	assertContains(t, q.SQL, "status = ?")
	assertContains(t, q.SQL, "updated_at = ?")
	// unconditional — no WHERE
	assertNotContains(t, q.SQL, "WHERE")
}

func TestWriteSQL_LambdaConditional(t *testing.T) {
	executor := &orm.DebugExecutor{}
	body, err := executor.ParseBodyFromSource(`func(c Customer) {
		if c.Age > 40 {
			c.Status = "Senior"
			c.Discount = 0.15
		}
	}`)
	if err != nil {
		t.Fatal(err)
	}

	schema, _ := models.GetModel(&Customer{})
	q, err := query.LambdaWrite(body, schema)
	if err != nil {
		t.Fatal(err)
	}

	assertContains(t, q.SQL, "UPDATE customers SET")
	assertContains(t, q.SQL, "status = ?")
	assertContains(t, q.SQL, "discount = ?")
	assertContains(t, q.SQL, "WHERE customers.age > ?")
	assertEqual(t, "Senior", q.Args[0])
	assertEqual(t, 0.15, q.Args[1])
	assertEqual(t, int64(40), q.Args[len(q.Args)-1])
}

func TestWriteSQL_LambdaWithM2OJoin(t *testing.T) {
	executor := &orm.DebugExecutor{}
	body, err := executor.ParseBodyFromSource(`func(o Order) {
		if o.Customer.Country == "USA" && o.Total > 1000 {
			o.Priority = "High"
			o.ShippingMethod = "Express"
		}
	}`)
	if err != nil {
		t.Fatal(err)
	}

	schema, _ := models.GetModel(&Order{})
	q, err := query.LambdaWrite(body, schema)
	if err != nil {
		t.Fatal(err)
	}

	assertContains(t, q.SQL, "UPDATE orders SET")
	assertContains(t, q.SQL, "priority = ?")
	assertContains(t, q.SQL, "shipping_method = ?")
	assertContains(t, q.SQL, "FROM customers")
	assertContains(t, q.SQL, "orders.customer_id = customers.id")
	assertContains(t, q.SQL, "customers.country = ?")
	assertContains(t, q.SQL, "orders.total_amount > ?")
}

func TestWriteSQL_LambdaFieldToField(t *testing.T) {
	executor := &orm.DebugExecutor{}
	body, err := executor.ParseBodyFromSource(`func(c Customer) {
		c.Nickname = c.Login
	}`)
	if err != nil {
		t.Fatal(err)
	}

	schema, _ := models.GetModel(&Customer{})
	q, err := query.LambdaWrite(body, schema)
	if err != nil {
		t.Fatal(err)
	}

	assertContains(t, q.SQL, "nickname = customers.login")
}

// --- Execution tests (with DB) ---

func TestWrite_EntityUpdate(t *testing.T) {
	ctx, cleanup := setupDB(t)
	defer cleanup()
	customers, _, _ := seedData(t, ctx)

	alice := customers[0].(*Customer)
	alice.Country = "UK"

	rows, err := ctx.Write([]Customer{*alice})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, int64(1), rows)

	results, _ := ctx.Search(&Customer{Model: orm.Model{ID: alice.ID}})
	assertEqual(t, "UK", results[0].(*Customer).Country)
}

func TestWrite_LambdaConditional(t *testing.T) {
	ctx, cleanup := setupDB(t)
	defer cleanup()
	customers, _, _ := seedData(t, ctx)

	rows, err := ctx.Write(func(c Customer) {
		if c.Age > 40 {
			c.Status = "Senior"
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, int64(1), rows)

	results, _ := ctx.Search(&Customer{Model: orm.Model{ID: customers[1].(*Customer).ID}})
	assertEqual(t, "Senior", results[0].(*Customer).Status)
}

func TestWrite_LambdaWithM2OJoin(t *testing.T) {
	ctx, cleanup := setupDB(t)
	defer cleanup()
	_, orders, _ := seedData(t, ctx)

	rows, err := ctx.Write(func(o Order) {
		if o.Customer.Country == "USA" && o.Total > 1000 {
			o.Priority = "High"
			o.ShippingMethod = "Express"
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, int64(1), rows)

	results, _ := ctx.Search(&Order{Model: orm.Model{ID: orders[0].(*Order).ID}})
	assertEqual(t, "High", results[0].(*Order).Priority)
	assertEqual(t, "Express", results[0].(*Order).ShippingMethod)
}

func TestWrite_LambdaUnconditional(t *testing.T) {
	ctx, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx)

	rows, err := ctx.Write(func(c Customer) {
		c.Country = "USA"
	})
	if err != nil {
		t.Fatal(err)
	}
	// Both customers updated
	assertEqual(t, int64(2), rows)
}
