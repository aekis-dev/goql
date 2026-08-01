package tests

import (
	"context"
	"database/sql"
	"os"
	"reflect"
	"testing"

	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/tests/models"
	_ "github.com/mattn/go-sqlite3"
)

type Widget = models.Widget
type Meta = models.Meta

func setupWidgetDB(t *testing.T) (context.Context, *goql.Engine, *sql.DB, func()) {
	t.Helper()
	dbPath := t.Name() + ".db"
	os.Remove(dbPath)
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	e := goql.New(db, goql.SQLite{})
	if err := e.CreateTables(&Widget{}); err != nil {
		t.Fatal(err)
	}
	return context.Background(), e, db, func() { db.Close(); os.Remove(dbPath) }
}

func rawJSON(t *testing.T, db *sql.DB, id int64) (meta, tags, attrs string) {
	t.Helper()
	var m, tg, a []byte
	if err := db.QueryRow(`SELECT meta, tags, attrs FROM widgets WHERE id = ?`, id).Scan(&m, &tg, &a); err != nil {
		t.Fatalf("scan raw: %v", err)
	}
	return string(m), string(tg), string(a)
}

func TestJSON_CreatePersists(t *testing.T) {
	ctx, e, db, cleanup := setupWidgetDB(t)
	defer cleanup()

	res, err := goql.Create(ctx, e, []Widget{
		{Name: "w1", Meta: Meta{Color: "red", Size: 5}, Tags: []string{"a", "b"}, Attrs: map[string]any{"k": "v"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	w := res[0]

	meta, tags, attrs := rawJSON(t, db, w.ID)
	if meta != `{"color":"red","size":5}` {
		t.Errorf("meta = %q", meta)
	}
	if tags != `["a","b"]` {
		t.Errorf("tags = %q", tags)
	}
	if attrs != `{"k":"v"}` {
		t.Errorf("attrs = %q", attrs)
	}
}

func TestJSON_EntityWritePersists(t *testing.T) {
	ctx, e, db, cleanup := setupWidgetDB(t)
	defer cleanup()

	res, err := goql.Create(ctx, e, []Widget{
		{Name: "w1", Meta: Meta{Color: "red", Size: 5}, Tags: []string{"a"}, Attrs: map[string]any{"k": "v"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	w := res[0]

	w.Meta = Meta{Color: "blue", Size: 9}
	w.Tags = []string{"x", "y"}
	w.Attrs = map[string]any{"k2": "v2"}
	if _, err := goql.Write(ctx, e, []Widget{*w}); err != nil {
		t.Fatalf("write: %v", err)
	}

	meta, tags, attrs := rawJSON(t, db, w.ID)
	if meta != `{"color":"blue","size":9}` {
		t.Errorf("meta = %q", meta)
	}
	if tags != `["x","y"]` {
		t.Errorf("tags = %q", tags)
	}
	if attrs != `{"k2":"v2"}` {
		t.Errorf("attrs = %q", attrs)
	}
}

func TestJSON_SearchRoundTrips(t *testing.T) {
	ctx, e, _, cleanup := setupWidgetDB(t)
	defer cleanup()

	if _, err := goql.Create(ctx, e, []Widget{
		{Name: "w1", Meta: Meta{Color: "red", Size: 5}, Tags: []string{"a", "b"}, Attrs: map[string]any{"k": "v"}},
		{Name: "w2", Meta: Meta{Color: "green", Size: 1}},
	}); err != nil {
		t.Fatal(err)
	}

	// Filtering on a self column used to hit the table-alias bug (FROM widgets w …
	// WHERE widgets.name); that is fixed, so the predicate is exercised for real here.
	found, err := goql.Select[Widget](ctx, e, func(w *Widget) bool {
		return w.Name == "w1"
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 widget, got %d", len(found))
	}
	got := found[0]

	if !reflect.DeepEqual(got.Meta, Meta{Color: "red", Size: 5}) {
		t.Errorf("Meta = %+v", got.Meta)
	}
	if !reflect.DeepEqual(got.Tags, []string{"a", "b"}) {
		t.Errorf("Tags = %v", got.Tags)
	}
	if got.Attrs["k"] != "v" {
		t.Errorf("Attrs = %v", got.Attrs)
	}
}
