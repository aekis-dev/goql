package tests

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/models"
	_ "github.com/mattn/go-sqlite3"
)

// A model registered only for the migration tests, so altering it cannot disturb the rest
// of the suite.
type Gadget struct {
	goql.Model
	Name  string
	Notes string
}

func init() {
	if err := models.AddModel(&Gadget{}, "gadgets",
		&models.Field{Name: "Name", Type: models.TypeVarchar, Size: 100, NotNull: true},
		&models.Field{Name: "Notes", Type: models.TypeText},
	); err != nil {
		panic(err)
	}
}

// setupEmptyDB opens a database with no tables, so migrations start from nothing.
func setupEmptyDB(t *testing.T) (context.Context, *goql.Engine, *sql.DB, func()) {
	t.Helper()
	path := t.Name() + ".db"
	os.Remove(path)

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	return context.Background(), goql.New(db, goql.SQLite{}), db, func() {
		db.Close()
		os.Remove(path)
	}
}

// columnsOf reads a table's column names straight from SQLite, independent of goql.
func columnsOf(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out[name] = true
	}
	return out
}

// A table that does not exist yet is created outright — nothing to ask about.
func TestMigrate_CreatesMissingTable(t *testing.T) {
	ctx, e, db, cleanup := setupEmptyDB(t)
	defer cleanup()

	entities := []models.Entity{&Gadget{}}

	plan, err := e.MigrationPlan(ctx, entities, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 0, len(plan.Questions))
	if len(plan.Changes) == 0 {
		t.Fatal("expected a create-table change")
	}
	assertEqual(t, goql.CreateTable, plan.Changes[0].Kind)

	summary, err := e.Migrate(ctx, entities, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Applied) == 0 {
		t.Fatal("expected changes to be applied")
	}

	cols := columnsOf(t, db, "gadgets")
	for _, want := range []string{"id", "name", "notes", "goql_created", "goql_updated"} {
		if !cols[want] {
			t.Errorf("expected column %s, got %v", want, cols)
		}
	}
}

// Once the schema matches, planning finds nothing to do.
func TestMigrate_NoChangesWhenUpToDate(t *testing.T) {
	ctx, e, _, cleanup := setupEmptyDB(t)
	defer cleanup()

	entities := []models.Entity{&Gadget{}}
	if _, err := e.Migrate(ctx, entities, nil); err != nil {
		t.Fatal(err)
	}

	plan, err := e.MigrationPlan(ctx, entities, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Empty() {
		t.Fatalf("expected an empty plan, got %d changes and %d questions",
			len(plan.Changes), len(plan.Questions))
	}
}

// A column the model gained is simply added: adding is not destructive, so it needs no
// confirmation.
func TestMigrate_AddsNewColumn(t *testing.T) {
	ctx, e, db, cleanup := setupEmptyDB(t)
	defer cleanup()
	entities := []models.Entity{&Gadget{}}

	// Start from a table missing the notes column.
	if _, err := db.Exec(`CREATE TABLE "gadgets" ("id" INTEGER PRIMARY KEY AUTOINCREMENT,
		"name" VARCHAR(100), "goql_created" TIMESTAMP, "goql_updated" TIMESTAMP,
		"goql_deleted" TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

	plan, err := e.MigrationPlan(ctx, entities, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 0, len(plan.Questions))
	assertEqual(t, 1, len(plan.Changes))
	assertEqual(t, goql.AddColumn, plan.Changes[0].Kind)
	assertEqual(t, "notes", plan.Changes[0].Column)
	assertEqual(t, false, plan.Changes[0].Destructive)

	if _, err := e.Migrate(ctx, entities, nil); err != nil {
		t.Fatal(err)
	}
	if !columnsOf(t, db, "gadgets")["notes"] {
		t.Error("expected notes to be added")
	}
}

// A column that disappeared alongside one that appeared is ambiguous: it may be a rename or
// a drop-and-add. goql asks rather than guessing, and refuses to apply until it is answered.
func TestMigrate_AsksAboutAmbiguousColumn(t *testing.T) {
	ctx, e, db, cleanup := setupEmptyDB(t)
	defer cleanup()
	entities := []models.Entity{&Gadget{}}

	// The table has "remarks" where the model now wants "notes".
	if _, err := db.Exec(`CREATE TABLE "gadgets" ("id" INTEGER PRIMARY KEY AUTOINCREMENT,
		"name" VARCHAR(100), "remarks" TEXT, "goql_created" TIMESTAMP,
		"goql_updated" TIMESTAMP, "goql_deleted" TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

	plan, err := e.MigrationPlan(ctx, entities, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(plan.Questions))
	question := plan.Questions[0]
	assertEqual(t, "gadgets.remarks", question.ID)
	assertContains(t, question.Prompt, "remarks")

	// Rename, drop and skip must all be offered.
	values := map[string]bool{}
	for _, option := range question.Options {
		values[option.Value] = true
	}
	if !values["rename:notes"] || !values["drop"] || !values["skip"] {
		t.Fatalf("expected rename/drop/skip options, got %v", values)
	}

	// Applying without an answer must do nothing.
	_, err = e.Migrate(ctx, entities, nil)
	assertError(t, err)
	if !errors.Is(err, goql.ErrUnresolvedQuestions) {
		t.Fatalf("expected ErrUnresolvedQuestions, got %v", err)
	}
}

// Answering "rename" preserves the data, which is the whole reason for asking.
func TestMigrate_RenamePreservesData(t *testing.T) {
	ctx, e, db, cleanup := setupEmptyDB(t)
	defer cleanup()
	entities := []models.Entity{&Gadget{}}

	if _, err := db.Exec(`CREATE TABLE "gadgets" ("id" INTEGER PRIMARY KEY AUTOINCREMENT,
		"name" VARCHAR(100), "remarks" TEXT, "goql_created" TIMESTAMP,
		"goql_updated" TIMESTAMP, "goql_deleted" TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO "gadgets" ("name", "remarks") VALUES ('widget', 'keep me')`); err != nil {
		t.Fatal(err)
	}

	decisions := map[string]string{"gadgets.remarks": "rename:notes"}

	plan, err := e.MigrationPlan(ctx, entities, decisions)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 0, len(plan.Questions))
	assertEqual(t, 1, len(plan.Changes))
	assertEqual(t, goql.RenameColumn, plan.Changes[0].Kind)
	// A rename must not be flagged destructive.
	assertEqual(t, false, plan.Changes[0].Destructive)

	if _, err := e.Migrate(ctx, entities, decisions); err != nil {
		t.Fatal(err)
	}

	cols := columnsOf(t, db, "gadgets")
	if !cols["notes"] || cols["remarks"] {
		t.Fatalf("expected remarks to become notes, got %v", cols)
	}

	var notes string
	if err := db.QueryRow(`SELECT "notes" FROM "gadgets"`).Scan(&notes); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "keep me", notes)
}

// Answering "drop" discards the column, and the change is marked destructive so a UI can
// warn before it runs.
func TestMigrate_DropIsMarkedDestructive(t *testing.T) {
	ctx, e, db, cleanup := setupEmptyDB(t)
	defer cleanup()
	entities := []models.Entity{&Gadget{}}

	if _, err := db.Exec(`CREATE TABLE "gadgets" ("id" INTEGER PRIMARY KEY AUTOINCREMENT,
		"name" VARCHAR(100), "notes" TEXT, "obsolete" TEXT, "goql_created" TIMESTAMP,
		"goql_updated" TIMESTAMP, "goql_deleted" TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

	decisions := map[string]string{"gadgets.obsolete": "drop"}
	plan, err := e.MigrationPlan(ctx, entities, decisions)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(plan.Changes))
	assertEqual(t, goql.DropColumn, plan.Changes[0].Kind)
	assertEqual(t, true, plan.Changes[0].Destructive)

	if _, err := e.Migrate(ctx, entities, decisions); err != nil {
		t.Fatal(err)
	}
	if columnsOf(t, db, "gadgets")["obsolete"] {
		t.Error("expected obsolete to be dropped")
	}
}

// Answering "skip" leaves the column in place, changing nothing.
func TestMigrate_SkipLeavesColumnAlone(t *testing.T) {
	ctx, e, db, cleanup := setupEmptyDB(t)
	defer cleanup()
	entities := []models.Entity{&Gadget{}}

	if _, err := db.Exec(`CREATE TABLE "gadgets" ("id" INTEGER PRIMARY KEY AUTOINCREMENT,
		"name" VARCHAR(100), "notes" TEXT, "legacy" TEXT, "goql_created" TIMESTAMP,
		"goql_updated" TIMESTAMP, "goql_deleted" TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

	decisions := map[string]string{"gadgets.legacy": "skip"}
	plan, err := e.MigrationPlan(ctx, entities, decisions)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 0, len(plan.Questions))
	assertEqual(t, 0, len(plan.Changes))

	if columnsOf(t, db, "gadgets")["legacy"] != true {
		t.Error("expected legacy to remain")
	}
}

// Tables goql knows nothing about are never inspected and never proposed for removal.
func TestMigrate_IgnoresUnmanagedTables(t *testing.T) {
	ctx, e, db, cleanup := setupEmptyDB(t)
	defer cleanup()

	if _, err := db.Exec(`CREATE TABLE "legacy_audit" ("id" INTEGER, "note" TEXT)`); err != nil {
		t.Fatal(err)
	}

	entities := []models.Entity{&Gadget{}}
	if _, err := e.Migrate(ctx, entities, nil); err != nil {
		t.Fatal(err)
	}

	// Still there, untouched.
	if !columnsOf(t, db, "legacy_audit")["note"] {
		t.Error("an unmanaged table must not be altered")
	}

	plan, err := e.MigrationPlan(ctx, entities, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range plan.Changes {
		if change.Table == "legacy_audit" {
			t.Errorf("unmanaged table appeared in the plan: %+v", change)
		}
	}
}

// Every applied change is recorded, since the audit table is the only history of how the
// database was brought into line with the models.
func TestMigrate_RecordsAuditLog(t *testing.T) {
	ctx, e, db, cleanup := setupEmptyDB(t)
	defer cleanup()
	entities := []models.Entity{&Gadget{}}

	summary, err := e.Migrate(ctx, entities, nil)
	if err != nil {
		t.Fatal(err)
	}

	var recorded int
	if err := db.QueryRow(`SELECT COUNT(*) FROM "goql_migrations"`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, len(summary.Applied), recorded)

	var kind, table string
	if err := db.QueryRow(
		`SELECT "kind", "table_name" FROM "goql_migrations" ORDER BY "rowid" LIMIT 1`,
	).Scan(&kind, &table); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, string(goql.CreateTable), kind)
	assertEqual(t, "gadgets", table)
}

// Introspection reads the live shape, which is the only basis for the diff.
func TestMigrate_IntrospectReadsLiveSchema(t *testing.T) {
	ctx, e, db, cleanup := setupEmptyDB(t)
	defer cleanup()

	if _, err := db.Exec(`CREATE TABLE "gadgets" ("id" INTEGER PRIMARY KEY,
		"name" TEXT NOT NULL, "notes" TEXT)`); err != nil {
		t.Fatal(err)
	}

	live, err := e.Introspect(ctx, []string{"gadgets", "not_created_yet"})
	if err != nil {
		t.Fatal(err)
	}

	table := live.Table("gadgets")
	if table == nil {
		t.Fatal("expected gadgets to be introspected")
	}
	assertEqual(t, true, table.Columns["name"].NotNull)
	assertEqual(t, false, table.Columns["notes"].NotNull)

	if live.Table("not_created_yet") != nil {
		t.Error("a table that does not exist must not appear")
	}
}

// --- Socket ---

// The socket refuses to start without the configuration that makes it safe.
func TestMigrateSocket_RequiresPathAndToken(t *testing.T) {
	_, e, _, cleanup := setupEmptyDB(t)
	defer cleanup()
	entities := []models.Entity{&Gadget{}}

	_, err := e.NewMigrateSocket(entities, goql.MigrateSocketConfig{Token: "t"})
	assertError(t, err)
	if !errors.Is(err, goql.ErrSocketConfig) {
		t.Fatalf("expected ErrSocketConfig, got %v", err)
	}

	_, err = e.NewMigrateSocket(entities, goql.MigrateSocketConfig{Path: "/tmp/x.sock"})
	assertError(t, err)
	if !errors.Is(err, goql.ErrSocketConfig) {
		t.Fatalf("expected ErrSocketConfig for a missing token, got %v", err)
	}
}

// End-to-end over the real socket: a client plans, answers the question, applies, and the
// data survives. This exercises the protocol the migration CLI speaks.
func TestMigrateSocket_EndToEnd(t *testing.T) {
	_, e, db, cleanup := setupEmptyDB(t)
	defer cleanup()
	entities := []models.Entity{&Gadget{}}

	if _, err := db.Exec(`CREATE TABLE "gadgets" ("id" INTEGER PRIMARY KEY AUTOINCREMENT,
		"name" VARCHAR(100), "remarks" TEXT, "goql_created" TIMESTAMP,
		"goql_updated" TIMESTAMP, "goql_deleted" TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO "gadgets" ("name", "remarks") VALUES ('widget', 'survive')`); err != nil {
		t.Fatal(err)
	}

	socketPath := filepath.Join(t.TempDir(), "migrate.sock")
	socket, err := e.NewMigrateSocket(entities, goql.MigrateSocketConfig{
		Path:  socketPath,
		Token: "s3cret",
	})
	if err != nil {
		t.Fatal(err)
	}

	served := make(chan error, 1)
	go func() { served <- socket.Serve() }()
	defer socket.Close()

	waitForSocket(t, socketPath)
	client := goql.DialMigrateSocket(socketPath)

	// A wrong token is refused, so the socket is not reachable by accident.
	resp, err := socketPost(client, "bad-token", "/plan", nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()

	// Plan: the renamed column is ambiguous, so it comes back as a question.
	var plan goql.Plan
	if err := socketJSON(client, "s3cret", "/plan", nil, &plan); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(plan.Questions))
	assertEqual(t, "gadgets.remarks", plan.Questions[0].ID)

	// Answer it and re-plan: the question resolves into a rename.
	decisions := map[string]string{"gadgets.remarks": "rename:notes"}
	var resolved goql.Plan
	if err := socketJSON(client, "s3cret", "/plan", decisions, &resolved); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 0, len(resolved.Questions))
	assertEqual(t, 1, len(resolved.Changes))
	assertEqual(t, goql.RenameColumn, resolved.Changes[0].Kind)

	// Apply through the socket.
	var applied goql.ApplyResponse
	if err := socketJSON(client, "s3cret", "/apply", decisions, &applied); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "", applied.Error)
	assertEqual(t, 1, len(applied.Summary.Applied))

	var notes string
	if err := db.QueryRow(`SELECT "notes" FROM "gadgets"`).Scan(&notes); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "survive", notes)

	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-served; err != nil {
		t.Fatalf("serve returned %v", err)
	}
}

// waitForSocket blocks until the listener is accepting, so the test does not race startup.
func waitForSocket(t *testing.T, path string) {
	t.Helper()
	for range 50 {
		if conn, err := net.Dial("unix", path); err == nil {
			conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s never became ready", path)
}

func socketPost(client *http.Client, token, path string, decisions map[string]string) (*http.Response, error) {
	body, err := json.Marshal(map[string]any{"decisions": decisions})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, "http://goql"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Goql-Token", token)
	return client.Do(req)
}

func socketJSON(client *http.Client, token, path string, decisions map[string]string, out any) error {
	resp, err := socketPost(client, token, path, decisions)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("socket returned %s: %s", resp.Status, payload)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// --- Type-change detection ---
//
// PlanAgainst diffs against a schema supplied here rather than one read from a database,
// which is how Postgres and MySQL detection is exercised without running those engines.

// liveGadgets builds a live schema for the gadgets table with the given column types.
func liveGadgets(types map[string]string) *goql.LiveSchema {
	table := &goql.LiveTable{Name: "gadgets", Columns: map[string]*goql.LiveColumn{}}
	for name, columnType := range types {
		table.Columns[name] = &goql.LiveColumn{Name: name, Type: columnType}
	}
	return &goql.LiveSchema{Tables: map[string]*goql.LiveTable{"gadgets": table}}
}

// gadgetColumns is what the Gadget model maps to, so only the column under test differs.
func gadgetColumns(nameType string) map[string]string {
	return map[string]string{
		"id":    "integer",
		"name":  nameType,
		"notes": "text",
		// The base timestamp fields declare Precision 6, and Postgres reports a precision
		// inside the type name.
		"goql_created": "timestamp(6) without time zone",
		"goql_updated": "timestamp(6) without time zone",
		"goql_deleted": "timestamp(6) without time zone",
	}
}

// Postgres reports `character varying(100)` for what goql emits as `varchar(100)`. Those mean
// the same thing, so nothing should be proposed.
func TestMigrateTypes_PostgresEquivalentSpellingIsNotAChange(t *testing.T) {
	_, _, _, cleanup := setupEmptyDB(t)
	defer cleanup()

	pg := goql.New(nil, goql.Postgres{})
	plan, err := pg.PlanAgainst(liveGadgets(gadgetColumns("character varying(100)")),
		[]models.Entity{&Gadget{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Empty() {
		t.Fatalf("expected no changes, got %d changes and %d questions: %+v",
			len(plan.Changes), len(plan.Questions), plan.Changes)
	}
}

// A real difference is detected, and always asked about rather than applied silently.
func TestMigrateTypes_PostgresRealDifferenceAsks(t *testing.T) {
	_, _, _, cleanup := setupEmptyDB(t)
	defer cleanup()

	pg := goql.New(nil, goql.Postgres{})
	entities := []models.Entity{&Gadget{}}
	live := liveGadgets(gadgetColumns("character varying(50)")) // model wants 100

	plan, err := pg.PlanAgainst(live, entities, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 0, len(plan.Changes))
	assertEqual(t, 1, len(plan.Questions))

	question := plan.Questions[0]
	assertEqual(t, "gadgets.name:type", question.ID)
	assertContains(t, question.Prompt, "character varying(50)")
	assertContains(t, question.Prompt, "varchar(100)")

	// Answering "change" produces an ALTER, flagged because narrowing truncates.
	resolved, err := pg.PlanAgainst(live, entities, map[string]string{"gadgets.name:type": "change"})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 0, len(resolved.Questions))
	assertEqual(t, 1, len(resolved.Changes))
	assertEqual(t, goql.ChangeType, resolved.Changes[0].Kind)
	assertEqual(t, true, resolved.Changes[0].Destructive)
	assertContains(t, resolved.Changes[0].SQL, "ALTER COLUMN")

	// Answering "skip" leaves it alone.
	skipped, err := pg.PlanAgainst(live, entities, map[string]string{"gadgets.name:type": "skip"})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 0, len(skipped.Changes))
}

// MySQL reports a boolean as tinyint(1); that must not read as a change.
func TestMigrateTypes_MySQLBooleanIsNotAChange(t *testing.T) {
	_, _, _, cleanup := setupEmptyDB(t)
	defer cleanup()

	my := goql.New(nil, goql.MySQL{})
	live := liveGadgets(map[string]string{
		"id":           "int",
		"name":         "varchar(100)",
		"notes":        "text",
		"goql_created": "datetime(6)",
		"goql_updated": "datetime(6)",
		"goql_deleted": "datetime(6)",
	})

	plan, err := my.PlanAgainst(live, []models.Entity{&Gadget{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Empty() {
		t.Fatalf("expected no changes, got %+v / %+v", plan.Changes, plan.Questions)
	}
}

// MySQL's MODIFY COLUMN is the shape for a type change there.
func TestMigrateTypes_MySQLUsesModifyColumn(t *testing.T) {
	_, _, _, cleanup := setupEmptyDB(t)
	defer cleanup()

	my := goql.New(nil, goql.MySQL{})
	live := liveGadgets(map[string]string{
		"id":           "int",
		"name":         "varchar(50)",
		"notes":        "text",
		"goql_created": "datetime(6)",
		"goql_updated": "datetime(6)",
		"goql_deleted": "datetime(6)",
	})

	plan, err := my.PlanAgainst(live, []models.Entity{&Gadget{}},
		map[string]string{"gadgets.name:type": "change"})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(plan.Changes))
	assertContains(t, plan.Changes[0].SQL, "MODIFY COLUMN")
}

// SQLite cannot alter a column in place, so the question says so and only offers to leave it.
func TestMigrateTypes_SQLiteCannotAlterInPlace(t *testing.T) {
	_, e, _, cleanup := setupEmptyDB(t)
	defer cleanup()

	entities := []models.Entity{&Gadget{}}
	// INTEGER affinity where the model wants text affinity: a genuine difference in SQLite.
	live := liveGadgets(gadgetColumns("INTEGER"))

	plan, err := e.PlanAgainst(live, entities, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(plan.Questions))
	assertEqual(t, 1, len(plan.Questions[0].Options))
	assertEqual(t, "skip", plan.Questions[0].Options[0].Value)
	assertContains(t, plan.Questions[0].Options[0].Detail, "cannot change a column type in place")

	// Choosing to change anyway is refused with an explanation rather than bad SQL.
	_, err = e.PlanAgainst(live, entities, map[string]string{"gadgets.name:type": "change"})
	assertError(t, err)
	assertContains(t, err.Error(), "rebuild")
}

// SQLite treats VARCHAR(100) and TEXT as the same storage, so no migration is proposed.
func TestMigrateTypes_SQLiteAffinityAvoidsNoOpChange(t *testing.T) {
	_, e, _, cleanup := setupEmptyDB(t)
	defer cleanup()

	plan, err := e.PlanAgainst(liveGadgets(gadgetColumns("TEXT")), []models.Entity{&Gadget{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Empty() {
		t.Fatalf("expected no changes for an affinity-equivalent type, got %+v", plan.Changes)
	}
}
