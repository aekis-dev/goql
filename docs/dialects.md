# Dialects and portability

```go
e := goql.New(db, goql.SQLite{})     // or goql.Postgres{} / goql.MySQL{}
```

The engine is named explicitly because `database/sql` exposes no driver name — guessing would
mean a type switch that breaks on wrapped drivers.

## How it is structured

`query.Spec` carries only what genuinely differs between engines; the shared builders — the
`WHERE` walk, join collection, `SET` assembly — are written **once** against it. Divergence
is confined to the Spec, so no engine gets its own copy to drift out of step.

## What differs

| | SQLite | PostgreSQL | MySQL |
|---|---|---|---|
| Identifier quoting | `"x"` | `"x"` | `` `x` `` |
| Placeholders | `?` | `$1`, `$2`, … | `?` |
| Generated keys | `LastInsertId` | `INSERT … RETURNING` | `LastInsertId` |
| Auto-increment | `AUTOINCREMENT` | identity | `AUTO_INCREMENT` |
| Insert-ignore | `INSERT OR IGNORE` | `ON CONFLICT DO NOTHING` | `INSERT IGNORE` |
| Joined update | `UPDATE … FROM` | `UPDATE … FROM` | `UPDATE … JOIN … SET` |
| Open-ended limit | `LIMIT -1` | `LIMIT ALL` | `LIMIT 18446744073709551615` |
| String concat | `\|\|` | `\|\|` | `CONCAT()` |
| Foreign keys | must be enabled | always on | always on |

Two divergences are **statement-shaped** rather than token-shaped: `Create` branches at
execution level for `RETURNING`, and a joined `UPDATE` is a different statement on MySQL.

## Feature support

| | SQLite | PostgreSQL | MySQL |
|---|---|---|---|
| `LEFT JOIN` | ✓ | ✓ | ✓ |
| `RIGHT JOIN` | 3.39+ | ✓ | ✓ |
| `FULL JOIN` | 3.39+ | ✓ | **✗** |
| `WITH` | 3.8.3+ | ✓ | 8.0+ |
| `WITH RECURSIVE` | 3.8.3+ | ✓ | 8.0+ |
| `INTERSECT` / `EXCEPT` | ✓ | ✓ | 8.0.31+ |
| Transactional DDL | ✓ | ✓ | **✗** |

An unsupported **join kind** is refused while building, with a message naming the engine.
Missing `WITH` falls back to a derived table, except for recursion, which is refused. The
`INTERSECT`/`EXCEPT` floor is not checked — an old MySQL fails at the server.

## Placeholder ordering

Every builder appends bound values in the same order it writes their markers, because
PostgreSQL numbers its parameters. That ordering is asserted per dialect across whole
statements — a `WITH` definition binds before the outer query, a projection before the
`WHERE`, a join's `ON` before the `WHERE`, and the `LIMIT`/`OFFSET` tail last.

## Types

The [column type vocabulary](models.md#column-types) is deliberately small and each dialect
maps it to a physical type. A type outside the set is emitted verbatim — the escape hatch, at
the cost of targeting one engine.

## Writing portable models

- Declare `Type` explicitly on anything where the inferred type matters.
- Use `TypeJSON` for JSON columns; SQLite accepts `jsonb` through type affinity.
- Keep `Default` values to literals or the recognised expressions
  (`CURRENT_TIMESTAMP`, `NOW()`, …).
- `Checks` are passed through verbatim — they are SQL, and yours to keep portable.
- Call `EnableForeignKeys()` at startup: a no-op where enforcement is always on, and required
  on SQLite.

## Testing status

!!! warning
    Live coverage is **SQLite only**. The PostgreSQL and MySQL SQL is generated and asserted
    per dialect — quoting, placeholder numbering, `RETURNING`, the joined-update shape, type
    mapping, insert-ignore spellings — but has never been run against those servers. Real
    driver behaviour (scanning `RETURNING`, timestamp handling, MySQL's SQL modes) is
    unverified. Treat PostgreSQL and MySQL as "generated correctly, not yet proven".
