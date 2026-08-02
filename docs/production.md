# Production builds

In development, goql reads a lambda's source at runtime. **A released binary has no source**,
so a build tagged `prod` consults a registry generated ahead of time by the same parser.

```bash
go generate ./...
go build -tags prod ./...
```

This is not optional. Without the registry, every lambda call fails with
`no compiled body for key …`.

## Why the generator lives in your module

Resolving a lambda's fields requires the models to be **registered**, which only happens when
the packages declaring them are imported so their `init()` runs `models.AddModel`. A
standalone binary cannot know your import paths — run generically, it skips every lambda with
"no registered model found for type …".

So the driver is a small `package main` in your own module:

```go
//go:build !prod

// tools/goqlc/main.go
package main

import (
    "log"
    "os"

    "github.com/aekis-dev/goql/generator"
    _ "myapp/models"      // ← the import that registers your models
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

And a directive next to your queries:

```go
//go:generate go run ./tools/goqlc .
```

One registry is emitted per package directory, so a project with lambdas in several packages
is handled. The generated file is build-tagged `prod` and should **not** be committed —
regenerate it as part of the build.

## Dev and prod are the same parser

The generator instantiates the real parser and runs it ahead of time. Prod is not a second
implementation, which is what makes the two paths trustworthy. The strongest available check
is that the demo binary produces **byte-identical output** in both modes, and it is run on
every change.

## The rule that will bite you

**Keys are positional.** A body is keyed by the SHA-256 of its source position — the base
name of its file, and the line the Go runtime reports for it. A prod binary has no source to
read, so a position is the only identity available at runtime.

Consequently:

!!! danger "Editing a file shifts the lines below the edit."
    Without regenerating, a lookup finds no entry and the call fails with
    `ErrNoCompiledBody`. Run `go generate` before every `-tags prod` build, in CI as well as
    locally.

That failure is loud, which is deliberate. An earlier scheme keyed on the compiler's `funcN`
index, where adding or reordering a closure silently resolved a lambda to a **different
body** — the wrong query, no error.

### Why position rather than the function name

The runtime's name for a closure is only stable for one written directly in a function. The
compiler rewrites it for a nested closure, and differently depending on inlining:

```text
                 default build                          -gcflags=all=-l
top-level        main.Outer.func2                       main.Outer.func2      (same)
nested           main.Outer.Outer.func2.func4           main.Outer.func2.1
two deep         main.Outer.Outer.func3.…func5.func6    main.Outer.func3.1.1
```

The line the runtime reports is identical in every one of those, and across `-N`, `-N -l` and
`-race`. That is what lets a lambda written inside another closure — the shape
[`Transaction`](transactions.md) forces, since it takes one — be keyed like any other.

Two details fall out of it:

- The generator emits a key for **both lines the runtime may report**, the `func` keyword and
  the first statement, because which one it picks depends on the body's shape.
- Keys use the file's **base name**, not its path, so `-trimpath` builds still match. goqlc
  refuses to generate if two lambdas in the build would collide, naming both.

## Suggested CI

```yaml
- run: go generate ./...
- run: git diff --exit-code        # the registry must be up to date
- run: go vet ./...
- run: go test ./...
- run: go build -tags prod ./...
```

If the registry is gitignored (recommended), drop the `git diff` step and simply generate
before building.

## Debugging

`e.WithDebug()` returns an engine that logs every statement and its arguments:

```go
e := goql.New(db, goql.SQLite{}).WithDebug()
```

Useful when a query returns nothing and you cannot tell whether it is the SQL or the data.
It is deliberately not on in tests, where logging every statement buries the assertion that
actually failed.
