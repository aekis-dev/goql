# goql

Un ORM para Go donde las consultas se escriben como funciones Go corrientes.

```go
orders, err := goql.Select[Order](ctx, e, func(o *Order) bool {
    return o.Customer.Country == "USA" && o.Total > 1000
})
```

Eso genera un `SELECT` con un join sobre `customers`. Los resultados llegan como `[]*Order`
— sin conversiones de tipo, sin un DSL de cadenas, y una errata en el nombre de un campo es
un error de compilación.

## Lo único que hay que saber

**Los cuerpos de las lambdas se analizan, no se ejecutan.**

La función de arriba nunca se llama. goql localiza su código fuente, lo analiza con `go/ast`
y compila las sentencias a SQL. Se ve más claro en una actualización:

```go
rows, err := goql.Update[Order](ctx, e, func(o *Order) {
    if o.Total > 1000 {
        o.Priority = "High"
    } else {
        o.Priority = "Normal"
    }
})
```

Nada se muta en Go. Las asignaciones describen cláusulas `SET`, el `if` describe un `WHERE`,
y cada rama se convierte en su propio `UPDATE` con condiciones mutuamente excluyentes.

Todo lo sorprendente de goql se deriva de ese único hecho. Lee
[Las lambdas se analizan, no se ejecutan](lambdas.md) antes que nada: explica por qué existe
la struct de parámetros, por qué existe `goql.Condition` y por qué una lambda no puede usar
una variable del ámbito que la rodea.

## Qué puede expresar

| | |
|---|---|
| [Predicados](predicates.md) | comparaciones, `&&`/`||`, `if`/`else`, `switch`, `LIKE`, `IN`, `IS NULL` |
| [Relaciones](relations.md) | recorrido (`o.Customer.Country`), precarga por lotes, sin N+1 |
| [Joins](joins.md) | implícitos entre modelos declarados, o explícitos con `INNER`/`LEFT`/`RIGHT`/`FULL` |
| [Agregados](aggregates.md) | `SUM`/`AVG`/`MIN`/`MAX`/`COUNT` sobre un tipo de resultado propio |
| [Agrupación](grouping.md) | `GROUP BY` derivado de la proyección, `HAVING` separado del `WHERE` |
| [Subconsultas](subqueries.md) | una llamada goql escrita dentro de una lambda, correlacionada o no |
| [Operaciones de conjuntos](set-operations.md) | `UNION`, `UNION ALL`, `INTERSECT`, `EXCEPT` |
| [CTE](cte.md) | leer de una consulta con nombre, incluidos recorridos [recursivos](recursive.md) de jerarquías |
| [Expresiones](expressions.md) | aritmética y concatenación de cadenas en cualquier posición de valor |
| [INSERT … SELECT](insert-select.md) | construir filas a partir de filas que ya están en la base de datos |
| [Migraciones](migrations.md) | introspección en vivo, ambigüedad resuelta preguntando |

Portable entre **SQLite**, **PostgreSQL** y **MySQL** — consulta
[Dialectos](dialects.md) para ver qué difiere y qué rechaza cada motor.

## Instalación

```bash
go get github.com/aekis-dev/goql
```

Empieza por [Primeros pasos](getting-started.md) y sigue con [Lambdas](lambdas.md).

## Estado real

goql es usable pero joven. La cobertura de tests contra una base de datos real es **solo
SQLite**: el SQL de PostgreSQL y MySQL se genera y se verifica por dialecto, pero no se ha
ejecutado contra esos servidores. El borrado lógico está a medias, las migraciones no comparan
índices ni restricciones, y la comprobación `copylocks` de `go vet` salta al pasar entidades
por valor. La lista completa está en [Limitaciones](limitations.md), escrita a propósito sin
adornos.
