package tests

import (
	"testing"

	"github.com/aekis/goql/pkg/models"
	"github.com/aekis/goql/pkg/orm"
	"github.com/aekis/goql/pkg/query"
)

func TestDeleteSQL_Entity(t *testing.T) {
	schema, _ := models.GetModel(&Order{})
	q, err := query.EntityDelete("id", int64(1), schema)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "DELETE FROM orders WHERE id IN (?)", q.SQL)
	assertEqual(t, []any{int64(1)}, q.Args)
}

func TestDeleteSQL_Lambda(t *testing.T) {
	executor := &orm.DebugExecutor{}
	body, err := executor.ParseBodyFromSource(`func(o Order) bool {
		return o.Priority == "Normal"
	}`)
	if err != nil {
		t.Fatal(err)
	}

	schema, _ := models.GetModel(&Order{})
	q, err := query.LambdaDelete(body, schema)
	if err != nil {
		t.Fatal(err)
	}

	assertEqual(t, "DELETE FROM orders WHERE orders.priority = ?", q.SQL)
	assertEqual(t, []any{"Normal"}, q.Args)
}

func TestDeleteSQL_LambdaWithJoin(t *testing.T) {
	executor := &orm.DebugExecutor{}
	body, err := executor.ParseBodyFromSource(`func(o Order) bool {
		return o.Customer.Country == "USA"
	}`)
	if err != nil {
		t.Fatal(err)
	}

	schema, _ := models.GetModel(&Order{})
	q, err := query.LambdaDelete(body, schema)
	if err != nil {
		t.Fatal(err)
	}

	assertContains(t, q.SQL, "DELETE FROM orders WHERE id IN")
	assertContains(t, q.SQL, "INNER JOIN customers")
	assertContains(t, q.SQL, "customers.country = ?")
}

func TestDelete_Entity(t *testing.T) {
	ctx, cleanup := setupDB(t)
	defer cleanup()
	_, orders, _ := seedData(t, ctx)

	order := orders[0].(*Order)
	rows, err := ctx.Delete(order)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, int64(1), rows)

	results, _ := ctx.Search(&Order{Model: orm.Model{ID: order.ID}})
	assertEqual(t, 0, len(results))
}

func TestDelete_Lambda(t *testing.T) {
	ctx, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx)

	rows, err := ctx.Delete(func(o Order) bool {
		return o.Priority == "Normal"
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, int64(1), rows)
}
