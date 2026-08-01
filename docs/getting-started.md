# Getting started

## Install

```bash
go get github.com/aekis-dev/goql
```

You also need a driver. The examples here use SQLite:

```bash
go get github.com/mattn/go-sqlite3
```

## Declare a model

Schema metadata is declared imperatively in `init()`, not in struct tags — tags stay free for
serialization, and a field definition can use a constant or a helper.

```go
package models

import (
    "github.com/aekis-dev/goql"
    "github.com/aekis-dev/goql/models"
)

type Customer struct {
    goql.Model // ID, Created, Updated, Deleted + change tracking
    Name    string
    Age     int
    Country string
    Orders  []Order
}

func init() {
    err := models.AddModel(&Customer{}, "customers",
        &models.Field{Name: "Name", Type: models.TypeVarchar, Size: 100, NotNull: true, Index: true},
        &models.Field{Name: "Age", Checks: []string{"age >= 0"}},
        &models.Field{Name: "Country"},
        &models.Field{Name: "Orders", OneToMany: &models.OneToMany{Ref: "customer_id"}},
    )
    if err != nil {
        panic(err)
    }
}
```

```go
type Order struct {
    goql.Model
    Total    float64
    Priority string
    Customer *Customer
    Tags     []Tag
}

func init() {
    err := models.AddModel(&Order{}, "orders",
        &models.Field{Name: "Total", Column: "total_amount", Type: models.TypeDecimal,
            Precision: 10, Scale: 2, NotNull: true},
        &models.Field{Name: "Priority", Type: models.TypeVarchar, Size: 20, Default: "Normal"},
        &models.Field{Name: "Customer", Column: "customer_id", NotNull: true},
        &models.Field{Name: "Tags", ManyToMany: &models.ManyToMany{
            Table: "order_tags", Column: "order_id", Ref: "tag_id"}},
    )
    if err != nil {
        panic(err)
    }
}
```

!!! warning "The package must be imported"
    `AddModel` runs in `init()`. A model only exists once the package declaring it has been
    imported — otherwise you get `ErrNotRegistered`, whose message says exactly this.

See [Declaring models](models.md) for the full field vocabulary.

## Open an engine

```go
db, err := sql.Open("sqlite3", "app.db")
if err != nil {
    log.Fatal(err)
}
defer db.Close()

e := goql.New(db, goql.SQLite{})   // or goql.Postgres{} / goql.MySQL{}
```

The dialect is named explicitly because `database/sql` exposes no driver name; guessing would
mean a type switch that breaks on wrapped drivers.

```go
if err := e.EnableForeignKeys(); err != nil {   // no-op where enforcement is always on
    log.Fatal(err)
}
if err := e.CreateTables(&models.Customer{}, &models.Order{}); err != nil {
    log.Fatal(err)
}
```

`CreateTables` is fine for tests and demos. For a schema that evolves, see
[Migrations](migrations.md).

## Write and read

```go
ctx := context.Background()

customers, err := goql.Create(ctx, e, []models.Customer{
    {Name: "Alice", Age: 40, Country: "USA"},
    {Name: "Bob", Age: 41, Country: "Canada"},
})
// customers is []*models.Customer, with generated IDs filled in

orders, err := goql.Create(ctx, e, []models.Order{
    {Total: 1500, Priority: "Normal", Customer: customers[0]},
})
```

Read by example, or by predicate:

```go
usa, err := goql.Search(ctx, e, models.Customer{Country: "USA"})

big, err := goql.Select[models.Order](ctx, e, func(o *models.Order) bool {
    return o.Total > 1000 && o.Customer.Country == "USA"
})
```

Both return `[]*T`.

## Update and delete

```go
rows, err := goql.Update[models.Order](ctx, e, func(o *models.Order) {
    if o.Total > 1000 {
        o.Priority = "High"
    }
})

rows, err = goql.Delete[models.Order](ctx, e, func(o *models.Order) bool {
    return o.Priority == "Normal"
})
```

Or through loaded entities, which persists only what changed:

```go
customers[0].Country = "Canada"
rows, err := goql.Write(ctx, e, []models.Customer{*customers[0]})

rows, err = goql.Remove(ctx, e, []models.Customer{*customers[1]})
```

## The shape of the API

Two halves. Struct calls describe rows **by example**; lambda calls describe them **by
predicate**. One model and one operation per call.

|        | struct   | lambda                     |
| ------ | -------- | -------------------------- |
| create | `Create` | `Insert` (INSERT … SELECT) |
| read   | `Search` | `Select`, `Exists`         |
| update | `Write`  | `Update`                   |
| delete | `Remove` | `Delete`                   |

Every call takes a `context.Context` first. `e.Transaction(...)` scopes a group of them —
see [Transactions](transactions.md).

## Before you build a binary

In development, lambda bodies are parsed from source at runtime. A released binary has no
source, so a `-tags prod` build reads a registry generated ahead of time:

```bash
go generate ./...
go build -tags prod ./...
```

This is not optional and the registry is keyed positionally. Read
[Production builds](production.md) before you ship.

## Next

- [Lambdas are parsed, not executed](lambdas.md) — the rules that follow from the core contract
- [Predicates and conditions](predicates.md) — everything a `WHERE` can say
- [Query options](options.md) — sorting, pagination, projection
