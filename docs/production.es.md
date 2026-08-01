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

**Las claves son posicionales.** Un cuerpo se indexa por el SHA-256 del nombre de su función en
el runtime, que incluye el índice `funcN` del compilador. Un binario de producción no tiene
código que hashear, así que el nombre de la función en el runtime es la única identidad
disponible.

En consecuencia:

!!! danger "Añadir, quitar o reordenar una clausura desplaza todos los índices posteriores de esa función."
    Sin regenerar, una búsqueda puede resolverse en silencio al cuerpo de **otra lambda**: la
    consulta equivocada, sin ningún error. Ejecuta `go generate` antes de cada compilación con
    `-tags prod`, tanto en CI como en local.

Se construyó una salvaguarda que comparaba el tipo de entidad para el que se generó cada
cuerpo y luego se eliminó por decisión propia, a favor de claves posicionales simples más la
disciplina de regenerar siempre.

### Anidamiento dentro de una clausura

Las clausuras escritas directamente en una función reciben `1..n` en orden de aparición. Las
anidadas continúan el mismo contador pero se nombran *bajo su padre*, así que nunca toman el
número de una hermana — goql cuenta solo los literales de primer nivel, que es lo que hace
correcta la numeración.

Una lambda de goql anidada **dentro de otra clausura** no se puede indexar, porque su nombre en
el runtime se construye a partir de una cadena de padres que goql no reproduce. El generador
la **omite de forma ruidosa**:

```text
goqlc: skipping Select lambda at main.go:216 — it is nested inside another closure,
which cannot be keyed; move it to the top level of its function
```

En producción, esa llamada falla con «no compiled body», que es el fallo ruidoso correcto.
Ojo: esto se refiere a una lambda de goql dentro de un `func() {…}` que escribiste tú — una
[subconsulta](subqueries.md) anidada dentro de otra lambda de goql no tiene problema, porque
se analiza como parte del cuerpo de su padre.

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
