# INSERT … SELECT

`Insert` builds rows in the database from rows already there. Nothing is loaded into Go.

```go
archived, err := goql.Insert[OrderArchive](ctx, e,
    func(a *OrderArchive, o *Order) {
        if o.Total > 1000 {
            a.Total  = o.Total
            a.Reason = "high value"
            a.Origin = o.ShippingMethod
        }
    })
```

```sql
INSERT INTO "order_archives" ("total", "reason", "origin", "goql_created", "goql_updated")
SELECT o."total_amount", ?, o."shipping_method", CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
FROM "orders" o WHERE o."total_amount" > ?
```

Returns a **row count**, not entities: recovering generated keys is only portable where
`INSERT … RETURNING` exists.

## Destination first, source second

The first lambda parameter is the destination, the second the source. Only the destination is
a type parameter — `Insert[OrderArchive]` reads as "insert into OrderArchive", and the source
is whichever model the lambda declares second.

Each assignment supplies **both halves of the statement at once**: the left names a
destination column, the right an expression selected from the source.

| Right-hand side | Becomes |
|---|---|
| a source field | a selected column |
| a literal | a bound constant, selected for every row |
| a [params](params.md) value | the same, from the call site |
| an [expression](expressions.md) | `o."price" * o."qty"` |

## Conditions and branches

A condition filters the `SELECT`. An `if`/`else` or `switch` emits one statement per arm, as
[`Update`](predicates.md#update) does, and the returned count is their sum:

```go
goql.Insert[OrderArchive](ctx, e, func(a *OrderArchive, o *Order) {
    if o.Total > 1000 {
        a.Total, a.Reason = o.Total, "high value"
    } else if o.Priority == "Urgent" {
        a.Total, a.Reason = o.Total, "urgent"
    }
})
```

Reaching through a relation joins, exactly as in a predicate:

```go
if o.Customer.Country == "USA" {
    a.Total, a.Reason = o.Total, "domestic"
}
```

## Timestamps

The destination's `AutoCreateTime` / `AutoUpdateTime` columns are filled with
`CURRENT_TIMESTAMP`, since no row is ever built in Go for the hooks to touch. An explicit
assignment to the same column wins.

## `goql.Conflict`

```go
goql.Insert[OrderArchive](ctx, e, func(a *OrderArchive, o *Order, c *goql.Conflict) {
    c.Ignore = true
    a.Total = o.Total
})
```

| Engine | Rendered as |
|---|---|
| SQLite | `INSERT OR IGNORE INTO …` |
| MySQL | `INSERT IGNORE INTO …` |
| PostgreSQL | `INSERT INTO … ON CONFLICT DO NOTHING` |

Only `Ignore`. The name is deliberately narrow: a full upsert (a conflict target plus
`DO UPDATE SET`) is a design of its own, and calling this `OnConflict` would half-claim it.
Passing `Conflict` to any other call is an error.

## Rules

- **Assigning to the source is an error**, not a silent no-op — `o.Priority = "x"` inside an
  Insert lambda describes a mutation no `INSERT … SELECT` performs.
- **Reading the destination is an error** too: it has no rows yet.
- The second parameter must be a **registered model**. Passing an option carrier or a plain
  struct there is an error, not a silent reinterpretation.
- A swapped signature is caught: the first parameter must match the type argument.
- **Relation assignments are refused.** Linking needs the primary key of a row that does not
  exist yet, and `INSERT … SELECT` does not report the keys it generated.
- `Sort`, `Limit` and `Offset` apply to the `SELECT`. `Fields` and `Preload` are refused —
  the projection comes from the assignments, and there is no result to load into.

## Copying a model into itself

Written with two parameters of the same type, destination first:

```go
goql.Insert[Order](ctx, e, func(dst *Order, src *Order) {
    if src.Priority == "High" {
        dst.Total    = src.Total
        dst.Priority = "Urgent"
        dst.Customer = src.Customer
    }
})
```
