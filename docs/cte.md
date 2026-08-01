# Common table expressions

A query can read from **another query** instead of a table. That is a `WITH` clause, and it is
what makes an aggregate over an aggregate expressible.

## The shape

```go
type CustomerTotal struct {
    Customer int64
    Total    float64
}

type Summary struct {
    Average float64
    Biggest float64
}

rows, err := goql.Select[Summary](ctx, e,
    func(s *Summary, t *CustomerTotal, from *goql.From) bool {

        totals, _ := goql.Select[CustomerTotal](ctx, e,
            func(ct *CustomerTotal, o *Order, f *goql.From, g *goql.Group) bool {
                f.Model = o
                g.By = []string{"Customer"}
                ct.Total = goql.Sum(o.Total)
                return o.Total > 0
            })

        from.Query = totals     // ← read FROM the named query
        from.Model = t          // ← t stands for one of its rows

        s.Average = goql.Avg(t.Total)
        s.Biggest = goql.Max(t.Total)
        return t.Total > 0
    })
```

```sql
WITH "totals" AS (
    SELECT o."customer_id", SUM(o."total_amount") AS "Total"
    FROM "orders" o WHERE o."total_amount" > ? GROUP BY o."customer_id")
SELECT AVG(t."Total") AS "Average", MAX(t."Total") AS "Biggest"
FROM "totals" t WHERE t."Total" > ?
```

`AVG(SUM(...))` is not legal SQL. This is how you spell it.

## The two assignments

| | |
|---|---|
| `from.Query = totals` | **what** to read from — a query bound earlier in this lambda |
| `from.Model = t` | the parameter standing for **one row** of it |

The CTE is named after the **Go variable** it was bound to, so the name is stated once and
cannot drift.

The row handle is a pointer to the defining query's result type (`t *CustomerTotal`).
Pairing it with `Query` explicitly is what lets two CTEs share a result type without
ambiguity — nothing is inferred from the type alone.

## The columns are the projection

A CTE presents exactly the columns its defining query projects, under their `Into` names. So
`t.Total` is checked **while parsing**:

```text
field not found: totals does not select Customer — it selects Total
```

Nothing is registered globally: a CTE cannot be referenced outside the statement that defines
it.

## Requirements and refusals

**The defining query must project.** A query selecting whole model rows has no named columns
to read:

```go
rows, _ := goql.Select[Order](ctx, e, func(o *Order) bool { return o.Total > 0 })
from.Query = rows      // ✗ "selects whole Order rows, so it has no named columns to read"
```

**It cannot be correlated.** A CTE is evaluated *before* the outer query produces a row, so
referencing the outer model is refused:

```go
totals, _ := goql.Select[CustomerTotal](ctx, e, func(ct *CustomerTotal, x *Order, f *goql.From) bool {
    f.Model = x
    ct.Total = x.Total
    return x.Total > o.Total     // ✗ o belongs to the enclosing query
})
```

That is exactly the difference from a [subquery](subqueries.md) used as a value: `IN` and
`EXISTS` *may* correlate; a `WITH` may not. Join the tables instead.

## Joining a CTE

`goql.Join` takes `Query` as well as `Model`:

```go
func(s *Summary, t *CustomerTotal, o *Order, from *goql.From, j *goql.Join) bool {
    totals, _ := goql.Select[CustomerTotal](ctx, e, …)
    from.Model = o
    j.Query = totals
    j.Model = t
    j.On = o.Customer.ID == t.Customer
    …
}
```

## Engine support

`WITH` is available on SQLite 3.8.3+, every PostgreSQL, and MySQL 8.0+.

Where an engine has no CTEs, goql falls back to an **inline derived table**:

```sql
SELECT AVG(t."Total") … FROM (SELECT … GROUP BY …) t
```

Same result. A definition used more than once is repeated, which is a worse plan but beats a
query that works on PostgreSQL and fails on MySQL 5.7.

!!! warning "No fallback for recursion"
    A derived table cannot reference itself, so a [recursive query](recursive.md) on an engine
    without `WITH` is refused outright rather than degraded.

## What is not done

A subquery used purely as a **value** (`IN (…)`) still renders inline rather than becoming a
`WITH`. The Go source is identical either way, so it was never a syntax question — only a
rendering one — and it was deliberately dropped.
