# Struct calls (by example)

Four calls describe rows with values rather than predicates: `Create`, `Search`, `Write`,
`Remove`. They are separate from the lambda half so that per-query options attach
unambiguously.

## Create

```go
customers, err := goql.Create(ctx, e, []Customer{
    {Name: "Alice", Age: 40, Country: "USA"},
    {Name: "Bob", Age: 41, Country: "Canada"},
})
```

Returns `[]*Customer` with generated primary keys filled in. **The returned pointers alias
the slice you passed**, so the keys are visible through either.

Relations are persisted too:

```go
orders, err := goql.Create(ctx, e, []Order{{
    Total:    1500,
    Customer: customers[0],              // many2one → customer_id
    Tags:     []Tag{*tag1, *tag2},       // many2many → join rows
}})
```

On PostgreSQL the key comes back with `INSERT … RETURNING`; elsewhere via `LastInsertId`.

!!! note "Zero values"
    A zero-valued nullable field is left out of the `INSERT`, so the column default applies.
    Declare the field `NotNull`, or use a pointer, when `false`/`0`/`""` must be stored
    explicitly.

## Search

Non-zero fields become the `WHERE` clause — exact matches, combined with `AND`:

```go
usa, err := goql.Search(ctx, e, Customer{Country: "USA"})
// → WHERE "customers"."country" = ?

alice, err := goql.Search(ctx, e, Customer{Country: "USA", Name: "Alice"})
// → WHERE "customers"."country" = ? AND "customers"."name" = ?
```

Search by primary key alone:

```go
one, err := goql.Search(ctx, e, Customer{Model: goql.Model{ID: 42}})
```

Several examples produce an `IN` per column:

```go
some, err := goql.Search(ctx, e, Customer{Country: "USA"}, Customer{Country: "Canada"})
```

Matching is always exact. Pattern matching belongs to the predicate language — see
[`goql.Condition`](predicates.md).

[Options](options.md) are trailing values:

```go
page, err := goql.Search(ctx, e, Customer{Country: "USA"},
    goql.Sort{By: "Age", Desc: true},
    goql.Limit{Value: 20},
    goql.Offset{Value: 40},
    goql.Preload{Fields: []string{"Orders"}},
)
```

## Write

Persists **only what changed** on entities that were loaded by goql:

```go
alice := customers[0]
alice.Country = "Canada"

rows, err := goql.Write(ctx, e, []Customer{*alice})
// → UPDATE "customers" SET "country" = ?, "goql_updated" = ? WHERE "id" = ?
```

Change tracking is per entity, initialised when a row is scanned. An entity you built by
hand has nothing to diff against, so prefer `Create` for new rows and
[`Update`](predicates.md#update) for set-based edits.

Relations are synced too:

```go
order.Tags = []Tag{*urgent}     // drops any tag not listed, adds the ones that are
goql.Write(ctx, e, []Order{*order})
```

For one2many, rows no longer listed have their foreign key cleared. Where that column is
`NOT NULL` it cannot be cleared, so goql returns `ErrRelationConstraint` naming the column
rather than leaving a stale link or surfacing a driver error.

## Remove

Deletes by primary key:

```go
rows, err := goql.Remove(ctx, e, []Tag{*tag3})
```

This is a hard delete. `Deleted` exists on `goql.Model` but is not yet wired up — see
[Limitations](limitations.md).

## When to use which half

| Use a struct call when | Use a lambda when |
|---|---|
| you have the row in hand | you are describing a set of rows |
| the criteria are exact values | you need `>`, `LIKE`, `IN`, `OR`, joins |
| you want per-row change tracking | you want one statement to affect many rows |

They compose: `Search` to load, mutate in Go, `Write` to persist.
