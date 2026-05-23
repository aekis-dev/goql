package tests

import (
	"database/sql"
	"os"
	"testing"

	"github.com/aekis/goql/pkg/orm"
	"github.com/aekis/goql/pkg/tests/models"
	_ "github.com/mattn/go-sqlite3"
)

func setupDB(t *testing.T) (*orm.GoqlContext, func()) {
	t.Helper()

	dbPath := t.Name() + ".db"
	os.Remove(dbPath)

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx := orm.NewGoqlContext(db).WithDebug()
	if err := ctx.EnableForeignKeys(); err != nil {
		t.Fatal(err)
	}

	if err := ctx.CreateTables(&Customer{}, &Order{}, &Tag{}); err != nil {
		t.Fatal(err)
	}

	return ctx, func() {
		db.Close()
		os.Remove(dbPath)
	}
}

func seedData(t *testing.T, ctx *orm.GoqlContext) (customers []any, orders []any, tags []any) {
	t.Helper()

	var err error
	tags, err = ctx.Create([]Tag{
		{Name: "urgent"},
		{Name: "vip"},
		{Name: "fragile"},
	})
	if err != nil {
		t.Fatal(err)
	}

	customers, err = ctx.Create([]Customer{
		{Name: "Alice", Age: 40, Number: 1, Country: "USA", Status: "Active", Login: "alice"},
		{Name: "Bob", Age: 41, Number: 2, Country: "Canada", Status: "Active", Login: "bob"},
	})
	if err != nil {
		t.Fatal(err)
	}

	orders, err = ctx.Create([]Order{
		{
			Total:          1500.00,
			Priority:       "Normal",
			ShippingMethod: "Standard",
			Customer:       customers[0].(*Customer),
			Tags:           []Tag{*tags[0].(*Tag), *tags[1].(*Tag)},
		},
		{
			Total:          700.00,
			Priority:       "High",
			ShippingMethod: "Overnight",
			Customer:       customers[1].(*Customer),
			Tags:           []Tag{*tags[2].(*Tag)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	return customers, orders, tags
}

// Type aliases for convenience
type Customer = models.Customer
type Order = models.Order
type Tag = models.Tag
