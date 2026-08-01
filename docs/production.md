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

**Keys are positional.** A body is keyed by the SHA-256 of its runtime function name, which
embeds the compiler's `funcN` index. A prod binary has no source to hash, so the runtime
function name is the only identity available.

Consequently:

!!! danger "Adding, removing or reordering a closure shifts every later index in that function."
    Without regenerating, a lookup can silently resolve to a **different lambda's body** —
    the wrong query, no error. Run `go generate` before every `-tags prod` build, in CI as
    well as locally.

A guard comparing the entity type each body was generated for was built and then removed by
choice, in favour of bare positional keys plus the discipline of always regenerating.

### Nesting inside a closure

Closures written directly in a function get `1..n` in source order. Nested ones continue the
same counter but are named *under their parent*, so they never take a sibling's number — goql
counts only top-level literals, which is what makes the numbering correct.

A goql lambda nested **inside another closure** cannot be keyed, because its runtime name is
built from a parent chain goql does not reproduce. The generator **skips it loudly**:

```text
goqlc: skipping Select lambda at main.go:216 — it is nested inside another closure,
which cannot be keyed; move it to the top level of its function
```

In prod such a call fails with "no compiled body", which is the correct loud failure. Note
this is about a goql lambda inside a `func() {…}` you wrote — a
[subquery](subqueries.md) nested inside another goql lambda is fine, because it is parsed as
part of its parent's body.

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
