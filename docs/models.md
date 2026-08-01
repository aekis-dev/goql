# Declaring models

A model is a Go struct embedding `goql.Model`, plus a call to `models.AddModel` in `init()`.

```go
type Customer struct {
    goql.Model
    Name    string
    Age     int
    Country string
    Orders  []Order
}

func init() {
    err := models.AddModel(&Customer{}, "customers",
        &models.Field{Name: "Name", Type: models.TypeVarchar, Size: 100, NotNull: true},
        &models.Field{Name: "Age"},
        &models.Field{Name: "Country", Index: true},
        &models.Field{Name: "Orders", OneToMany: &models.OneToMany{Ref: "customer_id"}},
    )
    if err != nil {
        panic(err)
    }
}
```

## What `goql.Model` provides

| Field | Column | Notes |
|---|---|---|
| `ID` | `id` | `int64`, primary key, auto-increment |
| `Created` | `goql_created` | set on insert |
| `Updated` | `goql_updated` | set on every write |
| `Deleted` | `goql_deleted` | `*time.Time`; see [Limitations](limitations.md) — soft delete is half-built |

It also carries per-entity change tracking, which is what lets [`Write`](struct-calls.md)
persist only the columns that actually changed.

!!! warning "`copylocks`"
    `goql.Model` embeds a `sync.RWMutex`, so `go vet` flags `[]Customer{*alice}`. The API takes
    `[]T` by value, so this is currently part of the public surface. See
    [Limitations](limitations.md).

## Why declarations, not struct tags

Tags stay free for serialization, and a field definition can use a constant, a variable or a
helper — which a tag cannot. It also means the registry is built by running code, which is why
**the package declaring a model must be imported** for that model to exist.

## Field reference

```go
&models.Field{
    Name:   "Total",          // Go field name (required)
    Column: "total_amount",   // defaults to snake_case(Name)
    Type:   models.TypeDecimal,
    Precision: 10, Scale: 2,  // Size for varchar/bytes

    NotNull: true,
    Unique:  true,
    Default: "Normal",
    Checks:  []string{"total_amount > 0"},
    Comment: "order value",
    Collation: "NOCASE",

    Index:   true,            // or "idx_name" to share one index between fields
    Preload: true,            // load this relation on every read
}
```

### Column types

A deliberately small, portable vocabulary. Each dialect maps it to that engine's physical
type.

| goql | SQLite | PostgreSQL | MySQL |
|---|---|---|---|
| `TypeInteger` | INTEGER | INTEGER | INT |
| `TypeBigInt` | INTEGER | BIGINT | BIGINT |
| `TypeReal` | REAL | REAL | FLOAT |
| `TypeDouble` | REAL | DOUBLE PRECISION | DOUBLE |
| `TypeDecimal` | NUMERIC | NUMERIC(p,s) | DECIMAL(p,s) |
| `TypeText` | TEXT | TEXT | TEXT |
| `TypeVarchar` | TEXT | VARCHAR(n) | VARCHAR(n) |
| `TypeBoolean` | BOOLEAN | BOOLEAN | BOOLEAN |
| `TypeTimestamp` | TIMESTAMP | TIMESTAMP(p) | DATETIME(p) |
| `TypeBytes` | BLOB | BYTEA | BLOB |
| `TypeJSON` | TEXT affinity | JSONB | JSON |

Leave `Type` empty and it is inferred from the Go type. A type outside the vocabulary is
emitted verbatim — the escape hatch, at the cost of targeting one engine.

### Defaults

A string default is quoted as a literal, so `Default: "Active"` emits `DEFAULT 'Active'`.
A recognised SQL expression passes through unquoted: `CURRENT_TIMESTAMP`, `CURRENT_DATE`,
`CURRENT_TIME`, `NOW()`, `NULL`. Booleans render as `TRUE`/`FALSE`.

### Nullable fields

Declare a pointer where "unset" must be distinguishable from an explicit zero:

```go
Nickname *string
```

Pointers are deliberately **opt-in**. Making every field a pointer would force
`goql.Ptr(40)` at every construction site and `*c.Age > 40` in every predicate — a large
permanent cost to solve a problem that affects a few columns.

## Relations

### many2one

A pointer to another model. The foreign key column lives on this table.

```go
type Order struct {
    goql.Model
    Customer *Customer
}

&models.Field{Name: "Customer", Column: "customer_id", NotNull: true}
```

`Column` names the FK; without it, `snake_case(Name) + "_id"`.

### one2many

A slice, with `Ref` naming the FK column **on the other table**.

```go
type Customer struct {
    goql.Model
    Orders []Order
}

&models.Field{Name: "Orders", OneToMany: &models.OneToMany{Ref: "customer_id"}}
```

### many2many

```go
type Order struct {
    goql.Model
    Tags []Tag
}

&models.Field{Name: "Tags", ManyToMany: &models.ManyToMany{
    Table:  "order_tags",   // join table
    Column: "order_id",     // this model's FK in it
    Ref:    "tag_id",       // the target's FK in it
}}
```

`CreateTables` creates the join table too.

### Self-referencing

A model may point at itself — the shape a [recursive query](recursive.md) walks:

```go
type Category struct {
    goql.Model
    Name     string
    Parent   *Category
    Children []Category
}

&models.Field{Name: "Parent", Column: "parent_id"},
&models.Field{Name: "Children", OneToMany: &models.OneToMany{Ref: "parent_id"}},
```

## Reading a foreign key

Relation fields come back **nil** unless [preloaded](relations.md) — a foreign key is a key,
not a row, and leaving it nil keeps "not loaded" distinguishable from "loaded but empty".

To use the key itself, name the target's primary key through the relation:

```go
goql.Select[Order](ctx, e, func(o *Order, p Key) bool {
    return o.Customer.ID == p.ID
})
// → WHERE o."customer_id" = ?     (no join: the FK column already holds that value)
```

`o.Customer.ID` resolves to the local column, so it costs nothing and compares against a
plain value.

## JSON columns

Declare `TypeJSON` and a struct, map or slice field round-trips as JSON:

```go
type Widget struct {
    goql.Model
    Meta map[string]any
}

&models.Field{Name: "Meta", Type: models.TypeJSON}
```

Always declare the type explicitly, so the model stays portable — SQLite accepts `jsonb`
through type affinity.

!!! note
    A JSON column cannot yet be assigned from a params struct or from a composite literal in
    an update lambda, and querying *into* JSON has no spelling. Use
    [`goql.Condition` with a raw column](predicates.md#raw-columns) as the escape hatch.

## Indexes

```go
&models.Field{Name: "Country", Index: true}                  // idx_customers_country
&models.Field{Name: "Country", Index: "idx_geo"}             // named
&models.Field{Name: "City",    Index: "idx_geo"}             // composite: same name
```

`Unique: true` on the field makes the index unique.

## Registry rules

- One `AddModel` per type. A second registration is an error, not a silent overwrite.
- Type names must be unique across packages: parsed queries name their model by type name,
  so two `Invoice` types would resolve to whichever the registry hit first.
- `models.ErrNotRegistered` names the likely cause — the package was not imported.
