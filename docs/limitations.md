# Limitations

Kept blunt on purpose. If something here matters to you, it is better to know now.

## Testing

- **Live coverage is SQLite only.** PostgreSQL and MySQL SQL is generated and asserted per
  dialect, but has never run against those servers. Real driver behaviour — scanning
  `RETURNING`, timestamp handling, MySQL SQL modes — is unverified.
- Migration coverage is likewise SQLite-only; the other engines' introspection SQL is
  exercised through `PlanAgainst` with a supplied schema.

## The query language

- **`!x` is not parsed.** Go's unary not has no parser case. Negate with
  `goql.Condition(x, "NOT IN", …)`, `"NOT LIKE"`, `"IS NOT NULL"` or `<>`.
- **`c.Deleted == nil` is refused** — `nil` reads as an identifier. Use
  `goql.Condition(c.Deleted, "IS NULL")`.
- **No SQL functions.** `COALESCE`, `LOWER`, `ABS`, date arithmetic — none have a spelling.
  Use [raw SQL](raw-sql.md).
- **No window functions**, and no `DISTINCT` beyond the automatic `COUNT(DISTINCT pk)` over a
  join.
- **Field paths stop at two segments.** `o.Customer.Company.Name` is not supported.
- **A subquery cannot use a table the enclosing query already uses.** Both would render with
  the same alias; self-joins need per-occurrence aliases, which the alias map does not model.
- **Concatenating two params values reads as arithmetic**, because a params placeholder
  carries a name and not a type. Write one side as a column or literal.
- **Only inner joins are implicit.** A `LEFT`/`RIGHT`/`FULL` join needs
  [`goql.Join`](joins.md).

## Data model

- **Soft delete is half-built.** `Deleted *time.Time` exists on every model, but `Delete`
  hard-deletes and `Search` never filters on it. Either implement it in your own predicates
  or ignore the column.
- **`go vet` reports `copylocks`.** `goql.Model` embeds a `sync.RWMutex` for change tracking,
  and the API takes `[]T` by value, so `[]Customer{*alice}` is flagged. It is safe — the
  mutex guards per-entity tracking that is rarely shared — but it is noise you cannot
  currently silence.
- **Zero values are omitted from an INSERT** for nullable columns, so `false`/`0`/`""` fall
  back to the column default. Declare `NotNull`, or use a pointer, when that matters.
- **JSON columns cannot be assigned from a params struct** or from a composite literal in an
  update lambda, and there is no spelling for querying *into* JSON — use a
  [raw column](predicates.md#raw-columns).

## Migrations

- **Index and constraint drift is not diffed.** Only columns, column types and whole tables.
- **No migration files**, so no replayable history. `goql_migrations` is an audit log.
- Planning requires a **reachable database** — the comparison is always against reality.
- SQLite cannot alter a column's type in place, so a type change there is reported and
  refused rather than emitted.

## Transactions

- No isolation levels, no read-only transactions, no explicit `Begin`/`Commit`/`Rollback`
  handles. `Transaction` is the only entry point.
- No `SAVEPOINT` nesting: a nested call joins the outer transaction.

## Production builds

- **Registry keys are positional.** Adding, removing or reordering a closure in a function
  shifts every later index. Regenerate before every `-tags prod` build — a stale registry can
  silently resolve to a different lambda's body. See [Production builds](production.md).
- **A goql lambda nested inside another closure cannot be keyed.** The generator skips it
  loudly and the prod call fails with `ErrNoCompiledBody`.

## Deliberately not built

Each of these was considered and left out, not overlooked:

- **A `WITH` for a subquery used only as a value.** The Go source is identical either way, so
  it changes nothing semantically.
- **Derived tables as a first-class shape.** Their real uses sit downstream of window
  functions.
- **`SelectInto`.** [`Bind`](raw-sql.md) covers scanning into a struct.
- **Lazy relation loading.** It would require relation fields to stop being plain
  `*Customer` / `[]Tag`, which the whole design rests on.
- **A full upsert.** `goql.Conflict{Ignore: true}` is the narrow piece; a conflict target
  plus `DO UPDATE SET` is a design of its own.
