package tests

import (
	"strings"
	"testing"

	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/query"
)

// CatNode is the row a hierarchy walk produces: the category, plus how deep it sits.
type CatNode struct {
	ID    int64
	Name  string
	Depth int64
}

type TreeSummary struct {
	Nodes   int64
	Deepest int64
}

func TestRecursive_Rendering(t *testing.T) {
	body := parseSource(t, `func(s *TreeSummary, n *CatNode, from *goql.From) bool {
		tree, _ := goql.Select[CatNode](ctx, e, func(t []*CatNode) bool {
			roots, _ := goql.Select[CatNode](ctx, e, func(r *CatNode, c *Category, f *goql.From) bool {
				f.Model = c
				r.ID = c.ID
				r.Name = c.Name
				r.Depth = 0
				return goql.Condition(c.Parent, "IS NULL")
			})
			children, _ := goql.Select[CatNode](ctx, e, func(r *CatNode, prev *CatNode, c *Category, f *goql.From, j *goql.Join) bool {
				f.Model = c
				j.Query = t
				j.Model = prev
				j.On = c.Parent.ID == prev.ID
				r.ID = c.ID
				r.Name = c.Name
				r.Depth = prev.Depth + 1
				return prev.Depth < 5
			})
			return goql.UnionAll(roots, children)
		})
		from.Query = tree
		from.Model = n
		s.Nodes = goql.Count()
		s.Deepest = goql.Max(n.Depth)
		return true
	}`)

	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)

	assertContains(t, q.SQL, `WITH RECURSIVE "tree" AS (`)
	assertContains(t, q.SQL, `UNION ALL`)
	assertContains(t, q.SQL, `INNER JOIN "tree" t ON c."parent_id" = t."ID"`)
	assertContains(t, q.SQL, `(t."Depth" + ?) AS "Depth"`)
	assertContains(t, q.SQL, `WHERE t."Depth" < ?`)
	assertContains(t, q.SQL, `FROM "tree" t`)
	assertContains(t, q.SQL, `MAX(t."Depth") AS "Deepest"`)
}

// The anchor cannot reference the query being defined: nothing has been produced yet.
func TestRecursive_AnchorCannotSelfReference(t *testing.T) {
	_, err := (&goql.DebugExecutor{}).ParseQueryFromSource(`func(s *TreeSummary, n *CatNode, from *goql.From) bool {
		tree, _ := goql.Select[CatNode](ctx, e, func(t []*CatNode) bool {
			roots, _ := goql.Select[CatNode](ctx, e, func(r *CatNode, prev *CatNode, c *Category, f *goql.From, j *goql.Join) bool {
				f.Model = c
				j.Query = t
				j.Model = prev
				j.On = c.Parent.ID == prev.ID
				r.ID = c.ID
				r.Name = c.Name
				r.Depth = 0
				return true
			})
			children, _ := goql.Select[CatNode](ctx, e, func(r *CatNode, c *Category, f *goql.From) bool {
				f.Model = c
				r.ID = c.ID
				r.Name = c.Name
				r.Depth = 1
				return true
			})
			return goql.UnionAll(roots, children)
		})
		from.Query = tree
		from.Model = n
		s.Nodes = goql.Count()
		return true
	}`, "Select")
	if err == nil || !strings.Contains(err.Error(), "anchor") {
		t.Fatalf("expected a self-referencing anchor to be refused, got: %v", err)
	}
}

// End to end: walk a three-level hierarchy and report its depth.
func TestRecursive_WalksHierarchy(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	if err := e.CreateTables(&Category{}); err != nil {
		t.Fatal(err)
	}
	roots, err := goql.Create(ctx, e, []Category{{Name: "root", Active: true}})
	if err != nil {
		t.Fatal(err)
	}
	mid, err := goql.Create(ctx, e, []Category{{Name: "mid", Active: true, Parent: roots[0]}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := goql.Create(ctx, e, []Category{
		{Name: "leaf-a", Active: true, Parent: mid[0]},
		{Name: "leaf-b", Active: true, Parent: mid[0]},
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := goql.Select[TreeSummary](ctx, e,
		func(s *TreeSummary, n *CatNode, from *goql.From) bool {
			tree, _ := goql.Select[CatNode](ctx, e, func(t []*CatNode) bool {
				roots, _ := goql.Select[CatNode](ctx, e,
					func(r *CatNode, c *Category, f *goql.From) bool {
						f.Model = c
						r.ID = c.ID
						r.Name = c.Name
						r.Depth = 0
						return goql.Condition(c.Parent, "IS NULL")
					})
				children, _ := goql.Select[CatNode](ctx, e,
					func(r *CatNode, prev *CatNode, c *Category, f *goql.From, j *goql.Join) bool {
						f.Model = c
						j.Query = t
						j.Model = prev
						j.On = c.Parent.ID == prev.ID
						r.ID = c.ID
						r.Name = c.Name
						r.Depth = prev.Depth + 1
						return prev.Depth < 10
					})
				return goql.UnionAll(roots, children)
			})
			from.Query = tree
			from.Model = n
			s.Nodes = goql.Count()
			s.Deepest = goql.Max(n.Depth)
			return true
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one summary row, got %d", len(rows))
	}
	// root(0), mid(1), leaf-a(2), leaf-b(2)
	if rows[0].Nodes != 4 || rows[0].Deepest != 2 {
		t.Fatalf("expected 4 nodes with depth 2, got %+v", rows[0])
	}
}

// A derived table cannot reference itself, so an engine without WITH cannot express
// recursion at all — it is refused rather than degraded into something else.
func TestRecursive_NoFallbackWithoutCTEs(t *testing.T) {
	body := parseSource(t, `func(s *TreeSummary, n *CatNode, from *goql.From) bool {
		tree, _ := goql.Select[CatNode](ctx, e, func(t []*CatNode) bool {
			roots, _ := goql.Select[CatNode](ctx, e, func(r *CatNode, c *Category, f *goql.From) bool {
				f.Model = c
				r.ID = c.ID
				r.Name = c.Name
				r.Depth = 0
				return goql.Condition(c.Parent, "IS NULL")
			})
			children, _ := goql.Select[CatNode](ctx, e, func(r *CatNode, prev *CatNode, c *Category, f *goql.From, j *goql.Join) bool {
				f.Model = c
				j.Query = t
				j.Model = prev
				j.On = c.Parent.ID == prev.ID
				r.ID = c.ID
				r.Name = c.Name
				r.Depth = prev.Depth + 1
				return prev.Depth < 5
			})
			return goql.UnionAll(roots, children)
		})
		from.Query = tree
		from.Model = n
		s.Nodes = goql.Count()
		return true
	}`)

	_, err := query.NewDialect(noCTE{}).LambdaSearch(body, nil)
	if err == nil || !strings.Contains(err.Error(), "recursive") {
		t.Fatalf("expected recursion to be refused without CTE support, got: %v", err)
	}
}

// The recursive term may not aggregate, group, order or limit: no engine allows it, and
// emitting it would fail at the database against SQL the caller never wrote.
func TestRecursive_RejectsOrderingInRecursiveTerm(t *testing.T) {
	body := parseSource(t, `func(s *TreeSummary, n *CatNode, from *goql.From) bool {
		tree, _ := goql.Select[CatNode](ctx, e, func(t []*CatNode) bool {
			roots, _ := goql.Select[CatNode](ctx, e, func(r *CatNode, c *Category, f *goql.From) bool {
				f.Model = c
				r.ID = c.ID
				r.Name = c.Name
				r.Depth = 0
				return goql.Condition(c.Parent, "IS NULL")
			})
			children, _ := goql.Select[CatNode](ctx, e, func(r *CatNode, prev *CatNode, c *Category, f *goql.From, j *goql.Join, sort *goql.Sort) bool {
				f.Model = c
				j.Query = t
				j.Model = prev
				j.On = c.Parent.ID == prev.ID
				sort.By = "Depth"
				r.ID = c.ID
				r.Name = c.Name
				r.Depth = prev.Depth + 1
				return prev.Depth < 5
			})
			return goql.UnionAll(roots, children)
		})
		from.Query = tree
		from.Model = n
		s.Nodes = goql.Count()
		return true
	}`)

	_, err := sqlite.LambdaSearch(body, nil)
	if err == nil || !strings.Contains(err.Error(), "orders") {
		t.Fatalf("expected ordering in a recursive term to be refused, got: %v", err)
	}
}

// A depth column is what bounds the walk, so the filter on it must actually be applied.
func TestRecursive_DepthBoundsTheWalk(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	if err := e.CreateTables(&Category{}); err != nil {
		t.Fatal(err)
	}
	parent, err := goql.Create(ctx, e, []Category{{Name: "l0"}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		next, err := goql.Create(ctx, e, []Category{{Name: "l", Parent: parent[0]}})
		if err != nil {
			t.Fatal(err)
		}
		parent = next
	}

	rows, err := goql.Select[TreeSummary](ctx, e,
		func(s *TreeSummary, n *CatNode, from *goql.From) bool {
			tree, _ := goql.Select[CatNode](ctx, e, func(t []*CatNode) bool {
				roots, _ := goql.Select[CatNode](ctx, e,
					func(r *CatNode, c *Category, f *goql.From) bool {
						f.Model = c
						r.ID = c.ID
						r.Name = c.Name
						r.Depth = 0
						return goql.Condition(c.Parent, "IS NULL")
					})
				children, _ := goql.Select[CatNode](ctx, e,
					func(r *CatNode, prev *CatNode, c *Category, f *goql.From, j *goql.Join) bool {
						f.Model = c
						j.Query = t
						j.Model = prev
						j.On = c.Parent.ID == prev.ID
						r.ID = c.ID
						r.Name = c.Name
						r.Depth = prev.Depth + 1
						return prev.Depth < 2
					})
				return goql.UnionAll(roots, children)
			})
			from.Query = tree
			from.Model = n
			s.Nodes = goql.Count()
			s.Deepest = goql.Max(n.Depth)
			return true
		})
	if err != nil {
		t.Fatal(err)
	}
	// Six levels exist. The recursive term only extends a row whose depth is below 2, so it
	// produces depths 1 and 2 and stops: three rows, deepest 2. Without the bound the walk
	// would reach depth 5.
	if rows[0].Deepest != 2 || rows[0].Nodes != 3 {
		t.Fatalf("expected the walk bounded to depth 2 with 3 nodes, got %+v", rows[0])
	}
}

// RootID is a params struct used to seed a walk from a runtime value.
type RootID struct{ Value int64 }

// Params are declared once on the outermost lambda and are in scope for every nested body,
// including a CTE branch — the model-typed subquery path already inherited them, and a
// projection was inconsistent with it.
func TestRecursive_ParamsReachTheAnchor(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	if err := e.CreateTables(&Category{}); err != nil {
		t.Fatal(err)
	}
	root, err := goql.Create(ctx, e, []Category{{Name: "root"}})
	if err != nil {
		t.Fatal(err)
	}
	mid, err := goql.Create(ctx, e, []Category{{Name: "mid", Parent: root[0]}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := goql.Create(ctx, e, []Category{{Name: "leaf", Parent: mid[0]}}); err != nil {
		t.Fatal(err)
	}

	// Walk from "mid" rather than from the roots: it and its one child.
	rows, err := goql.Select[TreeSummary](ctx, e,
		func(s *TreeSummary, n *CatNode, from *goql.From, p RootID) bool {
			sub, _ := goql.Select[CatNode](ctx, e, func(t []*CatNode) bool {
				seed, _ := goql.Select[CatNode](ctx, e,
					func(r *CatNode, c *Category, f *goql.From) bool {
						f.Model = c
						r.ID, r.Name, r.Depth = c.ID, c.Name, 0
						return c.ID == p.Value
					})
				kids, _ := goql.Select[CatNode](ctx, e,
					func(r *CatNode, prev *CatNode, c *Category, f *goql.From, j *goql.Join) bool {
						f.Model = c
						j.Query, j.Model = t, prev
						j.On = c.Parent.ID == prev.ID
						r.ID, r.Name = c.ID, c.Name
						r.Depth = prev.Depth + 1
						return prev.Depth < 10
					})
				return goql.UnionAll(seed, kids)
			})
			from.Query, from.Model = sub, n
			s.Nodes = goql.Count()
			s.Deepest = goql.Max(n.Depth)
			return true
		}, RootID{Value: mid[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Nodes != 2 || rows[0].Deepest != 1 {
		t.Fatalf("expected mid and its child, got %+v", rows[0])
	}
}
