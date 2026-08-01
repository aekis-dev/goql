package tests

import (
	"strings"
	"testing"

	"github.com/aekis-dev/goql"
)

// o.Customer.ID names the primary key of the related row, which is the value the foreign
// key column already holds — so it resolves to orders.customer_id, and no join is emitted.
func TestFKPath_ResolvesToLocalColumn(t *testing.T) {
	body := parseSource(t, `func(o *Order) bool {
		return o.Customer.ID == 7
	}`)

	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)

	if strings.Contains(strings.ToUpper(q.SQL), "JOIN") {
		t.Fatalf("expected no join for a foreign key path, got:\n%s", q.SQL)
	}
	assertContains(t, q.SQL, `o."customer_id" = ?`)
}

// A path ending anywhere but the target's primary key still joins.
func TestFKPath_NonKeyPathStillJoins(t *testing.T) {
	body := parseSource(t, `func(o *Order) bool {
		return o.Customer.Country == "USA"
	}`)

	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)

	if !strings.Contains(strings.ToUpper(q.SQL), "JOIN") {
		t.Fatalf("expected a join for a non-key relation path, got:\n%s", q.SQL)
	}
	assertContains(t, q.SQL, `c."country" = ?`)
}

// End to end: the collapsed path selects the same rows the join would have.
func TestFKPath_SelectsByForeignKey(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	customers, orders, _ := seedData(t, ctx, e)

	type Key struct{ ID int64 }
	found, err := goql.Select[Order](ctx, e, func(o *Order, k Key) bool {
		return o.Customer.ID == k.ID
	}, Key{ID: customers[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].ID != orders[0].ID {
		t.Fatalf("expected order %d, got %d rows", orders[0].ID, len(found))
	}
}

// The same path on the value side of a comparison: two foreign keys compared directly.
func TestFKPath_OnBothSides(t *testing.T) {
	body := parseSource(t, `func(o *Order, p *Payment) bool {
		return o.Customer.ID == p.ID
	}`)

	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, q.SQL, `o."customer_id"`)
}
