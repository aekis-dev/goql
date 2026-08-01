# Declarar modelos

Un modelo es una struct de Go que embebe `goql.Model`, más una llamada a `models.AddModel` en
`init()`.

```go
type Customer struct {
    goql.Model
    Name    string
    Age     int
    Country string
    Orders  []Order
}

func init() {
    err := models.AddModel(&Customer{}, "customers",
        &models.Field{Name: "Name", Type: models.TypeVarchar, Size: 100, NotNull: true},
        &models.Field{Name: "Age"},
        &models.Field{Name: "Country", Index: true},
        &models.Field{Name: "Orders", OneToMany: &models.OneToMany{Ref: "customer_id"}},
    )
    if err != nil {
        panic(err)
    }
}
```

## Qué aporta `goql.Model`

| Campo | Columna | Notas |
|---|---|---|
| `ID` | `id` | `int64`, clave primaria, autoincremental |
| `Created` | `goql_created` | se fija al insertar |
| `Updated` | `goql_updated` | se fija en cada escritura |
| `Deleted` | `goql_deleted` | `*time.Time`; ver [Limitaciones](limitations.md) — el borrado lógico está a medias |

También lleva el seguimiento de cambios por entidad, que es lo que permite a
[`Write`](struct-calls.md) persistir solo las columnas que realmente cambiaron.

!!! warning "`copylocks`"
    `goql.Model` embebe un `sync.RWMutex`, así que `go vet` señala `[]Customer{*alice}`. La
    API recibe `[]T` por valor, de modo que esto forma parte de la superficie pública. Ver
    [Limitaciones](limitations.md).

## Por qué declaraciones y no etiquetas de struct

Las etiquetas quedan libres para la serialización, y la definición de un campo puede usar una
constante, una variable o una función auxiliar — cosa que una etiqueta no puede. También
significa que el registro se construye ejecutando código, que es la razón por la que **hay que
importar el paquete que declara un modelo** para que ese modelo exista.

## Referencia de campos

```go
&models.Field{
    Name:   "Total",          // nombre del campo Go (obligatorio)
    Column: "total_amount",   // por defecto, snake_case(Name)
    Type:   models.TypeDecimal,
    Precision: 10, Scale: 2,  // Size para varchar/bytes

    NotNull: true,
    Unique:  true,
    Default: "Normal",
    Checks:  []string{"total_amount > 0"},
    Comment: "valor del pedido",
    Collation: "NOCASE",

    Index:   true,            // o "idx_nombre" para compartir un índice entre campos
    Preload: true,            // cargar esta relación en cada lectura
}
```

### Tipos de columna

Un vocabulario deliberadamente pequeño y portable. Cada dialecto lo traduce al tipo físico de
su motor.

| goql | SQLite | PostgreSQL | MySQL |
|---|---|---|---|
| `TypeInteger` | INTEGER | INTEGER | INT |
| `TypeBigInt` | INTEGER | BIGINT | BIGINT |
| `TypeReal` | REAL | REAL | FLOAT |
| `TypeDouble` | REAL | DOUBLE PRECISION | DOUBLE |
| `TypeDecimal` | NUMERIC | NUMERIC(p,s) | DECIMAL(p,s) |
| `TypeText` | TEXT | TEXT | TEXT |
| `TypeVarchar` | TEXT | VARCHAR(n) | VARCHAR(n) |
| `TypeBoolean` | BOOLEAN | BOOLEAN | BOOLEAN |
| `TypeTimestamp` | TIMESTAMP | TIMESTAMP(p) | DATETIME(p) |
| `TypeBytes` | BLOB | BYTEA | BLOB |
| `TypeJSON` | afinidad TEXT | JSONB | JSON |

Si dejas `Type` vacío, se infiere del tipo Go. Un tipo fuera del vocabulario se emite tal
cual: la vía de escape, a costa de fijar el modelo a un motor.

### Valores por defecto

Un valor por defecto de tipo cadena se entrecomilla como literal, así que `Default: "Active"`
emite `DEFAULT 'Active'`. Una expresión SQL reconocida pasa sin comillas:
`CURRENT_TIMESTAMP`, `CURRENT_DATE`, `CURRENT_TIME`, `NOW()`, `NULL`. Los booleanos se
representan como `TRUE`/`FALSE`.

### Campos nulos

Declara un puntero donde «sin valor» deba distinguirse de un cero explícito:

```go
Nickname *string
```

Los punteros son **opcionales a propósito**. Hacer puntero cada campo obligaría a
`goql.Ptr(40)` en cada construcción y a `*c.Age > 40` en cada predicado — un coste
permanente y enorme para resolver un problema que afecta a unas pocas columnas.

## Relaciones

### many2one

Un puntero a otro modelo. La columna de clave foránea vive en esta tabla.

```go
type Order struct {
    goql.Model
    Customer *Customer
}

&models.Field{Name: "Customer", Column: "customer_id", NotNull: true}
```

`Column` nombra la clave foránea; sin ella, `snake_case(Name) + "_id"`.

### one2many

Un slice, con `Ref` nombrando la columna de clave foránea **en la otra tabla**.

```go
type Customer struct {
    goql.Model
    Orders []Order
}

&models.Field{Name: "Orders", OneToMany: &models.OneToMany{Ref: "customer_id"}}
```

### many2many

```go
type Order struct {
    goql.Model
    Tags []Tag
}

&models.Field{Name: "Tags", ManyToMany: &models.ManyToMany{
    Table:  "order_tags",   // tabla intermedia
    Column: "order_id",     // clave foránea de este modelo en ella
    Ref:    "tag_id",       // clave foránea del destino en ella
}}
```

`CreateTables` crea también la tabla intermedia.

### Autorreferencia

Un modelo puede apuntar a sí mismo — la forma que recorre una
[consulta recursiva](recursive.md):

```go
type Category struct {
    goql.Model
    Name     string
    Parent   *Category
    Children []Category
}

&models.Field{Name: "Parent", Column: "parent_id"},
&models.Field{Name: "Children", OneToMany: &models.OneToMany{Ref: "parent_id"}},
```

## Leer una clave foránea

Los campos de relación llegan **nil** salvo que se [precarguen](relations.md) — una clave
foránea es una clave, no una fila, y dejarla nil mantiene distinguible «no cargada» de
«cargada pero vacía».

Para usar la clave en sí, nombra la clave primaria del destino a través de la relación:

```go
goql.Select[Order](ctx, e, func(o *Order, p Key) bool {
    return o.Customer.ID == p.ID
})
// → WHERE o."customer_id" = ?     (sin join: la columna ya contiene ese valor)
```

`o.Customer.ID` se resuelve a la columna local, así que no cuesta nada y se compara contra un
valor simple.

## Columnas JSON

Declara `TypeJSON` y un campo struct, map o slice hace ida y vuelta como JSON:

```go
type Widget struct {
    goql.Model
    Meta map[string]any
}

&models.Field{Name: "Meta", Type: models.TypeJSON}
```

Declara siempre el tipo explícitamente para que el modelo siga siendo portable — SQLite acepta
`jsonb` por afinidad de tipos.

!!! note
    Una columna JSON todavía no se puede asignar desde una struct de parámetros ni desde un
    literal compuesto en una lambda de actualización, y consultar *dentro* del JSON no tiene
    sintaxis propia. Usa [`goql.Condition` con una columna literal](predicates.md#columnas-literales)
    como vía de escape.

## Índices

```go
&models.Field{Name: "Country", Index: true}                  // idx_customers_country
&models.Field{Name: "Country", Index: "idx_geo"}             // con nombre
&models.Field{Name: "City",    Index: "idx_geo"}             // compuesto: mismo nombre
```

`Unique: true` en el campo hace que el índice sea único.

## Reglas del registro

- Un `AddModel` por tipo. Un segundo registro es un error, no una sobrescritura silenciosa.
- Los nombres de tipo deben ser únicos entre paquetes: las consultas analizadas nombran su
  modelo por el nombre del tipo, así que dos tipos `Invoice` se resolverían al que el registro
  encontrara primero.
- `models.ErrNotRegistered` nombra la causa probable: no se importó el paquete.
