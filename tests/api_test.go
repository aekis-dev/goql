package tests

import (
	"context"
	"errors"
	"testing"

	"github.com/aekis-dev/goql"
)

// A value-typed entity parameter is rejected. Before the split both forms parsed
// identically, so `func(c Customer) { c.Status = "x" }` looked like dead code to any Go
// reader while silently working.
func TestAPI_ValueLambdaIsRejected(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	_, err := goql.Select[Customer](ctx, e, func(c Customer) bool {
		return c.Country == "USA"
	})
	assertError(t, err)
	if !errors.Is(err, goql.ErrInvalidLambda) {
		t.Fatalf("expected ErrInvalidLambda, got %v", err)
	}
	assertContains(t, err.Error(), "*Customer")
}

// The lambda's entity type must match the type parameter.
func TestAPI_MismatchedTypeParamIsRejected(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	_, err := goql.Select[Customer](ctx, e, func(o *Order) bool {
		return o.Priority == "High"
	})
	assertError(t, err)
	if !errors.Is(err, goql.ErrInvalidLambda) {
		t.Fatalf("expected ErrInvalidLambda, got %v", err)
	}
}

// A predicate must return bool; a write lambda must return nothing.
func TestAPI_WrongReturnShapeIsRejected(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	_, err := goql.Select[Customer](ctx, e, func(c *Customer) {})
	assertError(t, err)
	if !errors.Is(err, goql.ErrInvalidLambda) {
		t.Fatalf("expected ErrInvalidLambda for non-bool predicate, got %v", err)
	}

	_, err = goql.Update[Customer](ctx, e, func(c *Customer) bool { return true })
	assertError(t, err)
	if !errors.Is(err, goql.ErrInvalidLambda) {
		t.Fatalf("expected ErrInvalidLambda for value-returning write, got %v", err)
	}
}

// Sentinel errors are matchable with errors.Is rather than by string.
func TestAPI_CapturedVariableSentinel(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	minAge := 30
	_, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return c.Age > minAge
	})
	assertError(t, err)
	if !errors.Is(err, goql.ErrCapturedVariable) {
		t.Fatalf("expected ErrCapturedVariable, got %v", err)
	}
	_ = minAge
}

// A struct that does not embed goql.Model cannot be used as an entity.
func TestAPI_NonEntityIsRejected(t *testing.T) {
	type NotAModel struct{ Name string }

	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	_, err := goql.Create(ctx, e, []NotAModel{{Name: "x"}})
	assertError(t, err)
	if !errors.Is(err, goql.ErrNotEntity) {
		t.Fatalf("expected ErrNotEntity, got %v", err)
	}
}

// The per-call context reaches the driver: a cancelled context fails the query.
func TestAPI_PerCallContextIsHonoured(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	_, err := goql.Select[Customer](cancelled, e, func(c *Customer) bool {
		return c.Country == "USA"
	})
	assertError(t, err)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// Create fills primary keys into both the returned pointers and the caller's slice.
func TestAPI_CreateWritesBackPrimaryKeys(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	input := []Tag{{Name: "alpha"}, {Name: "beta"}}
	created, err := goql.Create(ctx, e, input)
	if err != nil {
		t.Fatal(err)
	}

	for i := range created {
		if created[i].ID == 0 {
			t.Fatalf("returned entity %d has no primary key", i)
		}
		if input[i].ID != created[i].ID {
			t.Errorf("caller slice element %d not updated: got %d, want %d",
				i, input[i].ID, created[i].ID)
		}
	}
}
