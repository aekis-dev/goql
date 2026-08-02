# Las lambdas se analizan, no se ejecutan

Esta es la página que conviene leer con atención. Todo lo inusual de goql se deriva de aquí.

## Qué ocurre en realidad

Cuando escribes:

```go
goql.Update[Order](ctx, e, func(o *Order) {
    if o.Total > 1000 {
        o.Priority = "High"
    }
})
```

goql **no** llama a esa función. Lo que hace es:

1. preguntar al runtime de Go dónde está el literal de función,
2. analizar el fichero fuente que la contiene con `go/parser`,
3. recorrer el AST del cuerpo,
4. compilarlo a `UPDATE orders SET priority = ? WHERE total_amount > ?`.

La asignación describe una cláusula `SET`. El `if` describe un `WHERE`. Nada se muta en Go, y
nada dentro del cuerpo tiene efecto alguno en tiempo de ejecución.

!!! info "Por qué el parámetro es un puntero"
    `func(o Order) { o.Priority = "High" }` se lee como código muerto para cualquier
    desarrollador de Go, y los linters lo señalan — mientras que ambas formas se analizaban
    igual. Ahora goql **exige** un puntero, para que la única señal de mutación que tiene Go
    conserve su significado habitual.

## Consecuencia 1: nada de variables capturadas

Las variables libres de una clausura no se pueden leer por reflexión — `reflect` no las
expone, y cualquier cosa que las extraiga del binario depende de detalles internos del
compilador y muere con binarios sin símbolos. Así que esto es un **error de análisis**, no una
consulta silenciosamente incorrecta:

```go
minTotal := 100.0

goql.Select[Order](ctx, e, func(o *Order) bool {
    return o.Total > minTotal    // ✗ ErrCapturedVariable
})
```

Versiones tempranas compilaban eso a `total_amount > 'minTotal'`, comparando contra la cadena
literal. Ahora falla, y el error nombra el mecanismo que hay que usar en su lugar:

```go
type OrderParams struct{ MinTotal float64 }

goql.Select[Order](ctx, e, func(o *Order, p OrderParams) bool {
    return o.Total > p.MinTotal
}, OrderParams{MinTotal: minTotal})
```

Consulta [Valores en tiempo de ejecución](params.md).

## Consecuencia 2: solo expresiones analizables

goql entiende un subconjunto definido de Go. Cualquier otra cosa se rechaza con
`ErrUnsupportedExpr` en lugar de aproximarse.

**Soportado:**

| Go | SQL |
|---|---|
| `==` `!=` `<` `<=` `>` `>=` | las mismas comparaciones |
| `&&` `\|\|` | `AND` `OR` |
| `if` / `else if` / `else` | una sentencia por rama, mutuamente excluyentes |
| `switch` (con y sin etiqueta) | igual |
| `goql.Filter(o.Tags, …)` | un `EXISTS` correlacionado sobre la relación |
| `o.Customer.Country` | un join, o la clave foránea local si la ruta termina en la clave del destino |
| `!x` | `NOT (…)` |
| `goql.Condition(...)` | `LIKE`, `IN`, `IS NULL`, `NOT IN`, … |
| `+ - * / %` | aritmética, o `\|\|`/`CONCAT` para cadenas |
| una llamada anidada `goql.Select` / `Exists` | una subconsulta |
| `goql.Sum(...)` y compañía | agregados |

**No soportado:**

- Llamar a funciones propias, llamadas a métodos, aserciones de tipo, `len()`, formateo de
  cadenas.
- `c.Deleted == nil` — `nil` se lee como un identificador, así que usa
  `goql.Condition(c.Deleted, "IS NULL")`.

## Dónde se encuentra el cuerpo

En **desarrollo**, el cuerpo se localiza por el nombre de la función en el runtime, se analiza
y se cachea. Dos lambdas en una misma línea se resuelven correctamente, porque el analizador
selecciona el literal por su índice `funcN` y no por su posición en el texto.

En **producción** (`-tags prod`) no hay código fuente, así que el mismo analizador se ejecuta
de antemano mediante `go generate` y escribe un registro. Consulta
[Compilaciones de producción](production.md), incluida la única regla que importa: una lambda
de goql **anidada dentro de otra clausura** no se puede indexar, y el generador la omite de
forma ruidosa.

## Leer los errores

Como el análisis ocurre donde está escrita la lambda, los fallos aparecen con el código
delante:

```text
captured variable minTotal: a lambda cannot reference variables from its surrounding
scope — pass the value through a params struct
```

```text
unsupported expression in lambda: condition of type *ast.UnaryExpr
```

```text
field Prioritee not found in models orders
```

La mayoría de los fallos con campos los detecta antes el compilador de Go: la lambda es Go de
verdad.

## Un modelo mental útil

Escribe la lambda como si *fuera* a ejecutarse, y comprueba que sería correcta si lo hiciera.
El trabajo de goql es hacer que la base de datos haga lo mismo. Donde esa correspondencia se
rompe — una variable capturada, una llamada a función, un operador que SQL no puede expresar —
goql te lo dice en lugar de adivinar.
