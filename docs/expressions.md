# Expressions

Arithmetic and string concatenation are available anywhere a value is. Nothing is stored: the
expression is rendered inline and the engine evaluates it per row.

## Operators

`+`  `-`  `*`  `/`  `%`

Grouping is preserved, because the Go parser already built the tree:

```go
return o.Total * (o.Total + 1) > 100
// → WHERE (o."total_amount" * (o."total_amount" + ?)) > ?
```

## Where they can appear

**In a predicate**, on either side:

```go
goql.Select[Order](ctx, e, func(o *Order) bool {
    return o.Total * 2 > 100
})
```

**In an `Update`:**

```go
goql.Update[Order](ctx, e, func(o *Order) {
    o.Total = o.Total * 1.1
})
// → UPDATE "orders" SET "total_amount" = ("orders"."total_amount" * ?)
```

**In an [`INSERT … SELECT`](insert-select.md):**

```go
goql.Insert[OrderArchive](ctx, e, func(a *OrderArchive, o *Order) {
    a.Total = o.Total * 100
})
```

**In a [projection](aggregates.md):**

```go
type Bucket struct {
    Band   float64
    Orders int64
}

goql.Select[Bucket](ctx, e, func(t *Bucket, o *Order, from *goql.From) bool {
    from.Model = o
    t.Band = o.Total / 1000
    t.Orders = goql.Count()
    return o.Total > 0
})
// → SELECT (o."total_amount" / ?) AS "Band", COUNT(*) AS "Orders" …
//   GROUP BY (o."total_amount" / ?)
```

A projected expression is a `GROUP BY` term, because SQL groups by the expression and not by
the alias.

A constant projects too — what a [recursive query](recursive.md) needs for its depth column:

```go
t.Depth = 0     // → ? AS "Depth"
```

## String concatenation

Go spells concatenation `+`, so which one you meant is decided from the operand types:

```go
t.Label = o.Priority + " / " + o.ShippingMethod
```

| Engine | Rendered as |
|---|---|
| SQLite | `((o."priority" \|\| ?) \|\| o."shipping_method")` |
| PostgreSQL | the same |
| MySQL | `CONCAT(CONCAT(o.`priority`, ?), o.`shipping_method`)` |

MySQL needs `CONCAT`: it reads `||` as logical OR unless `PIPES_AS_CONCAT` is set, and a bare
`+` **coerces both sides to numbers and answers 0** — silently wrong rather than an error,
which is why this is decided at parse time rather than swapped as a token.

!!! warning "Params and concatenation"
    A [params-struct](params.md) reference carries a name, not a type, so concatenating *two*
    params values reads as arithmetic. Write one side as a column or a literal and it
    resolves correctly.

Arithmetic over a text column (`o.Priority * 2`) is refused while parsing.

## What SQL does that Go does not

- **Integer division truncates** in both, so `/` agrees.
- **NULL propagates**: `prev.Depth + 1` over a NULL column is NULL, not 1. That is SQL, and
  goql does not paper over it.

## Not available

SQL functions — `COALESCE`, `LOWER`, `ABS`, date arithmetic. They need a marker vocabulary
and a portability decision of their own. Use [raw SQL](raw-sql.md) or a
[raw column](predicates.md#raw-columns) meanwhile.
