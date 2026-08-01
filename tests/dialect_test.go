package tests

import (
	"strings"
	"testing"

	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/models"
	"github.com/aekis-dev/goql/query"
)

// Per-dialect SQL assertions. These need no database: the point is that one model and one
// lambda render correctly for each engine, so divergence shows up here rather than in
// production against an engine nobody tested.
var (
	sqlite   = query.NewDialect(query.SQLite{})
	postgres = query.NewDialect(query.Postgres{})
	mysql    = query.NewDialect(query.MySQL{})
)

func parseSource(t *testing.T, source string) *query.ParseQuery {
	t.Helper()
	body, err := (&goql.DebugExecutor{}).ParseQueryFromSource(source, "Select")
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// parseInsertSource parses a lambda whose first parameter is an Insert destination. The
// mode is explicit because the signature alone cannot distinguish it from a predicate that
// joins another model.
func parseInsertSource(t *testing.T, source string) *query.ParseQuery {
	t.Helper()
	body, err := (&goql.DebugExecutor{}).ParseQueryFromSource(source, "Insert")
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// Identifier quoting: double quotes for SQLite and Postgres, backticks for MySQL.
func TestDialect_IdentifierQuoting(t *testing.T) {
	assertEqual(t, `"name"`, sqlite.QuoteIdent("name"))
	assertEqual(t, `"name"`, postgres.QuoteIdent("name"))
	assertEqual(t, "`name`", mysql.QuoteIdent("name"))
}

// An embedded quote is escaped by doubling it, which is what SQL wants — strconv.Quote
// would backslash-escape it and produce invalid SQL.
func TestDialect_QuotingEscapesEmbeddedQuote(t *testing.T) {
	assertEqual(t, `"we""ird"`, sqlite.QuoteIdent(`we"ird`))
	assertEqual(t, "`we``ird`", mysql.QuoteIdent("we`ird"))
}

// Placeholders: positional for Postgres, anonymous elsewhere. The numbering must follow
// the order values are bound.
func TestDialect_Placeholders(t *testing.T) {
	body := parseSource(t, `func(c *Customer) bool {
		return c.Country == "USA" && c.Age > 40
	}`)

	sq, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, sq.SQL, `c."country" = ?`)
	assertContains(t, sq.SQL, `c."age" > ?`)

	pq, err := postgres.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, pq.SQL, `c."country" = $1`)
	assertContains(t, pq.SQL, `c."age" > $2`)

	mq, err := mysql.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, mq.SQL, "c.`country` = ?")
}

// Postgres numbers across the whole statement, so a LIMIT after a WHERE continues the
// sequence rather than restarting.
func TestDialect_PlaceholdersContinueIntoOptions(t *testing.T) {
	body := parseSource(t, `func(c *Customer) bool {
		return c.Country == "USA"
	}`)
	limit := 10
	offset := 5
	opts := &query.Options{Limit: &limit, Offset: &offset}

	pq, err := postgres.LambdaSearch(body, opts)
	assertNoError(t, err)
	assertContains(t, pq.SQL, `c."country" = $1`)
	assertContains(t, pq.SQL, "LIMIT $2")
	assertContains(t, pq.SQL, "OFFSET $3")
	assertEqual(t, 3, len(pq.Args))
}

// An OFFSET with no LIMIT needs an open-ended limit, spelled differently per engine.
func TestDialect_OpenEndedLimit(t *testing.T) {
	body := parseSource(t, `func(c *Customer) bool { return c.Country == "USA" }`)
	offset := 5
	opts := &query.Options{Offset: &offset}

	sq, _ := sqlite.LambdaSearch(body, opts)
	assertContains(t, sq.SQL, "LIMIT -1 OFFSET ?")

	pq, _ := postgres.LambdaSearch(body, opts)
	assertContains(t, pq.SQL, "LIMIT ALL OFFSET $2")

	mq, _ := mysql.LambdaSearch(body, opts)
	assertContains(t, mq.SQL, "LIMIT 18446744073709551615 OFFSET ?")
}

// Postgres returns generated keys with the row; the others use LastInsertId.
func TestDialect_InsertReturning(t *testing.T) {
	schema, _ := models.GetModel(&Tag{})
	tag := &Tag{Name: "urgent"}

	pq, err := postgres.EntityCreate(tag, schema)
	assertNoError(t, err)
	assertContains(t, pq.SQL, `RETURNING "id"`)

	sq, err := sqlite.EntityCreate(tag, schema)
	assertNoError(t, err)
	assertNotContains(t, sq.SQL, "RETURNING")

	mq, err := mysql.EntityCreate(tag, schema)
	assertNoError(t, err)
	assertNotContains(t, mq.SQL, "RETURNING")
}

// A joined UPDATE is a different statement shape on MySQL, not just different tokens.
func TestDialect_JoinedUpdateShape(t *testing.T) {
	body := parseSource(t, `func(o *Order) {
		if o.Customer.Country == "USA" {
			o.Priority = "High"
		}
	}`)

	pq, err := postgres.LambdaWrite(body)
	assertNoError(t, err)
	assertEqual(t, 1, len(pq))
	assertContains(t, pq[0].SQL, `FROM "customers" c`)
	assertNotContains(t, pq[0].SQL, "JOIN")

	mq, err := mysql.LambdaWrite(body)
	assertNoError(t, err)
	assertEqual(t, 1, len(mq))
	assertContains(t, mq[0].SQL, "JOIN `customers` c ON")
	assertNotContains(t, mq[0].SQL, "FROM `customers`")
}

// Skipping an existing join row is spelled three different ways.
func TestDialect_InsertIgnore(t *testing.T) {
	schema, _ := models.GetModel(&Order{})
	m := schema.Fields["Tags"].ManyToMany

	assertContains(t, sqlite.JoinInsert(m).SQL, "INSERT OR IGNORE INTO")
	assertContains(t, postgres.JoinInsert(m).SQL, "ON CONFLICT DO NOTHING")
	assertContains(t, mysql.JoinInsert(m).SQL, "INSERT IGNORE INTO")
}

// Logical types render to each engine's own vocabulary.
func TestDialect_TypeMapping(t *testing.T) {
	cases := []struct {
		field                      models.Field
		sqliteT, postgresT, mysqlT string
	}{
		{models.Field{Type: models.TypeInteger}, "INTEGER", "integer", "INT"},
		{models.Field{Type: models.TypeBigInt}, "INTEGER", "bigint", "BIGINT"},
		{models.Field{Type: models.TypeBoolean}, "INTEGER", "boolean", "BOOLEAN"},
		{models.Field{Type: models.TypeDouble}, "REAL", "double precision", "DOUBLE"},
		{models.Field{Type: models.TypeText}, "TEXT", "text", "TEXT"},
		{models.Field{Type: models.TypeBytes}, "BLOB", "bytea", "BLOB"},
		{models.Field{Type: models.TypeJSON}, "TEXT", "jsonb", "JSON"},
		{models.Field{Type: models.TypeVarchar, Size: 20}, "VARCHAR(20)", "varchar(20)", "VARCHAR(20)"},
		{models.Field{Type: models.TypeDecimal, Precision: 10, Scale: 2}, "NUMERIC(10, 2)", "numeric(10, 2)", "DECIMAL(10, 2)"},
	}

	for _, c := range cases {
		field := c.field
		assertEqual(t, c.sqliteT, sqlite.TypeName(&field))
		assertEqual(t, c.postgresT, postgres.TypeName(&field))
		assertEqual(t, c.mysqlT, mysql.TypeName(&field))
	}
}

// A type outside the vocabulary is emitted verbatim, the escape hatch for an
// engine-specific column type.
func TestDialect_RawTypePassthrough(t *testing.T) {
	field := models.Field{Type: models.ColumnType("geography(Point,4326)")}
	assertEqual(t, "geography(Point,4326)", postgres.TypeName(&field))
}

// Regression: a string default was emitted bare, so `DEFAULT Active` reached the engine.
// SQLite tolerates it; Postgres reads it as a column reference. Known SQL expressions must
// still pass through unquoted.
func TestDialect_StringDefaultIsQuoted(t *testing.T) {
	schema, _ := models.GetModel(&Customer{})

	for name, d := range map[string]*query.Dialect{"sqlite": sqlite, "postgres": postgres, "mysql": mysql} {
		sql, err := d.CreateTable(schema)
		assertNoError(t, err)
		if !strings.Contains(sql, "DEFAULT 'Active'") {
			t.Errorf("%s: expected a quoted string default, got:\n%s", name, sql)
		}
		if !strings.Contains(sql, "DEFAULT CURRENT_TIMESTAMP") {
			t.Errorf("%s: expected CURRENT_TIMESTAMP to stay an expression, got:\n%s", name, sql)
		}
	}
}

// Auto-increment is a clause on SQLite and MySQL, and absent on Postgres.
func TestDialect_AutoIncrement(t *testing.T) {
	schema, _ := models.GetModel(&Tag{})

	sq, _ := sqlite.CreateTable(schema)
	assertContains(t, sq, "AUTOINCREMENT")

	mq, _ := mysql.CreateTable(schema)
	assertContains(t, mq, "AUTO_INCREMENT")

	pq, _ := postgres.CreateTable(schema)
	assertNotContains(t, pq, "AUTOINCREMENT")
	assertNotContains(t, pq, "AUTO_INCREMENT")
}

// --- Type normalisation ---
//
// Detecting a type change means comparing what the model wants against what introspection
// reports. Those are spelled differently even when identical, so each engine normalises both
// sides. These cases are the spellings that would otherwise cause a migration to be proposed
// on every run.

func TestDialect_NormalizePostgresReportedTypes(t *testing.T) {
	cases := []struct{ wanted, reported string }{
		{"varchar(50)", "character varying(50)"},
		{"text", "text"},
		{"timestamp", "timestamp without time zone"},
		{"double precision", "double precision"},
		{"numeric(10, 2)", "numeric(10,2)"},
		{"bigint", "int8"},
		{"integer", "int4"},
		{"jsonb", "jsonb"},
	}
	for _, c := range cases {
		if !postgres.TypesEqual(c.wanted, c.reported) {
			t.Errorf("postgres: %q and %q should compare equal (got %q vs %q)",
				c.wanted, c.reported, postgres.NormalizeType(c.wanted), postgres.NormalizeType(c.reported))
		}
	}
}

func TestDialect_NormalizeMySQLReportedTypes(t *testing.T) {
	cases := []struct{ wanted, reported string }{
		// MySQL stores a boolean as tinyint(1), which is what it reports back.
		{"BOOLEAN", "tinyint(1)"},
		{"VARCHAR(100)", "varchar(100)"},
		{"DECIMAL(10, 2)", "decimal(10,2)"},
		{"INT", "int"},
		{"DATETIME", "datetime"},
		{"JSON", "json"},
	}
	for _, c := range cases {
		if !mysql.TypesEqual(c.wanted, c.reported) {
			t.Errorf("mysql: %q and %q should compare equal (got %q vs %q)",
				c.wanted, c.reported, mysql.NormalizeType(c.wanted), mysql.NormalizeType(c.reported))
		}
	}
}

// SQLite normalises to the affinity it actually enforces, so goql never proposes a type
// change that the engine would treat as a no-op.
func TestDialect_NormalizeSQLiteAffinity(t *testing.T) {
	equal := []struct{ a, b string }{
		{"VARCHAR(100)", "TEXT"},
		{"CHAR(10)", "text"},
		{"INTEGER", "INT"},
		{"BIGINT", "integer"},
		{"DOUBLE", "REAL"},
		{"FLOAT", "real"},
	}
	for _, c := range equal {
		if !sqlite.TypesEqual(c.a, c.b) {
			t.Errorf("sqlite: %q and %q share an affinity and should compare equal", c.a, c.b)
		}
	}

	different := []struct{ a, b string }{
		{"INTEGER", "TEXT"},
		{"TEXT", "BLOB"},
		{"REAL", "INTEGER"},
		{"NUMERIC(10, 2)", "TEXT"},
	}
	for _, c := range different {
		if sqlite.TypesEqual(c.a, c.b) {
			t.Errorf("sqlite: %q and %q have different affinities and should differ", c.a, c.b)
		}
	}
}

// Genuinely different types must still be detected, or detection would be useless.
func TestDialect_NormalizeDetectsRealDifferences(t *testing.T) {
	if postgres.TypesEqual("varchar(50)", "character varying(100)") {
		t.Error("postgres: a length change must be detected")
	}
	if postgres.TypesEqual("integer", "bigint") {
		t.Error("postgres: a width change must be detected")
	}
	if mysql.TypesEqual("DECIMAL(10, 2)", "decimal(12,2)") {
		t.Error("mysql: a precision change must be detected")
	}

	// Postgres writes a precision in the middle of the type name, so words after the
	// parentheses still matter: dropping them would make these two compare equal.
	if postgres.TypesEqual("timestamp(6) with time zone", "timestamp(6) without time zone") {
		t.Error("postgres: time zone awareness must be part of the comparison")
	}
	if !postgres.TypesEqual("timestamp(6)", "timestamp(6) without time zone") {
		t.Error("postgres: a bare timestamp is the without-time-zone form")
	}
}

// INSERT … SELECT: the destination is named in full, the source is aliased, and the
// placeholders of the SELECT list precede those of the WHERE clause.
func TestDialect_InsertSelect(t *testing.T) {
	body := parseInsertSource(t, `func(a *OrderArchive, o *Order) {
		if o.Total > 1000 {
			a.Total = o.Total
			a.Reason = "big"
		}
	}`)
	src, _ := models.GetModel(&Order{})

	sq, err := sqlite.LambdaInsert(body, src, nil)
	assertNoError(t, err)
	assertEqual(t, 1, len(sq))
	assertContains(t, sq[0].SQL, `INSERT INTO "order_archives" (`)
	assertContains(t, sq[0].SQL, `SELECT o."total_amount", ?`)
	assertContains(t, sq[0].SQL, `FROM "orders" o WHERE o."total_amount" > ?`)
	// The destination's own timestamp columns are filled in, since no row exists in Go.
	assertContains(t, sq[0].SQL, `"goql_created", "goql_updated"`)
	assertContains(t, sq[0].SQL, "CURRENT_TIMESTAMP, CURRENT_TIMESTAMP")
	// The bound literal is selected as a constant, before the WHERE value.
	assertEqual(t, 2, len(sq[0].Args))
	assertEqual(t, "big", sq[0].Args[0])

	pq, err := postgres.LambdaInsert(body, src, nil)
	assertNoError(t, err)
	assertContains(t, pq[0].SQL, `SELECT o."total_amount", $1`)
	assertContains(t, pq[0].SQL, `> $2`)
}

// "Skip the conflicting row" is spelled in the verb by SQLite and MySQL, and in a trailing
// clause by Postgres.
func TestDialect_InsertSelectConflictIgnore(t *testing.T) {
	body := parseInsertSource(t, `func(a *OrderArchive, o *Order) {
		a.Total = o.Total
	}`)
	src, _ := models.GetModel(&Order{})
	opts := &query.Options{ConflictIgnore: true}

	sq, err := sqlite.LambdaInsert(body, src, opts)
	assertNoError(t, err)
	assertContains(t, sq[0].SQL, "INSERT OR IGNORE INTO")

	mq, err := mysql.LambdaInsert(body, src, opts)
	assertNoError(t, err)
	assertContains(t, mq[0].SQL, "INSERT IGNORE INTO")

	pq, err := postgres.LambdaInsert(body, src, opts)
	assertNoError(t, err)
	assertContains(t, pq[0].SQL, "ON CONFLICT DO NOTHING")
	assertNotContains(t, pq[0].SQL, "INSERT IGNORE")
}

// A nested goql call becomes a subquery, sharing the enclosing statement's aliases and
// placeholder counter.
func TestDialect_Subquery(t *testing.T) {
	body := parseSource(t, `func(o *Order) bool {
		usa, _ := goql.Select[Customer](ctx, e, func(c *Customer) bool {
			return c.Country == "USA"
		})
		return goql.Condition(o.Customer, "IN", usa) && o.Total > 100
	}`)

	sq, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, sq.SQL, `o."customer_id" IN (SELECT c."id" FROM "customers" c WHERE c."country" = ?)`)
	// The subquery's value is bound before the outer condition's, matching emission order.
	assertEqual(t, 2, len(sq.Args))
	assertEqual(t, "USA", sq.Args[0])

	pq, err := postgres.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, pq.SQL, `c."country" = $1`)
	assertContains(t, pq.SQL, `o."total_amount" > $2`)
}

// A correlated EXISTS refers to the outer row, so the two must share aliases.
func TestDialect_CorrelatedExists(t *testing.T) {
	body := parseSource(t, `func(c *Customer) bool {
		return goql.Unwrap(goql.Exists[Order](ctx, e, func(o *Order) bool {
			return o.Customer == c && o.Total > 1000
		}))
	}`)

	sq, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, sq.SQL, `WHERE EXISTS (SELECT 1 FROM "orders" o`)
	assertContains(t, sq.SQL, `o."customer_id" = c."id"`)
}

// A projection renders each column with its alias, and the plain ones become the GROUP BY.
func TestDialect_Projection(t *testing.T) {
	body := parseSource(t, `func(t *PriorityTotals, o *Order, from *goql.From) bool {
		from.Model = o
		t.Priority = o.Priority
		t.Total = goql.Sum(o.Total)
		t.Orders = goql.Count()
		return o.Total > 0
	}`)

	sq, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, sq.SQL, `SELECT o."priority" AS "Priority", SUM(o."total_amount") AS "Total", COUNT(*) AS "Orders"`)
	assertContains(t, sq.SQL, `GROUP BY o."priority"`)

	mq, err := mysql.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, mq.SQL, "SUM(o.`total_amount`) AS `Total`")
}

// GROUP BY is additive — the named keys, then the projected plain columns — and a condition
// over an aggregate lands in HAVING rather than WHERE.
func TestDialect_GroupByAndHaving(t *testing.T) {
	body := parseSource(t, `func(t *PriorityTotals, o *Order, from *goql.From, g *goql.Group) bool {
		from.Model = o
		g.By = []string{"ShippingMethod"}
		t.Priority = o.Priority
		t.Total = goql.Sum(o.Total)
		return o.Total > 100 && goql.Sum(o.Total) > 1000
	}`)

	sq, err := sqlite.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, sq.SQL, `WHERE o."total_amount" > ?`)
	assertContains(t, sq.SQL, `GROUP BY o."shipping_method", o."priority"`)
	assertContains(t, sq.SQL, `HAVING SUM(o."total_amount") > ?`)
	// The WHERE value binds before the HAVING one, matching emission order.
	assertEqual(t, 2, len(sq.Args))
	assertEqual(t, int64(100), sq.Args[0])
	assertEqual(t, int64(1000), sq.Args[1])

	pq, err := postgres.LambdaSearch(body, nil)
	assertNoError(t, err)
	assertContains(t, pq.SQL, `> $1`)
	assertContains(t, pq.SQL, `HAVING SUM(o."total_amount") > $2`)
}

// A set operation renders its branches with independent aliases but one placeholder
// sequence, and orders the combination by a projected column name.
func TestDialect_SetOperation(t *testing.T) {
	body := parseSource(t, `func(m *Movement, sort *goql.Sort) bool {
		sort.By = "Amount"
		sort.Desc = true
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
	}`)

	sq, err := sqlite.LambdaSearch(body, body.Body.Options)
	assertNoError(t, err)
	assertContains(t, sq.SQL, " UNION ALL ")
	// Both branches read the same table and both alias it "o": the scopes are per branch.
	assertEqual(t, 2, strings.Count(sq.SQL, `FROM "orders" o`))
	assertContains(t, sq.SQL, `ORDER BY "Amount" DESC`)

	// Postgres numbers parameters across the whole statement, not per branch.
	pq, err := postgres.LambdaSearch(body, body.Body.Options)
	assertNoError(t, err)
	assertContains(t, pq.SQL, `> $1`)
	assertContains(t, pq.SQL, `<= $2`)
}
