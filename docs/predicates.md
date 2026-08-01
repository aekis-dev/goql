# Predicates and conditions

A predicate is a lambda returning `bool`. It becomes the `WHERE` clause of a `Select`,
`Delete`, `Exists` or `Count`.

```go
orders, err := goql.Select[Order](ctx, e, func(o *Order) bool {
    return o.Priority == "High" && o.Total > 1000
})
// → SELECT o.* FROM "orders" o WHERE o."priority" = ? AND o."total_amount" > ?
```

## Operators

| Go | SQL |
|---|---|
| `==` `!=` | `=` `<>` |
| `<` `<=` `>` `>=` | the same |
| `&&` `\|\|` | `AND` `OR` |
| `+ - * / %` | [arithmetic](expressions.md) |

Parentheses are preserved, because the grouping is already in the tree the Go parser built:

```go
return (o.Total > 1000 || o.Priority == "Urgent") && o.ShippingMethod == "Express"
```

## `goql.Condition` — what Go has no syntax for

```go
func Condition(field any, op string, values ...any) bool
```

It is a parse-only marker, never executed, and combines with `&&`/`||` like any other term.

```go
goql.Select[Customer](ctx, e, func(c *Customer) bool {
    return goql.Condition(c.Name, "LIKE", "Ali%") &&
           goql.Condition(c.Country, "IN", "USA", "Canada") &&
           goql.Condition(c.Deleted, "IS NULL")
})
```

| Operator | Values | Notes |
|---|---|---|
| `=` `<>` `<` `<=` `>` `>=` | one | same as the Go operator |
| `LIKE` `NOT LIKE` | one | `%` and `_` are the engine's |
| `IN` `NOT IN` | one or more | or a [subquery](subqueries.md) |
| `IS NULL` `IS NOT NULL` | none | |

The operator is checked against an allowlist **with its arity** at parse time, so `"LIK"`,
`IS NULL` with a value, or `IN` with none all fail while parsing. An operator can only ever
be a literal in your own source, so this is about catching typos.

Values may come from a [params struct](params.md).

!!! tip "Negation"
    Go's `!` has no parser case yet. Negate with the operator: `NOT IN`, `NOT LIKE`,
    `IS NOT NULL`, or `<>`.

### Raw columns

A **string literal** in the field position is emitted verbatim — the escape hatch for
something goql cannot model, such as a JSON path. You own its correctness and its
portability across engines:

```go
goql.Condition(`o."meta"->>'tier'`, "=", "gold")
```

Anything else in that position is resolved as a field, so this only applies to a literal
written at the call site.

## Branching

An `if` chain compiles to mutually exclusive conditions. In a predicate, every arm that
returns `true` contributes, `OR`ed together:

```go
goql.Select[Customer](ctx, e, func(c *Customer) bool {
    if c.Country == "USA" {
        return true
    }
    if c.Age > 60 {
        return true
    }
    return false
})
// → WHERE country = ? OR (NOT (country = ?) AND age > ?)
```

Each arm carries the negation of the ones before it, so the arms cannot overlap.

A guard clause works as you would expect — the trailing `return true` carries the negation of
everything above it:

```go
func(c *Customer) bool {
    if c.Country == "USA" {
        return false
    }
    return true          // → NOT (country = ?)
}
```

`switch` works in both forms:

```go
switch c.Country {
case "USA", "Canada":     // values compared against the tag, ORed
    return true
default:                  // the negation of every case
    return false
}
```

```go
switch {                  // tagless: cases are boolean expressions
case c.Age > 60:
    return true
}
```

## Update

An `Update` lambda returns nothing and assigns instead. Each arm becomes its own `UPDATE`:

```go
rows, err := goql.Update[Customer](ctx, e, func(c *Customer) {
    if c.Age > 40 {
        c.Status = "Senior"
    } else {
        c.Status = "Premium"
    }
})
// → UPDATE customers SET status = 'Senior'  WHERE age > ?
//   UPDATE customers SET status = 'Premium' WHERE NOT (age > ?)
```

`rows` is the sum across statements, and they run in one transaction.

Assigning nothing at all is an error, not a no-op.

## Delete

```go
rows, err := goql.Delete[Order](ctx, e, func(o *Order) bool {
    return o.Priority == "Normal" && o.Total < 100
})
```

## Exists

```go
any, err := goql.Exists[Order](ctx, e, func(o *Order) bool {
    return o.Total > 10000
})
```

Rendered as `SELECT 1 … LIMIT 1` rather than `EXISTS(…)`, because "did a row come back" is
uniform across drivers while scanning a boolean is not.

## Counting

Counting is a [projection](aggregates.md), not a dedicated call:

```go
type Tally struct{ N int64 }

rows, err := goql.Select[Tally](ctx, e, func(t *Tally, o *Order, from *goql.From) bool {
    from.Model = o
    t.N = goql.Count()
    return o.Total > 100
})
// rows[0].N
```

When the query joins, `Count()` becomes `COUNT(DISTINCT pk)`, so a row matched through
several related rows is still counted once.

## Reaching into relations

Traversing a many2one joins the target table:

```go
func(o *Order) bool { return o.Customer.Country == "USA" }
// → INNER JOIN "customers" c ON o."customer_id" = c."id" WHERE c."country" = ?
```

Except when the path ends at the target's **primary key**, which the foreign key column
already holds — no join is needed:

```go
func(o *Order, p Key) bool { return o.Customer.ID == p.ID }
// → WHERE o."customer_id" = ?
```

Ranging over a one2many or many2many joins as well:

```go
func(o *Order) bool {
    for _, t := range o.Tags {
        if t.Name == "urgent" {
            return true
        }
    }
    return false
}
// → INNER JOIN "order_tags" … INNER JOIN "tags" t … WHERE t."name" = ?
```

See [Joins](joins.md) for models with no declared relation between them.
