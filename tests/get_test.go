package tests

import (
	"testing"

	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/models"
	"github.com/aekis-dev/goql/query"
)

// Get is the by-primary-key read. Before it, a key lookup had to be spelled as a
// partially-built entity — and because ID lives on the embedded goql.Model, that meant a
// nested struct literal for the most common query there is.

func TestGet_SingleKeyIsAnEquality(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	if err := e.CreateTables(&Customer{}); err != nil {
		t.Fatal(err)
	}

	created, err := goql.Create(ctx, e, []Customer{
		{Name: "Alice", Country: "USA", Login: "alice", Number: 1},
		{Name: "Bruno", Country: "Brazil", Login: "bruno", Number: 2},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := goql.Get[Customer](ctx, e, created[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(got))
	assertEqual(t, "Bruno", got[0].Name)
}

func TestGet_ManyKeys(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	if err := e.CreateTables(&Customer{}); err != nil {
		t.Fatal(err)
	}

	created, err := goql.Create(ctx, e, []Customer{
		{Name: "Alice", Country: "USA", Login: "alice", Number: 1},
		{Name: "Bruno", Country: "Brazil", Login: "bruno", Number: 2},
		{Name: "Chen", Country: "China", Login: "chen", Number: 3},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := goql.Get[Customer](ctx, e, []int64{created[0].ID, created[2].ID})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 2, len(got))
}

// A miss is an empty result, not an error — which is what makes the slice return work
// without a not-found sentinel.
func TestGet_MissingKeyIsEmpty(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	if err := e.CreateTables(&Customer{}); err != nil {
		t.Fatal(err)
	}

	got, err := goql.Get[Customer](ctx, e, int64(4242))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 0, len(got))
}

// No keys means no statement at all, rather than a WHERE that matches everything.
func TestGet_EmptyKeyListRunsNothing(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	if err := e.CreateTables(&Customer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := goql.Create(ctx, e, []Customer{{Name: "Alice", Country: "USA", Login: "alice", Number: 1}}); err != nil {
		t.Fatal(err)
	}

	got, err := goql.Get[Customer](ctx, e, []int64{})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 0, len(got))
}

// Options are the same carriers every other read takes.
func TestGet_TakesOptions(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	if err := e.CreateTables(&Customer{}); err != nil {
		t.Fatal(err)
	}

	created, err := goql.Create(ctx, e, []Customer{
		{Name: "Alice", Country: "USA", Login: "alice", Number: 1},
		{Name: "Bruno", Country: "Brazil", Login: "bruno", Number: 2},
		{Name: "Chen", Country: "China", Login: "chen", Number: 3},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := goql.Get[Customer](ctx, e,
		[]int64{created[0].ID, created[1].ID, created[2].ID},
		goql.Sort{By: "Name", Desc: true}, goql.Limit{Value: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 2, len(got))
	assertEqual(t, "Chen", got[0].Name)
}

// The emitted SQL, per shape.
func TestGet_SQL(t *testing.T) {
	schema, _ := models.GetModel(&Customer{})

	one, err := sqlite.PrimaryKeySearch([]any{int64(1)}, schema, nil)
	assertNoError(t, err)
	assertEqual(t, `SELECT "customers".* FROM "customers" WHERE "id" = ?`, one.SQL)

	many, err := sqlite.PrimaryKeySearch([]any{int64(1), int64(2)}, schema, nil)
	assertNoError(t, err)
	assertEqual(t, `SELECT "customers".* FROM "customers" WHERE "id" IN (?, ?)`, many.SQL)
}

// Postgres numbers its placeholders, and an options tail binds after the keys.
func TestGet_PostgresPlaceholders(t *testing.T) {
	schema, _ := models.GetModel(&Customer{})
	limit := 5

	q, err := postgres.PrimaryKeySearch([]any{int64(1), int64(2)}, schema,
		&query.Options{Limit: &limit})
	assertNoError(t, err)
	assertContains(t, q.SQL, `IN ($1, $2)`)
	assertContains(t, q.SQL, `LIMIT $3`)
}
