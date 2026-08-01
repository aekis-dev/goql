# Joins

Three ways a query reaches another table, in increasing order of explicitness.

## 1. Through a declared relation

Traversal joins automatically — see [Relations](relations.md):

```go
func(o *Order) bool { return o.Customer.Country == "USA" }
// → INNER JOIN "customers" c ON o."customer_id" = c."id"
```

Nothing to declare: the relation already says how the tables relate.

## 2. Between models with no relation

Declare both as parameters. A comparison whose two sides belong to **different** models is
the join condition; everything else is a filter.

```go
paid, err := goql.Select[Invoice](ctx, e, func(i *Invoice, p *Payment) bool {
    return i.Ref == p.Ref && p.Method == "card"
})
```

```sql
SELECT i.* FROM "invoices" i, "payments" p
WHERE i."ref" = p."ref" AND p."method" = ?
```

- The result is still `[]*Invoice`. Extra models **constrain** the query; they do not widen
  the result.
- A declared but unreferenced model is not joined, so a stray parameter cannot silently turn
  the query into a cross join.
- The join is **inner**: an equality cannot say which side is optional.
- `Update` and `Delete` reject a declared model rather than ignoring it — they reach other
  tables through relation joins, not a FROM list.

## 3. Explicit — `goql.Join`

When you need to state the condition, or choose the kind:

```go
open, err := goql.Select[Invoice](ctx, e,
    func(i *Invoice, p *Payment, j *goql.Join) bool {
        j.Model = p
        j.On    = i.Ref == p.Ref
        j.Type  = goql.Left
        return i.Status == "open"
    })
```

```sql
SELECT i.* FROM "invoices" i
LEFT JOIN "payments" p ON i."ref" = p."ref"
WHERE i."status" = ?
```

| Field | Meaning |
|---|---|
| `Model` | one of the lambda's own model parameters |
| `Query` | a [CTE](cte.md) bound in this lambda, instead of a model |
| `On` | the join condition — a `bool` field, so an ordinary comparison assigns to it |
| `Type` | `goql.Inner` (default), `goql.Left`, `goql.Right`, `goql.Full` |

`Model` names a **declared parameter** rather than a fresh value, pointing at the declaration
instead of restating it — the same rule `From.Model` uses.

Declare several `*goql.Join` parameters to join several tables; they apply in declaration
order.

### Why `On` can be a comparison

`On bool` is an ordinary struct field, so `j.On = i.Ref == p.Ref` compiles and the Go
compiler type-checks both sides. Then it is parsed, not evaluated, like every other
expression in a lambda.

## Join kinds and engines

| Kind | SQLite | PostgreSQL | MySQL |
|---|---|---|---|
| `Inner` | ✓ | ✓ | ✓ |
| `Left` | ✓ | ✓ | ✓ |
| `Right` | ✓ (3.39+) | ✓ | ✓ |
| `Full` | ✓ (3.39+) | ✓ | ✗ |

An unsupported kind is **refused while building**, with a message naming the engine — never
emitted and left to fail at the server. MySQL's usual `FULL JOIN` workaround is a union of a
`LEFT` and a `RIGHT` join, which is a different statement and not something goql substitutes
silently.

## Counting across a join

A join multiplies rows, so `goql.Count()` becomes `COUNT(DISTINCT pk)` whenever the query
joins — a row matched through several related rows is counted once. This applies to relation
joins, model participants and explicit joins alike.

## Which to use

| | |
|---|---|
| The models have a relation | traverse it — nothing to declare |
| No relation, inner join, obvious key | declare both models and compare |
| You need `LEFT`/`RIGHT`/`FULL`, or the condition is not a plain equality | `goql.Join` |
| The other side is a query, not a table | `goql.Join` with `Query` — see [CTEs](cte.md) |
