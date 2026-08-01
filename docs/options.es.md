# Opciones de consulta

Los mismos tipos sirven para las dos mitades de la API: **valores finales** en una llamada con
struct, **parámetros declarados** en una lambda.

```go
goql.Search(ctx, e, Customer{Country: "USA"},
    goql.Sort{By: "Age", Desc: true}, goql.Limit{Value: 20})

goql.Select[Customer](ctx, e, func(c *Customer, sort *goql.Sort, limit *goql.Limit) bool {
    sort.By = "Age"
    sort.Desc = true
    limit.Value = 20
    return c.Country == "USA"
})
```

Del lado de la lambda se **analizan, no se ejecutan**, como todo lo demás en un cuerpo. Los
parámetros se clasifican **por tipo**, así que su orden no importa.

!!! info "Por qué las dos formas difieren"
    Una opción queda fijada cuando se escribe la consulta y no tiene ningún valor que
    entregar, así que se asigna dentro del cuerpo. Una [struct de parámetros](params.md) lleva
    datos de tiempo de ejecución y debe pasarse en el sitio de llamada. Flujos de datos
    distintos, no una incoherencia.

## Los portadores

| Tipo | Campos | Se aplica a |
|---|---|---|
| `Sort` | `By string`, `Desc bool` | lecturas |
| `Limit` | `Value int` | lecturas |
| `Offset` | `Value int` | lecturas |
| `Fields` | `Names []string` | lecturas |
| `Preload` | `Fields []string` | lecturas |
| `Group` | `By []string` | [proyecciones](grouping.md) |
| `From` | `Model any`, `Query any` | [proyecciones](aggregates.md), [CTE](cte.md) |
| `Join` | `Model`, `Query`, `On`, `Type` | [joins](joins.md) |
| `Conflict` | `Ignore bool` | [`Insert`](insert-select.md) |

## Sort

`By` es un **nombre de campo Go**, resuelto contra el esquema — una errata es un error, no SQL
inválido.

```go
goql.Search(ctx, e, Customer{}, goql.Sort{By: "Age", Desc: true})
```

Declara varios parámetros `*Sort` para ordenar por varias columnas; se aplican en el orden de
declaración:

```go
func(c *Customer, first *goql.Sort, second *goql.Sort) bool {
    first.By = "Country"
    second.By = "Age"
    second.Desc = true
    return c.Age > 18
}
// → ORDER BY c."country", c."age" DESC
```

## Limit y Offset

```go
goql.Search(ctx, e, Customer{}, goql.Limit{Value: 20}, goql.Offset{Value: 40})
```

Todos los motores soportados exigen un límite antes de un desplazamiento, así que un `Offset`
por sí solo emite un límite abierto — `LIMIT -1` en SQLite, `LIMIT ALL` en PostgreSQL,
`LIMIT 18446744073709551615` en MySQL.

## Fields

Restringe la proyección. La clave primaria se incluye siempre, para que las filas leídas
sigan siendo identificables:

```go
goql.Search(ctx, e, Customer{Country: "USA"}, goql.Fields{Names: []string{"Name"}})
// → SELECT "customers"."id", "customers"."name" FROM "customers" WHERE …
```

Los campos no seleccionados llegan con el valor cero de Go, indistinguible de una columna
realmente vacía. Es un compromiso aceptado.

## Preload

Ver [Relaciones y precarga](relations.md).

```go
goql.Select[Order](ctx, e, func(o *Order, pre *goql.Preload) bool {
    pre.Fields = []string{"Customer", "Tags"}
    return o.Total > 500
})
```

Un `goql.Preload{}` vacío significa «no cargar nada», que es distinto de no pasar ninguno: una
consulta que nombra relaciones **reemplaza** por completo los valores por defecto
`Preload: true` del modelo.

## Reglas y rechazos

Las opciones se **rechazan donde no significan nada**, nunca se ignoran:

- `Sort`, `Limit`, `Offset` y `Preload` en `Exists`, que devuelve un solo valor.
- `Fields` y `Preload` en [`Insert`](insert-select.md) — la proyección sale de las
  asignaciones, y no hay resultado en el que cargar.
- `Conflict` en cualquier cosa que no sea `Insert`.
- Cualquier opción **asignada dentro de una rama** de un `if`/`switch`. Una opción describe la
  consulta entera; aplicar en silencio la opción de una rama a todas las filas sería
  incorrecto.

Un portador declarado y nunca asignado no pasa nada, salvo `Join`, que se reporta — un join
declarado y no descrito es una intención que no se llevó a cabo.
