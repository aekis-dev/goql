# Agregados y proyecciones

Un tipo de resultado que **no es un modelo** convierte la lambda en una proyección: cada
asignación es una columna de salida.

```go
type PriorityTotals struct {
    Priority string
    Orders   int64
    Total    float64
    Largest  float64
}

rows, err := goql.Select[PriorityTotals](ctx, e,
    func(t *PriorityTotals, o *Order, from *goql.From) bool {
        from.Model = o
        t.Priority = o.Priority          // una columna simple → también un término del GROUP BY
        t.Orders   = goql.Count()        // COUNT(*)
        t.Total    = goql.Sum(o.Total)   // SUM(total_amount)
        t.Largest  = goql.Max(o.Total)
        return o.Total > 0
    })
```

```sql
SELECT o."priority" AS "Priority", COUNT(*) AS "Orders",
       SUM(o."total_amount") AS "Total", MAX(o."total_amount") AS "Largest"
FROM "orders" o WHERE o."total_amount" > ? GROUP BY o."priority"
```

`rows` es `[]*PriorityTotals`.

## El modelo se indica, no se infiere

El tipo de resultado no es un modelo, así que hay que decirle a la consulta de dónde leer:

```go
from.Model = o
```

`o` es uno de los **propios parámetros de modelo** de la lambda — apuntando a la declaración
en lugar de repetirla, para que las dos no puedan discrepar. Cualquier otra cosa es un error,
y una proyección que nunca lo dice también lo es.

(La alternativa descartada era «el primer parámetro de modelo es el principal», que es una
convención oscura que habría que conocer.)

## Las funciones de agregado

```go
func Sum[T any](column T) T
func Avg[T any](column T) float64
func Min[T any](column T) T
func Max[T any](column T) T
func Count(column ...any) int64
```

Marcadores que solo se analizan, nunca se ejecutan. Son **funciones** genéricas de paquete y
no métodos de un portador por una razón concreta: `Min` y `Max` deben devolver lo que
recibieron —la marca de tiempo más temprana es una marca de tiempo— y Go prohíbe los
parámetros de tipo en los métodos.

`Sum[T](T) T` responde además, de forma discreta, a la pregunta de los decimales: el resultado
es el tipo que el modelo ya declara.

### El compilador hace buena parte de la comprobación

```go
t.Total = goql.Sum(o.Priority)   // ✗ no compila
```

`Sum[string]` devuelve `string`, que no se puede asignar a un campo `float64`. Lo que sí
compila pero sigue estando mal —sumar texto en un campo de texto— se detecta al analizar,
porque SQLite responde `0` en silencio mientras que PostgreSQL da error.

Un campo que no existe en el tipo de resultado es un error de compilación.

## El GROUP BY se deriva

Las asignaciones que **no** son agregados son las claves de agrupación. Es la propia regla de
SQL, así que la agrupación no puede discrepar de la proyección.

```go
t.Priority = o.Priority      // → GROUP BY o."priority"
t.Total    = goql.Sum(...)   // agregado
```

**Sin asignaciones simples no hay agrupación**: todo el conjunto coincidente se pliega en una
fila:

```go
type Summary struct {
    Orders int64
    Total  float64
}

rows, _ := goql.Select[Summary](ctx, e, func(s *Summary, o *Order, from *goql.From) bool {
    from.Model = o
    s.Orders = goql.Count()
    s.Total  = goql.Sum(o.Total)
    return o.Total > 0
})
// rows[0].Total — una fila, todo el conjunto
```

Para agrupar por algo que no puedes proyectar —una relación, por ejemplo— nómbralo
explícitamente. Ver [Agrupación y HAVING](grouping.md).

## Contar a través de un join

Un join multiplica filas, así que `goql.Count()` genera `COUNT(DISTINCT pk)` siempre que la
consulta haga join — por relación, por participante de modelo o por join explícito. Una
entidad que coincide por dos filas relacionadas se cuenta una vez.

## Toda columna lleva alias

Cada columna de salida lleva como alias el campo en el que aterriza, así que la lectura nunca
depende del orden de las columnas. Eso es también lo que permite a las
[operaciones de conjuntos](set-operations.md) y a las [CTE](cte.md) referirse por nombre a las
columnas de una consulta.

## Agregar sobre un agregado

`AVG(SUM(...))` no es SQL válido. La media de los totales por cliente son dos consultas
apiladas, que es para lo que sirve una [CTE](cte.md):

```go
goql.Select[Summary](ctx, e, func(s *Summary, t *CustomerTotal, from *goql.From) bool {
    totals, _ := goql.Select[CustomerTotal](ctx, e, /* … GROUP BY customer … */)
    from.Query = totals
    from.Model = t
    s.Average = goql.Avg(t.Total)
    return t.Total > 0
})
```

## No disponible

`DISTINCT`, salvo la regla del join de arriba. Funciones de ventana. Cláusulas `FILTER` en los
agregados.
