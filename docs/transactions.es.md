# Transacciones

```go
err := e.Transaction(func(tx *goql.Engine) error {
    customers, err := goql.Create(ctx, tx, []Customer{{Name: "Alice"}})
    if err != nil {
        return err   // hace rollback
    }
    _, err = goql.Create(ctx, tx, []Order{{Total: 100, Customer: customers[0]}})
    return err
})
```

El callback recibe un `*Engine` ligado a la transacción. **Úsalo**: las llamadas hechas contra
el engine exterior se ejecutan fuera de la transacción.

Devolver un error hace rollback; devolver nil confirma. Un panic también hace rollback y
vuelve a lanzarse.

## El anidamiento se une, no reabre

`Create`, `Write`, `Update`, `Delete` e `Insert` se envuelven cada uno en una transacción.
Cuando ya hay una en curso, se **unen** a ella en vez de abrir una segunda independiente — lo
que antes arriesgaba un bloqueo en SQLite y rompía la atomicidad en todas partes.

Así que esto es una sola transacción, no tres:

```go
e.Transaction(func(tx *goql.Engine) error {
    goql.Create(ctx, tx, …)
    goql.Update[Order](ctx, tx, …)
    goql.Delete[Tag](ctx, tx, …)
    return nil
})
```

## Un panic hace rollback

El rollback está diferido, así que un panic dentro de la función atraviesa `Transaction` de
camino hacia arriba, después de que la transacción se haya revertido y su conexión haya
vuelto al pool. Sin eso, un único handler que entra en panic retiraría una conexión del pool
durante toda la vida del proceso.

```go
e.Transaction(func(tx *goql.Engine) error {
    goql.Create(ctx, tx, …)
    panic("boom")          // rollback, conexión liberada, el panic sigue propagándose
})
```

goql no recupera el panic: recuperarlo es decisión de quien llama, y un framework web ya lo
hace por petición.

## Contexto

Toda llamada recibe un `context.Context` como primer argumento, que se pasa al driver — así
que un timeout de petición o una cancelación llegan a la base de datos:

```go
ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
defer cancel()

orders, err := goql.Select[Order](ctx, e, …)
```

## Las sentencias directas también se unen

[`Execute` y `Bind`](raw-sql.md) se ejecutan en la transacción cuando hay una en curso:

```go
e.Transaction(func(tx *goql.Engine) error {
    if _, err := goql.Execute(ctx, tx, `DELETE FROM audit WHERE created < ?`, cutoff); err != nil {
        return err
    }
    return nil
})
```

## Integración con frameworks web

No hay un paquete de integración. La receta es un middleware que construya un engine con
ámbito de petición y —si quieres una transacción por petición— confirme al terminar bien:

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

Los handlers sacan entonces el engine del contexto:

```go
tx := c.MustGet("db").(*goql.Engine)
orders, err := goql.Select[Order](c.Request.Context(), tx, …)
```

`*sql.DB` ya gestiona un pool de conexiones, así que construye **un** `*goql.Engine` al
arrancar y compártelo; un engine con ámbito de transacción es una copia superficial que lleva
el `*sql.Tx`.

## No disponible

Los niveles de aislamiento, las transacciones de solo lectura y los manejadores explícitos
`Begin`/`Commit`/`Rollback` todavía no están expuestos: `Transaction` es la única entrada.
Tampoco hay anidamiento con `SAVEPOINT`: una llamada anidada se une a la transacción exterior
en lugar de crear un punto de guardado.
