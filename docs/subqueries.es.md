# Subconsultas

Una llamada goql escrita **dentro** del cuerpo de una lambda también se analiza, y se convierte
en una consulta anidada.

La composición va dentro de la lambda, porque fuera de ella una consulta es un valor — y
alcanzar un valor desde dentro del cuerpo significaría capturar una variable, que es
[lo único que el diseño prohíbe](lambdas.md#consecuencia-1-nada-de-variables-capturadas).

## Dos formas de escribirlo

**Con nombre**, lo que la hace reutilizable dentro del cuerpo:

```go
usOrders, err := goql.Select[Order](ctx, e, func(o *Order) bool {
    usa, _ := goql.Select[Customer](ctx, e, func(c *Customer) bool {
        return c.Country == "USA"
    })
    return goql.Condition(o.Customer, "IN", usa)
})
```

```sql
SELECT o.* FROM "orders" o
WHERE o."customer_id" IN (SELECT c."id" FROM "customers" c WHERE c."country" = ?)
```

**Anidada directamente**, con `goql.Unwrap`:

```go
return goql.Condition(o.Customer, "IN",
    goql.Unwrap(goql.Select[Customer](ctx, e, func(c *Customer) bool {
        return c.Country == "USA"
    })))
```

!!! info "Por qué existe `Unwrap`"
    Go rechaza una llamada de dos valores como un argumento más entre otros
    (`multiple-value … in single-value context`), y `Select` debe devolver `([]*T, error)`.
    Pasar una llamada como la lista **completa** de argumentos de una función es la única
    excepción, así que `Unwrap[T](T, error) T` es lo que hace legal el anidamiento directo.
    Nunca se ejecuta dentro de una lambda.

## El error enlazado

Go obliga a usar un nombre enlazado, y el único uso honesto es descartarlo:

```go
usa, _ := goql.Select[Customer](ctx, e, …)     // ✓
```

```go
usa, err := goql.Select[Customer](ctx, e, …)
if err != nil { … }                            // ✗ se reporta con claridad
```

Una llamada anidada nunca se ejecuta, así que no tiene ningún error que inspeccionar. **Todo
fallo de una subconsulta lo reporta la llamada que la contiene**, que es lo único que se
ejecuta.

## Qué proyecta la subconsulta

Por defecto la clave primaria, que es lo que quiere un `IN`. Nombra otra columna con
`goql.Fields` **dentro de la lambda anidada**:

```go
refs, _ := goql.Select[Payment](ctx, e, func(p *Payment, f *goql.Fields) bool {
    f.Names = []string{"Ref"}
    return p.Method == "card"
})
return goql.Condition(i.Ref, "IN", refs)
```

Más de un campo es un error: una subconsulta produce una sola columna.

## Correlación

Un predicado anidado puede nombrar el modelo que lo contiene — la subconsulta se evalúa por
cada fila externa:

```go
bigBuyers, err := goql.Select[Customer](ctx, e, func(c *Customer) bool {
    return goql.Unwrap(goql.Exists[Order](ctx, e, func(o *Order) bool {
        return o.Customer == c && o.Total > 1000
    }))
})
```

```sql
SELECT c.* FROM "customers" c
WHERE EXISTS (SELECT 1 FROM "orders" o WHERE o."customer_id" = c."id" AND o."total_amount" > ?)
```

`o.Customer == c` compara la clave foránea con la clave primaria del modelo externo. Un modelo
heredado *no* se añade a la lista `FROM` propia de la subconsulta — ya está en la sentencia
que la contiene.

!!! warning "Correlación y CTE"
    Una subconsulta correlacionada funciona como valor de `IN`/`EXISTS`. **Nunca** puede
    convertirse en una [CTE](cte.md), que se evalúa antes de que la consulta externa haya
    producido una fila.

## Qué funciones se pueden anidar

`Select` y `Exists`. `Update`, `Delete` e `Insert` emiten una sentencia por rama, así que no
son valores — y una lambda anidada que *asigna* se rechaza.

## Dónde aparecen los fallos

| Etapa | Ejemplo | Lo reporta |
|---|---|---|
| compilación | un campo inexistente en la lambda anidada | el compilador de Go |
| análisis | una sublambda que asigna, más de un campo proyectado | la llamada externa |
| construcción | una colisión de alias, una tabla no registrada | la llamada externa |

## Rechazado en vez de aproximado

- **Una subconsulta sobre una tabla que la consulta externa ya usa** — ambas se generarían con
  el mismo alias. Los autojoins necesitan alias por aparición, que el mapa de alias todavía no
  modela.
- **Un `Exists` anidado dentro de `Condition`**, o un `Select` anidado por sí solo: a cada uno
  se le indica en qué posición corresponde.
