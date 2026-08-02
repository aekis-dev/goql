# goql

An ORM for Go where queries are written as ordinary Go functions.

```go
orders, err := goql.Select[Order](ctx, e, func(o *Order) bool {
    return o.Customer.Country == "USA" && o.Total > 1000
})
```

That generates a `SELECT` with a join on `customers`. The results come back as
`[]*Order` — no casts, no string DSL, and a typo in a field name is a compile error.

## The one thing to know

**Lambda bodies are inspected, not executed.** The function above is never called. goql
finds its source, parses it with `go/ast`, and compiles the statements into SQL. This is
clearest in an update:

```go
rows, err := goql.Update[Order](ctx, e, func(o *Order) {
    if o.Total > 1000 {
        o.Priority = "High"
    } else {
        o.Priority = "Normal"
    }
})
```

Nothing is mutated in Go. The assignments describe `SET` clauses and the `if` describes
`WHERE`; each arm becomes its own `UPDATE`, with mutually exclusive conditions. The
parameter is a pointer so the body reads as the mutation the statement performs — a value
receiver would look like dead code to any Go developer, so goql rejects it.

Two rules follow from "parsed, not executed", and they explain most of the API:

- **A lambda cannot use variables from the surrounding scope.** Nothing can read a
  closure's captured values by reflection, so an unresolved identifier is a parse error,
  not a silently wrong query. Call-time values go through a params struct (below).
- **Only parseable expressions are allowed** — comparisons, `&&`/`||`, `if`/`else`,
  `switch`, ranging over a relation, and `goql.Condition` for operators Go has no syntax
  for. (Go's `!` is not yet parsed; negate with `NOT IN` / `NOT LIKE`.)

## Install

```bash
go get github.com/aekis-dev/goql
```

## Declaring a model

Schemas are declared imperatively, so field metadata lives in Go rather than in struct
tags (tags are reserved for serialization):

```go
type Customer struct {
    goql.Model // ID, Created, Updated, Deleted + change tracking
    Name    string
    Age     int
    Country string
    Orders  []Order
}

func init() {
    models.AddModel(&Customer{}, "customers",
        &models.Field{Name: "Name", Type: models.TypeVarchar, Size: 100, NotNull: true, Index: true},
        &models.Field{Name: "Age", Checks: []string{"age >= 0"}},
        &models.Field{Name: "Country"},
        &models.Field{Name: "Orders", OneToMany: &models.OneToMany{Ref: "customer_id"}},
    )
}
```

Types not given are inferred from the Go type. `AddModel` runs in `init()`, so a package
declaring models must be imported for its models to exist.

## Using it

```go
db, _ := sql.Open("sqlite3", "app.db")
e := goql.New(db, goql.SQLite{}) // or goql.Postgres{} / goql.MySQL{}
```

The dialect is explicit because `database/sql` exposes no driver name; guessing would mean
a type switch that breaks on wrapped drivers.

There are two halves to the API. Struct calls describe rows by example, lambda calls by
predicate — one model and one operation per call:

|        | struct   | lambda                      |
| ------ | -------- | --------------------------- |
| create | `Create` | `Insert` (INSERT … SELECT)  |
| read   | `Search` | `Select`, `Exists`          |
| update | `Write`  | `Update`                    |
| delete | `Remove` | `Delete`                    |

```go
created, err := goql.Create(ctx, e, []Customer{{Name: "Alice", Country: "USA"}})
usa, err := goql.Search(ctx, e, Customer{Country: "USA"})   // non-zero fields → WHERE
one, err := goql.Get[Customer](ctx, e, 42)                  // by primary key, or a slice of them
any, err := goql.Exists[Order](ctx, e, func(o *Order) bool { return o.Total > 500 })
```

Every call takes a `context.Context` first, and `e.Transaction(func(e *goql.Engine) error)`
scopes a group of them; nested calls join the surrounding transaction rather than opening
their own.

### Copying rows: `Insert`

`Insert` builds rows in the database from rows already there. The first lambda parameter is
the destination, the second the source:

```go
archived, err := goql.Insert[OrderArchive](ctx, e, func(a *OrderArchive, o *Order) {
    if o.Total > 1000 {
        a.Total  = o.Total
        a.Reason = "high value"
    }
})
// → INSERT INTO order_archives (total, reason, …)
//   SELECT o.total, ?, … FROM orders o WHERE o.total > ?
```

Each assignment gives both halves at once: the left names a destination column, the right an
expression selected from the source — a source field, a literal, or a params value. Reaching
into a relation joins, an if/else emits one statement per arm, and `goql.Conflict{Ignore: true}`
skips rows that collide with an existing one. It returns a row count, not entities: recovering
generated keys is only portable where `INSERT … RETURNING` exists.

### Runtime values

Because parsed bodies are cached and compiled ahead of time, a value cannot be baked into
one. Declare a params struct and pass it at the call site:

```go
type OrderParams struct{ MinTotal float64 }

goql.Select[Order](ctx, e, func(o *Order, p OrderParams) bool {
    return o.Total > p.MinTotal
}, OrderParams{MinTotal: 100})
```

Field names are checked by the Go compiler, since the lambda is real Go.

### Options

The same types serve both halves — trailing values on a struct call, declared parameters
on a lambda:

```go
goql.Search(ctx, e, Customer{Country: "USA"}, goql.Sort{By: "Age", Desc: true}, goql.Limit{Value: 20})

goql.Select[Customer](ctx, e, func(c *Customer, sort *goql.Sort, limit *goql.Limit) bool {
    sort.By = "Age"
    sort.Desc = true
    limit.Value = 20
    return c.Country == "USA"
})
```

`Sort` (repeatable for multi-column), `Limit`, `Offset`, `Fields` (projection), and
`Preload`. Parameters are recognised by type, so their order does not matter.

### Relations

Relation fields come back nil unless you ask for them. Each preloaded relation costs a
fixed number of batched queries regardless of how many rows came back, so there is no N+1:

```go
goql.Select[Order](ctx, e, func(o *Order, pre *goql.Preload) bool {
    pre.Fields = []string{"Customer", "Tags"}
    return o.Total > 500
})
```

A field marked `Preload: true` loads on every read; a query naming relations replaces
those defaults entirely, so an empty `goql.Preload{}` means "none".

### Joining models that have no relation

A declared relation is joined by traversing it (`o.Customer.Country`). For two models with
no relation between them, declare both and relate them with a comparison:

```go
paid, err := goql.Select[Invoice](ctx, e, func(i *Invoice, p *Payment) bool {
    return i.Ref == p.Ref && p.Method == "card"
})
// → SELECT i.* FROM "invoices" i, "payments" p WHERE i."ref" = p."ref" AND p."method" = ?
```

A comparison whose two sides belong to *different* models is the join condition; everything
else is a filter. The result is still `[]*Invoice` — extra models constrain the query, they
don't widen the result. The join is an **inner** one: an equality can't say which side is
optional, so there is no LEFT join yet. `Update` and `Delete` reject a declared model rather
than ignoring it, since they reach other tables through relations, not a FROM list.

### Aggregates and grouping

A result type that isn't a model turns the lambda into a projection — each assignment is one
output column, and the plain ones are the `GROUP BY`:

```go
type PriorityTotals struct {
    Priority string
    Orders   int64
    Total    float64
}

rows, err := goql.Select[PriorityTotals](ctx, e,
    func(t *PriorityTotals, o *Order, from *goql.From) bool {
        from.Model = o
        t.Priority = o.Priority          // plain column → grouping key
        t.Orders   = goql.Count()        // COUNT(*)
        t.Total    = goql.Sum(o.Total)   // SUM(total_amount)
        return o.Total > 0
    })
// → SELECT o."priority" AS "Priority", COUNT(*) AS "Orders", SUM(o."total_amount") AS "Total"
//   FROM "orders" o WHERE o."total_amount" > ? GROUP BY o."priority"
```

`from.Model` names the model to read from — one of the lambda's own parameters, so it can't
disagree with the query. With no plain assignment there's no grouping, and you get the whole
set as one row.

`goql.Sum`, `Avg`, `Min`, `Max` and `Count` are parse-only markers. `Sum`, `Min` and `Max`
return their column's own type, so `goql.Sum(o.Priority)` doesn't even compile against a
`float64` field; summing text into a string field is caught while parsing, since SQLite
would quietly answer 0 where Postgres errors.

Group by something you can't project — a relation, or a column you don't want in the result —
with `goql.Group`, and filter groups by putting an aggregate in the predicate:

```go
goql.Select[Spend](ctx, e, func(s *Spend, o *Order, from *goql.From, g *goql.Group) bool {
    from.Model = o
    g.By    = []string{"Customer"}          // a relation has no scalar field to assign
    s.Total = goql.Sum(o.Total)
    return o.Total > 100 && goql.Sum(o.Total) > 1000
    //     └── WHERE          └── HAVING
})
```

Grouping keys are additive: projecting a plain column always groups by it. An aggregate
combined with `||`, or negated, is refused — SQL can't OR a row filter with a group filter,
so goql won't guess.

### Combining queries

Bind each branch to a name and return the combination — `Union`, `UnionAll`, `Intersect`,
`Except`:

```go
rows, err := goql.Select[Movement](ctx, e, func(m *Movement, sort *goql.Sort) bool {
    sort.By = "Amount"                     // applies to the combination

    live, _ := goql.Select[Movement](ctx, e, func(m *Movement, o *Order, from *goql.From) bool {
        from.Model = o
        m.Ref, m.Amount = o.Priority, o.Total
        return o.Total > 0
    })
    archived, _ := goql.Select[Movement](ctx, e, func(m *Movement, a *OrderArchive, from *goql.From) bool {
        from.Model = a
        m.Ref, m.Amount = a.Reason, a.Total
        return a.Total > 0
    })
    return goql.Union(live, archived)
})
```

Every branch is `[]*Movement`, so the compiler checks they have the same shape; goql adds
that they must fill the same fields. Each branch gets its own table aliases — so both may
read the same table — while placeholders stay numbered across the whole statement. Ordering
names a projected column, since there's no single model to resolve against.

`INTERSECT` and `EXCEPT` reach MySQL only in 8.0.31.

### Subqueries

A goql call written *inside* a lambda is parsed too, and compiles to a nested query. Name it
to reuse it, or nest it directly with `goql.Unwrap`:

```go
orders, err := goql.Select[Order](ctx, e, func(o *Order) bool {
    usa, _ := goql.Select[Customer](ctx, e, func(c *Customer) bool {
        return c.Country == "USA"
    })
    return goql.Condition(o.Customer, "IN", usa)
})
// → … WHERE o."customer_id" IN (SELECT c."id" FROM "customers" c WHERE c."country" = ?)
```

`Unwrap` exists only for the type checker: Go allows a two-value call as an argument only
when it is the *entire* argument list, so `goql.Unwrap(goql.Select[…](…))` is what makes
direct nesting legal Go. Nothing is executed either way.

`Select` projects the primary key; `goql.Fields` inside the nested lambda picks another
column. `Count` nests as a scalar, and `Exists` is a condition on its own — and may refer to
the outer row, which makes it correlated:

```go
buyers, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
    return goql.Unwrap(goql.Exists[Order](ctx, e, func(o *Order) bool {
        return o.Customer == c && o.Total > 1000
    }))
})
// → … WHERE EXISTS (SELECT 1 FROM "orders" o WHERE o."customer_id" = c."id" AND …)
```

Discard the nested error with `_`: nothing inside a lambda runs, so there is no error to
check there. Anything wrong with a subquery — an assigning sub-lambda, an alias collision —
is reported by the enclosing call, and an unknown field is a compile error as usual.

Only predicate-shaped calls nest — `Update`, `Insert` and `Delete` emit statements, not
values. A subquery over a table the enclosing query already uses is refused, since both
would render with the same alias.

### Operators Go has no syntax for

```go
goql.Select[Customer](ctx, e, func(c *Customer) bool {
    return goql.Condition(c.Name, "LIKE", "%smith%") && goql.Condition(c.Country, "NOT IN", "USA", "Canada")
})
```

Covers `LIKE`, `IN`, `IS NULL` and the plain comparisons, and combines with native
operators. The operator is checked against an allowlist while parsing. The left side may
also be a raw string, which is the escape hatch for engine-specific syntax such as JSON
paths — `goql.Condition("meta ->> 'key'", "=", "v")` — at the cost of portability.

### Raw SQL

`goql.Execute` runs a statement and returns the real `sql.Result`; `goql.Bind[T]` runs a
query and scans rows into models. Both join the surrounding transaction and honour the
call's context.

## Production builds

A released binary has no source to parse, so bodies are compiled ahead of time. Copy
[`tools/goqlc`](tools/goqlc/main.go) into your project, replace the model import with your
own, and put a directive next to your queries:

```go
//go:generate go run ./tools/goqlc .
```

Then `go generate ./...` and build with `-tags prod`. It is the *same parser*, run early —
the demo produces byte-identical output in both modes.

> Run `go generate` before every `-tags prod` build. Registry entries are keyed by the
> runtime's positional closure name, so adding or reordering a lambda shifts the keys and a
> stale registry can resolve to a different lambda's body.

## Migrations

goql diffs your models against the **live database** — there are no migration files, and
`goql_migrations` is an audit log of what was applied, not a ledger of files. Only tables
your models declare are inspected, so tables goql knows nothing about are never touched.

Anything ambiguous is asked, never guessed: a column that vanished while another appeared
is indistinguishable between a rename and a drop, and only you know which. Applying with a
question unanswered changes nothing.

Since only your own process has the models registered, the diff runs in-process and an
opt-in Unix socket lets a CLI drive the conversation:

```go
socket, _ := e.NewMigrateSocket(entities, goql.MigrateSocketConfig{
    Path:  "/run/goql-migrate.sock",
    Token: os.Getenv("GOQL_MIGRATE_TOKEN"),
})
go socket.Serve()
```

```bash
go run ./tools/goqlmigrate -socket /run/goql-migrate.sock -token "$GOQL_MIGRATE_TOKEN"
```

The socket can alter your schema, so it is deliberately awkward: off by default, a required
token with no default, Unix domain only at mode 0600, and a loud log line when it starts.
`e.MigrationPlan` / `e.Migrate` are the same flow without the socket.

## Status and limits

The suite is green (137 tests) and the dev and prod paths are verified to produce identical
output, but goql has not been used in production. Known limits:

- **Live coverage is SQLite-only.** The Postgres and MySQL dialects are covered by SQL-text
  assertions and written from documented behaviour; they have not run against a real server.
- **Migrations diff columns, column types and whole tables** — not indexes or constraints.
- **Soft delete is half-built**: `Deleted` exists, but `Delete` hard-deletes and reads do
  not filter on it.
- **Derived tables and CTEs** are not implemented.
- `goql.Model` embeds a mutex for change tracking, so `go vet`'s copylocks check flags
  passing entities by value.

## License

MIT — see [LICENSE](LICENSE).
