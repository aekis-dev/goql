package tests

import (
	"strings"
	"testing"

	"github.com/aekis-dev/goql"
)

// A result type that is not a model makes the lambda a projection: each assignment is one
// output column, and the plain ones are the GROUP BY.
type PriorityTotals struct {
	Priority string
	Orders   int64
	Total    float64
	Largest  float64
	Smallest float64
	Mean     float64
}

func TestAggregate_GroupsByPlainColumns(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()
	seedData(t, ctx, e)

	rows, err := goql.Select[PriorityTotals](ctx, e,
		func(t *PriorityTotals, o *Order, from *goql.From) bool {
			from.Model = o
			t.Priority = o.Priority
			t.Orders = goql.Count()
			t.Total = goql.Sum(o.Total)
			return o.Total > 0
		})
	if err != nil {
		t.Fatal(err)
	}

	// The seed has one Normal order at 1500 and one High at 700.
	byPriority := map[string]*PriorityTotals{}
	for _, row := range rows {
		byPriority[row.Priority] = row
	}
	if len(byPriority) != 2 {
		t.Fatalf("expected one row per priority, got %d", len(rows))
	}
	if byPriority["Normal"].Total != 1500 || byPriority["Normal"].Orders != 1 {
		t.Errorf("Normal group wrong: %+v", byPriority["Normal"])
	}
	if byPriority["High"].Total != 700 || byPriority["High"].Orders != 1 {
		t.Errorf("High group wrong: %+v", byPriority["High"])
	}
}

// With no plain column there is no grouping, so the whole set is one row — the scalar case.
func TestAggregate_WholeSetIsOneRow(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()
	seedData(t, ctx, e)

	rows, err := goql.Select[PriorityTotals](ctx, e,
		func(t *PriorityTotals, o *Order, from *goql.From) bool {
			from.Model = o
			t.Total = goql.Sum(o.Total)
			t.Orders = goql.Count()
			return o.Total > 0
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected a single row, got %d", len(rows))
	}
	if rows[0].Total != 2200 || rows[0].Orders != 2 {
		t.Fatalf("expected 2200 across 2 orders, got %+v", rows[0])
	}
}

// Every aggregate, including two over the same column — the case that made us drop a single
// destination per field.
func TestAggregate_AllFunctionsOverOneColumn(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()
	seedData(t, ctx, e)

	rows, err := goql.Select[PriorityTotals](ctx, e,
		func(t *PriorityTotals, o *Order, from *goql.From) bool {
			from.Model = o
			t.Total = goql.Sum(o.Total)
			t.Mean = goql.Avg(o.Total)
			t.Largest = goql.Max(o.Total)
			t.Smallest = goql.Min(o.Total)
			return o.Total > 0
		})
	if err != nil {
		t.Fatal(err)
	}
	row := rows[0]
	if row.Total != 2200 || row.Mean != 1100 || row.Largest != 1500 || row.Smallest != 700 {
		t.Fatalf("got %+v", row)
	}
}

// The predicate still filters, and it is a WHERE rather than a HAVING.
func TestAggregate_HonoursThePredicate(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()
	seedData(t, ctx, e)

	rows, err := goql.Select[PriorityTotals](ctx, e,
		func(t *PriorityTotals, o *Order, from *goql.From) bool {
			from.Model = o
			t.Total = goql.Sum(o.Total)
			return o.Priority == "High"
		})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Total != 700 {
		t.Fatalf("expected only the High order, got %v", rows[0].Total)
	}
}

// Summing text is caught twice over. Assigning it to a numeric field does not compile at
// all, since Sum[T] returns T — so this uses a string destination, which does compile, to
// reach goql's own check. SQLite would quietly answer 0 there while Postgres errors.
func TestAggregate_RejectsNonNumericColumn(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()

	type Bad struct{ Priority string }
	_, err := goql.Select[Bad](ctx, e, func(b *Bad, o *Order, from *goql.From) bool {
		from.Model = o
		b.Priority = goql.Sum(o.Priority)
		return o.Total > 0
	})
	if err == nil || !strings.Contains(err.Error(), "needs a numeric column") {
		t.Fatalf("expected a numeric-column error, got %v", err)
	}
}

// Min and Max keep their column's type, which is why they are generic functions rather than
// methods — a method cannot have type parameters.
func TestAggregate_MinMaxKeepTheColumnType(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()
	seedData(t, ctx, e)

	type Extremes struct {
		First string
		Last  string
	}
	rows, err := goql.Select[Extremes](ctx, e, func(x *Extremes, c *Customer, from *goql.From) bool {
		from.Model = c
		x.First = goql.Min(c.Name)
		x.Last = goql.Max(c.Name)
		return c.Age > 0
	})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].First != "Alice" || rows[0].Last != "Bob" {
		t.Fatalf("got %+v", rows[0])
	}
}

// The model must be stated, not inferred from parameter order.
func TestAggregate_RequiresFromModel(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()

	_, err := goql.Select[PriorityTotals](ctx, e, func(t *PriorityTotals, o *Order) bool {
		t.Total = goql.Sum(o.Total)
		return o.Total > 0
	})
	if err == nil || !strings.Contains(err.Error(), "does not say which model") {
		t.Fatalf("expected the model to be required, got %v", err)
	}
}

// from.Model must name a parameter the lambda declared.
func TestAggregate_RejectsUnknownFromModel(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()

	_, err := goql.Select[PriorityTotals](ctx, e,
		func(t *PriorityTotals, o *Order, from *goql.From) bool {
			from.Model = t
			t.Total = goql.Sum(o.Total)
			return o.Total > 0
		})
	if err == nil || !strings.Contains(err.Error(), "must be one of the lambda's model parameters") {
		t.Fatalf("expected from.Model to be checked, got %v", err)
	}
}

// A field that does not exist on the result type is caught by the Go compiler; one that
// exists but is assigned from something goql cannot select is caught here.
func TestAggregate_RejectsUnknownProjectionSource(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()

	_, err := goql.Select[PriorityTotals](ctx, e,
		func(t *PriorityTotals, o *Order, from *goql.From) bool {
			from.Model = o
			t.Priority = strings.ToUpper(o.Priority)
			return o.Total > 0
		})
	if err == nil || !strings.Contains(err.Error(), "not something a projection can select") {
		t.Fatalf("expected an unsupported projection source to be reported, got %v", err)
	}
}

// A projection that selects nothing is a mistake, not an empty query.
func TestAggregate_RequiresAProjection(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()

	_, err := goql.Select[PriorityTotals](ctx, e,
		func(t *PriorityTotals, o *Order, from *goql.From) bool {
			from.Model = o
			return o.Total > 0
		})
	if err == nil || !strings.Contains(err.Error(), "selects nothing") {
		t.Fatalf("expected an empty projection to be reported, got %v", err)
	}
}

// --- GROUP BY and HAVING ---

// Grouping keys are additive: g.By names the ones you cannot project — a relation has no
// scalar Go field to assign — and projected plain columns are keys anyway.
func TestGroup_ByRelation(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()
	seedData(t, ctx, e)

	type CustomerTotals struct {
		Total float64
		N     int64
	}
	rows, err := goql.Select[CustomerTotals](ctx, e,
		func(t *CustomerTotals, o *Order, from *goql.From, g *goql.Group) bool {
			from.Model = o
			g.By = []string{"Customer"}
			t.Total = goql.Sum(o.Total)
			t.N = goql.Count()
			return o.Total > 0
		})
	if err != nil {
		t.Fatal(err)
	}
	// One order each for two customers.
	if len(rows) != 2 {
		t.Fatalf("expected a row per customer, got %d", len(rows))
	}
	for _, row := range rows {
		if row.N != 1 {
			t.Fatalf("expected one order per customer, got %+v", row)
		}
	}
}

// Explicit and projected keys combine rather than replacing one another.
func TestGroup_AdditiveWithProjectedColumns(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()
	seedData(t, ctx, e)

	type Split struct {
		Priority string
		Total    float64
	}
	rows, err := goql.Select[Split](ctx, e,
		func(s *Split, o *Order, from *goql.From, g *goql.Group) bool {
			from.Model = o
			g.By = []string{"ShippingMethod"}
			s.Priority = o.Priority
			s.Total = goql.Sum(o.Total)
			return o.Total > 0
		})
	if err != nil {
		t.Fatal(err)
	}
	// The seed's two orders differ in both priority and shipping method.
	if len(rows) != 2 {
		t.Fatalf("expected two groups, got %d", len(rows))
	}
}

// An aggregate in the predicate filters groups, so it becomes HAVING rather than WHERE.
func TestHaving_FiltersGroups(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()
	seedData(t, ctx, e)

	rows, err := goql.Select[PriorityTotals](ctx, e,
		func(t *PriorityTotals, o *Order, from *goql.From) bool {
			from.Model = o
			t.Priority = o.Priority
			t.Total = goql.Sum(o.Total)
			return goql.Sum(o.Total) > 1000
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Total != 1500 {
		t.Fatalf("expected only the group above 1000, got %+v", rows)
	}
}

// A row filter and a group filter in the same predicate go to different clauses.
func TestHaving_SplitsFromWhere(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()
	seedData(t, ctx, e)

	rows, err := goql.Select[PriorityTotals](ctx, e,
		func(t *PriorityTotals, o *Order, from *goql.From) bool {
			from.Model = o
			t.Priority = o.Priority
			t.Total = goql.Sum(o.Total)
			// WHERE keeps both orders; HAVING drops the smaller group.
			return o.Total > 100 && goql.Sum(o.Total) > 1000
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Priority != "Normal" {
		t.Fatalf("expected only the Normal group, got %+v", rows)
	}
}

// SQL cannot OR a row filter with a group filter, so guessing is refused.
func TestHaving_RejectsOr(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()
	seedData(t, ctx, e)

	_, err := goql.Select[PriorityTotals](ctx, e,
		func(t *PriorityTotals, o *Order, from *goql.From) bool {
			from.Model = o
			t.Priority = o.Priority
			t.Total = goql.Sum(o.Total)
			return o.Total > 100 || goql.Sum(o.Total) > 1000
		})
	if err == nil || !strings.Contains(err.Error(), "only be combined with AND") {
		t.Fatalf("expected an OR with an aggregate to be refused, got %v", err)
	}
}

// The aggregate goes on the left, so the condition reads as the group being filtered.
func TestHaving_RejectsAggregateOnTheRight(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()

	_, err := goql.Select[PriorityTotals](ctx, e,
		func(t *PriorityTotals, o *Order, from *goql.From) bool {
			from.Model = o
			t.Priority = o.Priority
			t.Total = goql.Sum(o.Total)
			return 1000 < goql.Sum(o.Total)
		})
	if err == nil || !strings.Contains(err.Error(), "on the left") {
		t.Fatalf("expected a clear message, got %v", err)
	}
}

// Naming grouping keys without aggregating anything is a mistake, not an empty grouping.
func TestGroup_RequiresAnAggregate(t *testing.T) {
	ctx, e, done := setupDB(t)
	defer done()

	type Plain struct{ Priority string }
	_, err := goql.Select[Plain](ctx, e,
		func(p *Plain, o *Order, from *goql.From, g *goql.Group) bool {
			from.Model = o
			g.By = []string{"ShippingMethod"}
			p.Priority = o.Priority
			return o.Total > 0
		})
	if err == nil || !strings.Contains(err.Error(), "aggregates nothing") {
		t.Fatalf("expected grouping without aggregation to be reported, got %v", err)
	}
}
