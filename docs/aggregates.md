# Aggregates and projections

A result type that is **not a model** turns the lambda into a projection: each assignment is
one output column.

```go
type PriorityTotals struct {
    Priority string
    Orders   int64
    Total    float64
    Largest  float64
}

rows, err := goql.Select[PriorityTotals](ctx, e,
    func(t *PriorityTotals, o *Order, from *goql.From) bool {
        from.Model = o
        t.Priority = o.Priority          // a plain column → also a GROUP BY term
        t.Orders   = goql.Count()        // COUNT(*)
        t.Total    = goql.Sum(o.Total)   // SUM(total_amount)
        t.Largest  = goql.Max(o.Total)
        return o.Total > 0
    })
```

```sql
SELECT o."priority" AS "Priority", COUNT(*) AS "Orders",
       SUM(o."total_amount") AS "Total", MAX(o."total_amount") AS "Largest"
FROM "orders" o WHERE o."total_amount" > ? GROUP BY o."priority"
```

`rows` is `[]*PriorityTotals`.

## The model is stated, not inferred

The result type is not a model, so the query needs to be told what to read from:

```go
from.Model = o
```

`o` is one of the lambda's **own model parameters** — pointing at the declaration rather than
restating it, so the two cannot disagree. Anything else is an error, and a projection that
never says is an error too.

(The rejected alternative was "the first model parameter is the main one", which is an
obscure convention to have to know.)

## The aggregate functions

```go
func Sum[T any](column T) T
func Avg[T any](column T) float64
func Min[T any](column T) T
func Max[T any](column T) T
func Count(column ...any) int64
```

Parse-only markers, never executed. They are package-level generic **functions** rather than
methods on a carrier for a concrete reason: `Min` and `Max` must return what they were given —
the earliest timestamp is a timestamp — and Go forbids type parameters on methods.

`Sum[T](T) T` also answers the decimal question quietly: the result is whatever type the
model already declares.

### The compiler does much of the checking

```go
t.Total = goql.Sum(o.Priority)   // ✗ does not compile
```

`Sum[string]` returns `string`, which will not assign to a `float64` field. What compiles but
is still wrong — summing text into a string field — is caught while parsing, because SQLite
quietly answers `0` there while PostgreSQL errors.

A field that does not exist on the result type is a compile error.

## GROUP BY is derived

The assignments that are **not** aggregates are the grouping keys. That is SQL's own rule, so
the grouping cannot disagree with the projection.

```go
t.Priority = o.Priority      // → GROUP BY o."priority"
t.Total    = goql.Sum(...)   // aggregated
```

**No plain assignment means no grouping** — the whole matched set folds into one row:

```go
type Summary struct {
    Orders int64
    Total  float64
}

rows, _ := goql.Select[Summary](ctx, e, func(s *Summary, o *Order, from *goql.From) bool {
    from.Model = o
    s.Orders = goql.Count()
    s.Total  = goql.Sum(o.Total)
    return o.Total > 0
})
// rows[0].Total — one row, the whole set
```

To group by something you cannot project — a relation, say — name it explicitly. See
[Grouping and HAVING](grouping.md).

## Counting over a join

A join multiplies rows, so `goql.Count()` renders `COUNT(DISTINCT pk)` whenever the query
joins — through a relation, a model participant or an explicit join. An entity matched by two
related rows is counted once.

## Every column is aliased

Each output column is aliased to the field it lands in, so scanning never depends on column
order. That is also what makes [set operations](set-operations.md) and [CTEs](cte.md) able to
refer to a query's columns by name.

## Aggregating an aggregate

`AVG(SUM(...))` is not legal SQL. The average of per-customer totals is two queries stacked,
which is what a [CTE](cte.md) is for:

```go
goql.Select[Summary](ctx, e, func(s *Summary, t *CustomerTotal, from *goql.From) bool {
    totals, _ := goql.Select[CustomerTotal](ctx, e, /* … GROUP BY customer … */)
    from.Query = totals
    from.Model = t
    s.Average = goql.Avg(t.Total)
    return t.Total > 0
})
```

## Not available

`DISTINCT`, other than the join rule above. Window functions. Aggregate `FILTER` clauses.
