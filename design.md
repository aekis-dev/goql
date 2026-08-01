# goql — Design Notes

Living document of architectural decisions and the improvement backlog.
Format per decision: **Decision / Rationale / Alternatives considered / Implementation notes**.

---

## 1. Architecture Overview (as-built)

goql is a reflection-based Go ORM whose distinguishing feature is that **queries are
expressed as native Go lambdas**, parsed from source via the `go/ast` toolchain and
compiled into SQL. The Go closures are *not executed* — their bodies are inspected.

### Packages

Paths below are the **current** layout (Phase F, §9). Sections 2–8 were written against the
earlier `pkg/orm`, `pkg/query`, … layout and their file references should be read with that
in mind: `pkg/orm` became the root package, and `pkg/X` became `X`.

| Package | Responsibility |
|---|---|
| `models` | Schema metadata: `Model`, `Field`, relation kinds (`M2O`/`O2M`/`M2M`), the model `registry`, `ColumnType`, snake_case/DB-type inference. |
| `.` (root, `goql`) | Public API, `Engine`, the embedded base `Model` (id/timestamps/soft-delete + change tracking), scan/map helpers, dev vs prod executors, migrations. |
| `query` | SQL builders: `create`, `write` (UPDATE), `search` (SELECT), `delete`, `ddl`, `join`, the dialects, and the lambda `parse`r. |
| `generator` | Ahead-of-time lambda compilation for `-tags prod` builds. |
| `tests` | Integration tests + example models (`Customer`, `Order`, `Tag`, `Widget`, `Gadget`). |
| `examples/demo`, `tools/goqlc`, `tools/goqlmigrate` | Demo program, codegen driver, migration CLI. |

### Entity contract
- Every entity embeds `orm.Model` (provides `ID`, `Created`, `Updated`, `Deleted *time.Time`,
  and per-entity dirty tracking via `original`/`changes`/`isNew`).
- Schema/column metadata is registered imperatively in each model's `init()` through
  `models.AddModel(&T{}, "table", &Field{...}, ...)`.

### Public API surface (`GoqlContext`)
- `Create([]T) ([]any, error)` — INSERT, returns persisted entities (currently `[]any`).
- `Search(predicate func(T) bool) ([]any, error)` — SELECT; WHERE/JOIN derived from the predicate AST.
- `Write(...)` — UPDATE. **Two modes**: entity slices (diff against tracked original) or
  lambdas `func(T){ t.X = ... }` (assignments + optional `if` condition → SET/WHERE).
- `Delete(...)` — entity or predicate based.
- `CreateTables(...)`, `EnableForeignKeys()`.

### Write/read value conversion
- **Write**: `query.toDriverArg` ([ddl.go](pkg/query/ddl.go)) unwraps pointers (nil→NULL),
  honors `driver.Valuer`, and JSON-marshals maps/structs/slices (except `time.Time` and `[]byte`).
- **Read**: `orm.setFieldValue` ([helpers.go](pkg/orm/helpers.go)) handles scalars, `time.Time`
  (multi-layout), nullable pointers, and `[]byte`. **It does NOT unmarshal JSON into struct/map/slice fields** — see backlog §3.

### Dev vs Prod
- `executor_dev.go` resolves models via reflection at runtime.
- `executor_prod.go` + `generator/` emit `gony_registry_prod.go` for a reflection-light path.
- Decision pending: keep both, or treat prod as an optimization of an identical dev contract.

---

## 2. Improvement Backlog (agreed focus areas)

Target: improve usage on the existing base. DB stance: **portable across Postgres and SQLite**
(SQLite for tests today, Postgres as a first-class target).

### Decision A — Type-safe returns via generics
- **Decision**: Replace `[]any` returns with generic forms, e.g. `Create[T](ctx, []T) ([]*T, error)`,
  `Search[T](ctx, pred) ([]*T, error)`, eliminating caller-side `.(*models.Order)` casts seen
  throughout [main.go](pkg/main.go).
- **Rationale**: Casts are the dominant ergonomic cost today and a frequent error source.
- **Alternatives considered**: Keep `[]any` (status quo, rejected — poor DX); code-generated
  typed wrappers per model (rejected — more codegen surface than needed now).
- **Implementation notes**: Go 1.24 is available. Methods can't be generic, so these become
  package-level generic functions taking `ctx` (or a generic wrapper type). Lambda parsing needs
  the concrete `T` to resolve the schema — confirm the AST parser can key off `T` rather than the
  runtime value. Keep the existing methods as thin shims during migration.

### Decision B — Clarify `Write` semantics
- **Decision**: Document the two-mode mental model explicitly and consider splitting the API so the
  modes are not overloaded on one method (candidate: `Write(entities)` vs `Update(lambda)`).
- **Rationale**: The lambda form *looks* like it runs in Go but is AST-parsed; mutating a value
  receiver has no runtime effect. This is the highest foot-gun in the API.
- **Alternatives considered**: Leave overloaded (rejected — ambiguous); fully unify into one lambda
  form (rejected — entity-diff path is genuinely useful for round-tripped records).
- **Implementation notes**: Decide naming before refactor. Whatever we choose, design.md and the
  package docs must state: lambda bodies are inspected, not executed.

### Decision C — JSONB round-trip (portable) — IMPLEMENTED
- **Decision**: struct/map/slice fields read **and** write as JSON, gated on the declared
  column type being `jsonb`. Detection rule everywhere: `strings.ToLower(field.Type) == "jsonb"`.
- **Status**: Done.
  - **Write — Create**: `EntityCreate` ([create.go](pkg/query/create.go)) marshals when
    `Type == "jsonb"`; fixed a bug where the scalar branch appended a raw `reflect.Value`
    instead of `fv.Interface()`.
  - **Write — overloaded `Write`**: `EntityWrite` (entity-diff form) and `LambdaWrite`
    (lambda form) ([write.go](pkg/query/write.go)) both marshal when `Type == "jsonb"`.
    The **entity form is the working JSON path**.
  - **Read**: `mapColumnsToEntity` ([orm/helpers.go](pkg/orm/helpers.go)) `json.Unmarshal`s
    `[]byte`/`string` into the field when `Type == "jsonb"`, via `unmarshalJSONField`. Done
    where the schema/field is available, not inside `setFieldValue` (which lacks schema).
  - **Column-mapping fix (prerequisite)**: `FieldsByDB` was keyed with *quoted* names
    (`"meta"`) since the `strconv.Quote` commit, while drivers report unquoted columns — so
    **no field of any type was being populated on read**. Added `Field.ColumnName()` (raw,
    unquoted) and keyed `FieldsByDB` by it in [registry.go](pkg/models/registry.go).
    `GetColumnName()` still returns the quoted form for SQL emission. This single fix repaired
    8 previously-failing read tests across the suite.
- **Portability convention**: always declare `Type: "jsonb"` on JSON fields; SQLite accepts it
  via type affinity. `InferDBType` already defaults composite Go types to `jsonb`.
- **Tests**: `pkg/tests/json_test.go` + model `pkg/tests/models/widget.go` (create / entity-write
  / search round-trip).
- **Known limitation (follow-up)**: the lambda form **cannot** assign a JSON composite literal —
  `extractValue` ([orm/executor_dev.go](pkg/orm/executor_dev.go)) handles only
  `BasicLit`/`Ident`/`SelectorExpr`/`UnaryExpr`, not `CompositeLit`. So
  `Write(func(w Widget){ w.Meta = Meta{...} })` fails at parse time. JSON-via-lambda needs
  `CompositeLit` support in the parser — separate task.
- **Out of scope (still)**: querying *into* JSON (`o.Meta["k"] == "v"`) → Postgres `->>`/`@>`
  vs SQLite `json_extract`.

### Pre-existing issues observed — RESOLVED in Phase A (see §4)
- ~~**Stale SQL-assertion tests**~~ — updated to the quoting/aliasing decisions; suite green.
- ~~**Search WHERE table-alias bug**~~ — fixed by real alias threading (Decision A2 in §4).

### Decision D — Relation loading on Search
- **Decision**: Define and improve how `O2M`/`M2M` (and `M2O`) relations are populated when an
  entity is returned from `Search` (eager vs lazy, N+1 avoidance).
- **Rationale**: Predicates already traverse relations (`for _, t := range o.Tags` in
  [main.go](pkg/main.go)); the populated-result story needs to match.
- **Alternatives considered**: TBD — eager-by-default vs explicit preload list vs lazy proxies.
  Needs its own discussion before implementation.
- **Implementation notes**: Decide the loading strategy and batching approach first; this is the
  least-specified item and should be designed separately.

### Decision E — DB dialect abstraction (enabling, portable)
- **Decision**: Introduce a minimal dialect seam (type mapping + identifier quoting + JSON type)
  so Postgres and SQLite differences (e.g. `jsonb` vs `TEXT`, placeholder style `?` vs `$N`) live
  in one place.
- **Rationale**: Required to deliver Decision C portably and to make Postgres first-class.
- **Status**: Enabling work for C; scope to the minimum needed (no full dialect framework yet).

---

## Open Questions
- Generics: confirm lambda AST parser can resolve schema from type param `T` alone.
  → **Resolved 2026-07**: yes — the parser only needs the schema, resolvable via `reflect.TypeFor[T]()`.
- `Write` split: final method names.
- Relation loading: eager vs lazy vs explicit preload — needs a dedicated design pass.
- Dev/prod executors: keep dual path, or fold prod into an optimization of one contract?
- Query parameters: how lambdas reference runtime values (see Roadmap Phase 3) — the
  closure-capture decision shapes the whole query language.

---

## 3. Roadmap (agreed 2026-07, from full-codebase review)

Target state: feature-complete, usable by third parties, integrable with a gin web app.
Each phase gets its own interfaces-first design discussion before implementation.
Execution order: **0 → 1 → 2 → 3**, then 4/5/6/7 as prioritized (they are independent).

### Phase 0 — Correctness bugs (fix before building on top)
1. **Nested-transaction bug** — `GoqlContext.Transaction` ([context.go](pkg/orm/context.go))
   always calls `db.BeginTx`; `Create`/`Write`/`Delete` wrap themselves in `ctx.Transaction`,
   so inside a user transaction they open a *second independent* tx (SQLite deadlock risk,
   broken atomicity). Fix: if `ctx.tx != nil`, join it (optionally SAVEPOINT for nesting).
2. **No panic safety in `Transaction`** — a panic in `fn` leaks the tx; needs deferred rollback.
3. **Closure-variable foot-gun** — `extractValue` ([executor_dev.go](pkg/orm/executor_dev.go))
   turns any identifier into its *name as a string*: `c.Age > minAge` → `WHERE age > 'minAge'`.
   Until Phase 3 adds real parameter support, this must become a hard parse error.
4. **Infinite recursion in `extractValue` for `UnaryExpr`** — recurses on `expr` instead of
   `v.X`; a negative literal (`c.Discount = -0.15`) stack-overflows.
5. **Search alias bug** (known): `LambdaSearch` aliases the table to its first letter but
   `WhereClause` emits full `table.column` → `no such column`.
6. **`Delete` with mixed entity types** — groups PKs per table but resolves the schema from
   `entities[0]` for every group (and shadows the error).
7. **Zero values silently skipped on insert** — `EntityCreate` omits zero-valued nullable
   fields, so `false`/`0`/`""` can't be persisted explicitly. Needs a deliberate rule.
8. **Stale SQL-assertion tests** — update to the quoting decision; suite must be green to
   gate later phases.
9. Minor: dead `ChangeTracker`; `Model.SetPrimaryKey` registry lookup that can never succeed;
   helpers duplicated between `orm` and `query` (`isZeroValue`, `getFieldValue`, `containsValue`).

### Phase 1 — Type-safe generic API (Decision A, priority #1)
- Package-level generics (methods can't take type params): `goql.Search[T]`, `goql.Create[T]`,
  `goql.Write[T]`, `goql.Delete[T]` returning `[]*T`.
- Add read ergonomics in the same stroke: `First[T]`, `Get[T]` (by PK), `Count[T]`, `Exists[T]`.
- Predicates become compile-time-checked `func(T) bool`.
- Keep existing `GoqlContext` methods as deprecated shims during migration.
- Sub-question to settle: `Write` split naming (Decision B) — e.g. `Update[T](entities)` vs
  `UpdateWhere[T](lambda)`.

### Phase 2 — Transactions, context & gin integration (priority #2)
- **Context plumbing**: public API carries a `context.Context` (today frozen to `Background()`)
  for request timeouts/cancellation.
- **Transaction API**: propagation fix (Phase 0.1); panic-safe rollback; isolation/read-only
  options; explicit `Begin/Commit/Rollback` handles for middleware-managed lifecycles;
  optional SAVEPOINT nesting.
- **Gin integration** (`goql/ginx` or a documented recipe): middleware binding a
  request-scoped `*GoqlContext` into `gin.Context`; optional tx-per-request (commit on 2xx,
  rollback on error/panic); connection-pool guidance.

### Phase 3 — Query language completeness
- **Captured variables / parameters** — the #1 gap. Options to discuss: (a) explicit params
  arg (`Search[T](gctx, pred, goql.Args{...})`); (b) AST + `go/types` resolution of simple
  captures from caller source; (c) explicit marker helper (`goql.P(x)`).
- String matching (`strings.Contains/HasPrefix/HasSuffix` → `LIKE`), `IN`
  (`slices.Contains`), `IS NULL` (`c.Deleted == nil`), negation (`!x`, sentinel `== false`),
  time comparisons.
- **ORDER BY / LIMIT / OFFSET** — no pagination story today; likely option args
  (`goql.OrderBy(...)`, `goql.Limit(n)`).
- Field paths deeper than 2 (`o.Customer.Company.Name`).
- `CompositeLit` in write-lambdas (JSONB-via-lambda, Decision C follow-up).
- Replace `panic` in `mapOperator`/join builders with errors.

### Phase 4 — Soft delete (decide) + relation loading (Decision D)
- **Soft delete is half-built**: `Deleted *time.Time` exists but `Delete` hard-deletes and
  `Search` never filters `goql_deleted IS NULL`. Implement (UPDATE on delete, implicit
  filter, `Unscoped()` escape hatch) or remove the column.
- **Relation population on Search**: results come back with nil relations. Design pass:
  explicit preload with batched `IN` queries (leading candidate) vs eager-by-default vs lazy
  proxies. Also define O2M write semantics (currently only adds FK links, never disassociates).

### Phase 5 — Postgres via minimal dialect seam (Decision E)
- Placeholders `?` vs `$N`; `LastInsertId` vs `INSERT ... RETURNING` (pq/pgx don't support
  LastInsertId); `AUTOINCREMENT` vs identity; `INSERT OR IGNORE` vs `ON CONFLICT DO NOTHING`;
  type mapping; unify quoting (tables currently unquoted, columns quoted).
- One `Dialect` interface, SQLite + Postgres implementations, wired through `query` builders.
- Later: JSON path queries (`->>` vs `json_extract`).

### Phase 6 — Parser robustness & dev/prod story
- Harden `getFunctionSource` (runtime source re-read with hand-rolled brace counting —
  breaks on multiple closures per line, missing sources): parse the whole file with
  `go/parser` and select the FuncLit by `runtime.FuncForPC` position; use the existing
  (currently unused) parse cache.
- Prod codegen keys bodies by hash of `pkg.Func.funcN` name — closure reordering silently
  mismatches; needs a stability strategy. Decide: keep dual executors or fold prod into an
  optimization of one contract.
- Registry: duplicate `AddModel` handling, richer "not registered" errors.

### Phase 7 — Release readiness
- README (pitch + quickstart), godoc on the public API, `examples/` app; document loudly
  that lambdas are *parsed, not executed*.
- Module layout: move `go.mod` to repo root (`github.com/aekis-dev/goql`), demo `main.go`
  to `examples/`/`cmd/`; delete committed artifacts (`gony_prod` binary, `test*.db`,
  `.DS_Store`); add `.gitignore` + LICENSE; finish `gony_*` → `goql` naming.
- CI: `go vet`, green `go test ./...`, `golangci-lint`; later a Postgres service container.


---

## 4. Phase A — Correctness (implemented 2026-07-29)

Phase A of the agreed roadmap (see the walkthrough plan). Goal: make the parser and SQL
emission trustworthy, and get the suite green, before later phases build on them.
**Result: 39 tests passing, 0 failing** (baseline was 15 failing), stable across runs.

### A2 — Table aliasing decided at SQL-build time
- **Decision**: Introduce `query.AliasMap` ([pkg/query/alias.go](pkg/query/alias.go)) which
  assigns one alias per table per query: preferred first letter, numeric suffix on collision
  (`orders`→`o`, `order_tags`→`o2`, `tags`→`t`). It is created and threaded by the SQL
  builders, never by the parser. Statements that cannot portably alias their target
  (`UPDATE`, the outer `DELETE`) call `PinTableName`, which renders that table as its own
  quoted name — so one condition tree serves both aliased and unaliased statements.
- **Rationale**: `LambdaSearch` aliased the table to its first letter while `FullColumn()`
  emitted the full table name, producing `SELECT c.* FROM customers c WHERE customers.country
  = ?` → `no such column`. Aliasing is an emission concern, not a parsing one, so keeping it
  out of `ParseBody`/`FieldRef` also keeps the prod-codegen payload alias-free.
- **Alternatives considered**: dropping aliases entirely and always using full table names
  (simplest, rejected — the user wants aliases and they are needed for future self-joins);
  assigning aliases at parse time (rejected — would bake emission detail into cached/compiled
  parse results).
- **Implementation notes**: `FieldRef.FullColumn()` is replaced by
  `FieldRef.QualifiedColumn(am)`. `WhereClause`, `BuildJoinClause` and
  `BuildUpdateFromClause` all take the `AliasMap`. New `buildJoinClauses` helper removes the
  collect-then-render duplication across search/delete/PK-select. `SelectPKs` +
  `WhereClause` in `orm/context.go` are replaced by one self-contained
  `query.SelectPKsWhere(schema, cond)`, so the `orm` package no longer assembles SQL
  fragments itself.

### A1a — Identifiers quoted consistently (tables, FK columns, join tables, indexes)
- **Decision**: Quote every identifier goql emits, not just scalar columns:
  `Model.GetTableName()` and `Field.GetQuotedFKColumn()` added; join-table names/columns and
  index names quoted; PK columns in entity paths now come from
  `schema.PrimaryKey.GetColumnName()` rather than the raw string returned by
  `Entity.PrimaryKey()`.
- **Rationale**: Columns were quoted since commit 9825e3d but tables and FK columns were not,
  producing mixed statements like
  `INSERT INTO orders (..., customer_id, "goql_updated", ...)`. Reserved words or
  mixed-case names would break, and Postgres folds unquoted identifiers to lower case.
- **Alternatives considered**: quoting nothing (rejected — reserved-word breakage); deferring
  all of it to Phase E (rejected — the suite had to go green now, and Phase E only swaps the
  quoting *function*, not the call sites).
- **Implementation notes**: `strconv.Quote` is Go-string escaping, not SQL identifier
  quoting — it backslash-escapes where SQL wants a doubled quote character. **Phase E
  replaces it with per-dialect `QuoteIdent`** (MySQL uses backticks); the helpers above are
  the single choke points for that swap.

### A4-prep / determinism — stable column ordering
- **Decision**: Emit columns in sorted order everywhere a Go map was previously iterated
  directly: `EntityCreate` (sorted Go field name, reusing the existing `sortedFieldNames`),
  `EntityWrite` (sorted change keys), `LambdaWrite`'s autoUpdateTime injection, and
  `EntitySearch`'s condition list.
- **Rationale**: `schema.Fields` and the `changes` map are Go maps, so INSERT/UPDATE column
  order varied between runs. That makes exact SQL assertions impossible, defeats prepared
  statement/plan caching, and makes debug logs hard to compare.
- **Implementation notes**: Verified stable across repeated runs.

### Smaller correctness fixes
- **A3 — captured variables now error.** `extractValue`'s bare-identifier case returned the
  identifier's *name as a string*, silently compiling `c.Age > minAge` into `age > 'minAge'`.
  It now returns an error naming the params-struct mechanism planned for Phase C. As a
  consequence `c.Deleted == nil` also errors rather than binding the string `"nil"` — `IS
  NULL` arrives with `goql.Condition` in Theme 4.
- **A5 — negative literals.** The `UnaryExpr` branch recursed on `expr` (itself) instead of
  `v.X`, so any negative literal stack-overflowed. Fixing that exposed a second bug: the
  result was formatted back into a *string* (`"-0.15"`), so the value is now negated
  numerically and non-numeric negation errors.
- **A6 — mixed-type batch delete.** `Delete` grouped PKs per table but resolved the schema
  from `entities[0]` for every group (discarding the error), so a mixed-type batch deleted
  from the first entity's table repeatedly. The schema is now captured per group.
- **A7 — string literals** use `strconv.Unquote` instead of `strings.Trim(v, '"')`, which
  left Go escape sequences undecoded.
- **A7 — implicit `LIKE` removed.** `EntitySearch` silently switched `=` to `LIKE` whenever a
  value happened to contain `%`. Entity search is now always exact match; pattern matching
  belongs to the predicate language.
- **A7 — pointer fields.** `InferDBType` had no `reflect.Ptr` case, so a nullable `*int`
  silently inferred `text`. It now unwraps one pointer level, which is what makes the
  **opt-in pointer-field convention** work: declare a pointer only where "unset" must be
  distinguishable from an explicit zero value (pointers are deliberately *not* enforced —
  see the rejected alternative below).
- **A7 — no more panics in builders.** `BuildJoinClause`, `BuildUpdateFromClause` and
  `mapOperator` returned values and panicked on bad input; they now return errors, threaded
  through `LambdaSearch`/`LambdaWrite`/`LambdaDelete`. The duplicated
  "resolve relation target schema" blocks collapsed into one `relationTargetSchema` helper.

### Latent bug found and fixed while aliasing: M2O join used the wrong PK
- **Decision**: In `BuildJoinClause`/`BuildUpdateFromClause`, the M2O join condition now
  uses the **target** table's primary key.
- **Rationale**: Both emitted `ref.Field.TableSchema.PrimaryKey` — the *source* table's PK —
  on the target side of the `ON` clause. It worked only because every table in the test
  models names its PK `id`; any model with a differently-named PK would have generated a
  join against a non-existent column.

### Rejected: enforcing pointer fields for all model fields
- **Decision**: Rejected. Pointer fields stay opt-in, used only where the unset/zero
  distinction matters.
- **Rationale**: Universal pointers would force `goql.Ptr(40)` at every construction site and
  `*c.Age > 40` in every predicate (the lambda must still type-check as real Go), add an
  `*ast.StarExpr` case to the parser, and turn a stray direct call of a predicate into a nil
  dereference — a large permanent cost across every field to solve a problem that affects a
  few nullable ones.

### Known issue deferred to Phase E (found in Phase A)
- **String column defaults are emitted unquoted**: `GetDefault()` is
  `fmt.Sprintf("%v", fs.Default)`, so `Default: "Active"` emits `DEFAULT Active` instead of
  `DEFAULT 'Active'`. SQLite tolerates it; **Postgres would reject it** as an unknown column
  reference. It cannot be fixed by blindly quoting strings because the base timestamp fields
  use `Default: "CURRENT_TIMESTAMP"`, a SQL *expression* that must not be quoted. Needs a
  deliberate literal-vs-expression distinction (e.g. a separate `DefaultExpr`, or an
  expression allowlist) — folded into Phase E's DDL/type work.

### A4 — Multi-branch parsing (if / else-if / else + switch) with NOT
- **Decision**: `ParseBody` becomes a flat list of `ParseBranch{Condition, Assignments,
  RelationAssignments, Selects}` ([pkg/query/parse.go](pkg/query/parse.go)). Each branch's
  `Condition` already carries the negation of every preceding arm, so branches are mutually
  exclusive and independent. `LambdaWrite` returns `[]*Query` — **one UPDATE per assigning
  branch**; `Write`'s transaction loop executes them all and sums `RowsAffected`. Predicate
  lambdas instead OR the conditions of every `Selects` branch via
  `ParseBody.SelectCondition()`.
- **Rationale**: `parseIfBlock` appended else-arm assignments onto the *if* arm's result, so
  `if A { s = "x" } else { s = "y" }` emitted one UPDATE with two conflicting SET clauses on
  the same column, scoped to `A` — the else rows were never touched and the if rows got the
  wrong value. Branches also make switch support fall out for free.
- **Alternatives considered**: rejecting `else` with a clear error and telling users to write
  two lambdas (rejected — the multi-lambda batching it relied on is itself being removed in
  Phase C, and if/else is the natural Go spelling); special-casing only "unconditional
  default" else arms (rejected — too narrow).
- **Implementation notes**:
  - **`NOT` primitive**: `query.LogicalNot` + `query.Negate()`, rendered by `WhereClause` as
    `NOT (…)`. `CollectJoins` traverses NOT nodes unchanged because they are ordinary
    logical branches. **Correction (found in Phase F):** this does *not* close the Phase 3
    `!x` item, as previously recorded here. The node exists and else/default arms produce
    it, but `exprToCondition` has no `*ast.UnaryExpr` case, so a `!` written in a predicate
    still fails with `ErrUnsupportedExpr`. Negation is spelled `NOT IN`/`NOT LIKE` via
    `goql.Condition` today.
  - **Both switch forms**: tagless (`switch { case c.Age > 60: }`, cases are boolean
    expressions) and tag (`switch c.Country { case "USA", "Canada": }`, cases are values
    compared against the tag; multiple values in one case are ORed). `default` is collected
    and emitted last regardless of its position, so its condition is the negation of every
    case.
  - **Exhaustiveness**: a trailing `else`/`default` ends the chain, so no conditions leak to
    statements after it.
  - **Guard clauses (bug found while implementing)**: a top-level `return true` following
    conditional arms now carries the negation of those arms. Without this,
    `if A { return false }; return true` parsed as *unconditional* — silently selecting every
    row. Arm conditions are threaded back out of `parseIfChain`/`parseSwitchStmt` for this.
  - **`applyRelationAssignments`** extracted from `Write` onto `GoqlContext`; relation syncing
    now runs per branch, scoped to the rows that branch selects. `Write` also errors when a
    lambda assigns nothing at all, instead of silently doing nothing.
- **Tests**: `pkg/tests/parser_test.go` covers if/else, else-if chains, both switch forms,
  guard clauses and the captured-variable rejection; `pkg/tests/write_test.go` adds
  **end-to-end** checks that each arm updates its own disjoint row set (`if/else` and
  `switch` + `default`) against a real database.

---

## 5. Phase B — Dev/prod execution (implemented 2026-07-29)

The dual-executor architecture is **confirmed sound and kept**: `generator` instantiates a
real `orm.DebugExecutor` and calls `ParseBodyFromSource`, so prod is *the same parser* run
ahead of time, not a second implementation. Prod previously did not compile at all.

**Verification**: `go build -tags prod ./...` succeeds, and the demo binary built with
`-tags prod` produces **byte-identical output** to the dev build — the strongest available
check that the compiled registry reproduces the runtime parser.

### B1–B3 — Prod path compiles again
- **Decision**: Fix stale `…/goql/src/…` imports; rewrite the emitter against the current
  `query.*` types (it still emitted `orm.ConditionNode`/`orm.RelationAssignment`, moved and
  renamed long ago) **and** the Phase A `Branches` shape; make `CompileExecutor.ParseBody`
  return `*query.ParseBody` so it satisfies `QueryExecutor`.
- **Implementation notes**: `compiled_prod.go` referenced `schemaRegistry`, which is
  unexported in `models` and unreachable from `orm` — added `models.FindFieldByTable`.
  `ResolveField` keeps its panic: it is called inline from generated `init()` code where
  there is no caller to receive an error, and a miss means the registry disagrees with the
  registered models, which is not recoverable at runtime.

### B4 — Generation is driven from the consuming module (no installed binary)
- **Decision**: `generator` becomes an **importable package** (`//go:build !prod`) exposing
  `Run(dir)`. Each project adds a small `package main` driver that imports its own model
  packages and calls it, wired up with a directive next to the queries:
  `//go:generate go run ./tools/goqlc .` — see [pkg/tools/goqlc](pkg/tools/goqlc/main.go).
  There is no separate installed tool.
- **Rationale**: A standalone binary **cannot work**. Resolving a lambda's fields needs the
  models registered, which only happens when the packages declaring them are imported so
  their `init()` runs `AddModel` — and a generic tool cannot know those import paths. Running
  it standalone skipped all 12 demo lambdas with "no registered model found for type Order".
  This mirrors the migration decision: the process that owns the registry does the work.
- **Alternatives considered**: a temp-program bootstrap that emits and runs a schema dumper
  inside the user's module (rejected — compiles user code twice, more moving parts); static
  parsing of `AddModel` literals (rejected — breaks as soon as a field definition uses a
  constant, variable or helper).
- **Implementation notes**: `emit()` uses the package actually discovered rather than a
  hardcoded `package main`, and emits **one registry per package directory**, so a project
  with lambdas in several packages is handled.

### B5 — Generated registry is not committed
- Generated `goql_registry_prod.go` is gitignored and regenerated per project. Deleted the
  stale `gony_registry_prod.go`, the 8 MB `gony_prod` binary, `test*.db` and `.DS_Store`;
  added `.gitignore`.

### B6–B7 — Robust lambda location, and the cache is real
- **Decision**: Replace `getFunctionSource`'s hand-rolled brace/paren scanner with
  `go/parser.ParseFile` plus selection of the function literal by the **runtime's `funcN`
  index**, and memoize parsed bodies in the previously-dead `cache` field (now
  `map[string]*query.ParseBody` guarded by a mutex, since the executor is shared across
  goroutines).
- **Rationale**: The scanner counted characters, so a `}` inside a string desynchronized it,
  and it could not distinguish two lambdas written on one line. Indexing by `funcN` is exact.
  Without the cache every query re-read and re-parsed the whole source file.
- **Implementation notes**: `orm.EnclosingFuncName` renders a declaration the way the runtime
  names it (`(*Service).Sync`) and is **shared with the generator**, so ahead-of-time
  numbering and dev-mode lookup cannot drift apart.
- **Tests**: `TestParse_TwoLambdasOnOneLine` declares two lambdas on one line and asserts each
  resolves to its own body — impossible before, and it also guards the cache key (a
  line-based key would serve the first body for both).

### B8 — Content-addressed keys are impossible; positional keys retained
- **Decision**: Keys stay `sha256(runtime function name)`, i.e. positional. **This reverses
  the plan's decision** to hash the lambda's source.
- **Rationale**: A prod binary has **no source to hash** — that is the whole reason prod mode
  exists. The only identity available at runtime is the runtime function name, which embeds a
  positional index. A stale-registry guard comparing the entity type each body was generated
  for was built and then **dropped at the user's direction**, in favour of bare positional
  keys plus the discipline of running `go generate` as part of every build.
- **Consequence**: reordering, adding or removing closures inside a function shifts every
  later `funcN`. Without regenerating, a lookup can silently resolve to a *different*
  lambda's body. `go generate` must run before any `-tags prod` build.
- **Two latent key bugs fixed here** (both found by actually running the prod binary):
  package **main** is qualified literally as `main` by the runtime while every other package
  uses its **full import path** — the old generator hardcoded the short name (correct only
  for main), and an initial "fix" to always use the import path broke main instead; and
  method receivers were missing, so closures inside methods were misnamed.

### B9 — Registry hardening
- Duplicate `AddModel` for the same type now errors instead of silently overwriting;
  the "not registered" error names the likely cause (package not imported).

### Unrelated demo bug fixed to enable verification
- `pkg/main.go` created customers named `"A"`/`"B"`, violating its own
  `CHECK (length(name) > 2)`, so the demo aborted before exercising most queries.

---

## 6. Phase C — Public API (in progress, 2026-07-29)

Themes 3 and 4 merged into one API rewrite so call sites changed once.
**Status: C0, C7a, C1–C3 and C5 done (65 tests passing, dev and prod both clean, dev/prod
output still byte-identical). C4 (params struct) and C6/C7b outstanding.**

### C0 — Package `orm` renamed to `goql`
- **Decision**: `pkg/orm` → `pkg/goql`, done first so the new API reads with its final
  names: `goql.Engine`, `goql.Model`, `goql.Select`, `goql.Sort`.
- **Rationale**: The agreed API spelling is `goql.X`; renaming after building the API would
  have meant touching every new call site twice.

### C1–C3 — Split API, generics, Engine, context, pointer-only lambdas
- **Decision**: Struct-based `Create`/`Search`/`Write`/`Remove` and lambda-based
  `Select`/`Update`/`Delete`, package-level generics returning `[]*T`, one model and one
  operation per call. `GoqlContext` → `Engine`; `context.Context` is the first parameter of
  every call. Old methods replaced outright. `Insert` is reserved for INSERT … SELECT and
  deliberately not defined, rather than shipped as a function that always fails.
- **Implementation notes**:
  - Per-call context is threaded by `Engine.withContext`, which returns a shallow copy
    carrying that context — no change needed at the dozens of `exec`/`query` call sites.
  - The old variadic dispatchers survive unexported (`createAny`, `searchAny`, …) so the
    tested internals were reused rather than rewritten; the public generics box `[]T` into
    `[]models.Entity` and type the results back.
  - `Create`'s returned pointers **alias the caller's slice**, so generated primary keys are
    visible through either.
  - Entity boxing collapsed into one `asEntity` helper, which also fixed a real break: the
    generic API passes `[]models.Entity`, whose elements are interfaces, and the old
    per-call-site boxing only handled pointers and structs.
  - **Pointer-only lambda parameters**, enforced at the API boundary *and* in the parser
    (`resolveEntityTypeFromFuncLit` now requires `*ast.StarExpr`). Rationale:
    `func(c Customer){ c.Status = "x" }` reads as dead code to any Go developer and linters
    flag it, while both forms previously parsed identically — so Go's one signal for
    mutation carried no meaning.
- **Trade-off accepted**: the predicate is typed `any`, not `func(*T) bool`, because C5
  options are declared as extra lambda parameters and therefore vary the signature. That
  costs the explicit type parameter (`Select[Customer]`) and compile-time checking of the
  predicate's shape (validated by reflection instead). **Noted for reconsideration**: the
  original reason for in-lambda options was that one call could mix several models and
  lambdas, which the C1 split removed — trailing option values would now restore full
  inference.
- **Coverage deliberately dropped**: `TestDelete_MixedEntityTypes` (the Phase A A6 fix)
  cannot be expressed through the typed API, since one model per call is the point. The fix
  and its per-table grouping remain, but that path is now unreachable from the public API,
  so the bug is prevented by construction rather than guarded by a test.

### C5 — Query options
- **Decision**: `goql.Sort{By, Desc}`, `goql.Limit{Value}`, `goql.Offset{Value}`,
  `goql.Fields{Names}`. Struct-based calls take them as trailing values; lambda calls
  declare them as extra parameters and assign them in the body, where they are recognised
  structurally — parsed, not executed, like everything else in a lambda.
- **Implementation notes**:
  - Parameters are classified **by type, not position**, so they may be declared in any
    order; several `*Sort` parameters compose a multi-column ORDER BY in declaration order.
  - `Sort.By` and `Fields.Names` are **Go field names**, resolved against the schema, so a
    typo is an error rather than invalid SQL.
  - Parsed modifiers live on `ParseBody.Options`, so they are cached and compiled into the
    prod registry along with the rest of the body.
  - `Fields` always includes the primary key. Unselected fields come back as Go zero
    values — indistinguishable from genuinely empty, an accepted trade.
  - `Offset` without `Limit` emits an open-ended `LIMIT -1`, since every supported engine
    requires a limit before an offset.
  - Setting an option **inside a branch** is rejected: options describe the whole query, and
    silently applying a branch's option to every row would be wrong.

### C7a — Sentinel errors
- `models.ErrNotRegistered` / `ErrDuplicateModel` / `ErrFieldNotFound`, and
  `goql.ErrUnsupportedExpr` / `ErrInvalidLambda` / `ErrCapturedVariable` /
  `ErrNoAssignments` / `ErrNoCompiledBody` / `ErrInvalidOption` / `ErrNotEntity`, all wrapped
  with `%w` and matchable with `errors.Is`. `goql` re-exports `ErrNotRegistered` so callers
  need one import.

### Generator kept in step with the API
- `findORMCalls` now unwraps generic instantiation: `goql.Select[Customer](…)` parses as an
  `IndexExpr` around the selector, so the old plain-selector match found nothing after the
  API change. It matches the lambda-taking functions (`Select`/`Update`/`Delete`, plus
  `Count`/`Exists` when they land).
- **The dropped prod guard bit immediately**: building `-tags prod` against a registry that
  predated the API change did not fail loudly — it applied an `Order` body to a `Customer`
  update, producing `no such column: priority`. Exactly the failure the entity-type guard
  would have caught. `go generate` must run before every prod build.

### Known issue (not addressed)
- `goql.Model` embeds a `sync.RWMutex`, so `go vet`'s copylocks check flags every value copy
  (`[]Customer{*alice}`). The API takes `[]T` by value, so this is now part of the public
  surface. The mutex only guards per-entity change tracking, which is rarely shared across
  goroutines; removing it would clear the warnings but touches `ChangeTracker`, which is
  parked pending review.

### C4 — Params struct for call-time values
- **Decision**: A lambda may declare one extra parameter that is not an option carrier;
  that is its params struct, and its value is passed as a trailing argument at the call
  site. Field references inside the body compile to a placeholder that is substituted with
  the real value just before execution.

  ```go
  goql.Select[Order](ctx, e, func(o *Order, p OrderParams) bool {
      return o.Total > p.MinTotal
  }, OrderParams{MinTotal: 100})
  ```
- **Rationale**: Parsed bodies are cached and compiled into the prod registry, so a
  call-time value can never be baked into the tree — something has to cross the boundary
  as data. This closes the `ErrCapturedVariable` gap: before it, **no query could be
  parameterized at all**, since every value had to be a literal in the lambda.
- **Alternatives considered**: reading captured variables out of the closure (impossible —
  `reflect` exposes no free variables, and `unsafe`/DWARF walking depends on compiler
  internals and dies on stripped binaries); resolving them from caller source (only ever
  works for literals); a `goql.P(...)` marker or an `Args{"name": v}` map (both add API
  surface for no gain over a plain trailing value).
- **Implementation notes** — deliberately reuses what options already established:
  - **No new classification concept.** `classifyParams` already inspects extra parameters
    by type; the params struct is simply the leftover case once option carriers are
    matched. At most one is allowed.
  - **No new parsing case.** `resolveValueRef` already walks a `SelectorExpr` on the value
    side; `tryParamRef` adds one branch for "base identifier is the params parameter".
  - **No tree mutation and no per-call bind pass.** `Args` is already rebuilt per call, so
    a `query.ParamRef` rides in it as an opaque placeholder. `query.ResolveParams`
    substitutes them in `Engine.exec`/`query` — the one place every statement passes
    through — and returns `args` untouched when there are none.
  - **Field typos are caught by the Go compiler**, since the lambda is ordinary Go code and
    the params struct is a real type. goql's own parse-time check therefore only has to
    reject what compiles but reflection cannot read: an **unexported** field.
  - Arity and type of the supplied value are checked at the API boundary
    (`ErrMissingParams` when declared but not supplied, `ErrInvalidParams` on a type
    mismatch or an unexpected extra value).
  - **Known gap**: a `jsonb` column assigned from a params struct is rejected with a clear
    error, because the value is not available at build time to marshal.
- **Why options and params differ in shape**: options are compile-time-fixed and have no
  value to hand over, so they are assigned inside the body; params are runtime data and
  must be passed at the call site. Different data flow, not an inconsistency.

### Latent bug found while implementing C4
- **`Transaction` rebuilt the Engine field by field**, so anything not explicitly listed
  was silently dropped inside a transaction — the new params struct, and **`debugMode`,
  which was already being lost** before this change. It now copies the receiver and only
  overrides `tx`. This is why `Update`/`Delete` (which wrap themselves in a transaction)
  initially could not see their params while `Select` could.

### C6 — Condition, Count/Exists, Execute/Bind
- **`goql.Condition(field, op, values…)`** — one new `*ast.CallExpr` case in
  `exprToCondition` covers LIKE, NOT LIKE, IN, NOT IN, IS NULL, IS NOT NULL and the plain
  comparisons. It coexists with native Go operators and combines with `&&`/`||`. The
  operator is checked against an allowlist **with arity** at parse time
  (`query.NormalizeOperator`), so `"LIK"`, `IS NULL` with a value, or `IN` with none all
  fail while parsing. As agreed, that is about catching typos: an operator can only ever be
  a literal in the caller's own source. Values may come from a params struct. `ParseNode`
  gained `Values []*ValueRef` for list operators and `RawColumn` for a verbatim left-hand
  side (the JSON-path escape hatch). This finally gives `IS NULL` a spelling —
  `c.Deleted == nil` is still rejected as a captured identifier.
- **`Count[T]` / `Exists[T]`** — lambda-only, reusing the predicate parser. `Count` uses
  `COUNT(DISTINCT pk)` when the predicate joins a relation, so a row matching two related
  rows is still counted once. `Exists` is `SELECT 1 … LIMIT 1` rather than `EXISTS(…)`,
  because "did a row come back" is uniform across drivers while scanning a boolean is not.
- **`Execute` / `Bind[T]`** — public wrappers over the existing `exec` and
  `scanRows`/`mapColumnsToEntity` paths, so they join the surrounding transaction and honour
  the call's context. `Execute` returns the real `sql.Result`, keeping `LastInsertId`.
- **Emitter**: `ParamRef` must be reconstructed as its own type rather than through `%#v`,
  which would have serialised the placeholder as a literal struct and produced a registry
  that bound garbage. Found only by exercising the prod path end to end.

### C7b — Index accepts a bool
`Index: true` derives `idx_<table>_<column>`; the string form still names an index and is
how several fields share one composite index.

---

## 7. Phase D — Relation loading (implemented 2026-07-30)

**103 tests passing, dev and prod clean, dev/prod output byte-identical.**

### D1 — Explicit preload, batched
- **Decision**: `goql.Preload{Fields: []string{…}}`, using the same option mechanism as
  Sort/Limit/Offset/Fields — a trailing value on the struct path, a declared parameter on
  the lambda path. Each relation costs a **fixed number of batched queries** regardless of
  result size, never one per row.
- **Rationale**: Results previously came back with every relation empty. Explicit loading
  keeps the cost visible; eager-by-default would over-fetch, and lazy proxies would require
  relation fields to stop being plain `*Customer`/`[]Tag`, which the whole design rests on.
- **Implementation notes**: M2O and O2M take two batched queries (keys, then rows), M2M also
  two (join rows, then targets). Keys are normalised through `normalizeKey` because drivers
  report the same integer key as `int64`, `int32` or `int` depending on column and platform.
  `assignRelated` writes into the field's own shape — a pointer for a single relation, a
  slice of values or pointers for a collection.

### D2/D3 — Schema defaults and full override
- `Preload: true` on a `models.Field` loads that relation on every read. A query naming any
  relations **replaces** those defaults entirely, so an empty `goql.Preload{}` is how a
  caller asks for none. `Options.PreloadSet` distinguishes "load nothing" from "not
  specified"; without it an empty list would be indistinguishable from absence.

### Bug found and fixed: foreign keys were scanned into stub entities
- **`mapColumnsToEntity` populated many2one fields from the FK column**, allocating an empty
  `&Customer{}` with no data. A caller could not tell "relation not loaded" from "loaded and
  blank", and preload would then chase a lookup for key 0.
- **Decision**: relation fields are left nil by the scanner — a foreign key is a key, not a
  row — and preloading reads keys straight from the table via `SelectKeyPairsIn`. This is
  why M2O and O2M each take two queries rather than one; the alternative (scanning FK stubs)
  buys one query at the cost of an ambiguous API.

### D4 — one2many disassociation
- **Decision**: `syncO2M` points the listed rows at the parent **and clears the foreign key
  of any row that previously pointed at it but is no longer listed**, mirroring what M2M
  already did in both directions.
- **Rationale**: only adding links left stale ones behind — a row dropped from the slice kept
  pointing at its old parent, so the database disagreed with the entity.
- **NOT NULL foreign keys**: the column cannot be cleared, so `O2MStale` checks for rows that
  would need disassociating and returns `ErrRelationConstraint` naming the column, instead of
  leaving a stale link or surfacing a driver-level constraint violation. Test models cover
  both: `Customer.Orders` (NOT NULL → reported) and `Tag.Orders` (nullable → cleared).

---

## 8. Phase E — Dialects and types (E1/E2 implemented 2026-07-30; E3 outstanding)

**115 tests passing, dev and prod clean, dev/prod output byte-identical.**
**E3 (migrations) is not started** — see the roadmap entry; it needs per-dialect
introspection plus the socket-driven interactive flow, and is comparable in size to a
phase of its own.

### E1 — Dialect seam
- **Decision**: `query.Spec` carries only what genuinely differs between engines;
  `query.Dialect` embeds a Spec and holds the shared builders, written once. SQLite,
  Postgres and MySQL implement Spec. The engine is named explicitly at construction —
  `goql.New(db, goql.Postgres{})` — because `database/sql` exposes no driver name, so
  guessing would mean a type-switch that breaks on wrapped drivers.
- **Rationale**: A builder-per-engine interface would triplicate the WHERE walk, join
  collection and SET assembly, and they would drift. Divergence is confined to the Spec.
- **Implementation notes**:
  - Every free function in `pkg/query` became a `*Dialect` method. Identifier quoting,
    type names and placeholders are now the dialect's, not the model's:
    `Field.GetColumnName`, `GetQuotedFKColumn`, `Model.GetTableName` and `GetDBType` are
    gone, replaced by raw `ColumnName()` plus `d.QuoteIdent`.
  - **Placeholders** are drawn from a per-statement `stmt` counter, because Postgres numbers
    its parameters. Every builder must therefore append bound values in the same order it
    writes their markers — including the options tail, which is rendered *after* the WHERE
    clause so `LIMIT $2 OFFSET $3` follows `country = $1`.
  - **Quoting is now correct**, not merely present: SQL escapes an embedded quote by
    doubling it, where the previous `strconv.Quote` backslash-escaped it and would have
    emitted invalid SQL. MySQL uses backticks.
  - Two divergences are **statement-shaped**, not token-shaped: `Create` branches at the
    *execution* level for `INSERT … RETURNING` (pq/pgx have no `LastInsertId`), and a joined
    UPDATE becomes `UPDATE … JOIN … SET` on MySQL, which has no `UPDATE … FROM`.
  - `EnableForeignKeys` is dialect-delegated and a no-op where enforcement is always on.
  - An open-ended limit for a bare OFFSET is spelled per engine (`LIMIT -1`, `LIMIT ALL`,
    `LIMIT 18446744073709551615`).

### E2 — Type system
- **Decision**: `models.ColumnType` with a **core-only** vocabulary (Integer, BigInt, Real,
  Double, Decimal, Text, Varchar, Boolean, Timestamp, Bytes, JSON). Each dialect maps it to
  that engine's physical type. Parameters come from the existing `Size`, `Precision` and
  `Scale` fields, which were dead before. A type outside the set is emitted verbatim — the
  escape hatch, at the cost of a model that targets one engine.
- **JSON detection centralised** into `Field.IsJSON()`, replacing a lowercase string
  comparison repeated at four call sites. `TypeJSON`'s underlying value stays `"jsonb"` so
  models written before the vocabulary keep working.
- **Bug found while wiring it**: the specs first switched on `field.Type`, which is empty for
  relations, so every foreign key column was typed `TEXT`. They now switch on
  `field.LogicalType()`, which resolves an undeclared type from the Go type and a foreign key
  to an integer.

### The deferred DEFAULT bug is fixed
`GetDefault` was `fmt.Sprintf("%v", …)`, so `Default: "Active"` emitted `DEFAULT Active` —
tolerated by SQLite, read as a column reference by Postgres. A string default is now quoted
as a literal, with an allowlist of expression defaults (`CURRENT_TIMESTAMP`, `NOW()`, …)
passing through unquoted, since `DEFAULT 'CURRENT_TIMESTAMP'` would store the words rather
than the time. Booleans render as TRUE/FALSE.

### Testing
Per-dialect SQL assertions (`pkg/tests/dialect_test.go`) cover quoting and quote escaping,
placeholder style and numbering across a whole statement, open-ended limits, RETURNING,
the joined-UPDATE shape, insert-ignore spellings, the full type mapping, raw passthrough,
quoted defaults and auto-increment — 12 tests, no database needed. Live round-trip coverage
remains SQLite-only, as agreed; real driver behaviour on PG/MySQL (RETURNING scanning,
timestamp handling) is still unverified.

### E3 — Migrations (implemented 2026-07-30)

**127 tests passing** including an end-to-end run over the real socket.

- **Live introspection only** (no stored snapshot): per-dialect SQL on `Spec`
  (`sqlite_master`/`pragma_table_info` for SQLite, `information_schema` for Postgres and
  MySQL, bound rather than interpolated). The trade-off, accepted in planning: planning a
  migration needs a reachable database, and the comparison is always against reality.
- **Only declared tables are inspected.** A database normally holds tables goql knows
  nothing about, so a table absent from the models is never read and never proposed for
  removal. `TestMigrate_IgnoresUnmanagedTables` pins this down.
- **Ambiguity is asked, never guessed.** A column that disappeared while another appeared is
  indistinguishable between a rename and a drop-and-add; only intent separates them. Each
  such column produces a `Question` offering *rename to each candidate* (keeps the data),
  *drop* (discards it) or *skip*. Applying with anything unanswered returns
  `ErrUnresolvedQuestions` and changes nothing.
- **Adding a column is unambiguous**, so it needs no question; the definition is relaxed to
  drop NOT NULL and UNIQUE, since neither can be added to a table with existing rows without
  a default.
- **Destructive changes are flagged** (`Change.Destructive`) so a UI can warn; only a
  `drop` answer ever produces one.
- **Apply re-plans from the live database** rather than trusting a plan handed back by a
  client, so a schema that moved since the plan was displayed cannot be migrated against
  stale assumptions.
- **No migration files.** `goql_migrations` is an audit log — one row per applied change,
  with its statement — not a file-applied ledger. Consequence, accepted: there is no
  replayable history, and a fresh environment bootstraps from the models.
- **Transactional where the engine allows it** (`SupportsTransactionalDDL`): on SQLite and
  Postgres a failure rolls everything back and the summary says so; on MySQL each DDL
  statement commits as it runs, so the summary reports how far it got and the CLI tells the
  user to re-run.
- **The socket is deliberately awkward to enable**: off by default, a required token with no
  default, Unix domain only, chmod 0600, and a loud log line when it starts. It can apply
  DDL, so it is a control channel into a running process. A wrong token gets 403 — covered
  by the end-to-end test.
- **The CLI** ([pkg/tools/goqlmigrate](pkg/tools/goqlmigrate/main.go)) re-plans after every
  answer, because resolving one question can change the next: a rename consumes a candidate
  that a later question would otherwise offer.

### E3b — Type-change detection, per dialect

Comparing "the type the model wants" against "the type the database reports" is the whole
difficulty: the two are spelled differently even when identical. Only the engine knows its own
canonical forms, so **normalisation is a Spec method** (`NormalizeType`), and both sides are
normalised before comparison (`Dialect.TypesEqual`).

- **Postgres**: introspection uses `pg_catalog` with **`format_type`** rather than
  reassembling from `information_schema` parts. `data_type` omits parameters entirely, and a
  precision cannot be reassembled correctly because Postgres writes it *inside* the name —
  `timestamp(6) without time zone`. An earlier hand-rolled `CASE` produced a phantom change on
  every timestamp column; the test caught it. `to_regclass` yields NULL for a missing table
  rather than erroring. Aliases map the reported spellings onto the emitted ones
  (`character varying`→`varchar`, `int8`→`bigint`, `timestamp without time zone`→`timestamp`).
- **MySQL**: `column_type` already carries parameters. Aliases cover the cases where MySQL
  stores something under another name — most importantly `BOOLEAN`, which it reports as
  `tinyint(1)`.
- **SQLite**: normalises to the **column affinity** the engine actually enforces, following
  SQLite's documented rules. SQLite does not distinguish `VARCHAR(100)` from `TEXT`, so
  comparing affinities means goql never proposes a change the engine would treat as a no-op.
- **A type change is always asked, never applied silently**, and is flagged
  `Destructive` because whether it truncates depends on the direction, which is not knowable
  from the types alone. Where the engine cannot alter a column in place (SQLite) the question
  says so and offers only to leave it; choosing to change anyway is refused with an
  explanation rather than emitting SQL that would fail.
- **Testability**: `Engine.PlanAgainst(live, …)` diffs against a supplied `LiveSchema` instead
  of reading one, which is how Postgres and MySQL detection is covered without running those
  engines.

**Subtlety worth keeping**: the type splitter treats text on *both* sides of the parentheses as
part of the base name. Discarding the trailing words would have made
`timestamp(6) with time zone` and `timestamp(6) without time zone` compare equal — a silent
false negative. There is a test for exactly that pair.

### Known limits
- **Index and constraint drift** is not diffed; only columns, column types and whole tables are.
- Live migration coverage is SQLite-only, like the rest of the suite: the Postgres and MySQL
  introspection SQL is written from their documented behaviour and exercised through
  `PlanAgainst`, but has not run against a real server.

---

## 9. Phase F — Release readiness (implemented 2026-07-30)

**137 tests passing, dev and prod both clean, `gofmt` clean, dev/prod demo output still
byte-identical after the move.**

### F1 — Module layout: root package `goql`
- **Decision**: `go.mod` moves to the repo root as `github.com/aekis-dev/goql`, and the
  packages move out of `pkg/`: `pkg/goql` → **the root package**, `pkg/models` → `models`,
  `pkg/query` → `query`, `pkg/tests` → `tests`, `pkg/tools` → `tools`, and the demo
  `pkg/main.go` → `examples/demo`.
- **Rationale**: The module previously declared `github.com/aekis-dev/goql/pkg`, so consumers
  imported `…/goql/pkg/goql`. Moving `go.mod` alone would **not** have fixed that: the path
  is module path + directory, so it would have come out `…/goql/pkg/goql` either way. The
  directories had to move too. Putting the API package at the root gives the idiomatic
  `import "github.com/aekis-dev/goql"` and `goql.Select[…]`.
- **Alternatives considered**: keeping `pkg/` and only relocating `go.mod` (rejected — no
  change to the import path, which was the whole point); a `goql/` subdirectory
  (rejected — `github.com/aekis-dev/goql/goql` stutters).
- **Implementation notes**: the generated registry is now emitted next to the demo, whose
  `go:generate` directive became `go run ../../tools/goqlc .`. Verified by rebuilding the
  demo in both modes and diffing the output — still identical, which is what proves the
  compiled registry survived the move.

### F2 — Documentation states the central contract first
- **Decision**: `doc.go` and the README both **open** with "lambda bodies are inspected, not
  executed", and derive the two API consequences from it: no captured variables (hence the
  params struct) and a restricted expression set.
- **Rationale**: Every surprising thing about goql follows from that one fact. A reader who
  meets it after the quickstart has already mis-modelled the library.
- **Implementation notes**: the README is honest about status in its own section —
  live coverage is SQLite-only, migrations do not diff indexes or constraints, soft delete
  is half-built, aggregates and `INSERT … SELECT` are absent, and `copylocks` fires on
  passing entities by value.

### F3 — MIT license; test logging silenced
- MIT, per the user's decision (initially AGPL v3, changed deliberately).
- `setupDB` no longer calls `WithDebug`: logging every statement buried the assertion that
  actually failed. `.gitignore` updated for the new paths.

### Documentation bug found while writing the README — `!x` is *not* supported
- Writing the README's `goql.Condition` example as `!goql.Condition(…)` failed with
  `unsupported expression in lambda: condition of type *ast.UnaryExpr`.
- §4 A4 claimed the `NOT` primitive "closes the Phase 3 backlog item for `!x` in
  predicates". It does not: `query.LogicalNot` exists and else/default arms produce it, but
  `exprToCondition` has no `*ast.UnaryExpr` case, so `!` written in source is rejected.
  Negation is spelled `NOT IN`/`NOT LIKE` through `goql.Condition`.
- **Corrected in place** in §4 rather than restated as new work. Adding the missing parser
  case is a small follow-up, deliberately not folded into a docs phase.

---

## 10. Insert — INSERT … SELECT (implemented 2026-07-30)

`Insert` was reserved in Phase C and left undefined rather than shipped as a function that
always fails. This implements it. **150 tests passing, dev and prod clean, dev/prod demo
output byte-identical.**

### Shape: assignment lambda, destination first
- **Decision**: `Insert[D](ctx, e, func(d *D, s *S){ … }, params…) (int64, error)`. Each
  assignment supplies both halves of the statement at once — the left side names a
  destination column, the right side an expression selected from the source (a source field,
  a literal, or a params-struct value). Conditions filter the SELECT; an if/else or switch
  emits one statement per arm, as `Update` does.
- **Rationale**: nothing new to parse. Assignments already resolve "column ← value" where the
  value may be a field reference or a literal, which is exactly a SELECT list; conditions are
  already a WHERE; reaching through a relation already produces a join. It also reads
  correctly under "parsed, not executed" — the body describes the destination row being built
  from a source row.
- **Alternatives considered**: a predicate-only form matching columns by name (rejected — the
  mapping would be invisible, adding a field to either model would silently change the
  statement, and constants could not be expressed; viable later as a shorthand); passing the
  source model as a value argument (rejected — the lambda would have nothing to reference).
- **Implementation notes**:
  - `parseContext` gains `destSchema`/`destParamName`. The **source is the primary schema**,
    so every condition and value path is reused untouched; only `tryParseAssignment` switches
    to the destination for its left-hand side.
  - **No new classification concept**: a second `Entity`-typed parameter *is* the signal, the
    same type-based rule options and params already use. Detected by reflection at runtime and
    by resolving the type name against the registry ahead of time.
  - Parameter groups are now flattened (`flatParams`), because inserting a model into itself
    is written `func(dst, src *Order)` — two parameters sharing one type expression. The old
    index-by-declaration-group logic would have mistaken that for one parameter.
  - The destination's `AutoCreateTime`/`AutoUpdateTime` columns are filled with
    `CURRENT_TIMESTAMP`, since no row is ever built in Go for the hooks to touch. An explicit
    assignment to the same column wins.
- **Returns a row count, not entities**: recovering generated keys is only portable where
  `INSERT … RETURNING` exists.

### Rejected in the lambda: relation assignments
Linking needs the primary key of a row that does not exist yet, and `INSERT … SELECT` does
not report the keys it generated. `LambdaInsert` returns an error saying so, rather than
silently dropping the assignment.

### Options: Sort/Limit/Offset apply; Fields/Preload are refused
`Fields` is meaningless (the projection comes from the assignments) and `Preload` has no
result to load into, so both return `ErrInvalidOption` rather than being ignored.

### `goql.Conflict{Ignore: true}`
- **Decision**: a narrow option carrier with one field, honoured only by `Insert`. New Spec
  method `InsertSelect(table, columns, selectSQL, ignore)`.
- **Rationale**: the divergence is statement-shaped, not token-shaped — SQLite and MySQL put
  "ignore" in the INSERT verb, Postgres in a trailing `ON CONFLICT DO NOTHING`. Reusing
  `InsertIgnore` was impossible: it renders a VALUES list.
- **Why not `OnConflict`**: that name invites the full upsert (a conflict target plus
  `DO UPDATE SET`), which is a design of its own. Shipping only `Ignore` under it would
  half-claim the name.
- Passing `Conflict` to `Select`/`Update`/`Delete` is an error, not a no-op.

### Cost accepted: the "multiple Entity params" shape is spent
Phase C left multiple `Entity`-typed lambda parameters open as the future spelling for
explicit joins. `Insert` claims that shape with a different meaning (destination, then
source). Resolvable — the function decides the reading — but joins will need their own
spelling.

### Bug found while implementing: the prod registry dropped every lambda-declared option
`emitParsedBody` never emitted `ParseBody.Options`, so a `-tags prod` binary ran
`Select`/`Update` **without** the `Sort`, `Limit`, `Offset`, `Fields` or `Preload` the lambda
declared — silently returning unordered, unpaginated, relation-less results. §6 C5 recorded
that these were "cached and compiled into the prod registry"; only the caching half was true.
`emitOptions` now writes them, with `query.IntPtr` so the pointer-valued Limit/Offset can be
written as an expression. The demo gained a sorted, limited `Select` specifically so the
dev/prod comparison covers it.

---

## 11. Multi-model joins (implemented 2026-07-30)

Phase C parked "multiple `Entity` lambda parameters could later express joins"; §10 spent
that shape for `Insert`. This gives it a meaning for predicates too, resolved by the calling
function rather than by the signature. **158 tests passing, dev and prod clean, dev/prod
demo output byte-identical.**

### The gap this fills
A **declared** relation is already joined by traversing it (`o.Customer.Country`,
`for _, t := range o.Tags`). What had no spelling was a join between models with *no*
relation between them — and those, by definition, have no foreign-key field to reference.

### Option B — a comparison between two declared models is the join condition
- **Decision**: extra `*Model` parameters are join participants; a comparison whose two
  sides resolve to **different** participants is the join condition, everything else is a
  WHERE term. The joined tables enter the FROM clause and are related by exactly the
  comparison the caller wrote.

  ```go
  goql.Select[Invoice](ctx, e, func(i *Invoice, p *Payment) bool {
      return i.Ref == p.Ref && p.Method == "card"
  })
  // → SELECT i.* FROM "invoices" i, "payments" p WHERE i."ref" = p."ref" AND p."method" = ?
  ```
- **Rationale**: no new syntax, no new marker, and no new concept — participants are
  classified by type like options and params already are, and `ValueRef.IsColumn` already
  models "the right side is a column". A comma-joined FROM relates by the WHERE clause on
  every supported engine, which is precisely what the predicate expresses.
- **Alternatives considered**: an explicit `goql.On(a, b)` marker (rejected for now — it
  buys nothing over an equality *unless* LEFT joins are wanted, since only a marker has
  somewhere to say which side is optional); a `*goql.Join` option carrier (rejected — an ON
  condition is a predicate and belongs in the body, not in assignments on a carrier).
- **Inner only, deliberately**: an equality cannot express which side is optional. LEFT
  joins would need option D's marker and are not implemented.
- **Filter-only**: the result is still `[]*T` of the primary model. Returning columns from
  several models is a projection question — the natural shape is `SelectInto[Row]`, which is
  `Insert`'s destination-first lambda scanning into a struct instead of inserting — and is
  deliberately left for its own pass.

### Implementation notes
- `parseContext.participants` maps a parameter name to its model, seeded with the primary
  entity. `ownerOf` walks a selector chain to its base identifier and looks it up, so
  resolution is **by receiver** rather than always against the primary schema.
- Only participants **actually referenced** are recorded (`noteJoined`), so a declared but
  unused parameter cannot silently turn the query into a cross join. There is a test for it.
- `ParseBody.Joined []string` carries the tables, so it caches and compiles into the prod
  registry like everything else.
- `Count` uses `COUNT(DISTINCT pk)` when a participant is joined, for the same reason it
  already did for relation joins: the join can multiply rows.
- **`Update` and `Delete` reject a participant** rather than ignoring it: they reach other
  tables through derived relation joins, not a FROM list, so honouring one is not a matter
  of adding a table.

### The executor is told the mode; it is not inferred from the signature
Every new join test initially failed with `field i not found in models payments`, because two
entity parameters were being read as an Insert. `func(a *Archive, o *Order)` and
`func(i *Invoice, p *Payment) bool` are the same shape with different meanings.

**Where the knowledge actually lives** — the ambiguity is local to the executor, not
fundamental:
- The **call site** knows: `Insert[D]` names the destination in its type parameter, and
  `insertLambdaParams` enforces that the lambda's parameters are exactly `*D` then `*S`
  (`ErrInvalidLambda` otherwise). A swapped or mismatched signature cannot compile a query.
- The **generator** knows: it records which goql function each lambda literal was passed to.
- The **executor** does not: it receives only `fn`, and `reflect.TypeOf(fn)` yields the
  signature with no trace of `D`, `S`, or the calling function.

- **Decision**: pass the mode down rather than re-deriving it. `QueryExecutor` gains
  `ParseInsertBody`; the generator calls `ParseInsertBodyFromSource` when the call was
  `Insert`. The prod executor aliases it to `ParseBody`, since the registry already holds a
  body parsed in the right mode.
- **Alternatives considered**: inferring from the return type (rejected — an `Update` lambda
  also returns nothing); reading the type arguments out of the call expression in the
  generator (possible, since `Insert[A]` parses as an `IndexListExpr`, but the call name
  is already exact and simpler).

### Roles are enforced in the body, not only in the signature
Signature checks alone left a hole, found by probing for it: an assignment to the **source**
inside an Insert lambda was **silently dropped** and the insert succeeded anyway, because the
left-hand side failed to resolve against the destination and unresolvable assignments are
skipped as "not a model field".
- **Decision**: `assignTarget` classifies an assignment's left-hand side by the parameter it
  is rooted at. Rooted at the write target → resolve normally. Rooted at *another declared
  model* → **error**. Rooted at anything else (a sentinel variable, an unrelated statement) →
  skip, as before. This covers both directions: `o.X = …` in an Insert, and `p.X = …` in an
  `Update` that declared `p` as a join participant.
- Reading the **destination** (`if a.Reason == "x"`) is likewise refused with a message
  saying the destination has no rows yet, rather than the confusing
  "field a not found in models orders" it produced before.
- **Rationale**: the whole point of parsing rather than executing is that a lambda's meaning
  is fixed at parse time; a statement goql cannot honour must be reported, never quietly
  ignored.

### Only the destination is a type parameter (revised 2026-07-30)
- **Decision**: `Insert[D]`, not `Insert[D, S]`. The source is whichever model the lambda
  declares second.
- **Rationale**: `S` appeared nowhere else in the signature — not in a parameter, not in the
  return — so the only thing it could ever be checked against was the lambda that already
  names it. It restated one place from another and could go out of step with nothing.
  `D` is different in kind: it names what the statement writes, matching `Update[T]` and
  `Delete[T]`, and `Insert[OrderArchive]` reads as "insert into OrderArchive".
- **What replaces the check**: the second parameter is validated for *being a registered
  model* rather than for matching a type argument, with a message saying what it is for.
  Passing an option carrier or a plain struct there is an error, not a silent
  reinterpretation.
- **Nothing was lost**: a swapped signature is still caught, by `D` versus the first
  parameter (`TestInsert_RejectsMismatchedDestination`).

---

## 12. Lambda numbering: nested closures (fixed 2026-07-30)

Found while designing subqueries, which would make nested lambdas the normal case. It is a
**pre-existing silent-wrong-results bug**, not one the new feature introduced.

### What the compiler actually does
Verified by printing `runtime.FuncForPC` names for a function containing three closures, two
of them with nested ones:

```
a:                composetest.Numbering.func1
b:                composetest.Numbering.func2
c:                composetest.Numbering.func3
a's nested one:   composetest.Numbering.Numbering.func1.func4
b's nested one:   composetest.Numbering.Numbering.func2.func5
```

**Closures written directly in a function get 1..n in source order.** Nested ones continue
the same counter but are named *under their parent*, so they never take a sibling's number.

### The bug
Both the dev-mode locator and the generator counted **every** function literal flat, in
source order. A nested closure therefore consumed an index, and every later lambda in the
same function was looked up one too high — resolving to a different lambda's body, or to a
closure that is not a lambda at all. Nothing errored.

- **Decision**: count only the literals written directly in the enclosing function.
  `goql.TopLevelFuncLits` is shared by `locateFuncLit` and the generator, the same way
  `EnclosingFuncName` already is, so the two cannot drift.
- **A goql lambda nested inside another closure is now skipped by the generator, loudly.**
  Its runtime name is built from a parent chain (`Outer.func2.…`) that goql does not
  reproduce; emitting a key anyway would resolve to something else. In prod such a call fails
  with "no compiled body", which is the correct loud failure.
- **Tests**: `TestParse_NestedClosureDoesNotShiftNumbering` writes a closure inside another
  closure ahead of a goql lambda. Confirmed to **fail without the fix** — it returned every
  customer instead of the one the predicate selects — and pass with it.
- **Prod verified end to end**: with a nested closure temporarily added ahead of every lambda
  in the demo, the `-tags prod` binary still produced byte-identical output to the dev build.

---

## 13. Subqueries (implemented 2026-07-30)

**171 tests passing, dev and prod clean, dev/prod demo output byte-identical.**

### A nested goql call is a subquery
- **Decision**: `Select`, `Count` and `Exists` written *inside* a lambda body compile to a
  nested query. Two spellings, both ordinary Go:

  ```go
  usa, _ := goql.Select[Customer](ctx, e, func(c *Customer) bool { return c.Country == "USA" })
  return goql.Condition(o.Customer, "IN", usa)

  return goql.Condition(o.Customer, "IN",
      goql.Unwrap(goql.Select[Customer](ctx, e, func(c *Customer) bool { … })))
  ```
- **Rationale**: composition belongs *inside* the lambda, where everything is already AST.
  Outside it, a query is a value and would have to reach the body as a captured variable —
  the one thing the design forbids. The named form also makes a subquery reusable within a
  body.
- **Why `Unwrap`**: verified against the compiler — Go rejects a two-value call as one
  argument among others (`multiple-value … in single-value context`), and `Select` must
  return `([]*T, error)`. Passing a call as a function's *entire* argument list is the one
  exception, so `Unwrap[T](T, error) T` is what makes direct nesting legal Go. It is never
  executed inside a lambda.
- **Alternatives considered**: a `sub.Select` subpackage with single-return signatures
  (rejected by the user — a duplicate layer whose only purpose is dropping the error, which
  the local-variable form or `Unwrap` already solves); a distinct `goql.Sub` marker
  (rejected — the user wants the real function names recognised).

### Which body, which branch
A sub-call is a **predicate**, so `ParseBody.SelectCondition()` — from Phase A — already
collapses multi-branch bodies into one WHERE. Nothing new was needed. A sub-lambda that
*assigns* is rejected: a write emits one statement per branch and is not a value.

### Details that fell out of existing machinery
- **Correlation**: the nested context inherits the enclosing lambda's participants, so a
  nested predicate may name the outer model. `o.Customer == c` compares the foreign key
  against the outer model's primary key. Inherited models are marked `correlated` so
  referencing one does *not* add it to the subquery's own FROM list.
- **Projection**: `goql.Fields` inside the nested lambda names the projected column; without
  it a `SubSelect` projects the primary key. More than one field is an error — a subquery
  yields one column.
- **Aliases and placeholders**: the subquery shares the enclosing statement's `AliasMap` and
  placeholder counter, which is what makes correlation render correctly and keeps Postgres
  numbering in order (`c."country" = $1 … o."total_amount" > $2`).
- **Prod**: `SubQuery` carries its table and column as *names*, not schema pointers, so it
  emits into the generated registry like everything else.

### Refused rather than approximated
- **A subquery over a table the enclosing query already uses** — both would render with the
  same alias. The error says so; self-joins need per-occurrence aliases, which `AliasMap`
  does not yet model.
- **A nested `Exists` inside `Condition`**, or a nested `Select`/`Count` standing alone: each
  is told which position it belongs in.

### `whereClause` now returns an error
Rendering a subquery can fail (unknown table, alias collision), and `whereClause` had no
error path. The first cut emitted `/* error */ 1 = 0`, which is precisely the silent
wrongness this project keeps removing, so the error is threaded through `whereClause` and
`leafClause` to all seven builders instead.

### Bug found by dumping the generated SQL
`IN` is a list operator, and `leafClause` checked that *before* the subquery case — so
`IN (SELECT …)` rendered as `IN ()`, matching nothing and raising no error. Only visible by
printing the SQL; the tests said "0 rows", which looks like a data problem. The subquery
check now comes first.

### The nested call's error
A nested call is never executed, so it has no error to inspect — but Go forces a bound name
to be used, and the habitual `if err != nil` used to fail with the meaningless
"field err not found in models orders". Binding the error is now remembered, and testing it
reports what is actually true: discard it with `_`, because **every subquery failure is
reported by the enclosing call**, which is the only thing that runs. Two tests pin this
down: one on the message, one proving a broken sub-lambda surfaces through the outer call.

Where subquery failures come from, and where they go:
- **Compile time** — an unknown field in a nested lambda is caught by the Go compiler, since
  the lambda is real Go.
- **Parse time** — an assigning sub-lambda, a non-model type argument, more than one
  projected field: returned by the enclosing `Select`/`Count`/`Exists`.
- **Build time** — an alias collision or an unregistered table: returned the same way, now
  that `whereClause` has an error path.

### Deferred, with reasons
- **UNION** — does *not* need a new result shape (a union's rows are still one model), so it
  is buildable whenever wanted; simply not built.
- **Derived tables** (`FROM (SELECT …)`) — their real uses are downstream of `GROUP BY` and
  aggregates, which are parked.
- **CTEs** — non-recursive ones are sugar over subqueries; recursive ones have no substitute
  and deserve their own design.
- **`SelectInto`** — dropped at the user's direction: `Bind` covers scanning into a struct.

---

## 14. Aggregates and projections (implemented 2026-07-30)

**185 tests passing, dev and prod clean, dev/prod demo output byte-identical.**

Replaces the scalar `Sum[T]`/`Count[T]` shipped earlier the same day. Those answered one
number over the whole matched set and could not express
`SELECT customer_id, SUM(total) … GROUP BY customer_id` — a projection mixing plain columns
with aggregates, which is what aggregates are usually for.

### The shape
A result type that is **not a model** turns the lambda into a projection: each assignment is
one output column.

```go
type PriorityTotals struct{ Priority string; Orders int64; Total float64 }

rows, err := goql.Select[PriorityTotals](ctx, e,
    func(t *PriorityTotals, o *Order, from *goql.From) bool {
        from.Model = o
        t.Priority = o.Priority        // plain column → also a GROUP BY term
        t.Orders   = goql.Count()      // COUNT(*)
        t.Total    = goql.Sum(o.Total) // SUM(total_amount)
        return o.Total > 0
    })
```

- **`GROUP BY` is derived**, not declared: it is the assignments that are not aggregates,
  which is SQL's own rule. It cannot disagree with the projection.
- **Every column is aliased** to the field it lands in, so scanning never depends on order.
- **No plain assignment means no grouping** — the whole set as one row, which is what the
  removed scalar functions did.
- `Select[Order]` with a model result is unchanged.

### Aggregates are package-level generic functions
- **Decision**: `goql.Sum`, `Avg`, `Min`, `Max`, `Count` — parse-only markers taking a
  column, never executed.
- **Rationale**: they were nearly methods on a carrier (`g.Sum(o.Total)`), which the user
  preferred, until the compiler settled it: **`method must have no type parameters`**. `Min`
  and `Max` must return what they were given — the earliest timestamp is a timestamp — and
  only a generic *function* can do that. `Sum[T](column T) T` also quietly answers the
  decimal question: the result is whatever the model already declares.
- **Alternatives considered**: `SumOf`/`AvgOf` markers alongside the scalar functions
  (rejected — two vocabularies); `f.Sums = []string{…}` lists on `goql.Fields` (rejected —
  aggregate results have nowhere to land, and `SUM(Total)` and `AVG(Total)` would collide on
  one model field); `Ref` pointer variables linking an aggregate to a result field (rejected
  — `a.Sum = *sumTotal` compiles but reads as a nil dereference).
- **The names were freed deliberately**: `Sum[T](ctx, e, pred)` and `Count[T](ctx, e, pred)`
  are gone. `Exists[T]` is untouched — `EXISTS` is a condition, never a projected column, so
  it never collided.
- **Cost accepted**: a nested `Count` subquery loses its spelling, since that was the same
  function.

### The model is stated, never inferred
`from.Model = o` assigns one of the lambda's **own model parameters** — pointing at the
declaration rather than restating it, so the two cannot disagree. The rejected alternative
was "the first model parameter is the main one", which the user called an obscure
convention. Anything that is not a declared model parameter is an error, and a projection
that never says is an error too.

### Bug found while wiring it: the option parser swallowed unknown carriers
`tryParseOptionAssignment` ended with `return true, nil` for any carrier it recognised by
name but did not handle — so `from.Model = o` was reported as "handled" and silently
dropped, leaving the query with no model and panicking on a nil schema. It now returns an
error for an unhandled carrier, which is the same silent-ignore class we removed for
`Insert` options and scalar aggregates.

### Checking is layered, and the compiler does most of it
- `t.Total = goql.Sum(o.Priority)` **does not compile**: `Sum[string]` returns `string`,
  which will not assign to a `float64` field. Go catches the common mistake.
- What compiles but is still wrong — summing text into a string field — is caught while
  parsing, because SQLite quietly answers 0 there while Postgres errors.
- A field that does not exist on the result type is a compile error; the parser additionally
  checks it by reflection for the runtime path.

### COUNT over a join still counts entities once
A relation join multiplies rows, so `goql.Count()` renders `COUNT(DISTINCT pk)` when the
query joins — the rule the dedicated Count builder used before projections existed. Two
tests pin it: one over a relation, one over a comma-joined participant.

### Known limits
- **No `DISTINCT`** other than the join rule above.
- The projection path replaces `LambdaCount`/`LambdaAggregate`, which are now unused.

## 15. Explicit GROUP BY and HAVING (implemented 2026-07-30)

**194 tests passing, dev and prod clean, dev/prod demo output byte-identical.**

### `goql.Group{By}` — additive, never replacing
- **Decision**: `g.By = []string{"Customer"}` names *extra* grouping keys; a projected plain
  column is a key regardless. The final list is the named keys followed by any projected
  column not already among them.
- **Rationale**: it removes the limitation §14 recorded — a many2one is a `*Customer` in Go,
  so `t.Customer = o.Customer` cannot compile and a relation could not be grouped by at all.
  Naming keys also allows grouping by a column you do not want in the result.
- **Alternatives considered**: *authoritative* (an explicit list replaces the derived one),
  rejected by the user in favour of additive. Authoritative would have required rejecting a
  projected column missing from the list — with additive that case cannot arise, since
  projecting a column adds it. One rule fewer.
- Naming keys while aggregating nothing is an error: it would emit a GROUP BY that groups a
  projection with nothing to fold.

### `HAVING` — an aggregate in the predicate
- **Decision**: a comparison whose left side is an aggregate marker filters groups.
  `SplitHaving` walks the condition tree and separates those leaves from the row filters, so
  one predicate produces both clauses:

  ```go
  return o.Total > 100 && goql.Sum(o.Total) > 1000
  //     └── WHERE          └── HAVING
  ```
- **Refused rather than guessed**: an aggregate combined with `||`, or negated, has no SQL
  equivalent — a pre-aggregation filter cannot be ORed with a post-aggregation one — so it is
  a parse error naming the combination. An aggregate on the *right* of a comparison is also
  refused, with a message to put it on the left, so a condition always reads as the group
  being filtered.
- Placeholders stay in emission order: the WHERE value binds before the HAVING one
  (`> $1 … HAVING SUM(…) > $2`), verified per dialect.

### Bug found again, and fixed at the root
`GroupBy` was silently dropped: `parseContext.options()` decided "no modifiers were set" from
a hand-written list of fields, and the new one was not in it — the same shape of bug that
lost `Preload` earlier. The check now lives on the struct as `Options.IsEmpty()`, next to the
fields it tests, so a field added without updating it is far harder to miss.

---

## 16. Set operations — UNION, UNION ALL, INTERSECT, EXCEPT (implemented 2026-07-31)

**202 tests passing, dev and prod clean, dev/prod demo output byte-identical.**

### The combination is the query
- **Decision**: branches are bound to names inside a lambda, and the lambda returns their
  combination:

  ```go
  goql.Select[Movement](ctx, e, func(m *Movement, sort *goql.Sort) bool {
      sort.By = "Amount"
      live, _     := goql.Select[Movement](ctx, e, …)
      archived, _ := goql.Select[Movement](ctx, e, …)
      return goql.Union(live, archived)
  })
  ```
- **Rationale**: this is the user's shape, and it is better than the
  `Union[T](ctx, e, []any{…}, opts…)` first proposed, for three reasons that only became
  clear once written down:
  - **The compiler checks branch compatibility.** `Union[T](branches ...[]*T) bool` forces
    one result type across branches.
  - **Options need no special case.** `Sort`/`Limit` are declared on the outer lambda and
    apply to the combination, because the outer lambda *is* the combination. The slice form
    would have needed trailing option values and a rule about what they attach to.
  - **Binding a branch is the subquery syntax already built** — `live, _ := goql.Select…`.
- **Alternatives considered**: a top-level `Union[T]` taking a slice of lambdas (rejected as
  above); composable markers returning `[]*T` so `Union(a, Intersect(b, c))` would type-check
  (rejected — the lambda's `return` must be a `bool`, so composition and the predicate shape
  are mutually exclusive; the markers are variadic instead, which covers multi-way
  combination, and SQL evaluates left to right regardless).

### Per-branch aliases, one placeholder sequence
Branches are independent statements, so each gets a **fresh `AliasMap`** while sharing the
statement's placeholder counter. This is not cosmetic: with one shared map, a union of two
queries over the *same table* would hit the "already uses that table" check from §13 and be
refused — an ordinary union. The shared counter is what keeps Postgres numbering right
(`> $1 … <= $2` across branches). `TestUnion_SameTableInBothBranches` and
`TestDialect_SetOperation` pin both halves.

### What is checked
- **Every branch must yield the same columns.** The compiler enforces one result type; goql
  additionally rejects a branch that fills a different *subset* of its fields, which SQL
  would otherwise line up by position and answer silently.
- **A set's ORDER BY names a projected column**, not a model field — there is no single model
  to resolve against — and naming one no branch selects is an error.
- An aggregate call (`Count`, `Exists`) as a branch is rejected: set operations combine rows.

### The outer lambda has no model
A combining lambda declares no model and no projection: its branches carry both. The
projection parser therefore skips its "which model?" and "selects nothing" checks when the
body turned out to be a set, and `ParseQuery.Model` is empty for such a query — the one case
where it is.

### Deliberately included
`INTERSECT` and `EXCEPT` shipped with the unions at the user's direction: same machinery, one
keyword apart. Worth knowing that MySQL only supports them from 8.0.31, so on an older server
they fail at the database rather than at parse time.

---

## 17. Foreign key paths resolve locally (implemented 2026-08-01)

**206 tests passing, dev and prod clean, dev/prod demo output byte-identical.**

First step of the CTE work (§18): a foreign key had no spelling comparable to a plain value,
which blocks joining a table to a CTE row — a CTE row is not a model, so §11's
`o.Customer == c` cannot be written against one.

### `o.Customer.ID` is `orders.customer_id`
- **Decision**: a two-segment field path whose terminal segment is the **primary key of a
  many2one target** resolves to the local foreign key column, and emits no join.
- **Rationale**: the primary key of the related row *is* the value the foreign key column
  holds. The previous emission proved it: `INNER JOIN "customers" c ON o."customer_id" =
  c."id" WHERE c."id" = ?` — a join whose only purpose was to compare a column to itself.
  Now `WHERE o."customer_id" = ?`.
- **Alternatives considered**, both rejected by the user in favour of this:
  - **A key field on the model** — a second Go field (`ParentID int64`) declared over the
    relation's column via `Field{FK: "Parent"}`. Built, then reverted: it bought one
    comparison at the cost of a permanent second view of one column, with agreement rules
    (both set and disagreeing) leaking into create, write, entity-diff, DDL, migration and
    the scanner. Five call sites changed to support a syntax detail.
  - **A `goql.Key(o.Parent)` marker** — one parse case, no model change, but a new function
    for something the existing path already spells correctly.
- **Implementation notes**: one branch in `resolveFieldRef` ([executor_dev.go](executor_dev.go))
  returns the collapsed `FieldRef{Field: relationField}` — which `columnName` already renders
  as the FK column for a bare many2one ref — instead of nesting. `CollectJoins` therefore
  never sees it, so the join disappears for free. Restricted to **M2O**: for a one2many or
  many2many, `c.Orders.ID` is genuinely a column on the other table.
- **Prod**: the collapse happens at parse time, so the generated registry stores the
  collapsed form and the emitter is unchanged.
- **Also a semantic improvement**: an inner join drops rows with a NULL foreign key, so the
  join form quietly changed which rows a predicate could match. The local column does not.
- **Tests**: `tests/fkpath_test.go` — asserts the emitted SQL has no join and names
  `o."customer_id"`, that a non-key path (`o.Customer.Country`) still joins, and an
  end-to-end select. The first test was **confirmed to fail before the change**, printing the
  redundant join.

---

## 18. Explicit joins (implemented 2026-08-01)

**216 tests passing, dev and prod clean, dev/prod demo output byte-identical.**

Second step of the CTE work. §11 could only express an inner join, and only implicitly — a
comparison between two declared models. The kind of join had no spelling at all, and §11
recorded that this was the blocker: "an equality cannot express which side is optional".

### `goql.Join` mirrors `goql.From`
- **Decision**: a carrier declared as a lambda parameter, with `Model` (the source),
  `On` (the condition) and `Type` (the kind, defaulting to `Inner`):

  ```go
  func(i *Invoice, p *Payment, j *goql.Join) bool {
      j.Model = p
      j.On    = i.Ref == p.Ref
      j.Type  = goql.Left
      return p.Method == "card"
  }
  // → FROM "invoices" i LEFT JOIN "payments" p ON i."ref" = p."ref" WHERE p."method" = ?
  ```
- **Rationale**: `From` already pairs a source with a row handle; a join is that pair plus a
  condition and a kind. `On` is a `bool` field, so an ordinary comparison assigns to it and
  the compiler checks it — then it is parsed, not evaluated, like everything else in a body.
  Several `*Join` parameters compose in declaration order, the way several `*Sort` already do.
- **Alternatives considered**: a `goql.On(a, b)` marker (rejected in §11 and still — it has
  nowhere to put the kind); keeping the implicit rule as the only spelling (rejected — it
  cannot say LEFT); inferring the kind from nil-ability (rejected — unwritable).
- **§11's implicit rule is kept** as the shorter spelling for the inner case. An explicit
  join is what makes the kind sayable, not a replacement.

### All four kinds, refused per engine
`Inner`, `Left`, `Right`, `Full`. New `Spec.SupportsJoinType` — MySQL has no `FULL JOIN`, so
it is refused while building with a message naming the engine, rather than emitted and left
to fail at the server. SQLite gained `RIGHT`/`FULL` in 3.39 and the bundled engine is far
newer, so goql allows them there.

### Implementation notes
- **One JoinSpec per carrier parameter**, held in `parseContext.joinSpecs` keyed by parameter
  name with a `joinOrder` — exactly the shape `sortSpecs`/`sortOrder` already had. No new
  classification concept: `*Join` is an option carrier, matched by type.
- **The joined table leaves the FROM list.** `noteJoined` records every referenced
  participant, so without this the table would be both comma-joined in `FROM` *and* joined —
  a cross join applied before the ON condition. `fromList` now skips a table an explicit join
  already brings in.
- **Placeholder order**: explicit joins render before the predicate, so their bound values are
  appended first. `args = whereArgs` became `args = append(args, whereArgs...)`, which was
  latently wrong the moment anything bound a value ahead of the WHERE clause. Pinned by a
  Postgres test asserting `ON … = $1` and `WHERE … = $2`.
- **`options()` now returns an error**, because a join is the first modifier that can be
  *incompletely* declared. A carrier with no `Model`, no `On`, or neither is reported —
  the alternative being a query silently missing a join the caller wrote.
- **Update and Delete refuse one** rather than ignoring it, extending `rejectJoined`: they
  reach other tables through derived relation joins, not a join list.
- **Prod**: `JoinSpec.On` is a condition tree, so `emitOptions` emits it through
  `emitParseNode`. `%#v` would have written the node's pointers as a literal struct and
  produced a registry that bound garbage — the same trap `ParamRef` hit in §6.

### Tests
`tests/explicitjoin_test.go` — 10 tests: the rendered INNER and LEFT forms, the table not
being double-listed, placeholder ordering across dialects, MySQL refusing `FULL`, a missing
`Model`/`On`/declared-participant each reported, `Update` refusing a join, and an end-to-end
check that a LEFT join keeps an invoice with no payment where the inner join drops it.
The demo gained a LEFT join so the dev/prod comparison covers one; the generated registry was
verified to contain `Joins: []query.JoinSpec{{Table: "payments", Type: "LEFT", …}}`.

---

## 19. Expressions on the value side (implemented 2026-08-01)

**225 tests passing, dev and prod clean, dev/prod demo output byte-identical.**

Third step of the CTE work. Called "computed columns" while planning, which was misleading —
nothing is stored and no DDL is involved. `t."depth" + 1` in a SELECT list is an expression
the engine evaluates per row; what goql lacked was any **representation** for one.

### `ValueRef` becomes recursive
- **Decision**: a value is a column, a literal, **or** a `ParseExpr{Op, Left, Right}` whose
  operands are themselves `ValueRef`s. Operators: `+ - * / %`.
- **Rationale**: `resolveValueRef` had no `*ast.BinaryExpr` case, so `prev.Depth + 1` failed
  to parse — the same class of gap as the missing `*ast.UnaryExpr` for `!x`. Precedence and
  grouping need no work: the Go parser already built the tree, so goql walks it and renders
  each node parenthesised, which also stops the engine's own precedence from re-associating
  it.
- **All four positions** the value side appears in, since they all flow through
  `resolveValueRef` and restricting them would be more code rather than less: a predicate
  (`o.Total * 2 > 100`, via the new `ParseNode.LeftValue`), an UPDATE's SET, an
  INSERT … SELECT's value list, and a projection (`ParseSelect.Value`).
- **A literal projection** (`t.Band = 0`) rides the same field — the recursive CTE's anchor
  term needs exactly that for its depth column.

### String concatenation is a dialect divergence, not a token
Go spells concatenation `+`, so which one it is has to be decided from the operands' Go
types (`ParseExpr.Text`, set while parsing). New `Spec.Concat`: `||` on SQLite and Postgres,
`CONCAT()` on MySQL — where `||` is *logical OR* unless `PIPES_AS_CONCAT` is set, and a bare
`+` coerces both sides to numbers and answers 0. Silent on the engine that matters most.
- **Known limit**: a params-struct reference carries a name, not a type, so concatenating two
  params values reads as arithmetic. Writing one side as a column or literal resolves it.
- Arithmetic over text (`o.Priority * 2`) is refused while parsing rather than emitted.

### Placeholder order became load-bearing again
A computed column can bind a value **in the SELECT list**, which is emitted before everything
else — so `projectionList`, `selectExpr` and `groupTerms` all had to start returning args, and
`lambdaSearchIn` appends them in emission order. A projected expression is also a GROUP BY
term (SQL groups by the expression, not the alias), so its values bind twice, in two places.
`TestExpr_PlaceholderOrder` pins the whole sequence on Postgres: `$1` in the projection, `$2`
in the WHERE, `$3` in the GROUP BY.

### Bug found by a test that was written to fail
`o.Total * (o.Total + 1)` was rejected: `resolveValueRef` had no `*ast.ParenExpr` case.
Parentheses carry no meaning of their own — the grouping they expressed is already in the
tree's shape — so they are unwrapped.

### Deliberately out of scope
SQL functions (`COALESCE`, `LOWER`, `ABS`) need a marker vocabulary and a portability
decision of their own, and nothing in tiers 2/3 needs them. NULL propagation is left as SQL
defines it: `prev.Depth + 1` over a NULL column is NULL, not 1.

### Tests
`tests/expr_test.go` — 8 tests: UPDATE SET, a computed left-hand side, preserved nesting,
a projected expression and its GROUP BY, a projected constant, concatenation across SQLite
and MySQL, arithmetic-on-text refused, placeholder ordering, and an end-to-end row. The demo
gained `t.Label = o.Priority + " / " + o.ShippingMethod`, and the generated registry was
verified to contain the reconstructed `query.ParseExpr`.

---

## 20. Tier 2 — reading from a query (implemented 2026-08-01)

**232 tests passing, dev and prod clean, dev/prod demo output byte-identical.**

The first case where a query's source is another query rather than a table. It closes a gap
nothing else could: **an aggregate over an aggregate** — the average of per-customer totals —
had no spelling at all.

### The binding is the CTE

```go
goql.Select[Summary](ctx, e, func(s *Summary, t *CustomerTotal, from *goql.From) bool {
    totals, _ := goql.Select[CustomerTotal](ctx, e, /* … GROUP BY customer … */)
    from.Query = totals
    from.Model = t
    s.Average = goql.Avg(t.Total)
    return t.Total > 0
})
// WITH "totals" AS (SELECT o."customer_id", SUM(o."total_amount") AS "Total" …)
// SELECT AVG(t."Total") AS "Average" FROM "totals" t WHERE t."Total" > ?
```

- **Decision**: `from.Query` names a query bound earlier in the body — the subquery spelling
  from §13, unchanged — and `from.Model` names the parameter standing for one of its rows.
  The CTE takes the **Go variable's name**, so the name is stated once.
- **Rationale**: `From` already meant "what this query reads from"; a named query is one of
  those. Pairing `Query` with `Model` was the user's call and it removes the collision the
  type-inferred alternative would have had — two CTEs with the same result type could not
  otherwise be told apart.
- **Alternatives considered**: `goql.Row(totals)` returning a row value (rejected — a second
  marker where the pairing already says it); inferring the row handle from its Go type
  (rejected — collides); a per-reference wrapper like `goql.Col(func(t *T) int {…})`
  (rejected — a marker at every reference site, and it cannot cover a WHERE or a join).

### No synthetic model is registered
A CTE's columns are exactly its defining query's projection, so `FieldRef` gained
`CTETable`/`CTEColumn` and a reference resolves to those names directly. `ParseCTE.Schema()`
builds a throwaway `models.Model` only where a builder needs a table name and a field map —
nothing is added to the registry, so a CTE cannot be referenced from outside its statement,
and the emitter has nothing to resolve at init time.

**A column the defining query does not select is caught while parsing**, since the column list
is known.

### Correlation is refused, and the refusal had to be built
A CTE is evaluated **before** the outer query produces a row, so it cannot reference one —
unlike an `IN`/`EXISTS` subquery, which §13 deliberately lets correlate. That turned out to be
structurally impossible already (a nested *projection* gets a fresh context and inherits no
participants), but the resulting error was `could not resolve field path: [o Total]`. The
outer models' names are now remembered purely so referencing one is reported for what it is.
The guard is in **two** places, because the value-side path discards the field-resolution
error and falls back to literal extraction.

### `WITH` always, derived table where it is missing
New `Spec.SupportsCTE`. All three shipped engines support `WITH`, so the fallback renders
`FROM (SELECT …) t` and repeats the subquery per use — the same result with a worse plan,
which beats a query that works on Postgres and fails on MySQL 5.7. Verified with a test-only
Spec that answers false, since otherwise the path would be unreachable and unverified.

### Details that had to change
- **A pointer parameter of an unregistered type is now a row handle, not the params struct**,
  at the API boundary *and* in the parser. A params struct is passed by value, which is what
  distinguishes them; no existing call used a pointer params struct.
- **Placeholder order**: definitions render before the statement that reads them, so their
  values bind first. Pinned on Postgres.
- **Per-CTE aliases**: a definition gets a fresh `AliasMap` (sharing the placeholder counter),
  so a CTE over a table the outer query also reads does not collide — the same rule §16 needed
  for set branches.
- **Two nil-pointer crashes fixed**: `checkAggregateColumn` and `isTextValue` both assumed
  every `FieldRef` carries a `models.Field`. A CTE column does not.
- **`newParseContext` registered a nil participant** under the empty name for projection
  lambdas, which crashed the moment a nested lambda iterated the enclosing participants.
  Pre-existing; reachable only now.

### Tier 1 — dropped
A binding used as a *value* (`IN (…)`) keeps rendering inline rather than becoming a `WITH`.
- **Decision**: not implemented, at the user's direction.
- **Rationale**: the Go source is identical either way — tier 1 was never a syntax question,
  only a rendering one — so it solves nothing. It would deduplicate a subquery referenced
  twice and produce SQL that reads better, both of which are cosmetic or a plan hint, against
  rewriting the SQL every subquery test asserts and restoring the correlation tracking tier 2
  made unnecessary (a correlated subquery can never be a CTE, so both spellings would have to
  coexist regardless).
- **The one case that is not cosmetic** stays a known limitation: §13 refuses a subquery over
  a table the enclosing query already uses, because both would render with the same alias.
  Promoting it to a CTE would fix that, since a CTE gets its own alias map — but if that case
  matters, per-occurrence aliases in `AliasMap` is the direct fix, not a rendering change
  whose side effect happens to help.

---

## 21. Tier 3 — recursive CTEs (implemented 2026-08-01)

**238 tests passing, dev and prod clean, dev/prod demo output byte-identical.**

The one thing with no substitute: walking a hierarchy. Built on §17–§20 — the FK path,
explicit joins, expressions and `from.Query` — with one genuinely new idea.

### The self-reference is a declared parameter

```go
tree, _ := goql.Select[CatNode](ctx, e, func(t []*CatNode) bool {   // ← t: the rows so far
    roots, _ := goql.Select[CatNode](ctx, e, /* anchor: parent IS NULL, Depth = 0 */)
    children, _ := goql.Select[CatNode](ctx, e,
        func(r *CatNode, prev *CatNode, c *Category, f *goql.From, j *goql.Join) bool {
            f.Model = c
            j.Query, j.Model = t, prev          // ← JOIN the CTE to itself
            j.On = c.Parent.ID == prev.ID
            r.Depth = prev.Depth + 1
            return prev.Depth < 5
        })
    return goql.UnionAll(roots, children)
})
```
```sql
WITH RECURSIVE "tree" AS (
      SELECT c."id" AS "ID", c."name" AS "Name", $1 AS "Depth" FROM "categories" c WHERE c."parent_id" IS NULL
    UNION ALL
      SELECT c."id" AS "ID", c."name" AS "Name", (t."Depth" + $2) AS "Depth"
      FROM "categories" c INNER JOIN "tree" t ON c."parent_id" = t."ID" WHERE t."Depth" < $3)
SELECT COUNT(*) AS "Nodes", MAX(t."Depth") AS "Deepest" FROM "tree" t
```

- **Decision**: a combining lambda may declare `t []*T` — the rows the query has produced so
  far — and a branch joins it with `join.Query`. **`RECURSIVE` is derived from that join**,
  never declared.
- **Rationale**: this was the user's requirement and it is the right one. The earlier proposal
  inferred recursion from a second parameter of the result type, which left the single most
  important fact about the query invisible: the Go source read as "a union of two similar
  queries". Here recursion is written down three times over — the handle is declared, joined,
  and its condition spelled out — and the three cannot disagree because two of them *are* the
  join.
- **Why `[]*T`**: it is exactly what a goql call returns, so the handle has the type the rows
  actually have. It also distinguishes itself from every other parameter shape by type, the
  same rule options, params, participants and row handles already use.
- **`goql.Recursive(...)` was dropped**: an explicit join says the same thing with machinery
  that already exists, and a marker would have been a second way to spell a union.

### The name is only known afterwards
The CTE is named after its Go binding (`tree`), which is not known while its own definition is
being parsed. So a self-reference carries the placeholder `"\x00self"` and is renamed when the
binding completes — a walk over the whole tree, since it appears both as a joined table and as
the table of every column read through the joined row handle. Finding one is also what sets
`Recursive`, so detection and naming are the same pass.

### Parse order is load-bearing for the first time
`prev.Depth` inside the recursive branch resolves against **the anchor's projection** — the
branch parsed before it — which is exactly where SQL takes a recursive CTE's column types
from. Joining the handle before anything has been selected into it is refused, naming the
anchor.

### Refused rather than emitted
`checkRecursive` rejects what no engine allows in a recursive term — aggregates, `GROUP BY`,
`ORDER BY`, `LIMIT`, more than one self-reference — and an anchor that references itself, and
a combination that is not `UNION`/`UNION ALL`. Each would otherwise fail at the database
against generated SQL the caller never wrote. **A derived table cannot reference itself**, so
the tier 2 fallback is refused for a recursive query rather than degraded.

### Non-termination stays the caller's problem
Postgres and SQLite have no depth guard and forbid `LIMIT` in a recursive term, so the tools
are `Union` (which dedups, terminating on cycles) or a depth column filtered in the recursive
branch — the shape above. `TestRecursive_DepthBoundsTheWalk` builds six levels and confirms
the walk stops where the predicate says.

### Two bugs found while wiring it
- **The combining lambda's first parameter was read as a result type.** A leading `[]*T` is
  the self handle and there is no result parameter — the branches carry the projection.
- **Inheritance overwrote the handle**: a lambda that declares its own self handle must not
  have the enclosing one copied over it.
- Also: a set-bodied CTE needed `lambdaSetSearchIn`, so a union nested in a `WITH` keeps the
  enclosing placeholder sequence instead of restarting it.

### Tests
`tests/recursive_test.go` — 6 tests: the rendered `WITH RECURSIVE`, a self-referencing anchor
refused, ordering in the recursive term refused, no fallback without CTEs, an end-to-end walk
of a three-level hierarchy, and the depth bound. The demo walks a category tree, and the
generated registry was verified to contain `Recursive: true`.

---

## 22. Documentation, and three bugs it found (2026-08-01)

**248 tests passing, dev and prod clean, dev/prod demo output byte-identical.**

A Read the Docs site (`docs/`, MkDocs + Material, `.readthedocs.yaml`) — 25 pages covering the
lambda contract, models, every query shape, dialects, migrations and production builds. Built
with `mkdocs build --strict`, so a broken internal link fails.

**Writing the docs was a review pass**, and it surfaced three defects that the test suite did
not. Each is fixed, with a regression test.

### A projection's ORDER BY named the model, not the projection
`sort.By = "Total"` on an aggregate query emitted `ORDER BY o."total_amount"` — the model's
column — rather than the projected `"Total"`. SQLite tolerates it; **Postgres rejects the
statement outright**, since that column is neither grouped nor aggregated. The set-operation
path already resolved sorts against projected columns, so the fix generalises that into
`projectionTail` and uses it for any query that names its own columns. Naming a column the
projection does not select is now an error rather than a wrong ORDER BY.

### A params struct could not reach a CTE branch
`parseSubBody` inherits `paramsName`/`paramsType` from the enclosing context;
`parseProjectionSource` did not. So a params value was unusable inside a CTE's defining
lambda — the natural way to seed a recursive walk from a runtime id. One-line inconsistency,
now fixed, with an end-to-end test walking a subtree from a params-supplied node.

### Tuple assignments were silently dropped
`r.ID, r.Name, r.Depth = c.ID, c.Name, 0` is ordinary Go and reads well in a projection — and
matched **nothing**. Every handler (`tryParseOptionAssignment`, `tryParseSubqueryDecl`,
`tryParseProjection`, `tryParseAssignment`) checks `len(s.Lhs) != 1` and skips, so a tuple
assignment fell through all four and vanished. Where every column used one it surfaced as
"the projection selects nothing"; mixed with single assignments it would have **silently
omitted columns**.
- **Decision**: expand `a, b = x, y` into one statement per target before dispatch
  (`expandAssignments`), applied at every statement-list walk. A declaration binding two names
  from one call (`x, _ := goql.Select(…)`) has a single right-hand side and is left alone.
- **Rationale**: it is the same silent-ignore class as the dropped `Preload`, the swallowed
  `from.Model` and the missing prod `Options` — fixed at the dispatch point rather than in
  each of the four handlers.

### Documented examples are tested
`tests/docs_test.go` exercises the examples that were written from memory rather than lifted
from a passing test — `Condition` forms, composed sorts, a subquery projecting a named column,
joining a CTE, inserting a model into itself, and `Conflict{Ignore}`. A change that invalidates
the documentation now fails the suite.
