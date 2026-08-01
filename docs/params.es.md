# Valores en tiempo de ejecución (params)

Una lambda no puede usar una variable del ámbito que la rodea — ver
[Lambdas](lambdas.md#consecuencia-1-nada-de-variables-capturadas). Los valores que solo se
conocen en el sitio de llamada viajan en una **struct de parámetros**.

## La forma

Declara un parámetro extra que sea una struct simple, y pasa su valor como argumento final:

```go
type OrderParams struct {
    MinTotal float64
    Priority string
}

orders, err := goql.Select[Order](ctx, e, func(o *Order, p OrderParams) bool {
    return o.Total > p.MinTotal && o.Priority == p.Priority
}, OrderParams{MinTotal: 100, Priority: "High"})
```

```sql
SELECT o.* FROM "orders" o WHERE o."total_amount" > ? AND o."priority" = ?
```

Las referencias a campos se compilan a marcadores de posición, sustituidos por los valores
reales justo antes de ejecutar. El cuerpo analizado sigue siendo reutilizable y cacheable, que
es justo el objetivo: un cuerpo se analiza una vez y —en producción— se compila de antemano,
así que un valor de tiempo de ejecución nunca puede quedar incrustado en él.

## Por qué una struct y no un marcador o un mapa

| Alternativa | Por qué no |
|---|---|
| leer los valores capturados de la clausura | imposible — `reflect` no expone variables libres |
| resolverlos desde el código del llamante | solo funciona cuando el valor es un literal |
| un marcador `goql.P(x)` | más API sin ganancia frente a un simple valor final |
| un mapa `goql.Args{"min": x}` | claves como cadenas, comprobadas en ejecución en vez de por el compilador |

Con una struct, **el compilador de Go comprueba los nombres de los campos**, porque la lambda
es Go corriente y la struct es un tipo real. La comprobación propia de goql solo tiene que
rechazar lo que compila pero la reflexión no puede leer: un campo no exportado.

## Reglas

- **Como mucho una** struct de parámetros por lambda.
- Se pasa **por valor**, que es lo que la distingue de los demás parámetros extra: un
  portador de opciones es `*goql.Sort`, un participante de join es `*Modelo`, y un
  [manejador de fila de CTE](cte.md) es `*Fila`.
- Declarada pero no suministrada → `ErrMissingParams`. Suministrada pero no declarada, o del
  tipo equivocado → `ErrInvalidParams`.
- La posición no importa: los parámetros extra se clasifican por tipo.

```go
// equivalentes
func(o *Order, p OrderParams, sort *goql.Sort) bool
func(o *Order, sort *goql.Sort, p OrderParams) bool
```

## Dónde pueden aparecer los valores

En cualquier sitio donde pueda ir un literal — una comparación, un operador de lista, una
asignación de `Update`, un valor de `INSERT … SELECT`, una [expresión](expressions.md) o
dentro de una [subconsulta](subqueries.md).

```go
type Bump struct {
    Priority string
    Factor   float64
}

goql.Update[Order](ctx, e, func(o *Order, p Bump) {
    if o.Priority == p.Priority {
        o.Total = o.Total * p.Factor
    }
}, Bump{Priority: "High", Factor: 1.1})
```

Con `goql.Condition`:

```go
type Countries struct{ A, B string }

goql.Select[Customer](ctx, e, func(c *Customer, p Countries) bool {
    return goql.Condition(c.Country, "IN", p.A, p.B)
}, Countries{A: "USA", B: "Canada"})
```

## Limitación conocida

Una [columna JSON](models.md#columnas-json) no se puede asignar desde una struct de
parámetros: el valor no está disponible en el momento de construir la sentencia para
serializarlo, así que se rechaza con un error claro en vez de escribirse mal.
