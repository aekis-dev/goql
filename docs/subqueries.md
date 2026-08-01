# Subqueries

A goql call written **inside** a lambda body is parsed too, and becomes a nested query.

Composition belongs inside the lambda, because outside it a query is a value — and reaching a
value from inside a body would mean capturing a variable, which is
[the one thing the design forbids](lambdas.md#consequence-1-no-captured-variables).

## Two spellings

**Named**, which makes it reusable within the body:

```go
usOrders, err := goql.Select[Order](ctx, e, func(o *Order) bool {
    usa, _ := goql.Select[Customer](ctx, e, func(c *Customer) bool {
        return c.Country == "USA"
    })
    return goql.Condition(o.Customer, "IN", usa)
})
```

```sql
SELECT o.* FROM "orders" o
WHERE o."customer_id" IN (SELECT c."id" FROM "customers" c WHERE c."country" = ?)
```

**Nested directly**, with `goql.Unwrap`:

```go
return goql.Condition(o.Customer, "IN",
    goql.Unwrap(goql.Select[Customer](ctx, e, func(c *Customer) bool {
        return c.Country == "USA"
    })))
```

!!! info "Why `Unwrap` exists"
    Go rejects a two-value call as one argument among others
    (`multiple-value … in single-value context`), and `Select` must return `([]*T, error)`.
    Passing a call as a function's *entire* argument list is the one exception, so
    `Unwrap[T](T, error) T` is what makes direct nesting legal Go. It is never executed
    inside a lambda.

## The bound error

Go forces a bound name to be used, and the only honest use is discarding it:

```go
usa, _ := goql.Select[Customer](ctx, e, …)     // ✓
```

```go
usa, err := goql.Select[Customer](ctx, e, …)
if err != nil { … }                            // ✗ reported clearly
```

A nested call is never executed, so it has no error to inspect. **Every subquery failure is
reported by the enclosing call**, which is the only thing that runs.

## What projection the subquery yields

By default the primary key — which is what an `IN` wants. Name a different column with
`goql.Fields` **inside the nested lambda**:

```go
refs, _ := goql.Select[Payment](ctx, e, func(p *Payment, f *goql.Fields) bool {
    f.Names = []string{"Ref"}
    return p.Method == "card"
})
return goql.Condition(i.Ref, "IN", refs)
```

More than one field is an error: a subquery yields one column.

## Correlation

A nested predicate may name the enclosing model — the subquery is evaluated per outer row:

```go
bigBuyers, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
    return goql.Unwrap(goql.Exists[Order](ctx, e, func(o *Order) bool {
        return o.Customer == c && o.Total > 1000
    }))
})
```

```sql
SELECT c.* FROM "customers" c
WHERE EXISTS (SELECT 1 FROM "orders" o WHERE o."customer_id" = c."id" AND o."total_amount" > ?)
```

`o.Customer == c` compares the foreign key against the outer model's primary key. An
inherited model is *not* added to the subquery's own `FROM` list — it is already in the
enclosing statement.

!!! warning "Correlation and CTEs"
    A correlated subquery works as an `IN`/`EXISTS` value. It can **never** become a
    [CTE](cte.md), which is evaluated before the outer query has produced a row.

## Which functions can nest

`Select` and `Exists`. `Update`, `Delete` and `Insert` emit one statement per branch, so they
are not values — and a nested lambda that *assigns* is refused.

## Where failures surface

| Stage | Example | Reported by |
|---|---|---|
| compile time | an unknown field in the nested lambda | the Go compiler |
| parse time | an assigning sub-lambda, more than one projected field | the enclosing call |
| build time | an alias collision, an unregistered table | the enclosing call |

## Refused rather than approximated

- **A subquery over a table the enclosing query already uses** — both would render with the
  same alias. Self-joins need per-occurrence aliases, which the alias map does not yet model.
- **A nested `Exists` inside `Condition`**, or a nested `Select` standing alone: each is told
  which position it belongs in.
