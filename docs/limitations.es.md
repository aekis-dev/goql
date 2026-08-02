# Limitaciones

Escritas sin adornos a propósito. Si algo de aquí te afecta, es mejor saberlo ya.

## Pruebas

- **La cobertura en vivo es solo SQLite.** El SQL de PostgreSQL y MySQL se genera y se
  verifica por dialecto, pero nunca se ha ejecutado contra esos servidores. El comportamiento
  real del driver —leer `RETURNING`, manejo de marcas de tiempo, modos SQL de MySQL— está sin
  verificar.
- La cobertura de migraciones es igualmente solo SQLite; el SQL de introspección de los otros
  motores se ejercita mediante `PlanAgainst` con un esquema suministrado.

## El lenguaje de consultas

- **`c.Deleted == nil` se rechaza** — `nil` se lee como un identificador. Usa
  `goql.Condition(c.Deleted, "IS NULL")`.
- **No hay funciones SQL.** `COALESCE`, `LOWER`, `ABS`, aritmética de fechas: ninguna tiene
  sintaxis. Usa [SQL directo](raw-sql.md).
- **No hay funciones de ventana**, ni `DISTINCT` más allá del `COUNT(DISTINCT pk)` automático
  cuando la consulta hace join con un participante de cardinalidad desconocida.
- **`goql.Filter` no puede alcanzar una colección a través de otra relación** — su argumento
  debe ser una colección declarada en el modelo consultado.
- **`goql.Filter` no puede apuntar a una tabla que la consulta externa ya usa**, por la misma
  razón de alias que las subconsultas.
- **Una subconsulta no puede usar una tabla que la consulta externa ya usa.** Ambas se
  generarían con el mismo alias; los autojoins necesitan alias por aparición, que el mapa de
  alias no modela.
- **Concatenar dos valores de parámetros se lee como aritmética**, porque un marcador de
  parámetro lleva un nombre y no un tipo. Escribe un lado como columna o literal.
- **Solo los joins internos son implícitos.** Un `LEFT`/`RIGHT`/`FULL` necesita
  [`goql.Join`](joins.md).

## Modelo de datos

- **El borrado lógico está a medias.** `Deleted *time.Time` existe en todos los modelos, pero
  `Delete` borra físicamente y `Search` nunca filtra por él. O lo implementas en tus propios
  predicados o ignoras la columna.
- **`go vet` informa de `copylocks`.** `goql.Model` embebe un `sync.RWMutex` para el
  seguimiento de cambios, y la API recibe `[]T` por valor, así que `[]Customer{*alice}` se
  señala. Es seguro —el mutex protege un seguimiento por entidad que rara vez se comparte—
  pero es ruido que hoy no se puede silenciar.
- **Los valores cero se omiten de un INSERT** en columnas nullables, así que `false`/`0`/`""`
  caen al valor por defecto de la columna. Declara `NotNull`, o usa un puntero, cuando eso
  importe.
- **Las columnas JSON no se pueden asignar desde una struct de parámetros** ni desde un literal
  compuesto en una lambda de actualización, y no hay sintaxis para consultar *dentro* del
  JSON — usa una [columna literal](predicates.md#columnas-literales).

## Migraciones

- **La deriva de índices y restricciones no se compara.** Solo columnas, tipos de columna y
  tablas completas.
- **No hay ficheros de migración**, así que no hay historial reproducible. `goql_migrations`
  es un registro de auditoría.
- Planificar requiere una **base de datos accesible**: la comparación es siempre contra la
  realidad.
- SQLite no puede alterar el tipo de una columna en el sitio, así que un cambio de tipo allí se
  reporta y se rechaza en lugar de emitirse.

## Transacciones

- Sin niveles de aislamiento, sin transacciones de solo lectura, sin manejadores explícitos
  `Begin`/`Commit`/`Rollback`. `Transaction` es la única entrada.
- Sin anidamiento con `SAVEPOINT`: una llamada anidada se une a la transacción exterior.

## Compilaciones de producción

- **Las claves del registro son posicionales.** Añadir, quitar o reordenar una clausura en una
  función desplaza todos los índices posteriores. Regenera antes de cada compilación con
  `-tags prod` — un registro obsoleto puede resolverse en silencio al cuerpo de otra lambda.
  Ver [Compilaciones de producción](production.md).
- **Una lambda de goql anidada dentro de otra clausura no se puede indexar.** El generador la
  omite de forma ruidosa y la llamada en producción falla con `ErrNoCompiledBody`.

## Deliberadamente no construido

Cada una de estas cosas se consideró y se dejó fuera, no se pasó por alto:

- **Un `WITH` para una subconsulta usada solo como valor.** El código Go es idéntico en ambos
  casos, así que no cambia nada semánticamente.
- **Las tablas derivadas como forma de primer nivel.** Sus usos reales están por debajo de las
  funciones de ventana.
- **`SelectInto`.** [`Bind`](raw-sql.md) cubre la lectura en una struct.
- **La carga perezosa de relaciones.** Exigiría que los campos de relación dejaran de ser
  `*Customer` / `[]Tag` corrientes, que es sobre lo que se apoya todo el diseño.
- **Un upsert completo.** `goql.Conflict{Ignore: true}` es la pieza estrecha; un objetivo de
  conflicto más `DO UPDATE SET` es un diseño propio.
