# Operaciones de conjuntos

`UNION`, `UNION ALL`, `INTERSECT` y `EXCEPT` combinan consultas enteras. Las ramas se enlazan a
nombres dentro de una lambda, y la lambda devuelve su combinación.

```go
type Movement struct {
    Ref    string
    Amount float64
}

movements, err := goql.Select[Movement](ctx, e, func(m *Movement, sort *goql.Sort) bool {
    sort.By = "Amount"
    sort.Desc = true

    live, _ := goql.Select[Movement](ctx, e,
        func(m *Movement, o *Order, from *goql.From) bool {
            from.Model = o
            m.Ref = o.Priority
            m.Amount = o.Total
            return o.Total > 0
        })

    archived, _ := goql.Select[Movement](ctx, e,
        func(m *Movement, a *OrderArchive, from *goql.From) bool {
            from.Model = a
            m.Ref = a.Reason
            m.Amount = a.Total
            return a.Total > 0
        })

    return goql.Union(live, archived)
})
```

```sql
SELECT o."priority" AS "Ref", o."total_amount" AS "Amount" FROM "orders" o WHERE … > ?
UNION
SELECT a."reason" AS "Ref", a."total" AS "Amount" FROM "order_archives" a WHERE … > ?
ORDER BY "Amount" DESC
```

## Los marcadores

```go
func Union[T any](branches ...[]*T) bool
func UnionAll[T any](branches ...[]*T) bool
func Intersect[T any](branches ...[]*T) bool
func Except[T any](branches ...[]*T) bool
```

Son variádicos, así que más de dos ramas se combinan en una sola llamada. SQL evalúa de
izquierda a derecha.

`Union` elimina duplicados; `UnionAll` no, y es más barato.

## Por qué esta forma

De enlazar las ramas a nombres, en lugar de pasar un slice de lambdas a una función de nivel
superior, se derivan tres cosas:

- **El compilador comprueba la compatibilidad entre ramas.**
  `Union[T](branches ...[]*T) bool` fuerza un único tipo de resultado en todas ellas.
- **Las opciones no necesitan un caso especial.** `Sort` y `Limit` se declaran en la lambda
  externa y se aplican a la combinación, porque la lambda externa *es* la combinación.
- **Enlazar una rama es la sintaxis de [subconsulta](subqueries.md) que ya existía.**

## Qué se comprueba

- **Todas las ramas deben producir las mismas columnas.** El compilador impone un único tipo
  de resultado; goql además rechaza una rama que rellene un *subconjunto* distinto de sus
  campos, cosa que SQL alinearía por posición y respondería en silencio.
- **El `ORDER BY` nombra una columna proyectada**, no un campo de modelo — no hay un único
  modelo contra el que resolver. Nombrar una columna que ninguna rama selecciona es un error.
- Una llamada de agregado (`Exists`) como rama se rechaza: las operaciones de conjuntos
  combinan filas.

## La lambda externa no tiene modelo

Una lambda que combina no declara modelo ni proyección — sus ramas llevan ambas cosas. Solo
declara las opciones que se aplican a la combinación.

## Ramas sobre la misma tabla

Cada rama recibe **sus propios alias de tabla**, así que una unión de dos consultas sobre la
misma tabla funciona:

```go
goql.Select[Movement](ctx, e, func(m *Movement) bool {
    high, _ := goql.Select[Movement](ctx, e, /* … Order … */)
    low, _  := goql.Select[Movement](ctx, e, /* … Order … */)
    return goql.UnionAll(high, low)
})
```

El contador de marcadores de posición se comparte entre ramas, que es lo que mantiene correcta
la numeración de PostgreSQL (`> $1 … <= $2`).

## Soporte por motor

`UNION` y `UNION ALL` son universales. `INTERSECT` y `EXCEPT` requieren **MySQL 8.0.31+** — en
un servidor anterior fallan en la base de datos, no al analizar.
