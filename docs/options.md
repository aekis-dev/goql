# Query options

The same types serve both halves of the API: **trailing values** on a struct call, **declared
parameters** on a lambda.

```go
goql.Search(ctx, e, Customer{Country: "USA"},
    goql.Sort{By: "Age", Desc: true}, goql.Limit{Value: 20})

goql.Select[Customer](ctx, e, func(c *Customer, sort *goql.Sort, limit *goql.Limit) bool {
    sort.By = "Age"
    sort.Desc = true
    limit.Value = 20
    return c.Country == "USA"
})
```

On the lambda side they are **parsed, not executed**, like everything else in a body.
Parameters are classified **by type**, so their order does not matter.

!!! info "Why the two forms differ"
    An option is fixed when the query is written and has no value to hand over, so it is
    assigned inside the body. A [params struct](params.md) carries runtime data and must be
    passed at the call site. Different data flow, not an inconsistency.

## The carriers

| Type | Fields | Applies to |
|---|---|---|
| `Sort` | `By string`, `Desc bool` | reads |
| `Limit` | `Value int` | reads |
| `Offset` | `Value int` | reads |
| `Fields` | `Names []string` | reads |
| `Preload` | `Fields []string` | reads |
| `Group` | `By []string` | [projections](grouping.md) |
| `From` | `Model any`, `Query any` | [projections](aggregates.md), [CTEs](cte.md) |
| `Join` | `Model`, `Query`, `On`, `Type` | [joins](joins.md) |
| `Conflict` | `Ignore bool` | [`Insert`](insert-select.md) |

## Sort

`By` is a **Go field name**, resolved against the schema — a typo is an error, not invalid
SQL.

```go
goql.Search(ctx, e, Customer{}, goql.Sort{By: "Age", Desc: true})
```

Declare several `*Sort` parameters to sort by several columns; they apply in declaration
order:

```go
func(c *Customer, first *goql.Sort, second *goql.Sort) bool {
    first.By = "Country"
    second.By = "Age"
    second.Desc = true
    return c.Age > 18
}
// → ORDER BY c."country", c."age" DESC
```

## Limit and Offset

```go
goql.Search(ctx, e, Customer{}, goql.Limit{Value: 20}, goql.Offset{Value: 40})
```

Every supported engine requires a limit before an offset, so an `Offset` on its own emits an
open-ended limit — `LIMIT -1` on SQLite, `LIMIT ALL` on PostgreSQL, `LIMIT
18446744073709551615` on MySQL.

## Fields

Restricts the projection. The primary key is always included, so scanned rows stay
identifiable:

```go
goql.Search(ctx, e, Customer{Country: "USA"}, goql.Fields{Names: []string{"Name"}})
// → SELECT "customers"."id", "customers"."name" FROM "customers" WHERE …
```

Unselected fields come back as Go zero values — indistinguishable from a column that is
genuinely empty. That is an accepted trade.

## Preload

See [Relations and preloading](relations.md).

```go
goql.Select[Order](ctx, e, func(o *Order, pre *goql.Preload) bool {
    pre.Fields = []string{"Customer", "Tags"}
    return o.Total > 500
})
```

An empty `goql.Preload{}` means "load nothing", which is distinct from not passing one at
all — a query that names any relations **replaces** the model's `Preload: true` defaults
entirely.

## Rules and refusals

Options are **refused where they mean nothing**, never ignored:

- `Sort`, `Limit`, `Offset` and `Preload` on `Exists`, which returns one value.
- `Fields` and `Preload` on [`Insert`](insert-select.md) — the projection comes from the
  assignments, and there is no result to load into.
- `Conflict` on anything but `Insert`.
- Any option **set inside a branch** of an `if`/`switch`. An option describes the whole
  query; silently applying a branch's option to every row would be wrong.

A carrier declared but never assigned is fine, except for `Join`, which is reported — a join
that was declared and not described is an intent that was not carried out.
