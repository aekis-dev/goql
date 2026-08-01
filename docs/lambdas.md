# Lambdas are parsed, not executed

This page is the one to read carefully. Everything unusual about goql follows from it.

## What actually happens

When you write:

```go
goql.Update[Order](ctx, e, func(o *Order) {
    if o.Total > 1000 {
        o.Priority = "High"
    }
})
```

goql does **not** call that function. It:

1. asks the Go runtime where the function literal is,
2. parses the enclosing source file with `go/parser`,
3. walks the body's AST,
4. compiles it into `UPDATE orders SET priority = ? WHERE total_amount > ?`.

The assignment describes a `SET` clause. The `if` describes a `WHERE`. Nothing is mutated in
Go, and nothing in the body has any runtime effect.

!!! info "Why the parameter is a pointer"
    `func(o Order) { o.Priority = "High" }` reads as dead code to any Go developer, and
    linters flag it — while both forms parsed identically. goql now **requires** a pointer, so
    Go's one signal for mutation carries its usual meaning.

## Consequence 1: no captured variables

A closure's free variables cannot be read by reflection — `reflect` does not expose them, and
anything that digs them out of the binary depends on compiler internals and dies on stripped
builds. So this is a **parse error**, not a silently wrong query:

```go
minTotal := 100.0

goql.Select[Order](ctx, e, func(o *Order) bool {
    return o.Total > minTotal    // ✗ ErrCapturedVariable
})
```

Early versions compiled that to `total_amount > 'minTotal'` — comparing against the literal
string. Now it fails, and the error names the mechanism to use instead:

```go
type OrderParams struct{ MinTotal float64 }

goql.Select[Order](ctx, e, func(o *Order, p OrderParams) bool {
    return o.Total > p.MinTotal
}, OrderParams{MinTotal: minTotal})
```

See [Runtime values](params.md).

## Consequence 2: only parseable expressions

goql understands a defined subset of Go. Anything else is refused with
`ErrUnsupportedExpr` rather than approximated.

**Supported:**

| Go | SQL |
|---|---|
| `==` `!=` `<` `<=` `>` `>=` | the same comparisons |
| `&&` `\|\|` | `AND` `OR` |
| `if` / `else if` / `else` | one statement per arm, mutually exclusive |
| `switch` (tag and tagless) | the same |
| `for _, t := range o.Tags` | a join on the relation |
| `o.Customer.Country` | a join, or the local FK if the path ends at the target's key |
| `goql.Condition(...)` | `LIKE`, `IN`, `IS NULL`, `NOT IN`, … |
| `+ - * / %` | arithmetic, or `\|\|`/`CONCAT` for strings |
| a nested `goql.Select` / `Exists` | a subquery |
| `goql.Sum(...)` and friends | aggregates |

**Not supported:**

- `!x` — Go's unary not has no parser case yet. Negate with `goql.Condition(x, "NOT IN", …)`
  or `"NOT LIKE"`.
- Calling your own functions, method calls, type assertions, `len()`, string formatting.
- `c.Deleted == nil` — `nil` reads as an identifier, so use `goql.Condition(c.Deleted, "IS NULL")`.

## Where the body is found

In **development**, the body is located by the runtime function name, parsed and cached. Two
lambdas on one line resolve correctly, because the parser selects the literal by its `funcN`
index rather than by position in the text.

In **production** (`-tags prod`) there is no source, so the same parser runs ahead of time via
`go generate` and writes a registry. See [Production builds](production.md) — including the
one rule that matters: a goql lambda **nested inside another closure** cannot be keyed, and
the generator skips it loudly.

## Reading the errors

Because parsing happens where the lambda is written, mistakes surface with the source in
hand:

```text
captured variable minTotal: a lambda cannot reference variables from its surrounding
scope — pass the value through a params struct
```

```text
unsupported expression in lambda: condition of type *ast.UnaryExpr
```

```text
field Prioritee not found in models orders
```

Most field mistakes are caught earlier still, by the Go compiler — the lambda is real Go.

## A useful mental model

Write the lambda as if it *would* run, and check that it would be correct if it did. goql's
job is to make the database do the same thing. Where that mapping breaks down — a captured
variable, a function call, an operator SQL cannot express — goql tells you instead of guessing.
