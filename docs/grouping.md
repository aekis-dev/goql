# Grouping and HAVING

## Derived grouping

In a [projection](aggregates.md), the assignments that are not aggregates are the grouping
keys. Nothing to declare:

```go
t.Priority = o.Priority        // → GROUP BY o."priority"
t.Total    = goql.Sum(o.Total)
```

## `goql.Group` — extra keys

`Group.By` names **additional** grouping keys as Go field names. The final list is the named
keys followed by any projected column not already among them.

```go
type CustomerSpend struct {
    Spend float64
}

big, err := goql.Select[CustomerSpend](ctx, e,
    func(c *CustomerSpend, o *Order, from *goql.From, g *goql.Group) bool {
        from.Model = o
        g.By = []string{"Customer"}
        c.Spend = goql.Sum(o.Total)
        return goql.Sum(o.Total) > 1000
    })
```

```sql
SELECT SUM(o."total_amount") AS "Spend" FROM "orders" o
GROUP BY o."customer_id" HAVING SUM(o."total_amount") > ?
```

Two reasons to name a key rather than project it:

- **You cannot project it.** A many2one is a `*Customer` in Go, so `t.Customer = o.Customer`
  will not compile against an `int64` result field.
- **You do not want it in the result.** Grouping by a column you are not selecting.

It is **additive**, not authoritative: projecting a column always groups by it, so the two
cannot contradict each other. Naming keys while aggregating nothing is an error — it would
emit a `GROUP BY` with nothing to fold.

## HAVING

A comparison whose left side is an **aggregate** filters groups rather than rows. One
predicate produces both clauses:

```go
return o.Total > 100 && goql.Sum(o.Total) > 1000
//     └── WHERE          └── HAVING
```

```sql
… WHERE o."total_amount" > $1 GROUP BY … HAVING SUM(o."total_amount") > $2
```

goql walks the condition tree and separates the aggregate leaves from the row filters.
Placeholders stay in emission order: the `WHERE` value binds before the `HAVING` one.

### What is refused

- **An aggregate combined with `||`, or negated.** A pre-aggregation filter cannot be `OR`ed
  with a post-aggregation one — there is no SQL equivalent, so it is a parse error naming the
  combination rather than a guess.
- **An aggregate on the right of a comparison.** `1000 < goql.Sum(o.Total)` is refused with a
  message to put it on the left, so a condition always reads as the group being filtered.

## Ordering by an aggregate

Sort by the **result field**, since every projected column is aliased:

```go
func(t *PriorityTotals, o *Order, from *goql.From, sort *goql.Sort) bool {
    from.Model = o
    t.Priority = o.Priority
    t.Total    = goql.Sum(o.Total)
    sort.By    = "Total"
    sort.Desc  = true
    return o.Total > 0
}
```
