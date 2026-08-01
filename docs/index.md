# goql

An ORM for Go where queries are written as ordinary Go functions.

```go
orders, err := goql.Select[Order](ctx, e, func(o *Order) bool {
    return o.Customer.Country == "USA" && o.Total > 1000
})
```

That generates a `SELECT` with a join on `customers`. Results come back as `[]*Order` — no
casts, no string DSL, and a typo in a field name is a compile error.

## The one thing to know

**Lambda bodies are inspected, not executed.**

The function above is never called. goql locates its source, parses it with `go/ast`, and
compiles the statements into SQL. This is clearest in an update:

```go
rows, err := goql.Update[Order](ctx, e, func(o *Order) {
    if o.Total > 1000 {
        o.Priority = "High"
    } else {
        o.Priority = "Normal"
    }
})
```

Nothing is mutated in Go. The assignments describe `SET` clauses, the `if` describes `WHERE`,
and each arm becomes its own `UPDATE` with mutually exclusive conditions.

Everything surprising about goql follows from that one fact. Read
[Lambdas are parsed, not executed](lambdas.md) before anything else — it explains why there is
a params struct, why `goql.Condition` exists, and why a lambda cannot use a variable from the
surrounding scope.

## What it can express

| | |
|---|---|
| [Predicates](predicates.md) | comparisons, `&&`/`||`, `if`/`else`, `switch`, `LIKE`, `IN`, `IS NULL` |
| [Relations](relations.md) | traversal (`o.Customer.Country`), batched preloading, no N+1 |
| [Joins](joins.md) | implicit between declared models, or explicit with `INNER`/`LEFT`/`RIGHT`/`FULL` |
| [Aggregates](aggregates.md) | `SUM`/`AVG`/`MIN`/`MAX`/`COUNT` into a result type of your own |
| [Grouping](grouping.md) | `GROUP BY` derived from the projection, `HAVING` split from `WHERE` |
| [Subqueries](subqueries.md) | a goql call written inside a lambda, correlated or not |
| [Set operations](set-operations.md) | `UNION`, `UNION ALL`, `INTERSECT`, `EXCEPT` |
| [CTEs](cte.md) | read from a named query, including [recursive](recursive.md) hierarchy walks |
| [Expressions](expressions.md) | arithmetic and string concatenation in any value position |
| [INSERT … SELECT](insert-select.md) | build rows from rows already in the database |
| [Migrations](migrations.md) | live introspection, ambiguity resolved by asking |

Portable across **SQLite**, **PostgreSQL** and **MySQL** — see
[Dialects](dialects.md) for what differs and what each engine refuses.

## Install

```bash
go get github.com/aekis-dev/goql
```

Start with [Getting started](getting-started.md), then read
[Lambdas](lambdas.md).

## Honest status

goql is usable but young. Live test coverage is SQLite-only: the Postgres and MySQL SQL is
generated and asserted per dialect, but has not been run against those servers. Soft delete is
half-built, migrations do not diff indexes or constraints, and `go vet`'s `copylocks` check
fires on passing entities by value. The full list is in [Limitations](limitations.md), kept
deliberately blunt.
