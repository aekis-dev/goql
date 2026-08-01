package tests

import (
	"errors"
	"testing"

	"github.com/aekis-dev/goql"
)

// Without a preload, relations come back empty — loading is explicit.
func TestPreload_NotLoadedByDefault(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Select[Order](ctx, e, func(o *Order) bool {
		return o.Priority == "Normal"
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))
	if rows[0].Customer != nil {
		t.Error("expected Customer to be nil without a preload")
	}
	assertEqual(t, 0, len(rows[0].Tags))
}

// many2one: the parent referenced by the foreign key.
func TestPreload_ManyToOne(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Select[Order](ctx, e, func(o *Order, pre *goql.Preload) bool {
		pre.Fields = []string{"Customer"}
		return o.Priority == "Normal"
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))
	if rows[0].Customer == nil {
		t.Fatal("expected Customer to be loaded")
	}
	assertEqual(t, "Alice", rows[0].Customer.Name)
}

// one2many: the children pointing back at each row.
func TestPreload_OneToMany(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Select[Customer](ctx, e, func(c *Customer, pre *goql.Preload) bool {
		pre.Fields = []string{"Orders"}
		return c.Country == "USA"
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))
	assertEqual(t, 1, len(rows[0].Orders))
	assertEqual(t, float64(1500), rows[0].Orders[0].Total)
}

// many2many, through the join table.
func TestPreload_ManyToMany(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Select[Order](ctx, e, func(o *Order, pre *goql.Preload) bool {
		pre.Fields = []string{"Tags"}
		return o.Priority == "Normal"
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))

	// Order 1 was seeded with "urgent" and "vip".
	assertEqual(t, 2, len(rows[0].Tags))
	names := map[string]bool{}
	for _, tag := range rows[0].Tags {
		names[tag.Name] = true
	}
	if !names["urgent"] || !names["vip"] {
		t.Errorf("expected urgent and vip, got %v", names)
	}
}

// Several relations at once, and the struct-based path takes the same option.
func TestPreload_MultipleRelationsAndStructPath(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Search(ctx, e, Order{Priority: "Normal"},
		goql.Preload{Fields: []string{"Customer", "Tags"}})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))
	if rows[0].Customer == nil {
		t.Fatal("expected Customer to be loaded")
	}
	assertEqual(t, "Alice", rows[0].Customer.Name)
	assertEqual(t, 2, len(rows[0].Tags))
}

// Every row gets its own related rows, and each relation costs a fixed number of queries
// rather than one per row.
func TestPreload_BatchesAcrossRows(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Search(ctx, e, Order{}, goql.Preload{Fields: []string{"Customer"}},
		goql.Sort{By: "Total"})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 2, len(rows))

	// Sorted by total ascending: order 2 (700, Bob) then order 1 (1500, Alice).
	if rows[0].Customer == nil || rows[1].Customer == nil {
		t.Fatal("expected both orders to have their customer loaded")
	}
	assertEqual(t, "Bob", rows[0].Customer.Name)
	assertEqual(t, "Alice", rows[1].Customer.Name)
}

func TestPreload_UnknownFieldIsRejected(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	_, err := goql.Search(ctx, e, Order{}, goql.Preload{Fields: []string{"Nope"}})
	assertError(t, err)
	assertContains(t, err.Error(), "Nope")
}

func TestPreload_NonRelationFieldIsRejected(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	_, err := goql.Search(ctx, e, Order{}, goql.Preload{Fields: []string{"Priority"}})
	assertError(t, err)
	if !errors.Is(err, goql.ErrInvalidOption) {
		t.Fatalf("expected ErrInvalidOption, got %v", err)
	}
}

// --- Schema-level defaults and override semantics ---

// Tag.Orders is declared with Preload: true, so it loads without being asked for.
func TestPreload_ModelDefaultApplies(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Select[Tag](ctx, e, func(tg *Tag) bool {
		return tg.Name == "urgent"
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))
	assertEqual(t, 1, len(rows[0].Orders))
	assertEqual(t, float64(1500), rows[0].Orders[0].Total)
}

// A query that names relations replaces the model's defaults entirely.
func TestPreload_QueryOverridesModelDefault(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	// Asking for nothing must switch the default off, not merge with it.
	rows, err := goql.Search(ctx, e, Tag{Name: "urgent"}, goql.Preload{})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))
	assertEqual(t, 0, len(rows[0].Orders))
}

// --- D4: one2many disassociation ---

// Dropping a row from a one2many slice must clear its foreign key, not leave it pointing
// at the old parent.
func TestPreload_O2MDisassociatesRemovedRows(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	_, _, tags := seedData(t, ctx, e)

	// Tag 1 ("urgent") is linked to order 1 through Tag.Orders (a nullable FK).
	urgent := byID[Tag](t, ctx, e, tags[0].ID)
	loaded, err := goql.Search(ctx, e, Tag{Model: goql.Model{ID: urgent.ID}},
		goql.Preload{Fields: []string{"Orders"}})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(loaded[0].Orders))

	// Clear the slice and write: the previously linked row must be disassociated.
	subject := loaded[0]
	subject.Orders = []Order{}
	if _, err := goql.Write(ctx, e, []Tag{*subject}); err != nil {
		t.Fatal(err)
	}

	after, err := goql.Search(ctx, e, Tag{Model: goql.Model{ID: urgent.ID}},
		goql.Preload{Fields: []string{"Orders"}})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 0, len(after[0].Orders))
}

// A NOT NULL foreign key cannot be cleared, so the attempt is reported rather than
// silently leaving a stale link or failing inside the driver.
func TestPreload_O2MNotNullDisassociationIsReported(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	customers, _, _ := seedData(t, ctx, e)

	// Customer.Orders points at orders.customer_id, which is NOT NULL.
	loaded, err := goql.Search(ctx, e, Customer{Model: goql.Model{ID: customers[0].ID}},
		goql.Preload{Fields: []string{"Orders"}})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(loaded[0].Orders))

	subject := loaded[0]
	subject.Orders = []Order{}
	_, err = goql.Write(ctx, e, []Customer{*subject})
	assertError(t, err)
	if !errors.Is(err, goql.ErrRelationConstraint) {
		t.Fatalf("expected ErrRelationConstraint, got %v", err)
	}
	assertContains(t, err.Error(), "customer_id")
}
