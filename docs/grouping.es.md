# Agrupación y HAVING

## Agrupación derivada

En una [proyección](aggregates.md), las asignaciones que no son agregados son las claves de
agrupación. No hay nada que declarar:

```go
t.Priority = o.Priority        // → GROUP BY o."priority"
t.Total    = goql.Sum(o.Total)
```

## `goql.Group` — claves adicionales

`Group.By` nombra claves de agrupación **adicionales**, como nombres de campo Go. La lista
final son las claves nombradas seguidas de cualquier columna proyectada que no esté ya entre
ellas.

```go
type CustomerSpend struct {
    Spend float64
}

big, err := goql.Select[CustomerSpend](ctx, e,
    func(c *CustomerSpend, o *Order, from *goql.From, g *goql.Group) bool {
        from.Model = o
        g.By = []string{"Customer"}
        c.Spend = goql.Sum(o.Total)
        return goql.Sum(o.Total) > 1000
    })
```

```sql
SELECT SUM(o."total_amount") AS "Spend" FROM "orders" o
GROUP BY o."customer_id" HAVING SUM(o."total_amount") > ?
```

Dos razones para nombrar una clave en vez de proyectarla:

- **No puedes proyectarla.** Un many2one es un `*Customer` en Go, así que
  `t.Customer = o.Customer` no compilará contra un campo de resultado `int64`.
- **No la quieres en el resultado.** Agrupar por una columna que no seleccionas.

Es **aditivo**, no autoritativo: proyectar una columna siempre agrupa por ella, así que ambas
cosas no pueden contradecirse. Nombrar claves sin agregar nada es un error — emitiría un
`GROUP BY` sin nada que plegar.

## HAVING

Una comparación cuyo lado izquierdo es un **agregado** filtra grupos en lugar de filas. Un
mismo predicado produce las dos cláusulas:

```go
return o.Total > 100 && goql.Sum(o.Total) > 1000
//     └── WHERE          └── HAVING
```

```sql
… WHERE o."total_amount" > $1 GROUP BY … HAVING SUM(o."total_amount") > $2
```

goql recorre el árbol de condiciones y separa las hojas con agregados de los filtros de fila.
Los marcadores de posición mantienen el orden de emisión: el valor del `WHERE` se enlaza antes
que el del `HAVING`.

### Qué se rechaza

- **Un agregado combinado con `||`, o negado.** Un filtro previo a la agregación no puede
  unirse con `OR` a uno posterior — no hay equivalente en SQL, así que es un error de análisis
  que nombra la combinación en vez de una suposición.
- **Un agregado a la derecha de una comparación.** `1000 < goql.Sum(o.Total)` se rechaza con
  un mensaje que pide ponerlo a la izquierda, para que una condición siempre se lea como el
  grupo que se está filtrando.

## Ordenar por un agregado

Ordena por el **campo del resultado**, ya que toda columna proyectada lleva alias:

```go
func(t *PriorityTotals, o *Order, from *goql.From, sort *goql.Sort) bool {
    from.Model = o
    t.Priority = o.Priority
    t.Total    = goql.Sum(o.Total)
    sort.By    = "Total"
    sort.Desc  = true
    return o.Total > 0
}
```
