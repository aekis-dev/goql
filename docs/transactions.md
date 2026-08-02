# Transactions

```go
err := e.Transaction(func(tx *goql.Engine) error {
    customers, err := goql.Create(ctx, tx, []Customer{{Name: "Alice"}})
    if err != nil {
        return err   // rolls back
    }
    _, err = goql.Create(ctx, tx, []Order{{Total: 100, Customer: customers[0]}})
    return err
})
```

The callback receives an `*Engine` bound to the transaction. **Use it** — calls made against
the outer engine run outside the transaction.

Returning an error rolls back; returning nil commits. A panic also rolls back, then
re-panics.

## Nesting joins, it does not re-open

`Create`, `Write`, `Update`, `Delete` and `Insert` each wrap themselves in a transaction. When
one is already in progress they **join** it rather than opening a second, independent one —
which previously risked a deadlock on SQLite and broke atomicity everywhere.

So this is one transaction, not three:

```go
e.Transaction(func(tx *goql.Engine) error {
    goql.Create(ctx, tx, …)
    goql.Update[Order](ctx, tx, …)
    goql.Delete[Tag](ctx, tx, …)
    return nil
})
```

## A panic rolls back

The rollback is deferred, so a panic in the function passes through `Transaction` on its way
up — after the transaction has been rolled back and its connection returned to the pool.
Without that, a single panicking handler would remove one connection from the pool for the
lifetime of the process.

```go
e.Transaction(func(tx *goql.Engine) error {
    goql.Create(ctx, tx, …)
    panic("boom")          // rolled back, connection released, panic still propagates
})
```

goql does not recover the panic — recovering is the caller's decision, and a web framework
already does it per request.

## Context

Every call takes a `context.Context` first, threaded to the driver — so a request timeout or
cancellation reaches the database:

```go
ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
defer cancel()

orders, err := goql.Select[Order](ctx, e, …)
```

## Raw statements join too

[`Execute` and `Bind`](raw-sql.md) run on the transaction when one is in progress:

```go
e.Transaction(func(tx *goql.Engine) error {
    if _, err := goql.Execute(ctx, tx, `DELETE FROM audit WHERE created < ?`, cutoff); err != nil {
        return err
    }
    return nil
})
```

## Web framework integration

There is no framework package. The recipe is a middleware that builds a request-scoped
engine, and — if you want a transaction per request — commits on success:

```go
func WithTx(e *goql.Engine) gin.HandlerFunc {
    return func(c *gin.Context) {
        err := e.Transaction(func(tx *goql.Engine) error {
            c.Set("db", tx)
            c.Next()
            if len(c.Errors) > 0 {
                return c.Errors[0]
            }
            return nil
        })
        if err != nil && !c.Writer.Written() {
            c.AbortWithStatus(http.StatusInternalServerError)
        }
    }
}
```

Handlers then pull the engine out of the context:

```go
tx := c.MustGet("db").(*goql.Engine)
orders, err := goql.Select[Order](c.Request.Context(), tx, …)
```

`*sql.DB` pools connections, so build **one** `*goql.Engine` at startup and share it; a
transaction-scoped engine is a shallow copy carrying the `*sql.Tx`.

## Not available

Isolation levels, read-only transactions and explicit `Begin`/`Commit`/`Rollback` handles are
not exposed yet — `Transaction` is the only entry point. `SAVEPOINT`-based nesting is not
implemented either: a nested call joins the outer transaction rather than creating a savepoint.
