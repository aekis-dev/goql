# Relaciones y precarga

## Los campos de relación llegan nil

Un `SELECT` devuelve una clave foránea, no una fila. goql deja los campos de relación a nil en
lugar de rellenarlos con un objeto vacío, para que «no cargada» siga siendo distinguible de
«cargada pero vacía»:

```go
orders, _ := goql.Select[Order](ctx, e, func(o *Order) bool { return o.Total > 100 })
orders[0].Customer   // nil
```

Para obtener la clave sin cargar la fila, nombra la clave primaria del destino — la columna de
clave foránea ya contiene ese valor, así que no cuesta ningún join:

```go
func(o *Order, p Key) bool { return o.Customer.ID == p.ID }
// → WHERE o."customer_id" = ?
```

## Precarga

Pide las relaciones explícitamente:

```go
orders, err := goql.Select[Order](ctx, e, func(o *Order, pre *goql.Preload) bool {
    pre.Fields = []string{"Customer", "Tags"}
    return o.Total > 500
})

orders[0].Customer.Name   // cargado
orders[0].Tags            // cargado
```

O en una llamada con struct:

```go
orders, err := goql.Search(ctx, e, Order{}, goql.Preload{Fields: []string{"Customer"}})
```

**Cada relación cuesta un número fijo de consultas por lotes, independientemente de cuántas
filas hayan vuelto** — nunca una consulta por fila. No hay N+1:

| Relación | Consultas |
|---|---|
| many2one | 2 (claves, luego filas) |
| one2many | 2 |
| many2many | 2 (filas intermedias, luego destinos) |

La precarga es explícita porque mantiene visible el coste. Cargar todo por defecto traería de
más, y los proxies perezosos exigirían que los campos de relación dejaran de ser
`*Customer` / `[]Tag` corrientes, que es sobre lo que se apoya todo el diseño.

## Valores por defecto del esquema

Un campo marcado `Preload: true` se carga en cada lectura de ese modelo:

```go
&models.Field{Name: "Customer", Column: "customer_id", Preload: true}
```

Una consulta que nombra **cualquier** relación reemplaza por completo esos valores por
defecto:

```go
goql.Preload{Fields: []string{"Tags"}}   // Customer NO se carga, pese al valor por defecto
goql.Preload{}                           // no se carga nada
// (sin opción Preload)                  // se aplican los valores por defecto del modelo
```

El caso vacío es la razón por la que «no cargar nada» se distingue de «no especificado».

## Escribir relaciones

`Create` persiste las relaciones que trae la entidad:

```go
goql.Create(ctx, e, []Order{{
    Total:    1500,
    Customer: customers[0],           // → customer_id
    Tags:     []Tag{*urgent, *vip},   // → dos filas intermedias
}})
```

`Write` las **sincroniza**: las filas listadas se enlazan y cualquier fila enlazada antes que
ya no aparezca se desenlaza:

```go
order.Tags = []Tag{*urgent}      // vip queda desasociada
goql.Write(ctx, e, []Order{*order})
```

En one2many, desenlazar significa poner a NULL la clave foránea del hijo. Cuando esa columna
es `NOT NULL` no se puede vaciar, así que goql devuelve `ErrRelationConstraint` nombrando la
columna en vez de dejar un enlace obsoleto o sacar una violación de restricción del driver.

## Recorrer relaciones en un predicado

Alcanzar un many2one hace join con el destino:

```go
func(o *Order) bool { return o.Customer.Country == "USA" }
```

Una colección se comprueba con `goql.Filter`, que compila a un `EXISTS` correlacionado —
incluida la tabla intermedia en many2many:

```go
func(o *Order) bool {
    return goql.Filter(o.Tags, func(t *Tag) bool { return t.Name == "urgent" })
}
```

Funciona en ambas direcciones:

```go
func(c *Customer) bool {
    return goql.Filter(c.Orders, func(o *Order) bool { return o.Total > 1000 })
}
```

Al ser un predicado y no un join, nunca duplica las filas que filtra, y compone con `||` y
`!`. Ver [Predicados](predicates.md) para el razonamiento.

!!! note "Recorrer no es precargar"
    Un predicado que hace join con `customers` **no** rellena `o.Customer`. El join filtra;
    la precarga rellena. Pide las dos cosas si quieres las dos.

Una ruta puede recorrer cualquier número de saltos many2one — `o.Customer.Company.Country.Code`
atraviesa cuatro tablas y lee una columna de la última. Cada salto es un `INNER JOIN` derivado
de la relación que declaran los modelos.

El verificador de tipos de Go mantiene la ruta en many2one por sí solo: una colección es un
slice, así que `o.Tags.Name` no compila. Eso es lo que garantiza que **una ruta nunca puede
multiplicar filas** — usa [`goql.Filter`](predicates.md) para comprobar una colección, o
[`goql.Join`](joins.md) para unirla deliberadamente.

!!! warning "Cada salto es un inner join"
    Una fila cuya clave foránea sea NULL se descarta, así que una ruta de cuatro saltos reduce
    el resultado cuatro veces. Usa [`goql.Join`](joins.md) con `Type: goql.Left` cuando eso
    importe.
