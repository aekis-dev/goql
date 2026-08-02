package tests

import (
	"strings"
	"testing"

	"github.com/aekis-dev/goql"
)

// j.Field declares a join by naming the relation path that reaches it, instead of writing the
// condition out. The models already say how each hop relates, so the ON clauses are derived.
//
// Unlike a path written in a predicate, this gives the far row a handle — so it can be sorted
// by, projected, or kept with LEFT semantics, none of which a path can do.

func TestJoinField_OneHop(t *testing.T) {
	body := parseSource(t, `func(o *Order, c *Customer, j *goql.Join) bool {
		j.Field = o.Customer
		j.Model = c
		return c.Country == "USA"
	}`)

	q, err := sqlite.LambdaSearch(body, body.Body.Options)
	assertNoError(t, err)
	assertEqual(t, `SELECT o.* FROM "orders" o `+
		`INNER JOIN "customers" c ON o."customer_id" = c."id" `+
		`WHERE c."country" = ?`, q.SQL)
}

func TestJoinField_MultiHop(t *testing.T) {
	body := parseSource(t, `func(c *Category, g *Category, j *goql.Join) bool {
		j.Field = c.Parent.Parent
		j.Model = g
		return g.Name == "root"
	}`)

	q, err := sqlite.LambdaSearch(body, body.Body.Options)
	assertNoError(t, err)
	assertEqual(t, `SELECT c.* FROM "categories" c `+
		`INNER JOIN "categories" c2 ON c."parent_id" = c2."id" `+
		`INNER JOIN "categories" c3 ON c2."parent_id" = c3."id" `+
		`WHERE c3."name" = ?`, q.SQL)
}

// A LEFT applies to every hop. A LEFT followed by an INNER would drop exactly the rows the
// LEFT existed to keep.
func TestJoinField_TypeAppliesToEveryHop(t *testing.T) {
	body := parseSource(t, `func(c *Category, g *Category, j *goql.Join) bool {
		j.Field = c.Parent.Parent
		j.Model = g
		j.Type = goql.Left
		return c.Active == true
	}`)

	q, err := sqlite.LambdaSearch(body, body.Body.Options)
	assertNoError(t, err)
	if n := strings.Count(q.SQL, "LEFT JOIN"); n != 2 {
		t.Fatalf("expected both hops LEFT, got %d: %s", n, q.SQL)
	}
	if strings.Contains(q.SQL, "INNER JOIN") {
		t.Fatalf("no hop should stay INNER: %s", q.SQL)
	}
}

// An extra condition lands in the last hop's ON, which for an outer join is the only place a
// filter can go without turning it back into an inner join.
func TestJoinField_OnJoinsTheLastHop(t *testing.T) {
	body := parseSource(t, `func(o *Order, c *Customer, j *goql.Join) bool {
		j.Field = o.Customer
		j.Model = c
		j.Type = goql.Left
		j.On = c.Status == "Active"
		return o.Total > 100
	}`)

	q, err := sqlite.LambdaSearch(body, body.Body.Options)
	assertNoError(t, err)
	assertContains(t, q.SQL, `LEFT JOIN "customers" c ON o."customer_id" = c."id" AND c."status" = ?`)
	assertContains(t, q.SQL, `WHERE o."total_amount" > ?`)
}

// A collection may be joined — this is where row multiplication is asked for deliberately,
// as opposed to a predicate, where goql.Filter keeps it out.
func TestJoinField_Collection(t *testing.T) {
	body := parseSource(t, `func(o *Order, tg *Tag, j *goql.Join) bool {
		j.Field = o.Tags
		j.Model = tg
		return tg.Name == "urgent"
	}`)

	q, err := sqlite.LambdaSearch(body, body.Body.Options)
	assertNoError(t, err)
	assertContains(t, q.SQL, `INNER JOIN "order_tags"`)
	assertContains(t, q.SQL, `INNER JOIN "tags"`)
}

// The joined table must not also appear in the FROM list, which would cross-join it before
// the ON condition applied.
func TestJoinField_TableNotAlsoInFrom(t *testing.T) {
	body := parseSource(t, `func(o *Order, c *Customer, j *goql.Join) bool {
		j.Field = o.Customer
		j.Model = c
		return c.Country == "USA"
	}`)

	q, err := sqlite.LambdaSearch(body, body.Body.Options)
	assertNoError(t, err)
	if strings.Contains(q.SQL, `"orders" o, "customers"`) {
		t.Fatalf("the joined table is also comma-joined: %s", q.SQL)
	}
	if n := strings.Count(q.SQL, `"customers"`); n != 1 {
		t.Fatalf("expected customers named once, got %d: %s", n, q.SQL)
	}
}

// Postgres numbers placeholders, so an ON value must bind before the WHERE it precedes.
func TestJoinField_PlaceholderOrder(t *testing.T) {
	body := parseSource(t, `func(o *Order, c *Customer, j *goql.Join) bool {
		j.Field = o.Customer
		j.Model = c
		j.On = c.Status == "Active"
		return o.Total > 100
	}`)

	q, err := postgres.LambdaSearch(body, body.Body.Options)
	assertNoError(t, err)
	assertContains(t, q.SQL, `c."status" = $1`)
	assertContains(t, q.SQL, `o."total_amount" > $2`)
	assertEqual(t, []any{"Active", int64(100)}, q.Args)
}

// The handle can be sorted by — the thing a path in a predicate cannot do.
func TestJoinField_SortByJoinedRow(t *testing.T) {
	body := parseSource(t, `func(o *Order, c *Customer, j *goql.Join, s *goql.Sort) bool {
		j.Field = o.Customer
		j.Model = c
		s.By = "Total"
		return c.Country == "USA"
	}`)

	q, err := sqlite.LambdaSearch(body, body.Body.Options)
	assertNoError(t, err)
	assertContains(t, q.SQL, `ORDER BY`)
}

func TestJoinField_RejectsPlainColumn(t *testing.T) {
	_, err := (&goql.DebugExecutor{}).ParseQueryFromSource(`func(o *Order, c *Customer, j *goql.Join) bool {
		j.Field = o.Priority
		j.Model = c
		return c.Country == "USA"
	}`, "Select")

	if err == nil {
		t.Fatal("expected a plain column to be refused")
	}
	assertContains(t, err.Error(), "is a plain column")
}

func TestJoinField_RejectsMismatchedModel(t *testing.T) {
	_, err := (&goql.DebugExecutor{}).ParseQueryFromSource(`func(o *Order, tg *Tag, j *goql.Join) bool {
		j.Field = o.Customer
		j.Model = tg
		return tg.Name == "urgent"
	}`, "Select")

	if err == nil {
		t.Fatal("expected a handle of the wrong model to be refused")
	}
	assertContains(t, err.Error(), "arrives at")
}

func TestJoinField_RequiresAHandle(t *testing.T) {
	_, err := (&goql.DebugExecutor{}).ParseQueryFromSource(`func(o *Order, c *Customer, j *goql.Join) bool {
		j.Field = o.Customer
		return o.Total > 100
	}`, "Select")

	if err == nil {
		t.Fatal("expected a path join with no handle to be refused")
	}
	assertContains(t, err.Error(), "needs a handle")
}

// End to end.
func TestJoinField_LeftKeepsUnmatchedRows(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	if err := e.CreateTables(&Category{}); err != nil {
		t.Fatal(err)
	}

	roots, err := goql.Create(ctx, e, []Category{{Name: "root", Active: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := goql.Create(ctx, e, []Category{{Name: "child", Active: true, Parent: roots[0]}}); err != nil {
		t.Fatal(err)
	}

	inner, err := goql.Select[Category](ctx, e, func(c *Category, p *Category, j *goql.Join) bool {
		j.Field = c.Parent
		j.Model = p
		return c.Active == true
	})
	if err != nil {
		t.Fatal(err)
	}

	left, err := goql.Select[Category](ctx, e, func(c *Category, p *Category, j *goql.Join) bool {
		j.Field = c.Parent
		j.Model = p
		j.Type = goql.Left
		return c.Active == true
	})
	if err != nil {
		t.Fatal(err)
	}

	// The root has no parent: an inner join drops it, a left join keeps it.
	assertEqual(t, 1, len(inner))
	assertEqual(t, 2, len(left))
}
