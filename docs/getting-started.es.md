# Primeros pasos

## Instalación

```bash
go get github.com/aekis-dev/goql
```

También necesitas un driver. Los ejemplos usan SQLite:

```bash
go get github.com/mattn/go-sqlite3
```

## Declarar un modelo

Los metadatos del esquema se declaran de forma imperativa en `init()`, no con etiquetas de
struct: las etiquetas quedan libres para la serialización, y la definición de un campo puede
usar una constante o una función auxiliar.

```go
package models

import (
    "github.com/aekis-dev/goql"
    "github.com/aekis-dev/goql/models"
)

type Customer struct {
    goql.Model // ID, Created, Updated, Deleted + seguimiento de cambios
    Name    string
    Age     int
    Country string
    Orders  []Order
}

func init() {
    err := models.AddModel(&Customer{}, "customers",
        &models.Field{Name: "Name", Type: models.TypeVarchar, Size: 100, NotNull: true, Index: true},
        &models.Field{Name: "Age", Checks: []string{"age >= 0"}},
        &models.Field{Name: "Country"},
        &models.Field{Name: "Orders", OneToMany: &models.OneToMany{Ref: "customer_id"}},
    )
    if err != nil {
        panic(err)
    }
}
```

```go
type Order struct {
    goql.Model
    Total    float64
    Priority string
    Customer *Customer
    Tags     []Tag
}

func init() {
    err := models.AddModel(&Order{}, "orders",
        &models.Field{Name: "Total", Column: "total_amount", Type: models.TypeDecimal,
            Precision: 10, Scale: 2, NotNull: true},
        &models.Field{Name: "Priority", Type: models.TypeVarchar, Size: 20, Default: "Normal"},
        &models.Field{Name: "Customer", Column: "customer_id", NotNull: true},
        &models.Field{Name: "Tags", ManyToMany: &models.ManyToMany{
            Table: "order_tags", Column: "order_id", Ref: "tag_id"}},
    )
    if err != nil {
        panic(err)
    }
}
```

!!! warning "Hay que importar el paquete"
    `AddModel` se ejecuta en `init()`. Un modelo solo existe una vez que se ha importado el
    paquete que lo declara; si no, obtienes `ErrNotRegistered`, cuyo mensaje dice exactamente
    esto.

Consulta [Declarar modelos](models.md) para el vocabulario completo de campos.

## Abrir un engine

```go
db, err := sql.Open("sqlite3", "app.db")
if err != nil {
    log.Fatal(err)
}
defer db.Close()

e := goql.New(db, goql.SQLite{})   // o goql.Postgres{} / goql.MySQL{}
```

El dialecto se indica explícitamente porque `database/sql` no expone el nombre del driver;
adivinarlo implicaría un type switch que se rompe con drivers envueltos.

```go
if err := e.EnableForeignKeys(); err != nil {   // no hace nada donde ya están activas
    log.Fatal(err)
}
if err := e.CreateTables(&models.Customer{}, &models.Order{}); err != nil {
    log.Fatal(err)
}
```

`CreateTables` va bien para tests y demos. Para un esquema que evoluciona, consulta
[Migraciones](migrations.md).

## Escribir y leer

```go
ctx := context.Background()

customers, err := goql.Create(ctx, e, []models.Customer{
    {Name: "Alice", Age: 40, Country: "USA"},
    {Name: "Bob", Age: 41, Country: "Canada"},
})
// customers es []*models.Customer, con los IDs generados ya rellenos

orders, err := goql.Create(ctx, e, []models.Order{
    {Total: 1500, Priority: "Normal", Customer: customers[0]},
})
```

Leer por ejemplo, o por predicado:

```go
usa, err := goql.Search(ctx, e, models.Customer{Country: "USA"})

big, err := goql.Select[models.Order](ctx, e, func(o *models.Order) bool {
    return o.Total > 1000 && o.Customer.Country == "USA"
})
```

Ambas devuelven `[]*T`.

## Actualizar y borrar

```go
rows, err := goql.Update[models.Order](ctx, e, func(o *models.Order) {
    if o.Total > 1000 {
        o.Priority = "High"
    }
})

rows, err = goql.Delete[models.Order](ctx, e, func(o *models.Order) bool {
    return o.Priority == "Normal"
})
```

O a través de entidades cargadas, lo que persiste solo lo que cambió:

```go
customers[0].Country = "Canada"
rows, err := goql.Write(ctx, e, []models.Customer{*customers[0]})

rows, err = goql.Remove(ctx, e, []models.Customer{*customers[1]})
```

## La forma de la API

Dos mitades. Las llamadas con structs describen filas **por ejemplo**; las llamadas con
lambdas las describen **por predicado**. Un modelo y una operación por llamada.

|            | struct   | lambda                     |
| ---------- | -------- | -------------------------- |
| crear      | `Create` | `Insert` (INSERT … SELECT) |
| leer       | `Search` | `Select`, `Exists`         |
| actualizar | `Write`  | `Update`                   |
| borrar     | `Remove` | `Delete`                   |

Toda llamada recibe un `context.Context` como primer argumento. `e.Transaction(...)` agrupa
varias — consulta [Transacciones](transactions.md).

## Antes de compilar un binario

En desarrollo, los cuerpos de las lambdas se analizan desde el código fuente en tiempo de
ejecución. Un binario publicado no tiene código fuente, así que una compilación con
`-tags prod` lee un registro generado de antemano:

```bash
go generate ./...
go build -tags prod ./...
```

Esto no es opcional y el registro se indexa por posición. Lee
[Compilaciones de producción](production.md) antes de publicar.

## Siguiente

- [Las lambdas se analizan, no se ejecutan](lambdas.md) — las reglas que se derivan del
  contrato central
- [Predicados y condiciones](predicates.md) — todo lo que puede decir un `WHERE`
- [Opciones de consulta](options.md) — ordenación, paginación, proyección
