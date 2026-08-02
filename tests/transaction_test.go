package tests

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/aekis-dev/goql"
	_ "github.com/mattn/go-sqlite3"
)

// Create, Write, Delete and Insert each wrap themselves in a transaction. Inside a user
// transaction that used to open a second, independent one — so an outer rollback did not
// undo the inner work, and a pool smaller than the nesting depth deadlocked.
//
// These tests need control over the pool, so they open their own database rather than
// using setupDB.

func txDB(t *testing.T, maxOpen int) (*goql.Engine, *sql.DB, func()) {
	t.Helper()

	path := t.Name() + ".db"
	os.Remove(path)

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if maxOpen > 0 {
		db.SetMaxOpenConns(maxOpen)
	}

	e := goql.New(db, goql.SQLite{})
	if err := e.CreateTables(&Customer{}); err != nil {
		t.Fatal(err)
	}
	return e, db, func() { db.Close(); os.Remove(path) }
}

// Rolling the outer transaction back must undo work done by a nested call.
func TestTransaction_NestedJoinsOuter(t *testing.T) {
	e, _, cleanup := txDB(t, 0)
	defer cleanup()
	ctx := context.Background()

	boom := errors.New("boom")
	err := e.Transaction(func(outer *goql.Engine) error {
		if _, err := goql.Create(ctx, outer, []Customer{
			{Name: "Alice", Login: "alice", Number: 1},
		}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("expected the outer error, got %v", err)
	}

	rows, err := goql.Search(ctx, e, Customer{})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 0, len(rows))
}

// The committing half: work done by a nested call is visible once the outer commits.
func TestTransaction_NestedCommitsWithOuter(t *testing.T) {
	e, _, cleanup := txDB(t, 0)
	defer cleanup()
	ctx := context.Background()

	if err := e.Transaction(func(outer *goql.Engine) error {
		_, err := goql.Create(ctx, outer, []Customer{{Name: "Alice", Login: "alice", Number: 1}})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := goql.Search(ctx, e, Customer{})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))
}

// A second BeginTx would wait for a connection the outer transaction is still holding.
// With a pool of one that is a deadlock, not a slowdown.
func TestTransaction_NestedDoesNotExhaustPool(t *testing.T) {
	e, _, cleanup := txDB(t, 1)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- e.Transaction(func(outer *goql.Engine) error {
			_, err := goql.Create(ctx, outer, []Customer{{Name: "Alice", Login: "alice", Number: 1}})
			return err
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("deadlock: the nested call waited for a connection the outer transaction holds")
	}
}

// A panic must release the connection on its way out. Without a deferred rollback the
// transaction is never finished, so its connection never returns to the pool.
func TestTransaction_PanicReleasesConnection(t *testing.T) {
	e, db, cleanup := txDB(t, 1)
	defer cleanup()

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic should propagate to the caller, not be swallowed")
			}
		}()
		_ = e.Transaction(func(inner *goql.Engine) error {
			panic("boom")
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connection leaked by the panicking transaction: %v", err)
	}
	if inUse := db.Stats().InUse; inUse != 0 {
		t.Fatalf("expected the connection returned to the pool, %d still in use", inUse)
	}
}

// A panic must also roll the work back, not leave it half-applied.
func TestTransaction_PanicRollsBack(t *testing.T) {
	e, _, cleanup := txDB(t, 0)
	defer cleanup()
	ctx := context.Background()

	func() {
		defer func() { recover() }()
		_ = e.Transaction(func(inner *goql.Engine) error {
			if _, err := goql.Create(ctx, inner, []Customer{
				{Name: "Alice", Login: "alice", Number: 1},
			}); err != nil {
				return err
			}
			panic("boom")
		})
	}()

	rows, err := goql.Search(ctx, e, Customer{})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 0, len(rows))
}
