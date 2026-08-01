# Migrations

goql has **no migration files**. The models are the source of truth, and a migration is a
comparison between them and the live database.

## Creating tables

For tests, demos and fresh databases:

```go
err := e.CreateTables(&Customer{}, &Order{}, &Tag{})
```

Creates tables, indexes and many2many join tables, all `IF NOT EXISTS`. It does not alter
anything that already exists.

## Planning a change

```go
plan, err := e.MigrationPlan(ctx, []models.Entity{&Customer{}, &Order{}}, nil)
if err != nil {
    return err
}

for _, c := range plan.Changes {
    fmt.Printf("%s: %s\n", c.Kind, c.Detail)
    if c.Destructive {
        fmt.Println("  ⚠ this can lose data")
    }
}
for _, q := range plan.Questions {
    fmt.Println(q.Prompt)
    for _, opt := range q.Options {
        fmt.Printf("  %s — %s\n", opt.Value, opt.Label)
    }
}
```

The comparison is against a **live** database, read by per-dialect introspection. Planning
therefore needs a reachable server — the accepted trade for always comparing against reality
rather than a stored snapshot.

Only **declared** tables are inspected. A database normally holds tables goql knows nothing
about; one absent from the models is never read and never proposed for removal.

## Ambiguity is asked, never guessed

A column that disappeared while another appeared is indistinguishable between a rename and a
drop-and-add — only intent separates them. Each such column produces a `Question`:

| Answer | Effect |
|---|---|
| `rename:NewName` | keeps the data |
| `drop` | discards it — flagged `Destructive` |
| `skip` | leaves the column alone |

A **type change** is likewise always asked, and flagged destructive: whether it truncates
depends on the direction, which the types alone do not say.

Adding a column is unambiguous and needs no question. Its definition is relaxed to drop
`NOT NULL` and `UNIQUE`, since neither can be added to a table with existing rows without a
default.

## Applying

```go
decisions := map[string]string{
    "customers.old_name": "rename:Nickname",
    "orders.legacy":      "drop",
}

summary, err := e.Migrate(ctx, entities, decisions)
```

- **Applying with anything unanswered returns `ErrUnresolvedQuestions` and changes nothing.**
- `Migrate` **re-plans from the live database** rather than trusting a plan handed back by a
  client, so a schema that moved since the plan was shown cannot be migrated against stale
  assumptions.
- Where the engine supports transactional DDL (SQLite, PostgreSQL) a failure rolls everything
  back and `summary.Rolled` says so. On MySQL each DDL statement commits as it runs, so the
  summary reports how far it got and you re-run.

`goql_migrations` records one row per applied change with its statement — an **audit log**,
not a file-applied ledger. There is no replayable history, and a fresh environment bootstraps
from the models.

## The CLI

`tools/goqlmigrate` drives the interactive flow. It re-plans after every answer, because
resolving one question can change the next — a rename consumes a candidate a later question
would otherwise have offered.

It talks to the **migrate socket of your running application**, because only that process has
the models registered:

```bash
go run ./tools/goqlmigrate -socket /run/goql-migrate.sock -token "$GOQL_MIGRATE_TOKEN"
```

It shows the plan, asks about anything the schema cannot answer on its own, and prints what
happened.

## Type comparison

"The type the model wants" and "the type the database reports" are spelled differently even
when identical, so both sides are normalised per engine before comparing:

- **PostgreSQL** introspects with `pg_catalog` and `format_type`, because `data_type` omits
  parameters and a precision cannot be reassembled — PostgreSQL writes it *inside* the name
  (`timestamp(6) without time zone`).
- **MySQL** uses `column_type`, which already carries parameters, plus aliases for what it
  stores under another name — most importantly `BOOLEAN`, reported as `tinyint(1)`.
- **SQLite** normalises to the **column affinity** the engine actually enforces, so goql never
  proposes a change SQLite would treat as a no-op.

## The remote socket

Enable it in the process that owns the models:

```go
socket, err := e.NewMigrateSocket(
    []models.Entity{&Customer{}, &Order{}},
    goql.MigrateSocketConfig{Path: "/run/goql-migrate.sock", Token: os.Getenv("GOQL_MIGRATE_TOKEN")},
)
if err != nil {
    log.Fatal(err)
}
go socket.Serve()
```

It is
deliberately awkward to enable: **off by default**, a required token with no default, Unix
domain only, `chmod 0600`, and a loud log line when it starts. It can apply DDL, so it is a
control channel into your process. A wrong token gets 403.

## Not covered

**Index and constraint drift is not diffed** — only columns, column types and whole tables.
Adding an index to a model will not be noticed by a migration; create it yourself or drop and
recreate the table.

Live migration coverage is SQLite-only, like the rest of the suite: the PostgreSQL and MySQL
introspection SQL is written from documented behaviour and exercised through `PlanAgainst`,
but has not run against a real server.
