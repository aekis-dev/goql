# Errors

Every failure goql can classify is a sentinel error, wrapped with `%w` and matchable with
`errors.Is`.

```go
if errors.Is(err, goql.ErrCapturedVariable) {
    // the lambda referenced a variable from the surrounding scope
}
```

## Parsing

| Error | Meaning |
|---|---|
| `ErrCapturedVariable` | the body referenced a variable from the surrounding scope — use a [params struct](params.md) |
| `ErrUnsupportedExpr` | an expression goql cannot compile (`!x`, a function call, `nil`) |
| `ErrInvalidLambda` | the signature or a statement is wrong for this call |
| `ErrNoAssignments` | an `Update` lambda assigns nothing |
| `ErrInvalidOption` | an option that does not apply, or an incompletely declared one |

## Params

| Error | Meaning |
|---|---|
| `ErrMissingParams` | the lambda declares a params struct but none was supplied |
| `ErrInvalidParams` | a value was supplied that was not declared, the wrong type, or more than one |

## Models

| Error | Meaning |
|---|---|
| `models.ErrNotRegistered` | no `AddModel` for this type — usually the package was not imported |
| `models.ErrDuplicateModel` | `AddModel` called twice for a type, or two models share a type name |
| `models.ErrFieldNotFound` | a field name that the schema (or a CTE's projection) does not have |

`goql.ErrNotRegistered` re-exports the first, so a caller needs one import.

## Runtime

| Error | Meaning |
|---|---|
| `ErrNoCompiledBody` | a `-tags prod` binary has no registry entry — run `go generate` |
| `ErrRelationConstraint` | a one2many row cannot be disassociated because its FK is `NOT NULL` |
| `ErrNotEntity` | a value passed where a goql model was expected |
| `ErrUnresolvedQuestions` | `Migrate` was called with questions unanswered; nothing was applied |

## Errors carry the fix

Messages are written to say what to do, not just what went wrong:

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

## Where subquery failures surface

A nested call never runs, so it has no error of its own. Everything is reported by the
**enclosing** call:

| Stage | Example | Caught by |
|---|---|---|
| compile | an unknown field in a nested lambda | the Go compiler |
| parse | an assigning sub-lambda, two projected fields | the enclosing call |
| build | an alias collision, an unregistered table | the enclosing call |

## A note on silence

goql treats "recognised but not handled" as a bug. An option it cannot apply, a join it
cannot honour, an assignment it cannot resolve — each is an error rather than a quiet
omission. Several real defects were exactly that shape, and the errors above are what
replaced them.
