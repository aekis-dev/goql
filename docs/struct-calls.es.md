# Llamadas con structs (por ejemplo)

Cinco llamadas describen filas con valores en lugar de predicados: `Create`, `Search`,
`Get`, `Write`, `Remove`. Están separadas de la mitad con lambdas para que las opciones por consulta
se asocien sin ambigüedad.

## Create

```go
customers, err := goql.Create(ctx, e, []Customer{
    {Name: "Alice", Age: 40, Country: "USA"},
    {Name: "Bob", Age: 41, Country: "Canada"},
})
```

Devuelve `[]*Customer` con las claves primarias generadas ya rellenas. **Los punteros
devueltos apuntan al slice que pasaste**, así que las claves son visibles por cualquiera de
las dos vías.

Las relaciones también se persisten:

```go
orders, err := goql.Create(ctx, e, []Order{{
    Total:    1500,
    Customer: customers[0],              // many2one → customer_id
    Tags:     []Tag{*tag1, *tag2},       // many2many → filas intermedias
}})
```

En PostgreSQL la clave vuelve con `INSERT … RETURNING`; en el resto, con `LastInsertId`.

!!! note "Valores cero"
    Un campo nullable con valor cero se omite del `INSERT`, así que se aplica el valor por
    defecto de la columna. Declara el campo `NotNull`, o usa un puntero, cuando haya que
    almacenar explícitamente `false`/`0`/`""`.

## Search

Los campos no cero forman la cláusula `WHERE` — coincidencias exactas combinadas con `AND`:

```go
usa, err := goql.Search(ctx, e, Customer{Country: "USA"})
// → WHERE "customers"."country" = ?

alice, err := goql.Search(ctx, e, Customer{Country: "USA", Name: "Alice"})
// → WHERE "customers"."country" = ? AND "customers"."name" = ?
```

Para una clave primaria, usa [`Get`](#get) en su lugar: `ID` vive en el `goql.Model`
embebido, así que expresar una clave como ejemplo obliga a un literal de struct anidado.

Varios ejemplos producen un `IN` por columna:

```go
some, err := goql.Search(ctx, e, Customer{Country: "USA"}, Customer{Country: "Canada"})
```

La coincidencia es siempre exacta. Los patrones pertenecen al lenguaje de predicados — ver
[`goql.Condition`](predicates.md).

Las [opciones](options.md) son valores finales:

```go
page, err := goql.Search(ctx, e, Customer{Country: "USA"},
    goql.Sort{By: "Age", Desc: true},
    goql.Limit{Value: 20},
    goql.Offset{Value: 40},
    goql.Preload{Fields: []string{"Orders"}},
)
```

## Get

Por clave primaria. `ids` es una sola clave o un slice de ellas:

```go
one, err := goql.Get[Customer](ctx, e, 42)
// → WHERE "id" = ?

some, err := goql.Get[Customer](ctx, e, []int64{1, 2, 3})
// → WHERE "id" IN (?, ?, ?)
```

Una clave que no coincide con nada simplemente no aparece en el resultado, así que un fallo
es un slice vacío y no un error. Las filas vuelven en el orden que decida el motor: pasa
`goql.Sort` para fijar uno.

Las [opciones](options.md) son valores finales, igual que en `Search`, y los `Preload`
declarados en el modelo se aplican del mismo modo:

```go
user, err := goql.Get[Customer](ctx, e, userID, goql.Preload{Fields: []string{"Orders"}})
```

## Write

Persiste **solo lo que cambió** en entidades que goql cargó:

```go
alice := customers[0]
alice.Country = "Canada"

rows, err := goql.Write(ctx, e, []Customer{*alice})
// → UPDATE "customers" SET "country" = ?, "goql_updated" = ? WHERE "id" = ?
```

El seguimiento de cambios es por entidad y se inicializa cuando se lee una fila. Una entidad
construida a mano no tiene contra qué comparar, así que prefiere `Create` para filas nuevas y
[`Update`](predicates.md#update) para ediciones por conjuntos.

Las relaciones también se sincronizan:

```go
order.Tags = []Tag{*urgent}     // quita las etiquetas no listadas, añade las que sí
goql.Write(ctx, e, []Order{*order})
```

En one2many, las filas que ya no aparecen ven su clave foránea puesta a NULL. Cuando esa
columna es `NOT NULL` no se puede vaciar, así que goql devuelve `ErrRelationConstraint`
nombrando la columna en lugar de dejar un enlace obsoleto o sacar un error del driver.

## Remove

Borra por clave primaria:

```go
rows, err := goql.Remove(ctx, e, []Tag{*tag3})
```

Es un borrado físico. `Deleted` existe en `goql.Model` pero todavía no está conectado — ver
[Limitaciones](limitations.md).

## Cuándo usar cada mitad

| Usa una llamada con struct cuando | Usa una lambda cuando |
|---|---|
| tienes la fila en la mano | describes un conjunto de filas |
| los criterios son valores exactos | necesitas `>`, `LIKE`, `IN`, `OR`, joins |
| quieres seguimiento de cambios por fila | quieres una sentencia que afecte a muchas filas |

Se combinan: `Search` para cargar, mutar en Go, `Write` para persistir.
