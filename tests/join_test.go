package tests

import (
	"context"
	"strings"
	"testing"

	"github.com/aekis-dev/goql"
)

// Invoice and Payment have no relation between them; they share a Ref column. A comparison
// between two declared models is the join condition.
func setupLedger(t *testing.T) (context.Context, *goql.Engine, func()) {
	t.Helper()
	ctx, e, done := setupDB(t)
	if err := e.CreateTables(&Invoice{}, &Payment{}); err != nil {
		done()
		t.Fatal(err)
	}

	_, err := goql.Create(ctx, e, []Invoice{
		{Ref: "A-1", Amount: 100, Status: "open"},
		{Ref: "A-2", Amount: 200, Status: "open"},
		{Ref: "A-3", Amount: 300, Status: "open"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = goql.Create(ctx, e, []Payment{
		{Ref: "A-1", Amount: 100, Method: "card"},
		{Ref: "A-3", Amount: 50, Method: "cash"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, e, done
}

func TestJoin_FiltersByAnotherModel(t *testing.T) {
	ctx, e, done := setupLedger(t)
	defer done()

	paid, err := goql.Select[Invoice](ctx, e, func(i *Invoice, p *Payment) bool {
		return i.Ref == p.Ref && p.Method == "card"
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(paid) != 1 {
		t.Fatalf("expected 1 invoice paid by card, got %d", len(paid))
	}
	if paid[0].Ref != "A-1" {
		t.Errorf("Ref = %q, want A-1", paid[0].Ref)
	}
	// The result is still the primary model, fully populated.
	if paid[0].Amount != 100 || paid[0].Status != "open" {
		t.Errorf("primary row not fully scanned: %+v", paid[0])
	}
}

// The join is an inner one, so an invoice with no payment drops out.
func TestJoin_IsInner(t *testing.T) {
	ctx, e, done := setupLedger(t)
	defer done()

	matched, err := goql.Select[Invoice](ctx, e, func(i *Invoice, p *Payment) bool {
		return i.Ref == p.Ref
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 2 {
		t.Fatalf("expected only the 2 invoices with a payment, got %d", len(matched))
	}
	for _, invoice := range matched {
		if invoice.Ref == "A-2" {
			t.Error("A-2 has no payment and must not match an inner join")
		}
	}
}

// Comparing a column against the other model's column, rather than a literal.
func TestJoin_ComparesColumnsAcrossModels(t *testing.T) {
	ctx, e, done := setupLedger(t)
	defer done()

	settled, err := goql.Select[Invoice](ctx, e, func(i *Invoice, p *Payment) bool {
		return i.Ref == p.Ref && i.Amount == p.Amount
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(settled) != 1 || settled[0].Ref != "A-1" {
		t.Fatalf("expected only the fully-settled invoice, got %+v", settled)
	}
}

func TestJoin_CountCountsDistinctPrimaryRows(t *testing.T) {
	ctx, e, done := setupLedger(t)
	defer done()

	// A second payment against A-1 would double-count without DISTINCT.
	if _, err := goql.Create(ctx, e, []Payment{{Ref: "A-1", Amount: 25, Method: "cash"}}); err != nil {
		t.Fatal(err)
	}

	type Tally struct{ N int64 }
	rows, err := goql.Select[Tally](ctx, e, func(t *Tally, i *Invoice, p *Payment, from *goql.From) bool {
		from.Model = i
		t.N = goql.Count()
		return i.Ref == p.Ref
	})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].N != 2 {
		t.Fatalf("expected 2 distinct invoices, got %d", rows[0].N)
	}
}

func TestJoin_WithParamsAndOptions(t *testing.T) {
	ctx, e, done := setupLedger(t)
	defer done()

	type MinAmount struct{ Value float64 }

	// Participants, an option carrier and a params struct in one signature, all classified
	// by type rather than position.
	rows, err := goql.Select[Invoice](ctx, e,
		func(i *Invoice, p *Payment, limit *goql.Limit, m MinAmount) bool {
			limit.Value = 1
			return i.Ref == p.Ref && i.Amount >= m.Value
		}, MinAmount{Value: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected the limit to cap this at 1 row, got %d", len(rows))
	}
}

// Update and Delete reach other tables through declared relations, not a FROM list, so a
// declared participant must be refused rather than ignored.
func TestJoin_RejectedOnUpdate(t *testing.T) {
	ctx, e, done := setupLedger(t)
	defer done()

	_, err := goql.Update[Invoice](ctx, e, func(i *Invoice, p *Payment) {
		if i.Ref == p.Ref {
			i.Status = "paid"
		}
	})
	if err == nil || !strings.Contains(err.Error(), "cannot join additional models") {
		t.Fatalf("expected a clear rejection, got %v", err)
	}
}

func TestJoin_RejectedOnDelete(t *testing.T) {
	ctx, e, done := setupLedger(t)
	defer done()

	_, err := goql.Delete[Invoice](ctx, e, func(i *Invoice, p *Payment) bool {
		return i.Ref == p.Ref
	})
	if err == nil || !strings.Contains(err.Error(), "cannot join additional models") {
		t.Fatalf("expected a clear rejection, got %v", err)
	}
}

// A declared-but-unused participant must not widen the FROM list, which would turn the
// query into a cross join.
func TestJoin_UnusedParticipantIsNotJoined(t *testing.T) {
	ctx, e, done := setupLedger(t)
	defer done()

	all, err := goql.Select[Invoice](ctx, e, func(i *Invoice, _ *Payment) bool {
		return i.Status == "open"
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected all 3 invoices, got %d — an unused participant widened the query", len(all))
	}
}

// A joined model may only be read from. Assigning to one used to be dropped in silence.
func TestJoin_RejectsAssignmentToParticipant(t *testing.T) {
	ctx, e, done := setupLedger(t)
	defer done()

	_, err := goql.Update[Invoice](ctx, e, func(i *Invoice, p *Payment) {
		p.Method = "cash"
		i.Status = "paid"
	})
	if err == nil || !strings.Contains(err.Error(), "may only be read from") {
		t.Fatalf("expected the participant assignment to be refused, got %v", err)
	}
}

// from.Model names the model a projection reads from — explicitly, not by parameter order.
// This lambda declares two models and names the second, so a positional fallback would read
// the wrong table.
func TestFrom_SelectsTheNamedModel(t *testing.T) {
	ctx, e, done := setupLedger(t)
	defer done()

	type Tally struct {
		Key string
		N   int64
	}

	// from.Model = p → FROM payments, grouped by the payment's method.
	byMethod, err := goql.Select[Tally](ctx, e,
		func(t *Tally, i *Invoice, p *Payment, from *goql.From) bool {
			from.Model = p
			t.Key = p.Method
			t.N = goql.Count()
			return i.Ref == p.Ref
		})
	if err != nil {
		t.Fatal(err)
	}
	methods := map[string]int64{}
	for _, row := range byMethod {
		methods[row.Key] = row.N
	}
	if methods["card"] != 1 || methods["cash"] != 1 {
		t.Fatalf("expected one payment per method, got %+v", methods)
	}

	// The same two models, naming the first instead → FROM invoices, grouped by status.
	byStatus, err := goql.Select[Tally](ctx, e,
		func(t *Tally, i *Invoice, p *Payment, from *goql.From) bool {
			from.Model = i
			t.Key = i.Status
			t.N = goql.Count()
			return i.Ref == p.Ref
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(byStatus) != 1 || byStatus[0].Key != "open" || byStatus[0].N != 2 {
		t.Fatalf("expected 2 distinct open invoices, got %+v", byStatus)
	}
}
