package tests

import (
	"strings"
	"testing"

	"github.com/aekis-dev/goql/models"
)

func TestCreateTable_Customer(t *testing.T) {
	schema, err := models.GetModel(&Customer{})
	if err != nil {
		t.Fatal(err)
	}

	sql, err := dialect.CreateTable(schema)
	if err != nil {
		t.Fatal(err)
	}

	// Must contain key elements
	assertContains(t, sql, `CREATE TABLE IF NOT EXISTS "customers"`)
	assertContains(t, sql, `"id" INTEGER PRIMARY KEY AUTOINCREMENT`)
	assertContains(t, sql, `"name" TEXT NOT NULL`)
	assertContains(t, sql, `"goql_created" TIMESTAMP NOT NULL`)
	assertContains(t, sql, `"goql_updated" TIMESTAMP NOT NULL`)
}

func TestCreateTable_Order(t *testing.T) {
	schema, err := models.GetModel(&Order{})
	if err != nil {
		t.Fatal(err)
	}

	sql, err := dialect.CreateTable(schema)
	if err != nil {
		t.Fatal(err)
	}

	assertContains(t, sql, `CREATE TABLE IF NOT EXISTS "orders"`)
	assertContains(t, sql, `"customer_id" INTEGER NOT NULL`)
	assertContains(t, sql, `"total_amount" NUMERIC(10, 2) NOT NULL`)
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

	sql, err := dialect.CreateJoinTable(tagsField, schema)
	if err != nil {
		t.Fatal(err)
	}

	assertContains(t, sql, `CREATE TABLE IF NOT EXISTS "order_tags"`)
	assertContains(t, sql, `"order_id" INTEGER NOT NULL`)
	assertContains(t, sql, `"tag_id" INTEGER NOT NULL`)
	assertContains(t, sql, `FOREIGN KEY ("order_id") REFERENCES "orders"("id")`)
	assertContains(t, sql, `FOREIGN KEY ("tag_id") REFERENCES "tags"("id")`)
	assertContains(t, sql, "ON DELETE CASCADE ON UPDATE CASCADE")
}

func TestCreateTables_Executes(t *testing.T) {
	_, _, cleanup := setupDB(t)
	defer cleanup()
	// setupDB calls CreateTables — if we get here it worked
}

// Index accepts a bool as well as a name: true derives idx_<table>_<column>, so the
// common single-column case needs no invented name.
func TestCreateIndexes_BoolIndexDerivesName(t *testing.T) {
	schema, err := models.GetModel(&Widget{})
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, sql := range dialect.BuildCreateIndexes(schema) {
		if strings.Contains(sql, `"idx_widgets_name"`) {
			found = true
			assertContains(t, sql, `ON "widgets" ("name")`)
		}
	}
	if !found {
		t.Errorf("expected a derived index name, got %v", dialect.BuildCreateIndexes(schema))
	}
}
