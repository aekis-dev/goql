# Migraciones

goql **no tiene ficheros de migración**. Los modelos son la fuente de verdad, y una migración
es una comparación entre ellos y la base de datos en vivo.

## Crear tablas

Para tests, demos y bases de datos nuevas:

```go
err := e.CreateTables(&Customer{}, &Order{}, &Tag{})
```

Crea tablas, índices y tablas intermedias de many2many, todo con `IF NOT EXISTS`. No modifica
nada que ya exista.

## Planificar un cambio

```go
plan, err := e.MigrationPlan(ctx, []models.Entity{&Customer{}, &Order{}}, nil)
if err != nil {
    return err
}

for _, c := range plan.Changes {
    fmt.Printf("%s: %s\n", c.Kind, c.Detail)
    if c.Destructive {
        fmt.Println("  ⚠ esto puede perder datos")
    }
}
for _, q := range plan.Questions {
    fmt.Println(q.Prompt)
    for _, opt := range q.Options {
        fmt.Printf("  %s — %s\n", opt.Value, opt.Label)
    }
}
```

La comparación es contra una base de datos **en vivo**, leída mediante introspección por
dialecto. Planificar necesita por tanto un servidor accesible: el compromiso aceptado a cambio
de comparar siempre contra la realidad y no contra una instantánea guardada.

Solo se inspeccionan las tablas **declaradas**. Una base de datos suele tener tablas de las
que goql no sabe nada; una tabla ausente de los modelos nunca se lee ni se propone eliminar.

## La ambigüedad se pregunta, nunca se adivina

Una columna que desapareció mientras otra apareció es indistinguible entre un renombrado y un
borrar-y-añadir — solo la intención las separa. Cada columna así produce una `Question`:

| Respuesta | Efecto |
|---|---|
| `rename:NuevoNombre` | conserva los datos |
| `drop` | los descarta — marcado `Destructive` |
| `skip` | deja la columna en paz |

Un **cambio de tipo** también se pregunta siempre, y se marca como destructivo: si trunca o no
depende de la dirección, que los tipos por sí solos no dicen.

Añadir una columna no es ambiguo y no necesita pregunta. Su definición se relaja para quitar
`NOT NULL` y `UNIQUE`, ya que ninguno de los dos se puede añadir a una tabla con filas
existentes sin un valor por defecto.

## Aplicar

```go
decisions := map[string]string{
    "customers.old_name": "rename:Nickname",
    "orders.legacy":      "drop",
}

summary, err := e.Migrate(ctx, entities, decisions)
```

- **Aplicar con algo sin responder devuelve `ErrUnresolvedQuestions` y no cambia nada.**
- `Migrate` **vuelve a planificar desde la base de datos en vivo** en lugar de fiarse de un
  plan devuelto por un cliente, así que un esquema que se movió desde que se mostró el plan no
  puede migrarse con supuestos obsoletos.
- Donde el motor soporta DDL transaccional (SQLite, PostgreSQL) un fallo deshace todo y
  `summary.Rolled` lo indica. En MySQL cada sentencia DDL confirma al ejecutarse, así que el
  resumen informa de hasta dónde llegó y hay que volver a lanzarlo.

`goql_migrations` registra una fila por cambio aplicado con su sentencia — un **registro de
auditoría**, no un libro de ficheros aplicados. No hay historial reproducible, y un entorno
nuevo arranca desde los modelos.

## La CLI

`tools/goqlmigrate` conduce el flujo interactivo. Vuelve a planificar tras cada respuesta,
porque resolver una pregunta puede cambiar la siguiente: un renombrado consume un candidato
que otra pregunta posterior habría ofrecido.

Habla con el **socket de migración de tu aplicación en ejecución**, porque solo ese proceso
tiene los modelos registrados:

```bash
go run ./tools/goqlmigrate -socket /run/goql-migrate.sock -token "$GOQL_MIGRATE_TOKEN"
```

Muestra el plan, pregunta lo que el esquema no puede responder por sí solo, e imprime lo que
ocurrió.

## Comparación de tipos

«El tipo que quiere el modelo» y «el tipo que informa la base de datos» se escriben distinto
aunque sean idénticos, así que ambos lados se normalizan por motor antes de compararse:

- **PostgreSQL** hace introspección con `pg_catalog` y `format_type`, porque `data_type` omite
  los parámetros y una precisión no se puede reensamblar: PostgreSQL la escribe *dentro* del
  nombre (`timestamp(6) without time zone`).
- **MySQL** usa `column_type`, que ya lleva los parámetros, más alias para lo que guarda con
  otro nombre — sobre todo `BOOLEAN`, que informa como `tinyint(1)`.
- **SQLite** normaliza a la **afinidad de columna** que el motor realmente aplica, de modo que
  goql nunca propone un cambio que SQLite trataría como nulo.

## El socket remoto

Actívalo en el proceso que posee los modelos:

```go
socket, err := e.NewMigrateSocket(
    []models.Entity{&Customer{}, &Order{}},
    goql.MigrateSocketConfig{Path: "/run/goql-migrate.sock", Token: os.Getenv("GOQL_MIGRATE_TOKEN")},
)
if err != nil {
    log.Fatal(err)
}
go socket.Serve()
```

Es deliberadamente incómodo de activar: **apagado por defecto**, un token obligatorio sin valor
por defecto, solo dominio Unix, `chmod 0600` y una línea de log ruidosa al arrancar. Puede
aplicar DDL, así que es un canal de control dentro de tu proceso. Un token incorrecto recibe
403.

## Lo que no cubre

**La deriva de índices y restricciones no se compara** — solo columnas, tipos de columna y
tablas completas. Añadir un índice a un modelo no lo detectará una migración; créalo tú o
borra y recrea la tabla.

La cobertura de migraciones en vivo es solo SQLite, como el resto de la suite: el SQL de
introspección de PostgreSQL y MySQL está escrito a partir del comportamiento documentado y se
ejercita mediante `PlanAgainst`, pero no se ha ejecutado contra un servidor real.
