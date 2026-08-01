# Dialectos y portabilidad

```go
e := goql.New(db, goql.SQLite{})     // o goql.Postgres{} / goql.MySQL{}
```

El motor se indica explícitamente porque `database/sql` no expone el nombre del driver —
adivinarlo implicaría un type switch que se rompe con drivers envueltos.

## Cómo está estructurado

`query.Spec` recoge solo lo que realmente difiere entre motores; los constructores comunes —el
recorrido del `WHERE`, la recolección de joins, el armado del `SET`— están escritos **una sola
vez** contra él. La divergencia queda confinada en el Spec, así que ningún motor tiene su
propia copia que pueda desincronizarse.

## Qué difiere

| | SQLite | PostgreSQL | MySQL |
|---|---|---|---|
| Comillas de identificadores | `"x"` | `"x"` | `` `x` `` |
| Marcadores de posición | `?` | `$1`, `$2`, … | `?` |
| Claves generadas | `LastInsertId` | `INSERT … RETURNING` | `LastInsertId` |
| Autoincremento | `AUTOINCREMENT` | identity | `AUTO_INCREMENT` |
| Insertar ignorando | `INSERT OR IGNORE` | `ON CONFLICT DO NOTHING` | `INSERT IGNORE` |
| UPDATE con join | `UPDATE … FROM` | `UPDATE … FROM` | `UPDATE … JOIN … SET` |
| Límite abierto | `LIMIT -1` | `LIMIT ALL` | `LIMIT 18446744073709551615` |
| Concatenar cadenas | `\|\|` | `\|\|` | `CONCAT()` |
| Claves foráneas | hay que activarlas | siempre activas | siempre activas |

Dos divergencias son **de forma de sentencia** y no de símbolo: `Create` se bifurca a nivel de
ejecución por `RETURNING`, y un `UPDATE` con join es otra sentencia distinta en MySQL.

## Soporte de funcionalidades

| | SQLite | PostgreSQL | MySQL |
|---|---|---|---|
| `LEFT JOIN` | ✓ | ✓ | ✓ |
| `RIGHT JOIN` | 3.39+ | ✓ | ✓ |
| `FULL JOIN` | 3.39+ | ✓ | **✗** |
| `WITH` | 3.8.3+ | ✓ | 8.0+ |
| `WITH RECURSIVE` | 3.8.3+ | ✓ | 8.0+ |
| `INTERSECT` / `EXCEPT` | ✓ | ✓ | 8.0.31+ |
| DDL transaccional | ✓ | ✓ | **✗** |

Un **tipo de join** no soportado se rechaza al construir la sentencia, con un mensaje que
nombra el motor. La ausencia de `WITH` cae a una tabla derivada, salvo para la recursión, que
se rechaza. El mínimo de versión para `INTERSECT`/`EXCEPT` no se comprueba: un MySQL antiguo
falla en el servidor.

## Orden de los marcadores de posición

Todos los constructores añaden los valores enlazados en el mismo orden en que escriben sus
marcadores, porque PostgreSQL numera sus parámetros. Ese orden se verifica por dialecto sobre
sentencias completas — una definición `WITH` se enlaza antes que la consulta externa, una
proyección antes que el `WHERE`, el `ON` de un join antes que el `WHERE`, y la cola
`LIMIT`/`OFFSET` al final.

## Tipos

El [vocabulario de tipos de columna](models.md#tipos-de-columna) es deliberadamente pequeño y
cada dialecto lo traduce a un tipo físico. Un tipo fuera del conjunto se emite tal cual: la
vía de escape, a costa de fijarse a un motor.

## Escribir modelos portables

- Declara `Type` explícitamente en todo lo que dependa del tipo inferido.
- Usa `TypeJSON` para columnas JSON; SQLite acepta `jsonb` por afinidad de tipos.
- Limita los `Default` a literales o a las expresiones reconocidas
  (`CURRENT_TIMESTAMP`, `NOW()`, …).
- Los `Checks` se pasan tal cual — son SQL, y su portabilidad es cosa tuya.
- Llama a `EnableForeignKeys()` al arrancar: no hace nada donde ya están activas, y es
  necesario en SQLite.

## Estado de las pruebas

!!! warning
    La cobertura contra una base de datos real es **solo SQLite**. El SQL de PostgreSQL y
    MySQL se genera y se verifica por dialecto —comillas, numeración de marcadores,
    `RETURNING`, la forma del `UPDATE` con join, el mapeo de tipos, las variantes de insertar
    ignorando— pero nunca se ha ejecutado contra esos servidores. El comportamiento real del
    driver (leer `RETURNING`, manejo de marcas de tiempo, los modos SQL de MySQL) está sin
    verificar. Considera PostgreSQL y MySQL como «generado correctamente, todavía sin
    demostrar».
