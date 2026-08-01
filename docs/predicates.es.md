# Predicados y condiciones

Un predicado es una lambda que devuelve `bool`. Se convierte en la cláusula `WHERE` de un
`Select`, `Delete`, `Exists` o `Count`.

```go
orders, err := goql.Select[Order](ctx, e, func(o *Order) bool {
    return o.Priority == "High" && o.Total > 1000
})
// → SELECT o.* FROM "orders" o WHERE o."priority" = ? AND o."total_amount" > ?
```

## Operadores

| Go | SQL |
|---|---|
| `==` `!=` | `=` `<>` |
| `<` `<=` `>` `>=` | igual |
| `&&` `\|\|` | `AND` `OR` |
| `+ - * / %` | [aritmética](expressions.md) |

Los paréntesis se conservan, porque la agrupación ya está en el árbol que construyó el
analizador de Go:

```go
return (o.Total > 1000 || o.Priority == "Urgent") && o.ShippingMethod == "Express"
```

## `goql.Condition` — lo que Go no puede escribir

```go
func Condition(field any, op string, values ...any) bool
```

Es un marcador que solo se analiza, nunca se ejecuta, y se combina con `&&`/`||` como
cualquier otro término.

```go
goql.Select[Customer](ctx, e, func(c *Customer) bool {
    return goql.Condition(c.Name, "LIKE", "Ali%") &&
           goql.Condition(c.Country, "IN", "USA", "Canada") &&
           goql.Condition(c.Deleted, "IS NULL")
})
```

| Operador | Valores | Notas |
|---|---|---|
| `=` `<>` `<` `<=` `>` `>=` | uno | igual que el operador de Go |
| `LIKE` `NOT LIKE` | uno | `%` y `_` son los del motor |
| `IN` `NOT IN` | uno o más | o una [subconsulta](subqueries.md) |
| `IS NULL` `IS NOT NULL` | ninguno | |

El operador se comprueba contra una lista permitida **con su aridad** al analizar, así que
`"LIK"`, `IS NULL` con un valor, o `IN` sin ninguno fallan durante el análisis. Un operador
solo puede ser un literal en tu propio código, así que esto sirve para cazar erratas.

Los valores pueden venir de una [struct de parámetros](params.md).

!!! tip "Negación"
    El `!` de Go todavía no tiene caso en el analizador. Niega con el operador: `NOT IN`,
    `NOT LIKE`, `IS NOT NULL` o `<>`.

### Columnas literales

Un **literal de cadena** en la posición del campo se emite tal cual — la vía de escape para
algo que goql no puede modelar, como una ruta JSON. Su corrección y su portabilidad entre
motores son tuyas:

```go
goql.Condition(`o."meta"->>'tier'`, "=", "gold")
```

Cualquier otra cosa en esa posición se resuelve como un campo, así que esto solo aplica a un
literal escrito en el sitio de llamada.

## Ramificación

Una cadena de `if` se compila a condiciones mutuamente excluyentes. En un predicado,
contribuye cada rama que devuelve `true`, unidas con `OR`:

```go
goql.Select[Customer](ctx, e, func(c *Customer) bool {
    if c.Country == "USA" {
        return true
    }
    if c.Age > 60 {
        return true
    }
    return false
})
// → WHERE country = ? OR (NOT (country = ?) AND age > ?)
```

Cada rama lleva la negación de las anteriores, de modo que no pueden solaparse.

Una cláusula de guarda funciona como esperarías — el `return true` final lleva la negación de
todo lo anterior:

```go
func(c *Customer) bool {
    if c.Country == "USA" {
        return false
    }
    return true          // → NOT (country = ?)
}
```

`switch` funciona en ambas formas:

```go
switch c.Country {
case "USA", "Canada":     // valores comparados con la etiqueta, unidos por OR
    return true
default:                  // la negación de todos los casos
    return false
}
```

```go
switch {                  // sin etiqueta: los casos son expresiones booleanas
case c.Age > 60:
    return true
}
```

## Update

Una lambda de `Update` no devuelve nada y en su lugar asigna. Cada rama se convierte en su
propio `UPDATE`:

```go
rows, err := goql.Update[Customer](ctx, e, func(c *Customer) {
    if c.Age > 40 {
        c.Status = "Senior"
    } else {
        c.Status = "Premium"
    }
})
// → UPDATE customers SET status = 'Senior'  WHERE age > ?
//   UPDATE customers SET status = 'Premium' WHERE NOT (age > ?)
```

`rows` es la suma de todas las sentencias, y se ejecutan en una sola transacción.

No asignar nada es un error, no una operación vacía.

## Delete

```go
rows, err := goql.Delete[Order](ctx, e, func(o *Order) bool {
    return o.Priority == "Normal" && o.Total < 100
})
```

## Exists

```go
any, err := goql.Exists[Order](ctx, e, func(o *Order) bool {
    return o.Total > 10000
})
```

Se genera como `SELECT 1 … LIMIT 1` en lugar de `EXISTS(…)`, porque «¿volvió alguna fila?» es
uniforme entre drivers mientras que leer un booleano no lo es.

## Contar

Contar es una [proyección](aggregates.md), no una llamada aparte:

```go
type Tally struct{ N int64 }

rows, err := goql.Select[Tally](ctx, e, func(t *Tally, o *Order, from *goql.From) bool {
    from.Model = o
    t.N = goql.Count()
    return o.Total > 100
})
// rows[0].N
```

Cuando la consulta hace join, `Count()` pasa a ser `COUNT(DISTINCT pk)`, de modo que una fila
que coincide por varias filas relacionadas se cuenta una sola vez.

## Alcanzar relaciones

Recorrer un many2one hace join con la tabla destino:

```go
func(o *Order) bool { return o.Customer.Country == "USA" }
// → INNER JOIN "customers" c ON o."customer_id" = c."id" WHERE c."country" = ?
```

Salvo cuando la ruta termina en la **clave primaria** del destino, que la columna de clave
foránea ya contiene — no hace falta join:

```go
func(o *Order, p Key) bool { return o.Customer.ID == p.ID }
// → WHERE o."customer_id" = ?
```

Iterar sobre un one2many o many2many también hace join:

```go
func(o *Order) bool {
    for _, t := range o.Tags {
        if t.Name == "urgent" {
            return true
        }
    }
    return false
}
// → INNER JOIN "order_tags" … INNER JOIN "tags" t … WHERE t."name" = ?
```

Ver [Joins](joins.md) para modelos sin relación declarada entre ellos.
