package tests

import (
	"testing"

	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/query"
)

// singleWrite asserts that a write lambda compiled to exactly one UPDATE and returns
// it. Lambdas with if/else or switch arms produce one statement per branch instead.
func singleWrite(t *testing.T, queries []*query.Query) *query.Query {
	t.Helper()
	if len(queries) != 1 {
		t.Fatalf("expected exactly 1 UPDATE statement, got %d", len(queries))
	}
	return queries[0]
}

// --- SQL generation tests (no DB) ---

func TestWriteSQL_LambdaSimple(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(c *Customer) {
		c.Status = "Premium"
	}`, "Update")
	if err != nil {
		t.Fatal(err)
	}

	queries, err := dialect.LambdaWrite(body)
	if err != nil {
		t.Fatal(err)
	}
	q := singleWrite(t, queries)

	assertContains(t, q.SQL, `UPDATE "customers" SET`)
	assertContains(t, q.SQL, `"status" = ?`)
	assertContains(t, q.SQL, `"goql_updated" = ?`)
	// unconditional — no WHERE
	assertNotContains(t, q.SQL, "WHERE")
}

func TestWriteSQL_LambdaConditional(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(c *Customer) {
		if c.Age > 40 {
			c.Status = "Senior"
			c.Discount = 0.15
		}
	}`, "Update")
	if err != nil {
		t.Fatal(err)
	}

	queries, err := dialect.LambdaWrite(body)
	if err != nil {
		t.Fatal(err)
	}
	q := singleWrite(t, queries)

	assertContains(t, q.SQL, `UPDATE "customers" SET`)
	assertContains(t, q.SQL, `"status" = ?`)
	assertContains(t, q.SQL, `"discount" = ?`)
	assertContains(t, q.SQL, `WHERE "customers"."age" > ?`)
	assertEqual(t, "Senior", q.Args[0])
	assertEqual(t, 0.15, q.Args[1])
	assertEqual(t, int64(40), q.Args[len(q.Args)-1])
}

func TestWriteSQL_LambdaWithM2OJoin(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(o *Order) {
		if o.Customer.Country == "USA" && o.Total > 1000 {
			o.Priority = "High"
			o.ShippingMethod = "Express"
		}
	}`, "Update")
	if err != nil {
		t.Fatal(err)
	}

	queries, err := dialect.LambdaWrite(body)
	if err != nil {
		t.Fatal(err)
	}
	q := singleWrite(t, queries)

	assertContains(t, q.SQL, `UPDATE "orders" SET`)
	assertContains(t, q.SQL, `"priority" = ?`)
	assertContains(t, q.SQL, `"shipping_method" = ?`)
	assertContains(t, q.SQL, `FROM "customers" c`)
	assertContains(t, q.SQL, `"orders"."customer_id" = c."id"`)
	assertContains(t, q.SQL, `c."country" = ?`)
	assertContains(t, q.SQL, `"orders"."total_amount" > ?`)
}

func TestWriteSQL_LambdaFieldToField(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(c *Customer) {
		c.Nickname = c.Login
	}`, "Update")
	if err != nil {
		t.Fatal(err)
	}

	queries, err := dialect.LambdaWrite(body)
	if err != nil {
		t.Fatal(err)
	}
	q := singleWrite(t, queries)

	assertContains(t, q.SQL, `"nickname" = "customers"."login"`)
}

// --- Execution tests (with DB) ---

func TestWrite_EntityUpdate(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	customers, _, _ := seedData(t, ctx, e)

	alice := customers[0]
	alice.Country = "UK"

	rows, err := goql.Write(ctx, e, []Customer{*alice})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, int64(1), rows)

	assertEqual(t, "UK", byID[Customer](t, ctx, e, alice.ID).Country)
}

func TestWrite_LambdaConditional(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	customers, _, _ := seedData(t, ctx, e)

	rows, err := goql.Update[Customer](ctx, e, func(c *Customer) {
		if c.Age > 40 {
			c.Status = "Senior"
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, int64(1), rows)

	assertEqual(t, "Senior", byID[Customer](t, ctx, e, customers[1].ID).Status)
}

func TestWrite_LambdaWithM2OJoin(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	_, orders, _ := seedData(t, ctx, e)

	rows, err := goql.Update[Order](ctx, e, func(o *Order) {
		if o.Customer.Country == "USA" && o.Total > 1000 {
			o.Priority = "High"
			o.ShippingMethod = "Express"
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, int64(1), rows)

	updated := byID[Order](t, ctx, e, orders[0].ID)
	assertEqual(t, "High", updated.Priority)
	assertEqual(t, "Express", updated.ShippingMethod)
}

// Regression (end to end): both arms must reach their own disjoint row set. Previously
// the else-arm's assignments were merged into a single UPDATE scoped to the if
// condition, so else rows were never touched and the if rows got two conflicting SETs.
func TestWrite_LambdaIfElseAppliesBothArms(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	customers, _, _ := seedData(t, ctx, e)

	// Alice is 40 (else arm), Bob is 41 (if arm).
	rows, err := goql.Update[Customer](ctx, e, func(c *Customer) {
		if c.Age > 40 {
			c.Status = "Senior"
		} else {
			c.Status = "Premium"
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, int64(2), rows)

	assertEqual(t, "Premium", byID[Customer](t, ctx, e, customers[0].ID).Status)
	assertEqual(t, "Senior", byID[Customer](t, ctx, e, customers[1].ID).Status)
}

// Same end-to-end check for a tag switch, including the default arm.
func TestWrite_LambdaSwitchAppliesEachArm(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	customers, _, _ := seedData(t, ctx, e)

	// Alice is in the USA, Bob in Canada (falls to default).
	rows, err := goql.Update[Customer](ctx, e, func(c *Customer) {
		switch c.Country {
		case "USA":
			c.Status = "Premium"
		default:
			c.Status = "Inactive"
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, int64(2), rows)

	assertEqual(t, "Premium", byID[Customer](t, ctx, e, customers[0].ID).Status)
	assertEqual(t, "Inactive", byID[Customer](t, ctx, e, customers[1].ID).Status)
}

func TestWrite_LambdaUnconditional(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Update[Customer](ctx, e, func(c *Customer) {
		c.Country = "USA"
	})
	if err != nil {
		t.Fatal(err)
	}
	// Both customers updated
	assertEqual(t, int64(2), rows)
}
