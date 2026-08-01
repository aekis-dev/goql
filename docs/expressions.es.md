# Expresiones

La aritmética y la concatenación de cadenas están disponibles en cualquier sitio donde vaya un
valor. No se almacena nada: la expresión se emite en línea y el motor la evalúa por fila.

## Operadores

`+`  `-`  `*`  `/`  `%`

La agrupación se conserva, porque el analizador de Go ya construyó el árbol:

```go
return o.Total * (o.Total + 1) > 100
// → WHERE (o."total_amount" * (o."total_amount" + ?)) > ?
```

## Dónde pueden aparecer

**En un predicado**, en cualquiera de los dos lados:

```go
goql.Select[Order](ctx, e, func(o *Order) bool {
    return o.Total * 2 > 100
})
```

**En un `Update`:**

```go
goql.Update[Order](ctx, e, func(o *Order) {
    o.Total = o.Total * 1.1
})
// → UPDATE "orders" SET "total_amount" = ("orders"."total_amount" * ?)
```

**En un [`INSERT … SELECT`](insert-select.md):**

```go
goql.Insert[OrderArchive](ctx, e, func(a *OrderArchive, o *Order) {
    a.Total = o.Total * 100
})
```

**En una [proyección](aggregates.md):**

```go
type Bucket struct {
    Band   float64
    Orders int64
}

goql.Select[Bucket](ctx, e, func(t *Bucket, o *Order, from *goql.From) bool {
    from.Model = o
    t.Band = o.Total / 1000
    t.Orders = goql.Count()
    return o.Total > 0
})
// → SELECT (o."total_amount" / ?) AS "Band", COUNT(*) AS "Orders" …
//   GROUP BY (o."total_amount" / ?)
```

Una expresión proyectada es un término del `GROUP BY`, porque SQL agrupa por la expresión y no
por el alias.

Una constante también se proyecta — lo que necesita una [consulta recursiva](recursive.md)
para su columna de profundidad:

```go
t.Depth = 0     // → ? AS "Depth"
```

## Concatenación de cadenas

Go escribe la concatenación con `+`, así que cuál de las dos quisiste decir se decide a partir
de los tipos de los operandos:

```go
t.Label = o.Priority + " / " + o.ShippingMethod
```

| Motor | Se genera como |
|---|---|
| SQLite | `((o."priority" \|\| ?) \|\| o."shipping_method")` |
| PostgreSQL | igual |
| MySQL | `CONCAT(CONCAT(o.`priority`, ?), o.`shipping_method`)` |

MySQL necesita `CONCAT`: lee `||` como un OR lógico salvo que esté activo `PIPES_AS_CONCAT`, y
un `+` a secas **convierte ambos lados a números y responde 0** — silenciosamente incorrecto
en lugar de dar un error, que es por lo que esto se decide al analizar y no cambiando un
símbolo.

!!! warning "Parámetros y concatenación"
    Una referencia a una [struct de parámetros](params.md) lleva un nombre, no un tipo, así
    que concatenar *dos* valores de parámetros se lee como aritmética. Escribe un lado como
    columna o literal y se resuelve correctamente.

La aritmética sobre una columna de texto (`o.Priority * 2`) se rechaza al analizar.

## Lo que SQL hace y Go no

- **La división entera trunca** en ambos, así que `/` coincide.
- **NULL se propaga**: `prev.Depth + 1` sobre una columna NULL da NULL, no 1. Eso es SQL, y
  goql no lo disimula.

## No disponible

Funciones SQL — `COALESCE`, `LOWER`, `ABS`, aritmética de fechas. Necesitan un vocabulario de
marcadores y una decisión propia sobre portabilidad. Mientras tanto usa
[SQL directo](raw-sql.md) o una [columna literal](predicates.md#columnas-literales).
