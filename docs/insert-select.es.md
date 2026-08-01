# INSERT … SELECT

`Insert` construye filas en la base de datos a partir de filas que ya están allí. No se carga
nada en Go.

```go
archived, err := goql.Insert[OrderArchive](ctx, e,
    func(a *OrderArchive, o *Order) {
        if o.Total > 1000 {
            a.Total  = o.Total
            a.Reason = "high value"
            a.Origin = o.ShippingMethod
        }
    })
```

```sql
INSERT INTO "order_archives" ("total", "reason", "origin", "goql_created", "goql_updated")
SELECT o."total_amount", ?, o."shipping_method", CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
FROM "orders" o WHERE o."total_amount" > ?
```

Devuelve un **número de filas**, no entidades: recuperar las claves generadas solo es portable
donde existe `INSERT … RETURNING`.

## Destino primero, origen después

El primer parámetro de la lambda es el destino, el segundo el origen. Solo el destino es un
parámetro de tipo — `Insert[OrderArchive]` se lee como «insertar en OrderArchive», y el origen
es el modelo que la lambda declare en segundo lugar.

Cada asignación aporta **las dos mitades de la sentencia a la vez**: la izquierda nombra una
columna del destino, la derecha una expresión seleccionada del origen.

| Lado derecho | Se convierte en |
|---|---|
| un campo del origen | una columna seleccionada |
| un literal | una constante enlazada, seleccionada para cada fila |
| un valor de [params](params.md) | lo mismo, desde el sitio de llamada |
| una [expresión](expressions.md) | `o."price" * o."qty"` |

## Condiciones y ramas

Una condición filtra el `SELECT`. Un `if`/`else` o un `switch` emiten una sentencia por rama,
como hace [`Update`](predicates.md#update), y el número devuelto es su suma:

```go
goql.Insert[OrderArchive](ctx, e, func(a *OrderArchive, o *Order) {
    if o.Total > 1000 {
        a.Total, a.Reason = o.Total, "high value"
    } else if o.Priority == "Urgent" {
        a.Total, a.Reason = o.Total, "urgent"
    }
})
```

Alcanzar una relación hace join, igual que en un predicado:

```go
if o.Customer.Country == "USA" {
    a.Total, a.Reason = o.Total, "domestic"
}
```

## Marcas de tiempo

Las columnas `AutoCreateTime` / `AutoUpdateTime` del destino se rellenan con
`CURRENT_TIMESTAMP`, ya que nunca se construye una fila en Go para que los hooks la toquen.
Una asignación explícita a la misma columna tiene prioridad.

## `goql.Conflict`

```go
goql.Insert[OrderArchive](ctx, e, func(a *OrderArchive, o *Order, c *goql.Conflict) {
    c.Ignore = true
    a.Total = o.Total
})
```

| Motor | Se genera como |
|---|---|
| SQLite | `INSERT OR IGNORE INTO …` |
| MySQL | `INSERT IGNORE INTO …` |
| PostgreSQL | `INSERT INTO … ON CONFLICT DO NOTHING` |

Solo `Ignore`. El nombre es deliberadamente estrecho: un upsert completo (un objetivo de
conflicto más `DO UPDATE SET`) es un diseño propio, y llamar a esto `OnConflict` reclamaría a
medias ese nombre. Pasar `Conflict` a cualquier otra llamada es un error.

## Reglas

- **Asignar al origen es un error**, no una operación vacía — `o.Priority = "x"` dentro de una
  lambda de Insert describe una mutación que ningún `INSERT … SELECT` realiza.
- **Leer el destino también es un error**: todavía no tiene filas.
- El segundo parámetro debe ser un **modelo registrado**. Pasar ahí un portador de opciones o
  una struct cualquiera es un error, no una reinterpretación silenciosa.
- Una firma intercambiada se detecta: el primer parámetro debe coincidir con el argumento de
  tipo.
- **Las asignaciones de relaciones se rechazan.** Enlazar necesita la clave primaria de una
  fila que aún no existe, e `INSERT … SELECT` no informa de las claves que generó.
- `Sort`, `Limit` y `Offset` se aplican al `SELECT`. `Fields` y `Preload` se rechazan: la
  proyección sale de las asignaciones, y no hay resultado en el que cargar.

## Copiar un modelo en sí mismo

Se escribe con dos parámetros del mismo tipo, destino primero:

```go
goql.Insert[Order](ctx, e, func(dst *Order, src *Order) {
    if src.Priority == "High" {
        dst.Total    = src.Total
        dst.Priority = "Urgent"
        dst.Customer = src.Customer
    }
})
```
