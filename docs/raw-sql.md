# Raw SQL

When goql cannot express something — a window function, a vendor extension, a hand-tuned
statement — drop to SQL. Both calls join the surrounding
[transaction](transactions.md) and honour the call's context.

## `Bind` — read rows into a type

```go
type Row struct {
    Country string
    Total   float64
}

rows, err := goql.Bind[Row](ctx, e,
    `SELECT country, SUM(total_amount) AS total
     FROM customers c JOIN orders o ON o.customer_id = c.id
     GROUP BY country HAVING SUM(total_amount) > ?`, 1000)
```

Returns `[]*Row`. Columns are matched to fields by the model's column mapping, so a result
type that is a **model** scans exactly as a `Select` would:

```go
orders, err := goql.Bind[Order](ctx, e, `SELECT * FROM orders WHERE total_amount > ?`, 500)
```

## `Execute` — statements

```go
result, err := goql.Execute(ctx, e, `UPDATE orders SET priority = ? WHERE total_amount > ?`,
    "High", 1000)

affected, _ := result.RowsAffected()
id, _ := result.LastInsertId()
```

Returns the real `sql.Result`, so `LastInsertId` is available where the driver supports it.

## Placeholders are yours to get right

goql does not rewrite the SQL you pass, so use the placeholder style of the engine you are
on — `?` for SQLite and MySQL, `$1` for PostgreSQL. If the statement must be portable, ask
the dialect:

```go
st := e.Dialect().NewStatement()
sqlText := fmt.Sprintf(`SELECT * FROM orders WHERE total_amount > %s`, st.Mark())
```

`Mark()` hands out placeholders in order, `Marks(n)` returns several comma-separated.

!!! danger "Never build SQL by concatenating values"
    Pass them as arguments. `fmt.Sprintf("… WHERE name = '%s'", name)` is an injection, and
    goql cannot protect you from a string it never parsed.

Quoting an identifier portably:

```go
col := e.Dialect().QuoteIdent("order")   // "order" / `order`
```

## When to reach for it

| Situation | Better answer |
|---|---|
| a window function, `DISTINCT ON`, a CTE goql cannot shape | raw SQL |
| a vendor extension (`ILIKE`, arrays, full-text) | raw SQL |
| a JSON path predicate | a [raw column](predicates.md#raw-columns) inside `goql.Condition` |
| a one-off aggregate | a [projection](aggregates.md) — usually shorter |
| dynamic filters | a [params struct](params.md) and branches |

A raw string is not parsed, so nothing checks the column names, nothing keeps it in step with
your models when they change, and it will not follow you across engines. Prefer the typed
path where one exists.
