# Joins

Tres formas de que una consulta alcance otra tabla, en orden creciente de explicitud.

## 1. A través de una relación declarada

Recorrerla hace join automáticamente — ver [Relaciones](relations.md):

```go
func(o *Order) bool { return o.Customer.Country == "USA" }
// → INNER JOIN "customers" c ON o."customer_id" = c."id"
```

No hay nada que declarar: la relación ya dice cómo se relacionan las tablas.

## 2. Entre modelos sin relación

Declara ambos como parámetros. Una comparación cuyos dos lados pertenecen a modelos
**distintos** es la condición del join; todo lo demás es un filtro.

```go
paid, err := goql.Select[Invoice](ctx, e, func(i *Invoice, p *Payment) bool {
    return i.Ref == p.Ref && p.Method == "card"
})
```

```sql
SELECT i.* FROM "invoices" i, "payments" p
WHERE i."ref" = p."ref" AND p."method" = ?
```

- El resultado sigue siendo `[]*Invoice`. Los modelos extra **restringen** la consulta; no
  ensanchan el resultado.
- Un modelo declarado pero no referenciado no se une, así que un parámetro suelto no puede
  convertir la consulta en un producto cartesiano.
- El join es **interno**: una igualdad no puede decir qué lado es opcional.
- `Update` y `Delete` rechazan un modelo declarado en lugar de ignorarlo — alcanzan otras
  tablas mediante joins de relación, no mediante una lista FROM.

## 3. Explícito — `goql.Join`

Cuando necesitas expresar la condición, o elegir el tipo:

```go
open, err := goql.Select[Invoice](ctx, e,
    func(i *Invoice, p *Payment, j *goql.Join) bool {
        j.Model = p
        j.On    = i.Ref == p.Ref
        j.Type  = goql.Left
        return i.Status == "open"
    })
```

```sql
SELECT i.* FROM "invoices" i
LEFT JOIN "payments" p ON i."ref" = p."ref"
WHERE i."status" = ?
```

| Campo | Significado |
|---|---|
| `Model` | uno de los propios parámetros de modelo de la lambda |
| `Query` | una [CTE](cte.md) enlazada en esta lambda, en lugar de un modelo |
| `On` | la condición del join — es un campo `bool`, así que una comparación corriente se le asigna |
| `Type` | `goql.Inner` (por defecto), `goql.Left`, `goql.Right`, `goql.Full` |

`Model` nombra un **parámetro declarado** en lugar de un valor nuevo, apuntando a la
declaración en vez de repetirla — la misma regla que usa `From.Model`.

Declara varios parámetros `*goql.Join` para unir varias tablas; se aplican en el orden de
declaración.

### Por qué `On` puede ser una comparación

`On bool` es un campo de struct corriente, así que `j.On = i.Ref == p.Ref` compila y el
compilador de Go comprueba ambos lados. Después se analiza, no se evalúa, como cualquier otra
expresión de una lambda.

## Tipos de join y motores

| Tipo | SQLite | PostgreSQL | MySQL |
|---|---|---|---|
| `Inner` | ✓ | ✓ | ✓ |
| `Left` | ✓ | ✓ | ✓ |
| `Right` | ✓ (3.39+) | ✓ | ✓ |
| `Full` | ✓ (3.39+) | ✓ | ✗ |

Un tipo no soportado se **rechaza al construir la sentencia**, con un mensaje que nombra el
motor — nunca se emite para que falle en el servidor. El apaño habitual de `FULL JOIN` en
MySQL es una unión de un `LEFT` y un `RIGHT`, que es otra sentencia distinta y goql no la
sustituye en silencio.

## Contar a través de un join

Un join multiplica filas, así que `goql.Count()` pasa a ser `COUNT(DISTINCT pk)` siempre que
la consulta haga join — una fila que coincide por varias filas relacionadas se cuenta una vez.
Esto vale igual para joins de relación, participantes de modelo y joins explícitos.

## Cuál usar

| | |
|---|---|
| Los modelos tienen una relación | recórrela — no hay nada que declarar |
| Sin relación, join interno, clave evidente | declara ambos modelos y compáralos |
| Necesitas `LEFT`/`RIGHT`/`FULL`, o la condición no es una igualdad simple | `goql.Join` |
| El otro lado es una consulta, no una tabla | `goql.Join` con `Query` — ver [CTE](cte.md) |
