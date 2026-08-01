# Expresiones de tabla común

Una consulta puede leer de **otra consulta** en lugar de una tabla. Eso es una cláusula
`WITH`, y es lo que hace expresable un agregado sobre un agregado.

## La forma

```go
type CustomerTotal struct {
    Customer int64
    Total    float64
}

type Summary struct {
    Average float64
    Biggest float64
}

rows, err := goql.Select[Summary](ctx, e,
    func(s *Summary, t *CustomerTotal, from *goql.From) bool {

        totals, _ := goql.Select[CustomerTotal](ctx, e,
            func(ct *CustomerTotal, o *Order, f *goql.From, g *goql.Group) bool {
                f.Model = o
                g.By = []string{"Customer"}
                ct.Total = goql.Sum(o.Total)
                return o.Total > 0
            })

        from.Query = totals     // ← leer DE la consulta con nombre
        from.Model = t          // ← t representa una de sus filas

        s.Average = goql.Avg(t.Total)
        s.Biggest = goql.Max(t.Total)
        return t.Total > 0
    })
```

```sql
WITH "totals" AS (
    SELECT o."customer_id", SUM(o."total_amount") AS "Total"
    FROM "orders" o WHERE o."total_amount" > ? GROUP BY o."customer_id")
SELECT AVG(t."Total") AS "Average", MAX(t."Total") AS "Biggest"
FROM "totals" t WHERE t."Total" > ?
```

`AVG(SUM(...))` no es SQL válido. Así es como se escribe.

## Las dos asignaciones

| | |
|---|---|
| `from.Query = totals` | **de qué** leer — una consulta enlazada antes en esta lambda |
| `from.Model = t` | el parámetro que representa **una fila** de ella |

La CTE toma el nombre de la **variable Go** a la que se enlazó, así que el nombre se dice una
sola vez y no puede desincronizarse.

El manejador de fila es un puntero al tipo de resultado de la consulta que la define
(`t *CustomerTotal`). Emparejarlo con `Query` explícitamente es lo que permite que dos CTE
compartan tipo de resultado sin ambigüedad: nada se infiere solo del tipo.

## Las columnas son la proyección

Una CTE presenta exactamente las columnas que proyecta la consulta que la define, con sus
nombres `Into`. Así que `t.Total` se comprueba **al analizar**:

```text
field not found: totals does not select Customer — it selects Total
```

No se registra nada globalmente: una CTE no puede referenciarse fuera de la sentencia que la
define.

## Requisitos y rechazos

**La consulta que la define debe proyectar.** Una consulta que selecciona filas completas de
un modelo no tiene columnas con nombre que leer:

```go
rows, _ := goql.Select[Order](ctx, e, func(o *Order) bool { return o.Total > 0 })
from.Query = rows      // ✗ «selecciona filas Order completas, así que no tiene columnas con nombre»
```

**No puede estar correlacionada.** Una CTE se evalúa *antes* de que la consulta externa
produzca una fila, así que referenciar el modelo externo se rechaza:

```go
totals, _ := goql.Select[CustomerTotal](ctx, e, func(ct *CustomerTotal, x *Order, f *goql.From) bool {
    f.Model = x
    ct.Total = x.Total
    return x.Total > o.Total     // ✗ o pertenece a la consulta externa
})
```

Esa es exactamente la diferencia con una [subconsulta](subqueries.md) usada como valor: `IN` y
`EXISTS` *pueden* correlacionarse; un `WITH` no. Haz un join entre las tablas en su lugar.

## Hacer join con una CTE

`goql.Join` acepta `Query` además de `Model`:

```go
func(s *Summary, t *CustomerTotal, o *Order, from *goql.From, j *goql.Join) bool {
    totals, _ := goql.Select[CustomerTotal](ctx, e, …)
    from.Model = o
    j.Query = totals
    j.Model = t
    j.On = o.Customer.ID == t.Customer
    …
}
```

## Soporte por motor

`WITH` está disponible en SQLite 3.8.3+, en todas las versiones de PostgreSQL y en MySQL 8.0+.

Donde un motor no tiene CTE, goql recurre a una **tabla derivada en línea**:

```sql
SELECT AVG(t."Total") … FROM (SELECT … GROUP BY …) t
```

Mismo resultado. Una definición usada más de una vez se repite, lo que da un peor plan de
ejecución pero es mejor que una consulta que funciona en PostgreSQL y falla en MySQL 5.7.

!!! warning "Sin alternativa para la recursión"
    Una tabla derivada no puede referenciarse a sí misma, así que una
    [consulta recursiva](recursive.md) en un motor sin `WITH` se rechaza directamente en lugar
    de degradarse.

## Lo que no está hecho

Una subconsulta usada solo como **valor** (`IN (…)`) sigue generándose en línea en lugar de
convertirse en un `WITH`. El código Go es idéntico en ambos casos, así que nunca fue una
cuestión de sintaxis —solo de representación— y se descartó a propósito.
