package tests

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aekis-dev/goql"
	testmodels "github.com/aekis-dev/goql/tests/models"
)

// ArchiveParams supplies a value the lambda cannot capture from its scope.
type ArchiveParams struct {
	Reason string
}

// setupArchive prepares the usual fixtures plus the archive destination table.
func setupArchive(t *testing.T) (context.Context, *goql.Engine, func()) {
	t.Helper()
	ctx, e, done := setupDB(t)
	if err := e.CreateTables(&OrderArchive{}); err != nil {
		done()
		t.Fatal(err)
	}
	return ctx, e, done
}

func TestInsert_CopiesMatchingRows(t *testing.T) {
	ctx, e, done := setupArchive(t)
	defer done()
	seedData(t, ctx, e)

	rows, err := goql.Insert[OrderArchive](ctx, e, func(a *OrderArchive, o *Order) {
		if o.Total > 1000 {
			a.Total = o.Total
			a.Reason = "high value"
			a.Origin = o.Priority
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("expected 1 archived row, got %d", rows)
	}

	archived, err := goql.Select[OrderArchive](ctx, e, func(a *OrderArchive) bool {
		return a.Reason == "high value"
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(archived) != 1 {
		t.Fatalf("expected 1 row selected back, got %d", len(archived))
	}
	if archived[0].Total != 1500 {
		t.Errorf("Total = %v, want 1500 — the source column was not copied", archived[0].Total)
	}
	if archived[0].Origin != "Normal" {
		t.Errorf("Origin = %q, want %q", archived[0].Origin, "Normal")
	}
	// The destination's own timestamps must be filled in, since no row was ever built in Go.
	if archived[0].Created.IsZero() {
		t.Error("Created is zero: the destination's autoCreateTime column was not populated")
	}
}

func TestInsert_JoinsThroughRelation(t *testing.T) {
	ctx, e, done := setupArchive(t)
	defer done()
	seedData(t, ctx, e)

	// Reaching into Customer must become a JOIN in the SELECT half.
	rows, err := goql.Insert[OrderArchive](ctx, e, func(a *OrderArchive, o *Order) {
		if o.Customer.Country == "Canada" {
			a.Total = o.Total
			a.Reason = "canadian"
			a.Origin = o.ShippingMethod
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("expected 1 row, got %d", rows)
	}

	archived := selectArchive(t, ctx, e)
	if len(archived) != 1 || archived[0].Total != 700 {
		t.Fatalf("expected only Bob's 700 order, got %+v", archived)
	}
}

func TestInsert_BranchesInsertDisjointSets(t *testing.T) {
	ctx, e, done := setupArchive(t)
	defer done()
	seedData(t, ctx, e)

	// One statement per arm, each with a mutually exclusive WHERE.
	rows, err := goql.Insert[OrderArchive](ctx, e, func(a *OrderArchive, o *Order) {
		if o.Total > 1000 {
			a.Total = o.Total
			a.Reason = "big"
			a.Origin = "b"
		} else {
			a.Total = o.Total
			a.Reason = "small"
			a.Origin = "s"
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("expected 2 rows across both arms, got %d", rows)
	}

	byReason := map[string]float64{}
	for _, row := range selectArchive(t, ctx, e) {
		byReason[row.Reason] = row.Total
	}
	if byReason["big"] != 1500 {
		t.Errorf("big = %v, want 1500", byReason["big"])
	}
	if byReason["small"] != 700 {
		t.Errorf("small = %v, want 700", byReason["small"])
	}
}

func TestInsert_ParamsSupplyConstants(t *testing.T) {
	ctx, e, done := setupArchive(t)
	defer done()
	seedData(t, ctx, e)

	rows, err := goql.Insert[OrderArchive](ctx, e, func(a *OrderArchive, o *Order, p ArchiveParams) {
		a.Total = o.Total
		a.Reason = p.Reason
		a.Origin = o.ShippingMethod
	}, ArchiveParams{Reason: "quarter close"})
	if err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("expected both orders archived, got %d", rows)
	}

	for _, row := range selectArchive(t, ctx, e) {
		if row.Reason != "quarter close" {
			t.Fatalf("Reason = %q, want the params value", row.Reason)
		}
	}
}

func TestInsert_SortAndLimit(t *testing.T) {
	ctx, e, done := setupArchive(t)
	defer done()
	seedData(t, ctx, e)

	// Options are declared as parameters and assigned in the body, as everywhere else.
	rows, err := goql.Insert[OrderArchive](ctx, e,
		func(a *OrderArchive, o *Order, sort *goql.Sort, limit *goql.Limit) {
			sort.By = "Total"
			sort.Desc = true
			limit.Value = 1
			a.Total = o.Total
			a.Reason = "top"
			a.Origin = o.Priority
		})
	if err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("expected the limit to cap this at 1 row, got %d", rows)
	}

	archived := selectArchive(t, ctx, e)
	if len(archived) != 1 || archived[0].Total != 1500 {
		t.Fatalf("expected the highest total to be the one inserted, got %+v", archived)
	}
}

func TestInsert_SameModel(t *testing.T) {
	ctx, e, done := setupArchive(t)
	defer done()
	seedData(t, ctx, e)

	// Destination and source are the same table, declared as one parameter group.
	rows, err := goql.Insert[Tag](ctx, e, func(dst, src *Tag) {
		if src.Name == "urgent" {
			dst.Name = "urgent-copy"
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("expected 1 copied tag, got %d", rows)
	}

	copies, err := goql.Select[Tag](ctx, e, func(tag *Tag) bool {
		return tag.Name == "urgent-copy"
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(copies) != 1 {
		t.Fatalf("expected the copy to exist, got %d rows", len(copies))
	}
}

func TestInsert_ConflictIgnore(t *testing.T) {
	ctx, e, done := setupArchive(t)
	defer done()
	seedData(t, ctx, e)

	// Origin is unique and both source rows map to the same literal, so the second row of
	// the statement collides with the first.
	_, err := goql.Insert[OrderArchive](ctx, e, func(a *OrderArchive, o *Order) {
		a.Total = o.Total
		a.Reason = "dup"
		a.Origin = "fixed"
	})
	if err == nil {
		t.Fatal("expected a unique-constraint failure without Conflict{Ignore: true}")
	}

	rows, err := goql.Insert[OrderArchive](ctx, e,
		func(a *OrderArchive, o *Order, cf *goql.Conflict) {
			cf.Ignore = true
			a.Total = o.Total
			a.Reason = "dup"
			a.Origin = "fixed"
		})
	if err != nil {
		t.Fatalf("Conflict{Ignore: true} should skip the collision, got %v", err)
	}
	if rows != 1 {
		t.Fatalf("expected 1 row inserted and 1 skipped, got %d", rows)
	}
}

func TestInsert_RejectsRelationAssignment(t *testing.T) {
	ctx, e, done := setupArchive(t)
	defer done()
	tags := seedTags(t, ctx, e)

	// A relation link needs a primary key that does not exist yet.
	_, err := goql.Insert[Order](ctx, e, func(dst, src *Order) {
		dst.Priority = src.Priority
		dst.Tags = []Tag{*tags[0]}
	})
	if err == nil || !strings.Contains(err.Error(), "relation assignments are not supported") {
		t.Fatalf("expected a clear relation error, got %v", err)
	}
}

func TestInsert_RejectsInapplicableOptions(t *testing.T) {
	ctx, e, done := setupArchive(t)
	defer done()

	_, err := goql.Insert[OrderArchive](ctx, e,
		func(a *OrderArchive, o *Order, fields *goql.Fields) {
			fields.Names = []string{"Total"}
			a.Total = o.Total
			a.Origin = o.Priority
		})
	if !errors.Is(err, goql.ErrInvalidOption) {
		t.Fatalf("expected ErrInvalidOption for Fields on Insert, got %v", err)
	}
}

func TestInsert_RejectsValueParameters(t *testing.T) {
	ctx, e, done := setupArchive(t)
	defer done()

	_, err := goql.Insert[OrderArchive](ctx, e, func(a OrderArchive, o *Order) {
		a.Total = o.Total
	})
	if !errors.Is(err, goql.ErrInvalidLambda) {
		t.Fatalf("expected ErrInvalidLambda for a value destination, got %v", err)
	}
}

func TestSelect_RejectsConflictOption(t *testing.T) {
	ctx, e, done := setupArchive(t)
	defer done()

	// Conflict has no meaning outside Insert, so it must be refused rather than dropped.
	_, err := goql.Select[Order](ctx, e, func(o *Order, cf *goql.Conflict) bool {
		cf.Ignore = true
		return o.Total > 0
	})
	if !errors.Is(err, goql.ErrInvalidOption) {
		t.Fatalf("expected ErrInvalidOption for Conflict on Select, got %v", err)
	}
}

// selectArchive returns every archived row.
func selectArchive(t *testing.T, ctx context.Context, e *goql.Engine) []*OrderArchive {
	t.Helper()
	rows, err := goql.Search(ctx, e, OrderArchive{})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// seedTags creates just the tags, for tests that do not need orders.
func seedTags(t *testing.T, ctx context.Context, e *goql.Engine) []*testmodels.Tag {
	t.Helper()
	tags, err := goql.Create(ctx, e, []Tag{{Name: "urgent"}})
	if err != nil {
		t.Fatal(err)
	}
	return tags
}

// Assigning to the source is meaningless in an INSERT … SELECT — nothing mutates the rows
// being read. It used to be silently dropped, and the insert succeeded anyway.
func TestInsert_RejectsAssignmentToSource(t *testing.T) {
	ctx, e, done := setupArchive(t)
	defer done()
	seedData(t, ctx, e)

	_, err := goql.Insert[OrderArchive](ctx, e, func(a *OrderArchive, o *Order) {
		a.Total = o.Total
		o.Priority = "mutated"
	})
	if !errors.Is(err, goql.ErrInvalidLambda) {
		t.Fatalf("expected ErrInvalidLambda for assigning to the source, got %v", err)
	}
	if !strings.Contains(err.Error(), "only read from") {
		t.Errorf("error should say the source is read-only, got %q", err)
	}

	// And nothing was written.
	if rows := selectArchive(t, ctx, e); len(rows) != 0 {
		t.Fatalf("expected no rows inserted, got %d", len(rows))
	}
}

// The destination row does not exist yet, so it cannot be read in a condition.
func TestInsert_RejectsReadingDestination(t *testing.T) {
	ctx, e, done := setupArchive(t)
	defer done()

	_, err := goql.Insert[OrderArchive](ctx, e, func(a *OrderArchive, o *Order) {
		if a.Reason == "x" {
			a.Total = o.Total
		}
	})
	if !errors.Is(err, goql.ErrInvalidLambda) {
		t.Fatalf("expected ErrInvalidLambda for reading the destination, got %v", err)
	}
	if !strings.Contains(err.Error(), "no rows yet") {
		t.Errorf("error should explain why, got %q", err)
	}
}

// The lambda's first parameter must be the model named by Insert[D], so the destination the
// call declares is the one the body assigns to. The source needs no type argument — it is
// whatever model the lambda declares second.
func TestInsert_RejectsMismatchedDestination(t *testing.T) {
	ctx, e, done := setupArchive(t)
	defer done()

	// Destination and source swapped relative to Insert[OrderArchive].
	_, err := goql.Insert[OrderArchive](ctx, e, func(o *Order, a *OrderArchive) {
		o.Priority = a.Reason
	})
	if !errors.Is(err, goql.ErrInvalidLambda) {
		t.Fatalf("expected ErrInvalidLambda for swapped parameters, got %v", err)
	}
	if !strings.Contains(err.Error(), "destination") {
		t.Errorf("error should name the offending role, got %q", err)
	}
}

// The source must be a model, not an option carrier or an arbitrary struct.
func TestInsert_RejectsNonModelSource(t *testing.T) {
	ctx, e, done := setupArchive(t)
	defer done()

	_, err := goql.Insert[OrderArchive](ctx, e, func(a *OrderArchive, p *ArchiveParams) {
		a.Reason = p.Reason
	})
	if !errors.Is(err, goql.ErrInvalidLambda) {
		t.Fatalf("expected ErrInvalidLambda for a non-model source, got %v", err)
	}
	if !strings.Contains(err.Error(), "rows are copied from") {
		t.Errorf("error should say what the second parameter is for, got %q", err)
	}
}
