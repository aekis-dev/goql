# Compilaciones de producción

En desarrollo, goql lee el código fuente de una lambda en tiempo de ejecución. **Un binario
publicado no tiene código fuente**, así que una compilación con la etiqueta `prod` consulta un
registro generado de antemano por el mismo analizador.

```bash
go generate ./...
go build -tags prod ./...
```

Esto no es opcional. Sin el registro, toda llamada con lambda falla con
`no compiled body for key …`.

## Por qué el generador vive en tu módulo

Resolver los campos de una lambda requiere que los modelos estén **registrados**, cosa que solo
ocurre cuando se importan los paquetes que los declaran para que su `init()` ejecute
`models.AddModel`. Un binario independiente no puede conocer tus rutas de importación:
ejecutado de forma genérica, omite todas las lambdas con «no registered model found for
type …».

Por eso el driver es un pequeño `package main` en tu propio módulo:

```go
//go:build !prod

// tools/goqlc/main.go
package main

import (
    "log"
    "os"

    "github.com/aekis-dev/goql/generator"
    _ "myapp/models"      // ← la importación que registra tus modelos
)

func main() {
    dir := "."
    if len(os.Args) > 1 {
        dir = os.Args[1]
    }
    if err := generator.Run(dir); err != nil {
        log.Fatal(err)
    }
}
```

Y una directiva junto a tus consultas:

```go
//go:generate go run ./tools/goqlc .
```

Se emite un registro por directorio de paquete, así que un proyecto con lambdas en varios
paquetes está cubierto. El fichero generado lleva la etiqueta de compilación `prod` y **no**
debería versionarse: regenéralo como parte de la compilación.

## Desarrollo y producción usan el mismo analizador

El generador instancia el analizador real y lo ejecuta de antemano. Producción no es una
segunda implementación, que es lo que hace fiables ambos caminos. La comprobación más fuerte
disponible es que el binario de demostración produce una **salida idéntica byte a byte** en
ambos modos, y se ejecuta en cada cambio.

## La regla que te va a morder

**Las claves son posicionales.** Un cuerpo se indexa por el SHA-256 de su posición en el
código: el nombre base de su fichero y la línea que el runtime de Go informa para él. Un
binario de producción no tiene fuente que leer, así que una posición es la única identidad
disponible en tiempo de ejecución.

En consecuencia:

!!! danger "Editar un fichero desplaza las líneas por debajo del cambio."
    Sin regenerar, la búsqueda no encuentra entrada y la llamada falla con
    `ErrNoCompiledBody`. Ejecuta `go generate` antes de cada compilación con `-tags prod`,
    tanto en CI como en local.

Ese fallo es ruidoso a propósito. Un esquema anterior indexaba por el índice `funcN` del
compilador, donde añadir o reordenar una clausura resolvía en silencio una lambda a un
**cuerpo distinto**: la consulta equivocada, sin error.

### Por qué la posición y no el nombre de la función

El nombre que el runtime da a una clausura solo es estable para una escrita directamente en
una función. El compilador lo reescribe para una clausura anidada, y de forma distinta según
el inlining:

```text
                 compilación normal                     -gcflags=all=-l
nivel superior   main.Outer.func2                       main.Outer.func2      (igual)
anidada          main.Outer.Outer.func2.func4           main.Outer.func2.1
dos niveles      main.Outer.Outer.func3.…func5.func6    main.Outer.func3.1.1
```

La línea que informa el runtime es idéntica en todos esos casos, y también con `-N`, `-N -l`
y `-race`. Eso es lo que permite indexar como cualquier otra una lambda escrita dentro de otra
clausura, que es la forma que impone [`Transaction`](transactions.md), ya que recibe una.

De ahí salen dos detalles:

- El generador emite una clave para **las dos líneas que el runtime puede informar**, la de
  la palabra `func` y la de la primera sentencia, porque cuál elige depende de la forma del
  cuerpo.
- Las claves usan el **nombre base** del fichero, no su ruta, así que las compilaciones con
  `-trimpath` siguen coincidiendo. goqlc se niega a generar si dos lambdas del build
  colisionarían, nombrando ambas.

## CI sugerido

```yaml
- run: go generate ./...
- run: git diff --exit-code        # el registro debe estar al día
- run: go vet ./...
- run: go test ./...
- run: go build -tags prod ./...
```

Si el registro está en `.gitignore` (recomendado), quita el paso `git diff` y simplemente
genera antes de compilar.

## Depuración

`e.WithDebug()` devuelve un engine que registra cada sentencia y sus argumentos:

```go
e := goql.New(db, goql.SQLite{}).WithDebug()
```

Útil cuando una consulta no devuelve nada y no sabes si es el SQL o los datos. A propósito no
está activo en los tests, donde registrar cada sentencia entierra la aserción que realmente
falló.
