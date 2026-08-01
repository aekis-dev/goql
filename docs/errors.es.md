# Errores

Todo fallo que goql puede clasificar es un error centinela, envuelto con `%w` y comparable con
`errors.Is`.

```go
if errors.Is(err, goql.ErrCapturedVariable) {
    // la lambda referenció una variable del ámbito que la rodea
}
```

## Análisis

| Error | Significado |
|---|---|
| `ErrCapturedVariable` | el cuerpo referenció una variable del ámbito exterior — usa una [struct de parámetros](params.md) |
| `ErrUnsupportedExpr` | una expresión que goql no puede compilar (`!x`, una llamada a función, `nil`) |
| `ErrInvalidLambda` | la firma o una sentencia es incorrecta para esta llamada |
| `ErrNoAssignments` | una lambda de `Update` no asigna nada |
| `ErrInvalidOption` | una opción que no aplica, o una declarada de forma incompleta |

## Parámetros

| Error | Significado |
|---|---|
| `ErrMissingParams` | la lambda declara una struct de parámetros pero no se suministró ninguna |
| `ErrInvalidParams` | se suministró un valor no declarado, del tipo equivocado, o más de uno |

## Modelos

| Error | Significado |
|---|---|
| `models.ErrNotRegistered` | no hay `AddModel` para este tipo — normalmente el paquete no se importó |
| `models.ErrDuplicateModel` | `AddModel` llamado dos veces para un tipo, o dos modelos con el mismo nombre de tipo |
| `models.ErrFieldNotFound` | un nombre de campo que el esquema (o la proyección de una CTE) no tiene |

`goql.ErrNotRegistered` reexporta el primero, para que un llamante necesite una sola
importación.

## Tiempo de ejecución

| Error | Significado |
|---|---|
| `ErrNoCompiledBody` | un binario `-tags prod` no tiene entrada en el registro — ejecuta `go generate` |
| `ErrRelationConstraint` | una fila one2many no se puede desasociar porque su clave foránea es `NOT NULL` |
| `ErrNotEntity` | se pasó un valor donde se esperaba un modelo de goql |
| `ErrUnresolvedQuestions` | se llamó a `Migrate` con preguntas sin responder; no se aplicó nada |

## Los errores llevan la solución

Los mensajes están escritos para decir qué hacer, no solo qué falló:

```text
captured variable minTotal: a lambda cannot reference variables from its surrounding
scope — pass the value through a params struct
```

```text
model not registered: Customer — import the package that declares it so its init()
calls models.AddModel before the model is used
```

```text
err is the error of a nested goql call, which is never executed and so never fails
here — discard it with _ and handle the error returned by the enclosing call, which
reports anything wrong with the subquery
```

```text
field not found: totals does not select Customer — it selects Total
```

```text
join parameter j is declared but never assigned — set j.Model and j.On, or remove it
```

## Dónde aparecen los fallos de subconsulta

Una llamada anidada nunca se ejecuta, así que no tiene error propio. Todo lo reporta la
llamada **externa**:

| Etapa | Ejemplo | Lo detecta |
|---|---|---|
| compilación | un campo inexistente en una lambda anidada | el compilador de Go |
| análisis | una sublambda que asigna, dos campos proyectados | la llamada externa |
| construcción | una colisión de alias, una tabla no registrada | la llamada externa |

## Una nota sobre el silencio

goql trata «reconocido pero no gestionado» como un bug. Una opción que no puede aplicar, un
join que no puede honrar, una asignación que no puede resolver — cada una es un error y no una
omisión callada. Varios defectos reales tenían exactamente esa forma, y los errores de arriba
son lo que los sustituyó.
