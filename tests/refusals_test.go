package tests

import (
	"strings"
	"testing"

	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/models"
)

// Every refusal the docs and design.md claim, checked through parsing and SQL generation. A
// refusal that does not fire is worse than a missing feature: the query compiles and answers
// something the caller did not ask for.
//
// Three further refusals are enforced at the API boundary rather than here, and have their
// own tests below: Insert rejecting Fields and Preload, and a recursive self handle passed
// straight to Select.
func TestRefusals_ParseAndBuild(t *testing.T) {
	cases := []struct {
		name string
		call string
		src  string
		want string // substring the error should contain ("" = any error)
	}{
		// docs/limitations.md — the query language
		{"nil comparison", "Select",
			`func(c *Customer) bool { return c.Deleted == nil }`, ""},
		{"filter through another relation", "Select",
			`func(o *Order) bool { return goql.Filter(o.Customer.Orders, func(x *Order) bool { return x.Total > 1 }) }`, ""},
		{"filter on the enclosing table", "Select",
			`func(o *Order) bool { return goql.Filter(o.Tags, func(t *Tag) bool { return goql.Filter(t.Orders, func(x *Order) bool { return x.Total > 1 }) }) }`, ""},
		{"captured variable", "Select",
			`func(o *Order) bool { return o.Total > minTotal }`, "captured variable"},
		{"arithmetic on text", "Select",
			`func(o *Order) bool { return o.Priority*2 == "x" }`, ""},

		// design.md §23 — for range retired
		{"for range over a relation", "Select",
			`func(o *Order) bool {
				for _, t := range o.Tags {
					if t.Name == "urgent" { return true }
				}
				return false
			}`, "Filter"},

		// design.md §10 — Insert option refusals
		{"assign to the source in an Insert", "Insert",
			`func(a *OrderArchive, o *Order) bool {
				o.Priority = "changed"
				a.Reason = o.Priority
				return true
			}`, ""},
		{"read the destination in an Insert", "Insert",
			`func(a *OrderArchive, o *Order) bool {
				a.Reason = o.Priority
				return a.Reason == "x"
			}`, ""},

		// design.md §11/§18 — Update and Delete refuse join participants
		{"Update with a participant", "Update",
			`func(o *Order, c *Customer) bool { o.Priority = "High"; return o.Total > c.Age }`, ""},
		{"Delete with an explicit join", "Delete",
			`func(o *Order, c *Customer, j *goql.Join) bool {
				j.Model = c
				j.On = o.Customer.ID == c.ID
				return c.Country == "USA"
			}`, ""},

		// design.md §15 — HAVING refusals
		{"aggregate ORed with a row filter", "Select",
			`func(t *PriorityTotals, o *Order, f *goql.From) bool {
				f.Model = o
				t.Priority = o.Priority
				t.Total = goql.Sum(o.Total)
				return o.Total > 1 || goql.Sum(o.Total) > 10
			}`, ""},
		{"aggregate on the right of a comparison", "Select",
			`func(t *PriorityTotals, o *Order, f *goql.From) bool {
				f.Model = o
				t.Priority = o.Priority
				t.Total = goql.Sum(o.Total)
				return 10 < goql.Sum(o.Total)
			}`, ""},

		// design.md §6 C5 — options describe the whole query
		{"option set inside a branch", "Update",
			`func(o *Order, s *goql.Sort) bool {
				if o.Total > 100 { s.By = "Total" }
				o.Priority = "High"
				return true
			}`, ""},

		// design.md §21 — recursive term restrictions

		// design.md §14 — a projection must say which model it reads
		{"projection with no From.Model", "Select",
			`func(t *PriorityTotals, o *Order) bool {
				t.Priority = o.Priority
				t.Total = goql.Sum(o.Total)
				return true
			}`, ""},

		// design.md §24f — a path join needs a handle
		{"path join with no Model", "Select",
			`func(o *Order, j *goql.Join) bool {
				j.Field = o.Customer
				return o.Total > 100
			}`, "handle"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q, err := (&goql.DebugExecutor{}).ParseQueryFromSource(c.src, c.call)
			stage := "parse"
			if err == nil {
				// Several refusals are build-time, not parse-time. Carry it through.
				stage = "build"
				switch c.call {
				case "Select":
					_, err = sqlite.LambdaSearch(q, q.Body.Options)
				case "Update":
					_, err = sqlite.LambdaWrite(q)
				case "Delete":
					_, err = sqlite.LambdaDelete(q)
				case "Insert":
					dest, derr := models.GetModel(&OrderArchive{})
					if derr != nil {
						t.Fatal(derr)
					}
					_, err = sqlite.LambdaInsert(q, dest, q.Body.Options)
				}
			}
			if err == nil {
				t.Fatalf("REFUSAL DID NOT FIRE — parsed and built successfully")
			}
			t.Logf("refused at %s", stage)
			if c.want != "" && !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error does not mention %q: %v", c.want, err)
			}
			t.Logf("refused: %v", err)
		})
	}
}

// Insert refuses the options that cannot mean anything for an INSERT … SELECT (design.md
// §10). The guard is at the API boundary, so these go through goql.Insert.
//
// The lambdas are declared at function scope rather than inside a subtest: a goql lambda
// nested in another closure is refused by design (§12), which would mask what is being
// tested here.

func TestRefusals_InsertRejectsFields(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	if err := e.CreateTables(&OrderArchive{}); err != nil {
		t.Fatal(err)
	}

	_, err := goql.Insert[OrderArchive](ctx, e, func(a *OrderArchive, o *Order, f *goql.Fields) {
		f.Names = []string{"Reason"}
		a.Reason = o.Priority
	})
	if err == nil {
		t.Fatal("expected Fields to be refused")
	}
	assertContains(t, err.Error(), "Fields does not apply to Insert")
}

func TestRefusals_InsertRejectsPreload(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	if err := e.CreateTables(&OrderArchive{}); err != nil {
		t.Fatal(err)
	}

	_, err := goql.Insert[OrderArchive](ctx, e, func(a *OrderArchive, o *Order, p *goql.Preload) {
		p.Fields = []string{"Customer"}
		a.Reason = o.Priority
	})
	if err == nil {
		t.Fatal("expected Preload to be refused")
	}
	assertContains(t, err.Error(), "Preload does not apply to Insert")
}

// A recursive self handle is only meaningful for a query bound inside another lambda and
// read through from.Query. Passed straight to Select it used to be reported as "first
// parameter must be *CatNode, not " — pointing at the wrong thing entirely.
func TestRefusals_SelfHandleNeedsABinding(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	_, err := goql.Select[CatNode](ctx, e, func(tr []*CatNode) bool {
		return goql.UnionAll(tr)
	})
	if err == nil {
		t.Fatal("expected a bare self handle to be refused")
	}
	assertContains(t, err.Error(), "self handle of a recursive query")
	assertContains(t, err.Error(), "from.Query")
}
