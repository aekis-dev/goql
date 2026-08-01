package tests

import (
	"errors"
	"testing"

	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/query"
)

func TestCondition_Like(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return goql.Condition(c.Name, "LIKE", "Ali%")
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))
	assertEqual(t, "Alice", rows[0].Name)
}

func TestCondition_In(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return goql.Condition(c.Country, "IN", "Canada", "Mexico")
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))
	assertEqual(t, "Bob", rows[0].Name)
}

func TestCondition_NotIn(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return goql.Condition(c.Country, "NOT IN", "Canada")
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))
	assertEqual(t, "Alice", rows[0].Name)
}

// IS NULL finally works: `c.Deleted == nil` is still rejected as a captured identifier,
// so this is the supported spelling.
func TestCondition_IsNull(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return goql.Condition(c.Deleted, "IS NULL")
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 2, len(rows))

	none, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return goql.Condition(c.Deleted, "IS NOT NULL")
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 0, len(none))
}

// Condition composes with native Go operators in the same predicate.
func TestCondition_CombinesWithNativeOperators(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return c.Age > 30 && goql.Condition(c.Login, "LIKE", "b%")
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))
	assertEqual(t, "Bob", rows[0].Name)
}

// Values may come from the params struct like any other bound value.
func TestCondition_WithParams(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	type Pattern struct{ Like string }

	rows, err := goql.Select[Customer](ctx, e, func(c *Customer, p Pattern) bool {
		return goql.Condition(c.Name, "LIKE", p.Like)
	}, Pattern{Like: "Bo%"})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))
	assertEqual(t, "Bob", rows[0].Name)
}

// A raw string left-hand side is emitted verbatim, the escape hatch for expressions goql
// cannot model.
func TestCondition_RawColumnExpression(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return goql.Condition("length(name)", ">", 3)
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))
	assertEqual(t, "Alice", rows[0].Name)
}

func TestCondition_TypoInOperatorIsRejected(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	_, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return goql.Condition(c.Name, "LIK", "x%")
	})
	assertError(t, err)
	if !errors.Is(err, query.ErrUnsupportedOperator) {
		t.Fatalf("expected ErrUnsupportedOperator, got %v", err)
	}
	assertContains(t, err.Error(), "LIK")
}

func TestCondition_WrongValueCountIsRejected(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()

	// IS NULL takes no values.
	_, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return goql.Condition(c.Deleted, "IS NULL", "x")
	})
	assertError(t, err)
	if !errors.Is(err, query.ErrUnsupportedOperator) {
		t.Fatalf("expected ErrUnsupportedOperator, got %v", err)
	}

	// IN needs at least one.
	_, err = goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return goql.Condition(c.Country, "IN")
	})
	assertError(t, err)
	if !errors.Is(err, query.ErrUnsupportedOperator) {
		t.Fatalf("expected ErrUnsupportedOperator, got %v", err)
	}
}

// --- Count / Exists ---

func TestCount_MatchingRows(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	all, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return c.Status == "Active"
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 2, len(all))

	usa, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
		return c.Country == "USA"
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(usa))
}

// A relation join can multiply rows, so the count is over distinct primary keys.
func TestCount_AcrossRelationCountsRowsOnce(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	// Order 1 carries two tags, so the join yields it twice; counting must still say one.
	type Tally struct{ N int64 }
	rows, err := goql.Select[Tally](ctx, e, func(t *Tally, o *Order, from *goql.From) bool {
		from.Model = o
		t.N = goql.Count()
		for _, tag := range o.Tags {
			if goql.Condition(tag.Name, "IN", "urgent", "vip") {
				return true
			}
		}
		return false
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, int64(1), rows[0].N)
}

func TestExists_ShortCircuits(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	yes, err := goql.Exists[Customer](ctx, e, func(c *Customer) bool {
		return c.Country == "USA"
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, true, yes)

	no, err := goql.Exists[Customer](ctx, e, func(c *Customer) bool {
		return c.Country == "Atlantis"
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, false, no)
}

func TestCount_WithParams(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	type MinAge struct{ Value int }

	count, err := goql.Select[Customer](ctx, e, func(c *Customer, m MinAge) bool {
		return c.Age > m.Value
	}, MinAge{Value: 40})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(count))
}

// --- Execute / Bind ---

func TestExecute_RunsRawStatement(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	customers, _, _ := seedData(t, ctx, e)

	res, err := goql.Execute(ctx, e,
		`UPDATE "customers" SET "nickname" = ? WHERE "id" = ?`, "ali", customers[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, int64(1), affected)

	assertEqual(t, "ali", byID[Customer](t, ctx, e, customers[0].ID).Nickname)
}

func TestBind_ScansIntoEntities(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Bind[Customer](ctx, e,
		`SELECT * FROM "customers" WHERE "country" = ? ORDER BY "age"`, "USA")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))
	assertEqual(t, "Alice", rows[0].Name)
	assertEqual(t, 40, rows[0].Age)
}

// Bind uses the same column mapping as Select, so a narrower projection is fine and the
// unselected fields stay zero-valued.
func TestBind_PartialProjection(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	rows, err := goql.Bind[Customer](ctx, e, `SELECT "id", "name" FROM "customers" ORDER BY "name"`)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 2, len(rows))
	assertEqual(t, "Alice", rows[0].Name)
	assertEqual(t, "", rows[0].Country)
	if rows[0].ID == 0 {
		t.Error("expected the primary key to be scanned")
	}
}
