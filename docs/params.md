# Runtime values (params)

A lambda cannot use a variable from its surrounding scope — see
[Lambdas](lambdas.md#consequence-1-no-captured-variables). Values known only at the call site
travel through a **params struct**.

## The shape

Declare one extra parameter that is a plain struct, and pass its value as a trailing argument:

```go
type OrderParams struct {
    MinTotal float64
    Priority string
}

orders, err := goql.Select[Order](ctx, e, func(o *Order, p OrderParams) bool {
    return o.Total > p.MinTotal && o.Priority == p.Priority
}, OrderParams{MinTotal: 100, Priority: "High"})
```

```sql
SELECT o.* FROM "orders" o WHERE o."total_amount" > ? AND o."priority" = ?
```

The field references compile to placeholders, substituted with the real values just before
execution. The parsed body stays reusable and cacheable, which is the whole point: a body is
parsed once and — in production — compiled ahead of time, so a call-time value can never be
baked into it.

## Why a struct rather than a marker or a map

| Alternative | Why not |
|---|---|
| read the closure's captured values | impossible — `reflect` does not expose free variables |
| resolve them from the caller's source | only ever works when the value is a literal |
| `goql.P(x)` marker | more API for no gain over a plain trailing value |
| `goql.Args{"min": x}` map | stringly-typed keys, checked at runtime instead of by the compiler |

With a struct, **the Go compiler checks the field names**, because the lambda is ordinary Go
and the struct is a real type. goql's own check only has to reject what compiles but
reflection cannot read: an unexported field.

## Rules

- **At most one** params struct per lambda.
- It is passed **by value**, which is what distinguishes it from other extra parameters — an
  option carrier is `*goql.Sort`, a join participant is `*Model`, and a
  [CTE row handle](cte.md) is `*Row`.
- Declared but not supplied → `ErrMissingParams`. Supplied but not declared, or the wrong
  type → `ErrInvalidParams`.
- Position does not matter: extra parameters are classified by type.

```go
// all equivalent
func(o *Order, p OrderParams, sort *goql.Sort) bool
func(o *Order, sort *goql.Sort, p OrderParams) bool
```

## Where params values can appear

Anywhere a literal can — a comparison, a list operator, an `Update` assignment, an
`INSERT … SELECT` value, an [expression](expressions.md), or inside a
[subquery](subqueries.md).

```go
type Bump struct {
    Priority string
    Factor   float64
}

goql.Update[Order](ctx, e, func(o *Order, p Bump) {
    if o.Priority == p.Priority {
        o.Total = o.Total * p.Factor
    }
}, Bump{Priority: "High", Factor: 1.1})
```

With `goql.Condition`:

```go
type Countries struct{ A, B string }

goql.Select[Customer](ctx, e, func(c *Customer, p Countries) bool {
    return goql.Condition(c.Country, "IN", p.A, p.B)
}, Countries{A: "USA", B: "Canada"})
```

## Known gap

A [JSON column](models.md#json-columns) cannot be assigned from a params struct — the value is
not available at build time to marshal, so it is refused with a clear error rather than
written wrongly.
