package tests

import (
	"context"
	"errors"
	"testing"

	"github.com/aekis-dev/goql"
)

// Combinations that no single feature's tests cover, because each was built in its own pass.

// Get is new (§25) and preloading is old (§7). Get claims to follow the same Preload rules
// as every other read; nothing checked that across the two.
func TestIntegration_GetWithPreload(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	customers, err := goql.Create(ctx, e, []Customer{{Name: "Alice", Login: "alice", Number: 1}})
	if err != nil {
		t.Fatal(err)
	}
	orders, err := goql.Create(ctx, e, []Order{{
		Total: 100, Customer: customers[0], Priority: "High", ShippingMethod: "Standard",
	}})
	if err != nil {
		t.Fatal(err)
	}

	got, err := goql.Get[Order](ctx, e, orders[0].ID, goql.Preload{Fields: []string{"Customer"}})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(got))
	if got[0].Customer == nil {
		t.Fatal("Preload was not honoured by Get")
	}
	assertEqual(t, "Alice", got[0].Customer.Name)
}

// An empty goql.Preload means "load nothing", overriding schema defaults (§7 D2/D3). Get
// resolves preloads through the same effectivePreload call, so the distinction must survive.
func TestIntegration_GetEmptyPreloadOverridesDefaults(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	customers, err := goql.Create(ctx, e, []Customer{{Name: "Alice", Login: "alice", Number: 1}})
	if err != nil {
		t.Fatal(err)
	}
	orders, err := goql.Create(ctx, e, []Order{{
		Total: 100, Customer: customers[0], Priority: "High", ShippingMethod: "Standard",
	}})
	if err != nil {
		t.Fatal(err)
	}

	got, err := goql.Get[Order](ctx, e, orders[0].ID, goql.Preload{})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(got))
	if got[0].Customer != nil {
		t.Fatal("an empty Preload should load no relations")
	}
}

// Transaction now joins rather than re-opening (§26). Get runs no transaction of its own,
// so it must observe uncommitted work done in the surrounding one.
func TestIntegration_GetSeesUncommittedWork(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	boom := errors.New("boom")
	var id int64

	err := e.Transaction(func(tx *goql.Engine) error {
		created, err := goql.Create(ctx, tx, []Customer{{Name: "Alice", Login: "alice", Number: 1}})
		if err != nil {
			return err
		}
		id = created[0].ID

		// Visible inside the transaction...
		inside, err := goql.Get[Customer](ctx, tx, id)
		if err != nil {
			return err
		}
		if len(inside) != 1 {
			t.Errorf("Get inside the transaction found %d rows, want 1", len(inside))
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("expected the rollback, got %v", err)
	}

	// ...and gone after the rollback.
	after, err := goql.Get[Customer](ctx, e, id)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 0, len(after))
}

// goql.Filter compiles to a correlated EXISTS (§23), which is a predicate — so it must
// compose inside a transaction that also writes, without the write's own transaction
// wrapping interfering.
func TestIntegration_FilterInsideTransaction(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	tags, err := goql.Create(ctx, e, []Tag{{Name: "urgent"}})
	if err != nil {
		t.Fatal(err)
	}
	customers, err := goql.Create(ctx, e, []Customer{{Name: "Alice", Login: "alice", Number: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := goql.Create(ctx, e, []Order{{
		Total: 100, Customer: customers[0], Priority: "Low",
		ShippingMethod: "Standard", Tags: []Tag{*tags[0]},
	}}); err != nil {
		t.Fatal(err)
	}

	// The lambda is hoisted out of the transaction closure deliberately: a goql lambda
	// written inside another closure cannot be located at runtime — see
	// TestIntegration_LambdaInsideTransactionIsRefused.
	promote := func(o *Order) {
		if goql.Filter(o.Tags, func(g *Tag) bool { return g.Name == "urgent" }) {
			o.Priority = "High"
		}
	}

	if err := e.Transaction(func(tx *goql.Engine) error {
		n, err := goql.Update[Order](ctx, tx, promote)
		if err != nil {
			return err
		}
		assertEqual(t, int64(1), n)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	got, err := goql.Select[Order](ctx, e, func(o *Order) bool { return o.Priority == "High" })
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(got))
}

// A deep path (§24d) reaching through a relation, combined with an option tail — path-keyed
// aliases must not disturb how ORDER BY resolves against the primary model.
func TestIntegration_DeepPathWithOptions(t *testing.T) {
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
		{Name: "leaf-b", Active: true, Parent: mids[0]},
		{Name: "leaf-a", Active: true, Parent: mids[0]},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := goql.Select[Category](ctx, e, func(c *Category, s *goql.Sort) bool {
		s.By = "Name"
		return c.Parent.Parent.Name == "root"
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 2, len(got))
	assertEqual(t, "leaf-a", got[0].Name)
	assertEqual(t, "leaf-b", got[1].Name)
}

// A goql lambda written inside another closure — the shape Transaction forces, since it
// takes one. The locator identifies a literal by the source position the runtime reports,
// which is stable for a nested closure where the compiler's funcN naming is not.
func TestIntegration_LambdaInsideTransaction(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	if err := e.Transaction(func(tx *goql.Engine) error {
		_, err := goql.Select[Customer](ctx, tx, func(c *Customer) bool {
			return c.Country == "USA"
		})
		return err
	}); err != nil {
		t.Fatalf("a lambda inside a transaction should work: %v", err)
	}
}

// Two levels deep, and each lambda must still resolve to its own body rather than a
// neighbour's — the silent-wrong-body failure the old positional scheme risked.
func TestIntegration_LambdasNestedTwoDeep(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	if err := e.Transaction(func(tx *goql.Engine) error {
		return tx.Transaction(func(inner *goql.Engine) error {
			usa, err := goql.Select[Customer](ctx, inner, func(c *Customer) bool {
				return c.Country == "USA"
			})
			if err != nil {
				return err
			}
			canada, err := goql.Select[Customer](ctx, inner, func(c *Customer) bool {
				return c.Country == "Canada"
			})
			if err != nil {
				return err
			}
			assertEqual(t, 1, len(usa))
			assertEqual(t, 1, len(canada))
			assertEqual(t, "Alice", usa[0].Name)
			assertEqual(t, "Bob", canada[0].Name)
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
}

// The two ways round it, both of which must keep working.
func TestIntegration_HoistedLambdaWorksInTransaction(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	usa := func(c *Customer) bool { return c.Country == "USA" }

	if err := e.Transaction(func(tx *goql.Engine) error {
		_, err := goql.Select[Customer](ctx, tx, usa)
		return err
	}); err != nil {
		t.Fatalf("a hoisted lambda should work inside a transaction: %v", err)
	}

	if err := e.Transaction(func(tx *goql.Engine) error {
		return selectUSA(ctx, tx)
	}); err != nil {
		t.Fatalf("a goql call in a named function should work inside a transaction: %v", err)
	}
}

func selectUSA(ctx context.Context, e *goql.Engine) error {
	_, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return c.Country == "USA"
	})
	return err
}

// The shape that exposed the flaw in position-only matching: the goql lambda's `func`
// keyword sits on the same line as the enclosing closure's first statement, so both anchor
// there — and the goql lambda's own body opens with a further literal, so the line of its
// first statement is claimed too. Only the signature tells them apart.
func TestIntegration_LambdaSharesALineWithItsEnclosingClosure(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	if err := e.Transaction(func(tx *goql.Engine) error {
		urgent, err := goql.Select[Order](ctx, tx, func(o *Order) bool {
			return goql.Filter(o.Tags, func(g *Tag) bool { return g.Name == "urgent" })
		})
		if err != nil {
			return err
		}
		// Picking the transaction closure instead would parse `if err != nil` as a predicate.
		if len(urgent) == 0 {
			t.Error("expected the tagged order, got none — the wrong literal may have been parsed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
