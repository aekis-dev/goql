package tests

import (
	"strings"
	"testing"

	"github.com/aekis-dev/goql"
)

// A field path traverses one relation per segment, to any depth. The relations are declared
// on the models, so every hop is derived rather than invented.
//
// The Go type checker keeps a path to many2one on its own: a collection is a slice, so
// o.Tags.Name does not compile. That is what guarantees a path can never multiply rows.

func TestPath_TwoHops(t *testing.T) {
	body := parseSource(t, `func(c *Category) bool {
		return c.Parent.Parent.Name == "root"
	}`)

	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)

	// categories appears three times — the row, its parent, its grandparent — so each
	// occurrence needs an alias of its own.
	assertEqual(t, `SELECT c.* FROM "categories" c `+
		`INNER JOIN "categories" c2 ON c."parent_id" = c2."id" `+
		`INNER JOIN "categories" c3 ON c2."parent_id" = c3."id" `+
		`WHERE c3."name" = ?`, q.SQL)
}

// The defect path-keyed aliases exist to prevent: keyed by table, two paths reaching the
// same table share one alias and the query asks for one row to be two different rows.
func TestPath_TwoPathsToOneTableGetDistinctAliases(t *testing.T) {
	body := parseSource(t, `func(c *Category) bool {
		return c.Parent.Name == "a" && c.Parent.Parent.Name == "b"
	}`)

	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)

	assertContains(t, q.SQL, `c2."name" = ?`)
	assertContains(t, q.SQL, `c3."name" = ?`)
	if strings.Count(q.SQL, "INNER JOIN") != 2 {
		t.Fatalf("expected two joins, got: %s", q.SQL)
	}
}

// The same path used twice is one join, not two.
func TestPath_RepeatedPathJoinsOnce(t *testing.T) {
	body := parseSource(t, `func(o *Order) bool {
		return o.Customer.Country == "USA" && o.Customer.Status == "Active"
	}`)

	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)

	if n := strings.Count(q.SQL, "INNER JOIN"); n != 1 {
		t.Fatalf("expected one join for one path, got %d: %s", n, q.SQL)
	}
	assertContains(t, q.SQL, `c."country" = ?`)
	assertContains(t, q.SQL, `c."status" = ?`)
}

// A path on the right of a comparison is joined too. Collecting only from the left would
// leave it referencing an alias no FROM clause introduced.
func TestPath_JoinedFromEitherSide(t *testing.T) {
	body := parseSource(t, `func(c *Category) bool {
		return c.Parent.Name == c.Parent.Parent.Name
	}`)

	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)

	if n := strings.Count(q.SQL, "INNER JOIN"); n != 2 {
		t.Fatalf("expected both sides joined, got %d: %s", n, q.SQL)
	}
	assertContains(t, q.SQL, `WHERE c2."name" = c3."name"`)
}

// The foreign-key collapse still applies at the end of a path: the primary key of a many2one
// target is what the foreign key column already holds, so the last hop is not needed.
func TestPath_ForeignKeyCollapseAtDepth(t *testing.T) {
	body := parseSource(t, `func(c *Category) bool {
		return c.Parent.Parent.ID == 7
	}`)

	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)

	// One join to reach the parent; the grandparent's key is the parent's own column.
	if n := strings.Count(q.SQL, "INNER JOIN"); n != 1 {
		t.Fatalf("expected the final hop collapsed, got %d joins: %s", n, q.SQL)
	}
	assertContains(t, q.SQL, `c2."parent_id" = ?`)
}

// A single hop renders exactly as it did before paths became unbounded.
func TestPath_SingleHopUnchanged(t *testing.T) {
	body := parseSource(t, `func(o *Order) bool {
		return o.Customer.Country == "USA"
	}`)

	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertEqual(t, `SELECT o.* FROM "orders" o `+
		`INNER JOIN "customers" c ON o."customer_id" = c."id" `+
		`WHERE c."country" = ?`, q.SQL)
}

func TestPath_RejectsNonRelationSegment(t *testing.T) {
	_, err := (&goql.DebugExecutor{}).ParseQueryFromSource(`func(o *Order) bool {
		return o.Priority.Nope.Name == "x"
	}`, "Select")

	if err == nil {
		t.Fatal("expected a non-relation segment to be refused")
	}
	assertContains(t, err.Error(), "is not a relation field")
}

// End to end: a three-level hierarchy walked by path.
func TestPath_WalksAHierarchy(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	if err := e.CreateTables(&Category{}); err != nil {
		t.Fatal(err)
	}

	roots, err := goql.Create(ctx, e, []Category{{Name: "root", Active: true}})
	if err != nil {
		t.Fatal(err)
	}
	mids, err := goql.Create(ctx, e, []Category{{Name: "mid", Active: true, Parent: roots[0]}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := goql.Create(ctx, e, []Category{
		{Name: "leaf", Active: true, Parent: mids[0]},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := goql.Select[Category](ctx, e, func(c *Category) bool {
		return c.Parent.Parent.Name == "root"
	})
	if err != nil {
		t.Fatal(err)
	}

	assertEqual(t, 1, len(got))
	assertEqual(t, "leaf", got[0].Name)
}
