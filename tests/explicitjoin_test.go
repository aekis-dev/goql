package tests

import (
	"strings"
	"testing"

	"github.com/aekis-dev/goql"
)

// An explicit join states the condition and the kind, where §11's implicit rule could only
// ever produce an inner join with the tables comma-listed in FROM.
func TestJoin_Explicit(t *testing.T) {
	body := parseSource(t, `func(i *Invoice, p *Payment, j *goql.Join) bool {
		j.Model = p
		j.On = i.Ref == p.Ref
		return p.Method == "card"
	}`)

	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, q.SQL, `FROM "invoices" i INNER JOIN "payments" p ON i."ref" = p."ref"`)
	assertContains(t, q.SQL, `WHERE p."method" = ?`)
}

// The kind is what an equality between two participants cannot express.
func TestJoin_Left(t *testing.T) {
	body := parseSource(t, `func(i *Invoice, p *Payment, j *goql.Join) bool {
		j.Model = p
		j.On = i.Ref == p.Ref
		j.Type = goql.Left
		return i.Status == "open"
	}`)

	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, q.SQL, `LEFT JOIN "payments" p ON`)
}

// The joined table appears once: in the JOIN, not also in the FROM list, which would
// cross-join it before its condition applied.
func TestJoin_TableNotAlsoInFrom(t *testing.T) {
	body := parseSource(t, `func(i *Invoice, p *Payment, j *goql.Join) bool {
		j.Model = p
		j.On = i.Ref == p.Ref
		return p.Method == "card"
	}`)

	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)
	if strings.Contains(q.SQL, `FROM "invoices" i, "payments" p`) {
		t.Fatalf("joined table must not also be in FROM:\n%s", q.SQL)
	}
	if got := strings.Count(q.SQL, `"payments"`); got != 1 {
		t.Fatalf("expected payments named once, got %d:\n%s", got, q.SQL)
	}
}

// Values bound by the ON condition are placed before the WHERE's, because the join is
// rendered first — which Postgres numbering makes visible.
func TestJoin_PlaceholderOrder(t *testing.T) {
	body := parseSource(t, `func(i *Invoice, p *Payment, j *goql.Join) bool {
		j.Model = p
		j.On = goql.Condition(p.Method, "=", "card")
		return i.Status == "open"
	}`)

	q, err := postgres.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, q.SQL, `ON p."method" = $1`)
	assertContains(t, q.SQL, `WHERE i."status" = $2`)
	if len(q.Args) != 2 || q.Args[0] != "card" || q.Args[1] != "open" {
		t.Fatalf("expected args in emission order, got %v", q.Args)
	}
}

// MySQL has no FULL JOIN, so it is refused rather than emitted and left to fail at the server.
func TestJoin_UnsupportedKindRefused(t *testing.T) {
	body := parseSource(t, `func(i *Invoice, p *Payment, j *goql.Join) bool {
		j.Model = p
		j.On = i.Ref == p.Ref
		j.Type = goql.Full
		return i.Status == "open"
	}`)

	if _, err := sqlite.LambdaSearch(body, nil); err != nil {
		t.Fatalf("SQLite supports FULL JOIN: %v", err)
	}
	_, err := mysql.LambdaSearch(body, nil)
	if err == nil || !strings.Contains(err.Error(), "FULL") {
		t.Fatalf("expected MySQL to refuse FULL JOIN, got: %v", err)
	}
}

// A join with no condition is not a join goql will guess at.
func TestJoin_RequiresCondition(t *testing.T) {
	_, err := (&goql.DebugExecutor{}).ParseQueryFromSource(`func(i *Invoice, p *Payment, j *goql.Join) bool {
		j.Model = p
		return i.Status == "open"
	}`, "Select")
	if err == nil || !strings.Contains(err.Error(), "On is not set") {
		t.Fatalf("expected a join without a condition to be refused, got: %v", err)
	}
}

func TestJoin_RequiresModel(t *testing.T) {
	_, err := (&goql.DebugExecutor{}).ParseQueryFromSource(`func(i *Invoice, p *Payment, j *goql.Join) bool {
		j.On = i.Ref == p.Ref
		return i.Status == "open"
	}`, "Select")
	if err == nil || !strings.Contains(err.Error(), "Model is not set") {
		t.Fatalf("expected a join without a model to be refused, got: %v", err)
	}
}

// Model must name a declared parameter, not an arbitrary value.
func TestJoin_ModelMustBeDeclared(t *testing.T) {
	_, err := (&goql.DebugExecutor{}).ParseQueryFromSource(`func(i *Invoice, j *goql.Join) bool {
		j.Model = "payments"
		j.On = i.Status == "open"
		return true
	}`, "Select")
	if err == nil || !strings.Contains(err.Error(), "model parameters") {
		t.Fatalf("expected join.Model to require a declared model, got: %v", err)
	}
}

// Update and Delete reach other tables through relations, so an explicit join is refused
// rather than ignored.
func TestJoin_RejectedByUpdate(t *testing.T) {
	body, err := (&goql.DebugExecutor{}).ParseQueryFromSource(`func(o *Order, p *Payment, j *goql.Join) {
		j.Model = p
		j.On = o.Priority == p.Ref
		o.ShippingMethod = "Express"
	}`, "Update")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.LambdaWrite(body); err == nil ||
		!strings.Contains(err.Error(), "explicit join") {
		t.Fatalf("expected Update to refuse an explicit join, got: %v", err)
	}
}

// End to end against a real database: a LEFT join keeps rows with no match.
func TestJoin_LeftKeepsUnmatchedRows(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	if err := e.CreateTables(&Invoice{}, &Payment{}); err != nil {
		t.Fatal(err)
	}
	if _, err := goql.Create(ctx, e, []Invoice{
		{Ref: "A-1", Amount: 100, Status: "open"},
		{Ref: "A-2", Amount: 200, Status: "open"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := goql.Create(ctx, e, []Payment{
		{Ref: "A-1", Amount: 100, Method: "card"},
	}); err != nil {
		t.Fatal(err)
	}

	inner, err := goql.Select[Invoice](ctx, e, func(i *Invoice, p *Payment, j *goql.Join) bool {
		j.Model = p
		j.On = i.Ref == p.Ref
		return i.Status == "open"
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inner) != 1 {
		t.Fatalf("expected 1 invoice from an inner join, got %d", len(inner))
	}

	left, err := goql.Select[Invoice](ctx, e, func(i *Invoice, p *Payment, j *goql.Join) bool {
		j.Model = p
		j.On = i.Ref == p.Ref
		j.Type = goql.Left
		return i.Status == "open"
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 2 {
		t.Fatalf("expected 2 invoices from a left join, got %d", len(left))
	}
}
