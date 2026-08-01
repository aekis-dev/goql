# Relations and preloading

## Relation fields come back nil

A `SELECT` returns a foreign key, not a row. goql leaves relation fields nil rather than
filling them with a stub, so "not loaded" stays distinguishable from "loaded but empty":

```go
orders, _ := goql.Select[Order](ctx, e, func(o *Order) bool { return o.Total > 100 })
orders[0].Customer   // nil
```

To get the key itself without loading the row, name the target's primary key — the FK column
already holds that value, so it costs no join:

```go
func(o *Order, p Key) bool { return o.Customer.ID == p.ID }
// → WHERE o."customer_id" = ?
```

## Preload

Ask for relations explicitly:

```go
orders, err := goql.Select[Order](ctx, e, func(o *Order, pre *goql.Preload) bool {
    pre.Fields = []string{"Customer", "Tags"}
    return o.Total > 500
})

orders[0].Customer.Name   // loaded
orders[0].Tags            // loaded
```

Or on a struct call:

```go
orders, err := goql.Search(ctx, e, Order{}, goql.Preload{Fields: []string{"Customer"}})
```

**Each relation costs a fixed number of batched queries regardless of how many rows came
back** — never one query per row. There is no N+1:

| Relation | Queries |
|---|---|
| many2one | 2 (keys, then rows) |
| one2many | 2 |
| many2many | 2 (join rows, then targets) |

Preloading is explicit because it keeps the cost visible. Eager-by-default would over-fetch,
and lazy proxies would require relation fields to stop being plain `*Customer` / `[]Tag`,
which the whole design rests on.

## Schema defaults

A field marked `Preload: true` loads on every read of that model:

```go
&models.Field{Name: "Customer", Column: "customer_id", Preload: true}
```

A query that names **any** relations replaces those defaults entirely:

```go
goql.Preload{Fields: []string{"Tags"}}   // Customer is NOT loaded, despite the default
goql.Preload{}                           // nothing is loaded
// (no Preload option at all)            // the model's defaults apply
```

The empty case is why "load nothing" is distinguishable from "not specified".

## Writing relations

`Create` persists relations given on the entity:

```go
goql.Create(ctx, e, []Order{{
    Total:    1500,
    Customer: customers[0],           // → customer_id
    Tags:     []Tag{*urgent, *vip},   // → two join rows
}})
```

`Write` **syncs** them — the listed rows are linked and any previously linked row that is no
longer listed is unlinked:

```go
order.Tags = []Tag{*urgent}      // vip is disassociated
goql.Write(ctx, e, []Order{*order})
```

For one2many, unlinking means clearing the child's foreign key. Where that column is
`NOT NULL` it cannot be cleared, so goql returns `ErrRelationConstraint` naming the column
instead of leaving a stale link or surfacing a driver-level constraint violation.

## Traversing relations in a predicate

Reaching through a many2one joins the target:

```go
func(o *Order) bool { return o.Customer.Country == "USA" }
```

Ranging over a collection joins it — including the join table for many2many:

```go
func(o *Order) bool {
    for _, t := range o.Tags {
        if t.Name == "urgent" {
            return true
        }
    }
    return false
}
```

Both directions work:

```go
func(c *Customer) bool {
    for _, o := range c.Orders {
        if o.Total > 1000 {
            return true
        }
    }
    return false
}
```

!!! note "Traversal is not preloading"
    A predicate that joins `customers` does **not** populate `o.Customer`. The join filters;
    preloading fills. Ask for both if you want both.

Field paths are limited to two segments — `o.Customer.Company.Name` is not supported.
