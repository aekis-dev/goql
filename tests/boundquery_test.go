package tests

import (
	"strings"
	"testing"

	"github.com/aekis-dev/goql"
)

// from.Query and join.Query name the same thing — a query bound in this lambda, read as a
// common table expression. They were written in separate phases and had drifted: the join
// path resolved the name but never registered the definition, so the statement referenced a
// table that was never defined. These pin the two paths to one behaviour.

const joinBoundQuery = `func(o *Order, r *CustomerTotal, j *goql.Join) bool {
	totals, _ := goql.Select[CustomerTotal](ctx, e, func(ct *CustomerTotal, x *Order, f *goql.From, g *goql.Group) bool {
		f.Model = x
		g.By = []string{"Customer"}
		ct.Total = goql.Sum(x.Total)
		return x.Total > 0
	})
	j.Query, j.Model = totals, r
	j.On = o.Total == r.Total
	return o.Total > 100
}`

// The defect: a joined query rendered as a bare table reference with no WITH clause, so the
// statement failed at the database with "no such table".
func TestBoundQuery_JoinDefinesItsCTE(t *testing.T) {
	body := parseSource(t, joinBoundQuery)

	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)

	assertContains(t, q.SQL, `WITH "totals" AS (SELECT`)
	assertContains(t, q.SQL, `INNER JOIN "totals" t ON`)
}

func TestBoundQuery_JoinRunsAgainstTheDatabase(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	_, err := goql.Select[Order](ctx, e, func(o *Order, r *CustomerTotal, j *goql.Join) bool {
		totals, _ := goql.Select[CustomerTotal](ctx, e, func(ct *CustomerTotal, x *Order, f *goql.From, g *goql.Group) bool {
			f.Model = x
			g.By = []string{"Customer"}
			ct.Total = goql.Sum(x.Total)
			return x.Total > 0
		})
		j.Query, j.Model = totals, r
		j.On = o.Total == r.Total
		return o.Total > 100
	})
	assertNoError(t, err)
}

// A definition named by two carriers is emitted once, not twice.
func TestBoundQuery_DefinedOncePerBinding(t *testing.T) {
	body := parseSource(t, `func(s *Summary, t *CustomerTotal, r *CustomerTotal, from *goql.From, j *goql.Join) bool {
		totals, _ := goql.Select[CustomerTotal](ctx, e, func(ct *CustomerTotal, x *Order, f *goql.From, g *goql.Group) bool {
			f.Model = x
			g.By = []string{"Customer"}
			ct.Total = goql.Sum(x.Total)
			return x.Total > 0
		})
		from.Query = totals
		from.Model = t
		j.Query, j.Model = totals, r
		j.On = t.Total == r.Total
		s.Average = goql.Avg(t.Total)
		return true
	}`)

	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)

	if n := strings.Count(q.SQL, `"totals" AS (`); n != 1 {
		t.Fatalf("expected the definition emitted once, got %d: %s", n, q.SQL)
	}
}

// Both carriers refuse a value-yielding call, and with the same explanation. join.Query used
// to reject it by falling through to a different check, reporting "has no named columns to
// join — project the columns it needs", which misdiagnoses it.
func TestBoundQuery_BothRefuseAnAggregate(t *testing.T) {
	sources := map[string]string{
		"from": `func(s *Summary, t *CustomerTotal, from *goql.From) bool {
			n, _ := goql.Exists[Customer](ctx, e, func(c *Customer) bool { return c.Country == "USA" })
			from.Query = n
			from.Model = t
			s.Average = goql.Avg(t.Total)
			return true
		}`,
		"join": `func(o *Order, r *CustomerTotal, j *goql.Join) bool {
			n, _ := goql.Exists[Customer](ctx, e, func(c *Customer) bool { return c.Country == "USA" })
			j.Query, j.Model = n, r
			j.On = o.Total == r.Total
			return o.Total > 100
		}`,
	}

	for carrier, source := range sources {
		_, err := (&goql.DebugExecutor{}).ParseQueryFromSource(source, "Select")
		if err == nil {
			t.Fatalf("%s.Query: expected a nested Exists to be refused", carrier)
		}
		if !strings.Contains(err.Error(), "yields a value rather than rows") {
			t.Fatalf("%s.Query: expected the value-yielding explanation, got: %v", carrier, err)
		}
	}
}

// A set-bodied query carries its projection on its branches. from.Query accepted one; the
// join path checked only Select and refused it.
func TestBoundQuery_BothAcceptASetBody(t *testing.T) {
	body := parseSource(t, `func(o *Order, r *Movement, j *goql.Join) bool {
		both, _ := goql.Select[Movement](ctx, e, func(m *Movement) bool {
			high, _ := goql.Select[Movement](ctx, e, func(m *Movement, x *Order, f *goql.From) bool {
				f.Model = x
				m.Ref = x.Priority
				m.Amount = x.Total
				return x.Total > 1000
			})
			low, _ := goql.Select[Movement](ctx, e, func(m *Movement, y *OrderArchive, f *goql.From) bool {
				f.Model = y
				m.Ref = y.Reason
				m.Amount = y.Total
				return y.Total <= 1000
			})
			return goql.Union(high, low)
		})
		j.Query, j.Model = both, r
		j.On = o.Total == r.Amount
		return o.Total > 100
	}`)

	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, q.SQL, `WITH "both" AS (`)
	assertContains(t, q.SQL, `UNION`)
}
