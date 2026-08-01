package tests

import (
	"strings"
	"testing"

	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/query"
)

// A query bound to a name and then read from is a CTE. The aggregate over an aggregate —
// the average of per-customer totals — has no other spelling.
type CustomerTotal struct {
	Customer int64
	Total    float64
}

type Summary struct {
	Average float64
	Biggest float64
}

func TestCTE_Rendering(t *testing.T) {
	body := parseSource(t, `func(s *Summary, t *CustomerTotal, from *goql.From) bool {
		totals, _ := goql.Select[CustomerTotal](ctx, e, func(ct *CustomerTotal, o *Order, f *goql.From, g *goql.Group) bool {
			f.Model = o
			g.By = []string{"Customer"}
			ct.Total = goql.Sum(o.Total)
			return o.Total > 0
		})
		from.Query = totals
		from.Model = t
		s.Average = goql.Avg(t.Total)
		return t.Total > 0
	}`)

	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, q.SQL, `WITH "totals" AS (SELECT`)
	assertContains(t, q.SQL, `FROM "totals" t`)
	assertContains(t, q.SQL, `AVG(t."Total") AS "Average"`)
	assertContains(t, q.SQL, `WHERE t."Total" > ?`)
}

// The definition binds before the outer statement, which Postgres numbering makes visible.
func TestCTE_PlaceholderOrder(t *testing.T) {
	body := parseSource(t, `func(s *Summary, t *CustomerTotal, from *goql.From) bool {
		totals, _ := goql.Select[CustomerTotal](ctx, e, func(ct *CustomerTotal, o *Order, f *goql.From, g *goql.Group) bool {
			f.Model = o
			g.By = []string{"Customer"}
			ct.Total = goql.Sum(o.Total)
			return o.Total > 100
		})
		from.Query = totals
		from.Model = t
		s.Average = goql.Avg(t.Total)
		return t.Total > 500
	}`)

	q, err := postgres.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, q.SQL, `> $1`)
	assertContains(t, q.SQL, `WHERE t."Total" > $2`)
	if len(q.Args) != 2 {
		t.Fatalf("expected 2 bound values, got %v", q.Args)
	}
}

// A column the defining query does not select is caught while parsing, because a CTE's
// columns are exactly its projection.
func TestCTE_UnknownColumn(t *testing.T) {
	_, err := (&goql.DebugExecutor{}).ParseQueryFromSource(`func(s *Summary, t *CustomerTotal, from *goql.From) bool {
		totals, _ := goql.Select[CustomerTotal](ctx, e, func(ct *CustomerTotal, o *Order, f *goql.From, g *goql.Group) bool {
			f.Model = o
			g.By = []string{"Customer"}
			ct.Total = goql.Sum(o.Total)
			return o.Total > 0
		})
		from.Query = totals
		from.Model = t
		s.Average = goql.Avg(t.Customer)
		return true
	}`, "Select")
	if err == nil || !strings.Contains(err.Error(), "does not select") {
		t.Fatalf("expected an unknown CTE column to be refused, got: %v", err)
	}
}

// A CTE is evaluated before the outer query has a row, so it cannot reference one.
func TestCTE_RejectsOuterReference(t *testing.T) {
	_, err := (&goql.DebugExecutor{}).ParseQueryFromSource(`func(s *Summary, o *Order, t *CustomerTotal, from *goql.From) bool {
		totals, _ := goql.Select[CustomerTotal](ctx, e, func(ct *CustomerTotal, x *Order, f *goql.From) bool {
			f.Model = x
			ct.Total = x.Total
			return x.Total > o.Total
		})
		from.Query = totals
		from.Model = t
		s.Average = goql.Avg(t.Total)
		return true
	}`, "Select")
	if err == nil || !strings.Contains(err.Error(), "enclosing query") {
		t.Fatalf("expected a correlated CTE to be refused, got: %v", err)
	}
}

// A query selecting whole model rows has no named columns to read from.
func TestCTE_RejectsUnprojectedQuery(t *testing.T) {
	_, err := (&goql.DebugExecutor{}).ParseQueryFromSource(`func(s *Summary, t *CustomerTotal, from *goql.From) bool {
		rows, _ := goql.Select[Order](ctx, e, func(o *Order) bool {
			return o.Total > 0
		})
		from.Query = rows
		from.Model = t
		s.Average = goql.Avg(t.Total)
		return true
	}`, "Select")
	if err == nil || !strings.Contains(err.Error(), "no named columns") {
		t.Fatalf("expected an unprojected query to be refused, got: %v", err)
	}
}

// End to end: an aggregate over an aggregate.
func TestCTE_AverageOfTotals(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Select[Summary](ctx, e,
		func(s *Summary, t *CustomerTotal, from *goql.From) bool {
			totals, _ := goql.Select[CustomerTotal](ctx, e,
				func(ct *CustomerTotal, o *Order, f *goql.From, g *goql.Group) bool {
					f.Model = o
					g.By = []string{"Customer"}
					ct.Total = goql.Sum(o.Total)
					return o.Total > 0
				})
			from.Query = totals
			from.Model = t
			s.Average = goql.Avg(t.Total)
			s.Biggest = goql.Max(t.Total)
			return t.Total > 0
		})
	if err != nil {
		t.Fatal(err)
	}
	// Seeded: customer 1 has 1500, customer 2 has 700 — so per-customer totals are
	// 1500 and 700, averaging 1100.
	if len(rows) != 1 {
		t.Fatalf("expected one summary row, got %d", len(rows))
	}
	if rows[0].Average != 1100 || rows[0].Biggest != 1500 {
		t.Fatalf("expected average 1100 and biggest 1500, got %+v", rows[0])
	}
}

// noCTE is MySQL as it behaved before 8.0. The three shipped specs all support WITH, so the
// derived-table fallback would otherwise be unreachable — and unverified.
type noCTE struct{ query.MySQL }

func (noCTE) SupportsCTE() bool { return false }

func TestCTE_DerivedTableFallback(t *testing.T) {
	body := parseSource(t, `func(s *Summary, t *CustomerTotal, from *goql.From) bool {
		totals, _ := goql.Select[CustomerTotal](ctx, e, func(ct *CustomerTotal, o *Order, f *goql.From, g *goql.Group) bool {
			f.Model = o
			g.By = []string{"Customer"}
			ct.Total = goql.Sum(o.Total)
			return o.Total > 0
		})
		from.Query = totals
		from.Model = t
		s.Average = goql.Avg(t.Total)
		return t.Total > 0
	}`)

	q, err := query.NewDialect(noCTE{}).LambdaSearch(body, nil)
	assertNoError(t, err)
	if strings.Contains(strings.ToUpper(q.SQL), "WITH ") {
		t.Fatalf("an engine without CTEs must not emit WITH:\n%s", q.SQL)
	}
	assertContains(t, q.SQL, "FROM (SELECT")
	assertContains(t, q.SQL, "GROUP BY o.`customer_id`) t")
	assertContains(t, q.SQL, "AVG(t.`Total`)")
}
