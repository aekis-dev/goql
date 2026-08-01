package tests

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/models"
	"github.com/aekis-dev/goql/query"
	testmodels "github.com/aekis-dev/goql/tests/models"
	_ "github.com/mattn/go-sqlite3"
)

// dialect renders SQL for the engine the tests run against, for the SQL-generation tests
// that do not open a database.
var dialect = query.NewDialect(query.SQLite{})

func setupDB(t *testing.T) (context.Context, *goql.Engine, func()) {
	t.Helper()

	dbPath := t.Name() + ".db"
	os.Remove(dbPath)

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}

	// No WithDebug: logging every statement buries the assertion that actually failed.
	// Add it here temporarily when you need to see the generated SQL.
	e := goql.New(db, goql.SQLite{})
	if err := e.EnableForeignKeys(); err != nil {
		t.Fatal(err)
	}

	if err := e.CreateTables(&Customer{}, &Order{}, &Tag{}); err != nil {
		t.Fatal(err)
	}

	return context.Background(), e, func() {
		db.Close()
		os.Remove(dbPath)
	}
}

func seedData(t *testing.T, ctx context.Context, e *goql.Engine) (customers []*Customer, orders []*Order, tags []*Tag) {
	t.Helper()

	var err error
	tags, err = goql.Create(ctx, e, []Tag{
		{Name: "urgent"},
		{Name: "vip"},
		{Name: "fragile"},
	})
	if err != nil {
		t.Fatal(err)
	}

	customers, err = goql.Create(ctx, e, []Customer{
		{Name: "Alice", Age: 40, Number: 1, Country: "USA", Status: "Active", Login: "alice"},
		{Name: "Bob", Age: 41, Number: 2, Country: "Canada", Status: "Active", Login: "bob"},
	})
	if err != nil {
		t.Fatal(err)
	}

	orders, err = goql.Create(ctx, e, []Order{
		{
			Total:          1500.00,
			Priority:       "Normal",
			ShippingMethod: "Standard",
			Customer:       customers[0],
			Tag:            tags[0],
			Tags:           []Tag{*tags[0], *tags[1]},
		},
		{
			Total:          700.00,
			Priority:       "High",
			ShippingMethod: "Overnight",
			Customer:       customers[1],
			Tags:           []Tag{*tags[2]},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	return customers, orders, tags
}

// byID fetches a single entity by primary key, for asserting persisted state.
func byID[T any](t *testing.T, ctx context.Context, e *goql.Engine, id int64) *T {
	t.Helper()
	results, err := goql.Search(ctx, e, *newWithID[T](id))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 row with id %d, got %d", id, len(results))
	}
	return results[0]
}

// countByID reports how many rows a primary key still matches, for delete assertions.
func countByID[T any](t *testing.T, ctx context.Context, e *goql.Engine, id int64) int {
	t.Helper()
	results, err := goql.Search(ctx, e, *newWithID[T](id))
	if err != nil {
		t.Fatal(err)
	}
	return len(results)
}

// newWithID builds a zero T with only its embedded primary key set.
func newWithID[T any](id int64) *T {
	entity := new(T)
	if setter, ok := any(entity).(models.Entity); ok {
		setter.SetPrimaryKey(id)
	}
	return entity
}

// Type aliases for convenience
type Customer = testmodels.Customer
type Order = testmodels.Order
type Tag = testmodels.Tag
type OrderArchive = testmodels.OrderArchive
type Invoice = testmodels.Invoice
type Payment = testmodels.Payment
type Category = testmodels.Category
