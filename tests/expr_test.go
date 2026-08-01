package tests

import (
	"strings"
	"testing"

	"github.com/aekis-dev/goql"
)

// An expression on the value side of an UPDATE.
func TestExpr_UpdateSet(t *testing.T) {
	body, err := (&goql.DebugExecutor{}).ParseQueryFromSource(`func(o *Order) {
		o.Total = o.Total * 2
	}`, "Update")
	if err != nil {
		t.Fatal(err)
	}
	queries, err := sqlite.LambdaWrite(body)
	assertNoError(t, err)
	assertContains(t, queries[0].SQL, `"total_amount" = ("orders"."total_amount" * ?)`)
	if queries[0].Args[0] != int64(2) {
		t.Fatalf("expected the literal bound, got %#v", queries[0].Args[0])
	}
}

// An expression on the left of a comparison.
func TestExpr_InPredicate(t *testing.T) {
	body := parseSource(t, `func(o *Order) bool {
		return o.Total * 2 > 100
	}`)
	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, q.SQL, `WHERE (o."total_amount" * ?) > ?`)
	if len(q.Args) != 2 {
		t.Fatalf("expected both values bound in order, got %v", q.Args)
	}
}

// Grouping comes from the Go parser, so precedence is preserved rather than re-derived.
func TestExpr_NestingIsPreserved(t *testing.T) {
	body := parseSource(t, `func(o *Order) bool {
		return o.Total * (o.Total + 1) > 100
	}`)
	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, q.SQL, `(o."total_amount" * (o."total_amount" + ?))`)
}

// A computed projection, and the expression it repeats in GROUP BY.
func TestExpr_ProjectedColumn(t *testing.T) {
	body := parseSource(t, `func(t *Bucket, o *Order, from *goql.From) bool {
		from.Model = o
		t.Band = o.Total / 1000
		t.Orders = goql.Count()
		return o.Total > 0
	}`)
	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, q.SQL, `(o."total_amount" / ?) AS "Band"`)
	assertContains(t, q.SQL, `GROUP BY (o."total_amount" / ?)`)
}

// A constant projected straight into a result field — what a recursive CTE's anchor needs
// for its depth column.
func TestExpr_ProjectedConstant(t *testing.T) {
	body := parseSource(t, `func(t *Bucket, o *Order, from *goql.From) bool {
		from.Model = o
		t.Band = 0
		t.Orders = goql.Count()
		return o.Total > 0
	}`)
	q, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, q.SQL, `? AS "Band"`)
}

// Go spells concatenation "+", so which one it is comes from the operand types.
func TestExpr_StringConcat(t *testing.T) {
	body := parseSource(t, `func(t *Label, o *Order, from *goql.From) bool {
		from.Model = o
		t.Text = o.Priority + " / " + o.ShippingMethod
		return o.Total > 0
	}`)

	sq, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, sq.SQL, `((o."priority" || ?) || o."shipping_method")`)

	// MySQL reads || as logical OR unless PIPES_AS_CONCAT is set, and would coerce a "+"
	// to numbers — CONCAT is the only spelling correct in every SQL mode.
	mq, err := mysql.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, mq.SQL, "CONCAT(CONCAT(o.`priority`, ?), o.`shipping_method`)")
	if strings.Contains(mq.SQL, "||") {
		t.Fatalf("MySQL must not use ||:\n%s", mq.SQL)
	}
}

// Arithmetic over text is refused rather than emitted for the engine to reject.
func TestExpr_RejectsArithmeticOnText(t *testing.T) {
	_, err := (&goql.DebugExecutor{}).ParseQueryFromSource(`func(o *Order) bool {
		return o.Priority * 2 > 10
	}`, "Select")
	if err == nil || !strings.Contains(err.Error(), "text") {
		t.Fatalf("expected arithmetic on text to be refused, got: %v", err)
	}
}

// Placeholder numbering must follow emission order across the whole statement, which
// Postgres makes visible: the SELECT list binds before the WHERE clause.
func TestExpr_PlaceholderOrder(t *testing.T) {
	body := parseSource(t, `func(t *Bucket, o *Order, from *goql.From) bool {
		from.Model = o
		t.Band = o.Total / 1000
		t.Orders = goql.Count()
		return o.Total > 500
	}`)
	q, err := postgres.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, q.SQL, `(o."total_amount" / $1) AS "Band"`)
	assertContains(t, q.SQL, `WHERE o."total_amount" > $2`)
	assertContains(t, q.SQL, `GROUP BY (o."total_amount" / $3)`)
	if len(q.Args) != 3 {
		t.Fatalf("expected 3 bound values, got %v", q.Args)
	}
}

// End to end: the engine evaluates the expression per row.
func TestExpr_EndToEnd(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	type Scaled struct {
		Priority string
		Doubled  float64
	}
	rows, err := goql.Select[Scaled](ctx, e,
		func(s *Scaled, o *Order, from *goql.From) bool {
			from.Model = o
			s.Priority = o.Priority
			s.Doubled = o.Total * 2
			return o.Total > 1000
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Doubled != 3000 {
		t.Fatalf("expected one row doubled to 3000, got %+v", rows)
	}
}
