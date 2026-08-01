package tests

import (
	"testing"

	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/models"
)

func TestDeleteSQL_Entity(t *testing.T) {
	schema, _ := models.GetModel(&Order{})
	q, err := dialect.EntityDelete(int64(1), schema)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, `DELETE FROM "orders" WHERE "id" IN (?)`, q.SQL)
	assertEqual(t, []any{int64(1)}, q.Args)
}

func TestDeleteSQL_Lambda(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(o *Order) bool {
		return o.Priority == "Normal"
	}`, "Select")
	if err != nil {
		t.Fatal(err)
	}

	q, err := dialect.LambdaDelete(body)
	if err != nil {
		t.Fatal(err)
	}

	assertEqual(t, `DELETE FROM "orders" WHERE "orders"."priority" = ?`, q.SQL)
	assertEqual(t, []any{"Normal"}, q.Args)
}

func TestDeleteSQL_LambdaWithJoin(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(o *Order) bool {
		return o.Customer.Country == "USA"
	}`, "Select")
	if err != nil {
		t.Fatal(err)
	}

	q, err := dialect.LambdaDelete(body)
	if err != nil {
		t.Fatal(err)
	}

	assertContains(t, q.SQL, `DELETE FROM "orders" WHERE "id" IN`)
	assertContains(t, q.SQL, `INNER JOIN "customers" c`)
	assertContains(t, q.SQL, `c."country" = ?`)
}

func TestDelete_Entity(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	_, orders, _ := seedData(t, ctx, e)

	order := orders[0]
	rows, err := goql.Remove(ctx, e, []Order{*order})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, int64(1), rows)

	assertEqual(t, 0, countByID[Order](t, ctx, e, order.ID))
}

func TestDelete_Lambda(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Delete[Order](ctx, e, func(o *Order) bool {
		return o.Priority == "Normal"
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, int64(1), rows)
}
