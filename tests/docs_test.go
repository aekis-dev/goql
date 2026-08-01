package tests

import (
	"testing"

	"github.com/aekis-dev/goql"
)

// These exercise the examples in the documentation, so a change that invalidates the docs
// fails the suite rather than being discovered by a reader.

// docs/predicates.md — Condition with LIKE / IN / IS NULL.
func TestDocs_ConditionForms(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	found, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return goql.Condition(c.Name, "LIKE", "Ali%") &&
			goql.Condition(c.Country, "IN", "USA", "Canada") &&
			goql.Condition(c.Deleted, "IS NULL")
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Name != "Alice" {
		t.Fatalf("expected Alice, got %d rows", len(found))
	}
}

// docs/options.md — several *Sort parameters compose in declaration order.
func TestDocs_MultipleSorts(t *testing.T) {
	body := parseSource(t, `func(c *Customer, first *goql.Sort, second *goql.Sort) bool {
		first.By = "Country"
		second.By = "Age"
		second.Desc = true
		return c.Age > 18
	}`)
	q, err := sqlite.LambdaSearch(body, body.Body.Options)
	assertNoError(t, err)
	assertContains(t, q.SQL, `ORDER BY c."country", c."age" DESC`)
}

// docs/subqueries.md — goql.Fields inside a nested lambda names the projected column.
func TestDocs_SubqueryProjectsANamedColumn(t *testing.T) {
	body := parseSource(t, `func(i *Invoice) bool {
		refs, _ := goql.Select[Payment](ctx, e, func(p *Payment, f *goql.Fields) bool {
			f.Names = []string{"Ref"}
			return p.Method == "card"
		})
		return goql.Condition(i.Ref, "IN", refs)
	}`)
	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, q.SQL, `IN (SELECT p."ref"`)
}

// docs/cte.md — joining a CTE with goql.Join.
func TestDocs_JoinACTE(t *testing.T) {
	body := parseSource(t, `func(s *Summary, t *CustomerTotal, o *Order, from *goql.From, j *goql.Join) bool {
		totals, _ := goql.Select[CustomerTotal](ctx, e, func(ct *CustomerTotal, x *Order, f *goql.From, g *goql.Group) bool {
			f.Model = x
			g.By = []string{"Customer"}
			ct.Total = goql.Sum(x.Total)
			return x.Total > 0
		})
		from.Model = o
		j.Query = totals
		j.Model = t
		j.On = o.Customer.ID == t.Total
		s.Average = goql.Avg(t.Total)
		return true
	}`)
	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, q.SQL, `INNER JOIN "totals" t ON o."customer_id" = t."Total"`)
}

// docs/insert-select.md — copying a model into itself, destination first.
func TestDocs_InsertIntoSameModel(t *testing.T) {
	body, err := (&goql.DebugExecutor{}).ParseQueryFromSource(`func(dst *Order, src *Order) {
		if src.Priority == "High" {
			dst.Total = src.Total
			dst.Priority = "Urgent"
			dst.Customer = src.Customer
		}
	}`, "Insert")
	if err != nil {
		t.Fatal(err)
	}
	// The destination and source are the same model, which is legal: two parameters of one
	// type, destination first.
	if body.Model != "Order" {
		t.Fatalf("expected the source model to be Order, got %q", body.Model)
	}
	if len(body.Body.Branches) != 1 || len(body.Body.Branches[0].Assignments) != 3 {
		t.Fatalf("expected three assignments in one branch, got %+v", body.Body.Branches)
	}
}

// docs/insert-select.md — Conflict declared as a lambda parameter.
func TestDocs_ConflictIgnore(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	if err := e.CreateTables(&OrderArchive{}); err != nil {
		t.Fatal(err)
	}
	seedData(t, ctx, e)

	rows, err := goql.Insert[OrderArchive](ctx, e,
		func(a *OrderArchive, o *Order, c *goql.Conflict) {
			c.Ignore = true
			a.Total = o.Total
			a.Reason = "copied"
		})
	if err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("expected 2 archived rows, got %d", rows)
	}
}
