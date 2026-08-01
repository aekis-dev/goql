package tests

import (
	"context"
	"strings"
	"testing"

	"github.com/aekis-dev/goql"
)

// Movement is the shape both branches of a set operation produce. Every branch is []*Movement,
// so the compiler is what checks that they match.
type Movement struct {
	Ref    string
	Amount float64
}

func setupArchived(t *testing.T) (context.Context, *goql.Engine, func()) {
	t.Helper()
	c, engine, cleanup := setupDB(t)
	if err := engine.CreateTables(&OrderArchive{}); err != nil {
		cleanup()
		t.Fatal(err)
	}
	seedData(t, c, engine)
	if _, err := goql.Create(c, engine, []OrderArchive{
		{Total: 300, Reason: "archived-a", Origin: "a"},
		{Total: 1500, Reason: "archived-b", Origin: "b"},
	}); err != nil {
		cleanup()
		t.Fatal(err)
	}
	return c, engine, cleanup
}

func TestUnion_CombinesTwoModels(t *testing.T) {
	ctx, e, done := setupArchived(t)
	defer done()

	rows, err := goql.Select[Movement](ctx, e, func(m *Movement, sort *goql.Sort) bool {
		sort.By = "Amount"

		live, _ := goql.Select[Movement](ctx, e, func(m *Movement, o *Order, from *goql.From) bool {
			from.Model = o
			m.Ref = o.Priority
			m.Amount = o.Total
			return o.Total > 0
		})
		archived, _ := goql.Select[Movement](ctx, e, func(m *Movement, a *OrderArchive, from *goql.From) bool {
			from.Model = a
			m.Ref = a.Reason
			m.Amount = a.Total
			return a.Total > 0
		})
		return goql.Union(live, archived)
	})
	if err != nil {
		t.Fatal(err)
	}

	// Two orders (700, 1500) and two archived rows (300, 1500); UNION removes the duplicate
	// amount only when the whole row matches, and these rows differ by Ref.
	if len(rows) != 4 {
		t.Fatalf("expected 4 rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].Amount != 300 {
		t.Errorf("expected the union sorted by amount, got %+v", rows[0])
	}
}

// UNION removes duplicate rows; UNION ALL keeps them.
func TestUnion_DeduplicatesWhereAllKeeps(t *testing.T) {
	ctx, e, done := setupArchived(t)
	defer done()

	deduped, err := goql.Select[Movement](ctx, e, func(m *Movement) bool {
		a, _ := goql.Select[Movement](ctx, e, func(m *Movement, o *Order, from *goql.From) bool {
			from.Model = o
			m.Ref = o.Priority
			m.Amount = o.Total
			return o.Total > 0
		})
		b, _ := goql.Select[Movement](ctx, e, func(m *Movement, o *Order, from *goql.From) bool {
			from.Model = o
			m.Ref = o.Priority
			m.Amount = o.Total
			return o.Total > 0
		})
		return goql.Union(a, b)
	})
	if err != nil {
		t.Fatal(err)
	}

	all, err := goql.Select[Movement](ctx, e, func(m *Movement) bool {
		a, _ := goql.Select[Movement](ctx, e, func(m *Movement, o *Order, from *goql.From) bool {
			from.Model = o
			m.Ref = o.Priority
			m.Amount = o.Total
			return o.Total > 0
		})
		b, _ := goql.Select[Movement](ctx, e, func(m *Movement, o *Order, from *goql.From) bool {
			from.Model = o
			m.Ref = o.Priority
			m.Amount = o.Total
			return o.Total > 0
		})
		return goql.UnionAll(a, b)
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(deduped) != 2 {
		t.Fatalf("UNION should collapse the identical branches to 2 rows, got %d", len(deduped))
	}
	if len(all) != 4 {
		t.Fatalf("UNION ALL should keep all 4 rows, got %d", len(all))
	}
}

// Both branches read the same table, which shared aliases would have refused.
func TestUnion_SameTableInBothBranches(t *testing.T) {
	ctx, e, done := setupArchived(t)
	defer done()

	rows, err := goql.Select[Movement](ctx, e, func(m *Movement) bool {
		high, _ := goql.Select[Movement](ctx, e, func(m *Movement, o *Order, from *goql.From) bool {
			from.Model = o
			m.Ref = o.Priority
			m.Amount = o.Total
			return o.Total > 1000
		})
		low, _ := goql.Select[Movement](ctx, e, func(m *Movement, o *Order, from *goql.From) bool {
			from.Model = o
			m.Ref = o.Priority
			m.Amount = o.Total
			return o.Total <= 1000
		})
		return goql.UnionAll(high, low)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected both orders, got %d", len(rows))
	}
}

func TestIntersect_KeepsRowsInEveryBranch(t *testing.T) {
	ctx, e, done := setupArchived(t)
	defer done()

	rows, err := goql.Select[Movement](ctx, e, func(m *Movement) bool {
		all, _ := goql.Select[Movement](ctx, e, func(m *Movement, o *Order, from *goql.From) bool {
			from.Model = o
			m.Ref = o.Priority
			m.Amount = o.Total
			return o.Total > 0
		})
		big, _ := goql.Select[Movement](ctx, e, func(m *Movement, o *Order, from *goql.From) bool {
			from.Model = o
			m.Ref = o.Priority
			m.Amount = o.Total
			return o.Total > 1000
		})
		return goql.Intersect(all, big)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Amount != 1500 {
		t.Fatalf("expected only the big order, got %+v", rows)
	}
}

func TestExcept_RemovesLaterBranches(t *testing.T) {
	ctx, e, done := setupArchived(t)
	defer done()

	rows, err := goql.Select[Movement](ctx, e, func(m *Movement) bool {
		all, _ := goql.Select[Movement](ctx, e, func(m *Movement, o *Order, from *goql.From) bool {
			from.Model = o
			m.Ref = o.Priority
			m.Amount = o.Total
			return o.Total > 0
		})
		big, _ := goql.Select[Movement](ctx, e, func(m *Movement, o *Order, from *goql.From) bool {
			from.Model = o
			m.Ref = o.Priority
			m.Amount = o.Total
			return o.Total > 1000
		})
		return goql.Except(all, big)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Amount != 700 {
		t.Fatalf("expected only the small order, got %+v", rows)
	}
}

// A branch that fills a different set of fields would line up by position in SQL, silently.
func TestUnion_RejectsMismatchedBranches(t *testing.T) {
	ctx, e, done := setupArchived(t)
	defer done()

	_, err := goql.Select[Movement](ctx, e, func(m *Movement) bool {
		full, _ := goql.Select[Movement](ctx, e, func(m *Movement, o *Order, from *goql.From) bool {
			from.Model = o
			m.Ref = o.Priority
			m.Amount = o.Total
			return o.Total > 0
		})
		partial, _ := goql.Select[Movement](ctx, e, func(m *Movement, o *Order, from *goql.From) bool {
			from.Model = o
			m.Amount = o.Total
			return o.Total > 0
		})
		return goql.Union(full, partial)
	})
	if err == nil || !strings.Contains(err.Error(), "must yield the same columns") {
		t.Fatalf("expected mismatched branches to be refused, got %v", err)
	}
}

// Ordering a set names a projected column, since there is no single model to resolve against.
func TestUnion_RejectsSortOnAColumnNoBranchSelects(t *testing.T) {
	ctx, e, done := setupArchived(t)
	defer done()

	_, err := goql.Select[Movement](ctx, e, func(m *Movement, sort *goql.Sort) bool {
		sort.By = "Nope"
		a, _ := goql.Select[Movement](ctx, e, func(m *Movement, o *Order, from *goql.From) bool {
			from.Model = o
			m.Ref = o.Priority
			m.Amount = o.Total
			return o.Total > 0
		})
		b, _ := goql.Select[Movement](ctx, e, func(m *Movement, o *Order, from *goql.From) bool {
			from.Model = o
			m.Ref = o.Priority
			m.Amount = o.Total
			return o.Total > 1000
		})
		return goql.Union(a, b)
	})
	if err == nil || !strings.Contains(err.Error(), "does not select it") {
		t.Fatalf("expected an unknown sort column to be refused, got %v", err)
	}
}
