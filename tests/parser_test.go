package tests

import (
	"testing"

	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/query"
)

// singleAssignment returns the only assignment across all branches of a parsed body.
func singleAssignment(t *testing.T, parsed *query.ParseQuery) *query.ParseAssign {
	body := parsed.Body
	t.Helper()
	var found []*query.ParseAssign
	for _, branch := range body.Branches {
		found = append(found, branch.Assignments...)
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly 1 assignment, got %d", len(found))
	}
	return found[0]
}

// Regression: extractValue's UnaryExpr case recursed on `expr` (itself) instead of
// `v.X`, so any negative literal stack-overflowed. The negated value must also come
// back numeric — it used to be formatted into a string.
func TestParse_NegativeLiteral(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(c *Customer) {
		c.Discount = -0.15
	}`, "Update")
	assertNoError(t, err)
	assertEqual(t, -0.15, singleAssignment(t, body).Value.Value)
}

func TestParse_NegativeIntLiteral(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(c *Customer) {
		c.Number = -5
	}`, "Update")
	assertNoError(t, err)
	assertEqual(t, int64(-5), singleAssignment(t, body).Value.Value)
}

// Regression: string literals were unwrapped with strings.Trim(v, `"`), which leaves
// Go escape sequences intact instead of decoding them.
func TestParse_StringLiteralEscapes(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(c *Customer) {
		c.Nickname = "say \"hi\""
	}`, "Update")
	assertNoError(t, err)
	assertEqual(t, `say "hi"`, singleAssignment(t, body).Value.Value)
}

// Regression: a bare identifier used to be compiled into its own name as a string
// value, so `c.Age > minAge` silently became `age > 'minAge'`.
func TestParse_CapturedVariableIsRejected(t *testing.T) {
	executor := &goql.DebugExecutor{}
	_, err := executor.ParseQueryFromSource(`func(c *Customer) bool {
		return c.Age > minAge
	}`, "Select")
	assertError(t, err)
	assertContains(t, err.Error(), "minAge")
}

// Two lambdas declared on a single source line must resolve to their own bodies.
// The previous extractor scanned braces from the reported line and could not tell them
// apart; a line-keyed parse cache would also serve the first body for both.
func TestParse_TwoLambdasOnOneLine(t *testing.T) {
	ctx, e, cleanup := setupDB(t)
	defer cleanup()
	seedData(t, ctx, e)

	inUSA, inCanada := func(c *Customer) bool { return c.Country == "USA" }, func(c *Customer) bool { return c.Country == "Canada" }

	usa, err := goql.Select[Customer](ctx, e, inUSA)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(usa))
	assertEqual(t, "Alice", usa[0].Name)

	canada, err := goql.Select[Customer](ctx, e, inCanada)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(canada))
	assertEqual(t, "Bob", canada[0].Name)
}

// --- Branch semantics (if / else-if / else, switch) ---

// Regression: else-branch assignments were appended under the *if* branch's condition,
// emitting two conflicting SET clauses in one UPDATE and never touching the else rows.
func TestParse_IfElseProducesTwoUpdates(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(c *Customer) {
		if c.Age > 40 {
			c.Status = "Senior"
		} else {
			c.Status = "Junior"
		}
	}`, "Update")
	assertNoError(t, err)

	queries, err := dialect.LambdaWrite(body)
	assertNoError(t, err)

	if len(queries) != 2 {
		t.Fatalf("expected 2 UPDATE statements, got %d", len(queries))
	}
	assertContains(t, queries[0].SQL, `WHERE "customers"."age" > ?`)
	assertNotContains(t, queries[0].SQL, "NOT")
	assertEqual(t, "Senior", queries[0].Args[0])

	assertContains(t, queries[1].SQL, `WHERE NOT ("customers"."age" > ?)`)
	assertEqual(t, "Junior", queries[1].Args[0])
}

func TestParse_ElseIfChainIsMutuallyExclusive(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(c *Customer) {
		if c.Age > 60 {
			c.Status = "Senior"
		} else if c.Age > 40 {
			c.Status = "Premium"
		} else {
			c.Status = "Active"
		}
	}`, "Update")
	assertNoError(t, err)

	queries, err := dialect.LambdaWrite(body)
	assertNoError(t, err)

	if len(queries) != 3 {
		t.Fatalf("expected 3 UPDATE statements, got %d", len(queries))
	}
	// Second arm excludes the first.
	assertContains(t, queries[1].SQL, `NOT ("customers"."age" > ?)`)
	assertContains(t, queries[1].SQL, `AND ("customers"."age" > ?)`)
	assertEqual(t, "Premium", queries[1].Args[0])

	// Final else excludes both.
	assertContains(t, queries[2].SQL, `(NOT ("customers"."age" > ?)) AND (NOT ("customers"."age" > ?))`)
	assertEqual(t, "Active", queries[2].Args[0])
}

func TestParse_TaglessSwitch(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(c *Customer) {
		switch {
		case c.Age > 60:
			c.Status = "Senior"
		default:
			c.Status = "Active"
		}
	}`, "Update")
	assertNoError(t, err)

	queries, err := dialect.LambdaWrite(body)
	assertNoError(t, err)

	if len(queries) != 2 {
		t.Fatalf("expected 2 UPDATE statements, got %d", len(queries))
	}
	assertContains(t, queries[0].SQL, `WHERE "customers"."age" > ?`)
	assertContains(t, queries[1].SQL, `WHERE NOT ("customers"."age" > ?)`)
	assertEqual(t, "Active", queries[1].Args[0])
}

func TestParse_TagSwitchWithMultipleValues(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(c *Customer) {
		switch c.Country {
		case "USA":
			c.Status = "Active"
		case "Canada", "Mexico":
			c.Status = "Premium"
		default:
			c.Status = "Inactive"
		}
	}`, "Update")
	assertNoError(t, err)

	queries, err := dialect.LambdaWrite(body)
	assertNoError(t, err)

	if len(queries) != 3 {
		t.Fatalf("expected 3 UPDATE statements, got %d", len(queries))
	}
	assertContains(t, queries[0].SQL, `WHERE "customers"."country" = ?`)

	// Two values in one case are ORed.
	assertContains(t, queries[1].SQL, `("customers"."country" = ?) OR ("customers"."country" = ?)`)
	assertEqual(t, "Premium", queries[1].Args[0])

	// default excludes every case value.
	assertContains(t, queries[2].SQL, "NOT")
	assertEqual(t, "Inactive", queries[2].Args[0])
}

// A guard-clause predicate must mean "everything the guard did not catch", not
// "everything" — the trailing `return true` carries the negation of the guard.
func TestParse_GuardClausePredicate(t *testing.T) {
	executor := &goql.DebugExecutor{}
	body, err := executor.ParseQueryFromSource(`func(c *Customer) bool {
		if c.Country == "USA" {
			return false
		}
		return true
	}`, "Select")
	assertNoError(t, err)

	q, err := dialect.LambdaSearch(body, nil)
	assertNoError(t, err)

	assertContains(t, q.SQL, `WHERE NOT (c."country" = ?)`)
	assertEqual(t, []any{"USA"}, q.Args)
}
