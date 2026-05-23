package tests

import (
	"testing"

	"github.com/aekis/goql/pkg/models"
	"github.com/aekis/goql/pkg/query"
)

func TestCreateTable_Customer(t *testing.T) {
	schema, err := models.GetModel(&Customer{})
	if err != nil {
		t.Fatal(err)
	}

	sql, err := query.CreateTable(schema)
	if err != nil {
		t.Fatal(err)
	}

	// Must contain key elements
	assertContains(t, sql, "CREATE TABLE IF NOT EXISTS customers")
	assertContains(t, sql, "id integer PRIMARY KEY AUTOINCREMENT")
	assertContains(t, sql, "name text NOT NULL")
	assertContains(t, sql, "created_at timestamp NOT NULL")
	assertContains(t, sql, "updated_at timestamp NOT NULL")
}

func TestCreateTable_Order(t *testing.T) {
	schema, err := models.GetModel(&Order{})
	if err != nil {
		t.Fatal(err)
	}

	sql, err := query.CreateTable(schema)
	if err != nil {
		t.Fatal(err)
	}

	assertContains(t, sql, "CREATE TABLE IF NOT EXISTS orders")
	assertContains(t, sql, "customer_id bigint NOT NULL")
	assertContains(t, sql, "total_amount decimal(10,2) NOT NULL")
	// Tags is many2many — no column in orders table
	assertNotContains(t, sql, "tags")
}

func TestCreateJoinTable_OrderTags(t *testing.T) {
	schema, err := models.GetModel(&Order{})
	if err != nil {
		t.Fatal(err)
	}

	tagsField := schema.Fields["Tags"]
	if tagsField == nil {
		t.Fatal("Tags field not found in Order schema")
	}

	sql, err := query.CreateJoinTable(tagsField, schema)
	if err != nil {
		t.Fatal(err)
	}

	assertContains(t, sql, "CREATE TABLE IF NOT EXISTS order_tags")
	assertContains(t, sql, "order_id INTEGER NOT NULL")
	assertContains(t, sql, "tag_id INTEGER NOT NULL")
	assertContains(t, sql, "FOREIGN KEY (order_id) REFERENCES orders")
	assertContains(t, sql, "FOREIGN KEY (tag_id) REFERENCES tags")
	assertContains(t, sql, "ON DELETE CASCADE ON UPDATE CASCADE")
}

func TestCreateTables_Executes(t *testing.T) {
	_, cleanup := setupDB(t)
	defer cleanup()
	// setupDB calls CreateTables — if we get here it worked
}
