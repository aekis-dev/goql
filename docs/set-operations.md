# Set operations

`UNION`, `UNION ALL`, `INTERSECT` and `EXCEPT` combine whole queries. Branches are bound to
names inside a lambda, and the lambda returns their combination.

```go
type Movement struct {
    Ref    string
    Amount float64
}

movements, err := goql.Select[Movement](ctx, e, func(m *Movement, sort *goql.Sort) bool {
    sort.By = "Amount"
    sort.Desc = true

    live, _ := goql.Select[Movement](ctx, e,
        func(m *Movement, o *Order, from *goql.From) bool {
            from.Model = o
            m.Ref = o.Priority
            m.Amount = o.Total
            return o.Total > 0
        })

    archived, _ := goql.Select[Movement](ctx, e,
        func(m *Movement, a *OrderArchive, from *goql.From) bool {
            from.Model = a
            m.Ref = a.Reason
            m.Amount = a.Total
            return a.Total > 0
        })

    return goql.Union(live, archived)
})
```

```sql
SELECT o."priority" AS "Ref", o."total_amount" AS "Amount" FROM "orders" o WHERE … > ?
UNION
SELECT a."reason" AS "Ref", a."total" AS "Amount" FROM "order_archives" a WHERE … > ?
ORDER BY "Amount" DESC
```

## The markers

```go
func Union[T any](branches ...[]*T) bool
func UnionAll[T any](branches ...[]*T) bool
func Intersect[T any](branches ...[]*T) bool
func Except[T any](branches ...[]*T) bool
```

Variadic, so more than two branches combine in one call. SQL evaluates left to right.

`Union` deduplicates; `UnionAll` does not and is cheaper.

## Why this shape

Three things fall out of binding branches to names rather than passing a slice of lambdas to
a top-level function:

- **The compiler checks branch compatibility.** `Union[T](branches ...[]*T) bool` forces one
  result type across every branch.
- **Options need no special case.** `Sort` and `Limit` are declared on the outer lambda and
  apply to the combination, because the outer lambda *is* the combination.
- **Binding a branch is the [subquery](subqueries.md) syntax already built.**

## What is checked

- **Every branch must yield the same columns.** The compiler enforces one result type; goql
  additionally rejects a branch that fills a different *subset* of its fields, which SQL would
  otherwise line up by position and answer silently.
- **`ORDER BY` names a projected column**, not a model field — there is no single model to
  resolve against. Naming a column no branch selects is an error.
- An aggregate call (`Exists`) as a branch is rejected: set operations combine rows.

## The outer lambda has no model

A combining lambda declares no model and no projection — its branches carry both. It only
declares the options that apply to the combination.

## Branches over the same table

Each branch gets **its own table aliases**, so a union of two queries over the same table
works:

```go
goql.Select[Movement](ctx, e, func(m *Movement) bool {
    high, _ := goql.Select[Movement](ctx, e, /* … Order … */)
    low, _  := goql.Select[Movement](ctx, e, /* … Order … */)
    return goql.UnionAll(high, low)
})
```

The placeholder counter is shared across branches, which is what keeps PostgreSQL numbering
correct (`> $1 … <= $2`).

## Engine support

`UNION` and `UNION ALL` are universal. `INTERSECT` and `EXCEPT` need **MySQL 8.0.31+** — on
an older server they fail at the database rather than at parse time.
