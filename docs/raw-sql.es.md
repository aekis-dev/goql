# SQL directo

Cuando goql no puede expresar algo —una función de ventana, una extensión del motor, una
sentencia afinada a mano— baja a SQL. Ambas llamadas se unen a la
[transacción](transactions.md) en curso y respetan el contexto de la llamada.

## `Bind` — leer filas en un tipo

```go
type Row struct {
    Country string
    Total   float64
}

rows, err := goql.Bind[Row](ctx, e,
    `SELECT country, SUM(total_amount) AS total
     FROM customers c JOIN orders o ON o.customer_id = c.id
     GROUP BY country HAVING SUM(total_amount) > ?`, 1000)
```

Devuelve `[]*Row`. Las columnas se emparejan con los campos usando el mapeo de columnas del
modelo, así que un tipo de resultado que **es** un modelo se lee exactamente igual que con un
`Select`:

```go
orders, err := goql.Bind[Order](ctx, e, `SELECT * FROM orders WHERE total_amount > ?`, 500)
```

## `Execute` — sentencias

```go
result, err := goql.Execute(ctx, e, `UPDATE orders SET priority = ? WHERE total_amount > ?`,
    "High", 1000)

affected, _ := result.RowsAffected()
id, _ := result.LastInsertId()
```

Devuelve el `sql.Result` real, así que `LastInsertId` está disponible donde el driver lo
soporte.

## Los marcadores de posición son cosa tuya

goql no reescribe el SQL que le pasas, así que usa el estilo de marcador del motor en el que
estés: `?` para SQLite y MySQL, `$1` para PostgreSQL. Si la sentencia debe ser portable,
pregunta al dialecto:

```go
st := e.Dialect().NewStatement()
sqlText := fmt.Sprintf(`SELECT * FROM orders WHERE total_amount > %s`, st.Mark())
```

`Mark()` reparte marcadores en orden, `Marks(n)` devuelve varios separados por comas.

!!! danger "Nunca construyas SQL concatenando valores"
    Pásalos como argumentos. `fmt.Sprintf("… WHERE name = '%s'", name)` es una inyección, y
    goql no puede protegerte de una cadena que nunca analizó.

Entrecomillar un identificador de forma portable:

```go
col := e.Dialect().QuoteIdent("order")   // "order" / `order`
```

## Cuándo recurrir a esto

| Situación | Mejor respuesta |
|---|---|
| una función de ventana, `DISTINCT ON`, una CTE que goql no puede formar | SQL directo |
| una extensión del motor (`ILIKE`, arrays, búsqueda de texto completo) | SQL directo |
| un predicado sobre una ruta JSON | una [columna literal](predicates.md#columnas-literales) dentro de `goql.Condition` |
| un agregado puntual | una [proyección](aggregates.md) — normalmente más corta |
| filtros dinámicos | una [struct de parámetros](params.md) y ramas |

Una cadena literal no se analiza, así que nada comprueba los nombres de columna, nada la
mantiene al día cuando cambian tus modelos, y no te acompañará entre motores. Prefiere la vía
tipada cuando exista.
