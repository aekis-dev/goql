package tests

import (
	"errors"
	"strings"
	"testing"

	"github.com/aekis-dev/goql"
)

// goql.Filter compiles to a correlated EXISTS rather than a JOIN. A join is a clause on the
// statement and is applied before the WHERE, so it both multiplies rows and drops rows with
// no related row at all. EXISTS is an expression and can do neither — which is what lets a
// relation predicate sit inside ||, ! and a branch arm.

func TestFilter_EmitsExists(t *testing.T) {
	parsed := parseSource(t, `func(o *Order) bool {
		return goql.Filter(o.Tags, func(tg *Tag) bool { return tg.Name == "urgent" })
	}`)

	q, err := sqlite.LambdaSearch(parsed, nil)
	if err != nil {
		t.Fatal(err)
	}

	assertEqual(t, `SELECT o.* FROM "orders" o WHERE EXISTS (SELECT 1 FROM "order_tags" o2 `+
		`INNER JOIN "tags" t ON t."id" = o2."tag_id" WHERE o2."order_id" = o."id" `+
		`AND (t."name" = ?))`, q.SQL)
	assertEqual(t, []any{"urgent"}, q.Args)

	if strings.Contains(q.SQL, "DISTINCT") {
		t.Errorf("EXISTS cannot multiply rows, so no DISTINCT is needed: %s", q.SQL)
	}
}

func TestFilter_EmitsExistsO2M(t *testing.T) {
	parsed := parseSource(t, `func(c *Customer) bool {
		return goql.Filter(c.Orders, func(o *Order) bool { return o.Total > 1000 })
	}`)

	q, err := sqlite.LambdaSearch(parsed, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, `SELECT c.* FROM "customers" c WHERE EXISTS (SELECT 1 FROM "orders" o `+
		`WHERE o."customer_id" = c."id" AND (o."total_amount" > ?))`, q.SQL)
}

// Postgres numbers its placeholders, so the subquery's values must bind in emission order.
func TestFilter_PlaceholderOrder(t *testing.T) {
	parsed := parseSource(t, `func(o *Order) bool {
		return o.Priority == "High" &&
			goql.Filter(o.Tags, func(tg *Tag) bool { return tg.Name == "urgent" })
	}`)

	q, err := postgres.LambdaSearch(parsed, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, q.SQL, `o."priority" = $1`)
	assertContains(t, q.SQL, `t."name" = $2`)
	assertEqual(t, []any{"High", "urgent"}, q.Args)
}

func TestFilter_NotEmitsNotExists(t *testing.T) {
	parsed := parseSource(t, `func(o *Order) bool {
		return !goql.Filter(o.Tags, func(tg *Tag) bool { return tg.Name == "urgent" })
	}`)

	q, err := sqlite.LambdaSearch(parsed, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, q.SQL, `NOT (EXISTS (SELECT 1 FROM "order_tags" o2`)
}

// --- The two defects this change exists to fix ---

// An entity matching two related rows was returned twice, because the join multiplied it.
func TestFilter_NoDuplicateRows(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	// Order 1 carries both "urgent" and "vip", so both related rows match.
	got, err := goql.Select[Order](ctx, e, func(o *Order) bool {
		return goql.Filter(o.Tags, func(tg *Tag) bool {
			return tg.Name == "urgent" || tg.Name == "vip"
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	assertEqual(t, 1, len(got))
}

// Worse than the duplicates, and not fixable with DISTINCT: a row with *no* related rows was
// eliminated by the join before the disjunction was ever evaluated, even though it satisfied
// the other arm.
func TestFilter_OrKeepsRowsWithNoRelated(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	customers, _, _ := seedData(t, ctx, e)

	// A high-value order carrying no tags at all.
	if _, err := goql.Create(ctx, e, []Order{{
		Total:          9000,
		Priority:       "Normal",
		ShippingMethod: "Standard",
		Customer:       customers[0],
	}}); err != nil {
		t.Fatal(err)
	}

	got, err := goql.Select[Order](ctx, e, func(o *Order) bool {
		return o.Total > 200 ||
			goql.Filter(o.Tags, func(tg *Tag) bool { return tg.Name == "urgent" })
	})
	if err != nil {
		t.Fatal(err)
	}

	// Orders 1 (1500), 2 (700) and 3 (9000) all clear 200. The untagged one must be there.
	assertEqual(t, 3, len(got))
	for _, o := range got {
		if o.Total == 9000 {
			return
		}
	}
	t.Fatal("the untagged order matching the other arm of the || was dropped")
}

// --- Positions a statement could never reach ---

// A range loop is a statement, so it could not appear in an `if` condition — which made a
// relation-conditioned Update inexpressible ("write function assigns nothing").
func TestFilter_InUpdate(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	n, err := goql.Update[Order](ctx, e, func(o *Order) {
		if goql.Filter(o.Tags, func(tg *Tag) bool { return tg.Name == "urgent" }) {
			o.Priority = "Urgent"
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, int64(1), n)

	updated := byID[Order](t, ctx, e, 1)
	assertEqual(t, "Urgent", updated.Priority)
}

// The same statement on MySQL would need UPDATE … JOIN … SET; with EXISTS there is no join
// to place, so all three engines emit the same statement.
func TestFilter_UpdateHasNoFromClause(t *testing.T) {
	body, err := (&goql.DebugExecutor{}).ParseQueryFromSource(`func(o *Order) {
		if goql.Filter(o.Tags, func(tg *Tag) bool { return tg.Name == "urgent" }) {
			o.Priority = "Urgent"
		}
	}`, "Update")
	if err != nil {
		t.Fatal(err)
	}

	queries, err := sqlite.LambdaWrite(body)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(queries))
	// The statement's own FROM would sit before the WHERE; the only FROM here belongs to
	// the EXISTS subquery.
	head, _, _ := strings.Cut(queries[0].SQL, " WHERE ")
	if strings.Contains(head, " FROM ") {
		t.Errorf("expected no UPDATE … FROM clause, got: %s", queries[0].SQL)
	}
	assertContains(t, queries[0].SQL, `WHERE EXISTS (SELECT 1 FROM "order_tags"`)
}

func TestFilter_InDelete(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	n, err := goql.Delete[Order](ctx, e, func(o *Order) bool {
		return goql.Filter(o.Tags, func(tg *Tag) bool { return tg.Name == "urgent" })
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, int64(1), n)
	assertEqual(t, 0, countByID[Order](t, ctx, e, 1))
	assertEqual(t, 1, countByID[Order](t, ctx, e, 2))
}

func TestFilter_InBranch(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	n, err := goql.Update[Order](ctx, e, func(o *Order) {
		if goql.Filter(o.Tags, func(tg *Tag) bool { return tg.Name == "urgent" }) {
			o.ShippingMethod = "Overnight"
		} else {
			o.ShippingMethod = "Standard"
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, int64(2), n)

	assertEqual(t, "Overnight", byID[Order](t, ctx, e, 1).ShippingMethod)
	assertEqual(t, "Standard", byID[Order](t, ctx, e, 2).ShippingMethod)
}

// --- Composition ---

// The filter's predicate may reference the enclosing model, which correlates.
func TestFilter_CorrelatedWithOuterModel(t *testing.T) {
	parsed := parseSource(t, `func(c *Customer) bool {
		return goql.Filter(c.Orders, func(o *Order) bool { return o.Priority == c.Status })
	}`)

	q, err := sqlite.LambdaSearch(parsed, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, q.SQL, `o."priority" = c."status"`)
}

// A many2one traversal inside the predicate joins within the subquery, before its WHERE.
func TestFilter_PredicateJoinsInsideSubquery(t *testing.T) {
	parsed := parseSource(t, `func(t *Tag) bool {
		return goql.Filter(t.Orders, func(o *Order) bool { return o.Customer.Country == "USA" })
	}`)

	q, err := sqlite.LambdaSearch(parsed, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, q.SQL, `INNER JOIN "customers" c ON`)
	// The join must precede the WHERE it feeds — the subquery's WHERE, which is the last
	// one in the statement.
	if strings.Index(q.SQL, "INNER JOIN") > strings.LastIndex(q.SQL, "WHERE") {
		t.Errorf("join rendered after the WHERE it feeds: %s", q.SQL)
	}
}

// --- Rejections ---

func TestFilter_RangeIsRetired(t *testing.T) {
	_, err := (&goql.DebugExecutor{}).ParseQueryFromSource(`func(o *Order) bool {
		for _, tg := range o.Tags {
			if tg.Name == "urgent" {
				return true
			}
		}
		return false
	}`, "Select")

	if err == nil {
		t.Fatal("expected ranging over a relation to be refused")
	}
	if !errors.Is(err, goql.ErrUnsupportedExpr) {
		t.Fatalf("expected ErrUnsupportedExpr, got %v", err)
	}
	// The error must name the replacement, not merely refuse.
	assertContains(t, err.Error(), "goql.Filter(o.Tags")
}

func TestFilter_RejectsNonCollection(t *testing.T) {
	_, err := (&goql.DebugExecutor{}).ParseQueryFromSource(`func(o *Order) bool {
		return goql.Filter(o.Customer, func(c *Customer) bool { return c.Country == "USA" })
	}`, "Select")

	if err == nil {
		t.Fatal("expected a many2one to be refused")
	}
	assertContains(t, err.Error(), "one2many or many2many")
}

func TestFilter_RejectsAssigningPredicate(t *testing.T) {
	_, err := (&goql.DebugExecutor{}).ParseQueryFromSource(`func(o *Order) bool {
		return goql.Filter(o.Tags, func(tg *Tag) bool { tg.Name = "x"; return true })
	}`, "Select")

	if err == nil {
		t.Fatal("expected an assigning filter predicate to be refused")
	}
	assertContains(t, err.Error(), "cannot assign")
}

// A relation predicate no longer joins, so nothing in a condition tree can multiply rows and
// the COUNT(DISTINCT pk) compensation is unnecessary.
func TestFilter_CountNeedsNoDistinct(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	n, err := goql.Exists[Order](ctx, e, func(o *Order) bool {
		return goql.Filter(o.Tags, func(tg *Tag) bool {
			return tg.Name == "urgent" || tg.Name == "vip"
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !n {
		t.Fatal("expected the order tagged urgent to exist")
	}
}
