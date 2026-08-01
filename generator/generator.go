//go:build !prod

// Package generator pre-parses goql lambdas into a registry so a `-tags prod` binary
// can run them without reading its own source at runtime.
//
// It is driven from your own module, because resolving a lambda's fields requires the
// models to be registered — which only happens when the packages declaring them are
// imported and their init() runs. Add a small program that imports your models and
// calls Run, then point go:generate at it:
//
//	//go:generate go run ./tools/goqlc .
//
// See pkg/tools/goqlc in this repository for a working example.
package generator

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/aekis-dev/goql"
	"github.com/aekis-dev/goql/query"
)

const generatedFileName = "goql_registry_prod.go"

// Run scans dir for goql lambdas and writes a registry file into each package that
// contains them. Models must already be registered in this process.
func Run(dir string) error {
	if dir == "" {
		dir = "."
	}
	// Tolerate the usual `./...` spelling from go:generate directives.
	dir = strings.TrimSuffix(strings.TrimSuffix(dir, "..."), "/")
	if dir == "" {
		dir = "."
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}

	modulePath, moduleRoot, err := findModule(absDir)
	if err != nil {
		return err
	}

	g := &generator{
		fset:       token.NewFileSet(),
		executor:   &goql.DebugExecutor{},
		modulePath: modulePath,
		moduleRoot: moduleRoot,
		outputs:    make(map[string]*pkgOutput),
	}

	log.Printf("goqlc: scanning %s (module %s)", absDir, modulePath)
	if err := g.walkDir(absDir); err != nil {
		return fmt.Errorf("walk: %w", err)
	}

	if len(g.outputs) == 0 {
		log.Printf("goqlc: no lambdas found")
		return nil
	}

	for _, out := range g.outputs {
		if err := g.emit(out); err != nil {
			return fmt.Errorf("emit: %w", err)
		}
		log.Printf("goqlc: %d bodies → %s", len(out.entries), filepath.Join(out.dir, generatedFileName))
	}
	return nil
}

// entry is one compiled lambda body.
type entry struct {
	key     string
	comment string
	parsed  *query.ParseQuery
}

// pkgOutput collects the entries for a single package directory. Lambdas in different
// packages need separate registry files, each declaring its own package.
type pkgOutput struct {
	dir     string
	pkgName string
	entries []*entry
}

type generator struct {
	fset       *token.FileSet
	executor   *goql.DebugExecutor
	modulePath string
	moduleRoot string
	outputs    map[string]*pkgOutput
}

// findModule walks up from dir to locate go.mod and returns the module path and the
// directory containing it.
func findModule(dir string) (modulePath, moduleRoot string, err error) {
	for d := dir; ; d = filepath.Dir(d) {
		candidate := filepath.Join(d, "go.mod")
		if data, readErr := os.ReadFile(candidate); readErr == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if path, ok := strings.CutPrefix(line, "module "); ok {
					return strings.TrimSpace(path), d, nil
				}
			}
			return "", "", fmt.Errorf("no module directive in %s", candidate)
		}
		if parent := filepath.Dir(d); parent == d {
			return "", "", fmt.Errorf("no go.mod found at or above %s", dir)
		}
	}
}

// importPath returns the full import path for a directory, which is what
// runtime.FuncForPC reports as the package qualifier — the short package name is only
// correct for package main.
func (g *generator) importPath(dir string) (string, error) {
	rel, err := filepath.Rel(g.moduleRoot, dir)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return g.modulePath, nil
	}
	return g.modulePath + "/" + filepath.ToSlash(rel), nil
}

func (g *generator) walkDir(absDir string) error {
	return filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := filepath.Base(path)
			if path != absDir && (base == "vendor" || base == "testdata" ||
				base == "generator" || strings.HasPrefix(base, ".") || strings.HasPrefix(base, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") ||
			strings.HasSuffix(path, "_prod.go") ||
			filepath.Base(path) == generatedFileName {
			return nil
		}
		return g.processFile(path)
	})
}

func (g *generator) processFile(path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	file, err := parser.ParseFile(g.fset, path, src, 0)
	if err != nil {
		log.Printf("goqlc: skipping %s: %v", path, err)
		return nil
	}

	dir := filepath.Dir(path)

	// Match how the runtime qualifies symbols: package main is named literally "main",
	// every other package by its full import path.
	pkgQualifier := file.Name.Name
	if pkgQualifier != "main" {
		var err error
		pkgQualifier, err = g.importPath(dir)
		if err != nil {
			return err
		}
	}

	for _, decl := range file.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok || funcDecl.Body == nil {
			continue
		}

		// Must match runtime.FuncForPC's naming exactly, e.g.
		// "github.com/you/app.DoThing" or "github.com/you/app.(*Service).Sync".
		// goql.EnclosingFuncName is shared with dev-mode lookup so the two cannot drift.
		enclosing := fmt.Sprintf("%s.%s", pkgQualifier, goql.EnclosingFuncName(funcDecl))

		ormCalls := findORMCalls(funcDecl.Body)
		if len(ormCalls) == 0 {
			continue
		}

		// The compiler numbers funcN over the literals written directly in the enclosing
		// function — 1..n in source order. A nested closure continues the same counter but
		// is named under its parent, so counting flat would shift every later sibling and
		// key it to the wrong body. goql.TopLevelFuncLits is shared with dev-mode lookup so
		// the two cannot drift.
		for i, fl := range goql.TopLevelFuncLits(funcDecl.Body) {
			method, isORM := ormCalls[fl]
			if !isORM {
				continue
			}

			start := g.fset.Position(fl.Pos())
			end := g.fset.Position(fl.End())
			if start.Offset >= end.Offset || end.Offset > len(src) {
				continue
			}
			lambdaSrc := string(src[start.Offset:end.Offset])

			// The generator knows which goql function the lambda was passed to, which the
			// signature cannot say: an Insert destination and a predicate that joins
			// another model look identical.
			parsed, err := g.executor.ParseQueryFromSource(lambdaSrc, method)
			if err != nil {
				log.Printf("goqlc: skipping lambda at %s:%d: %v", filepath.Base(path), start.Line, err)
				continue
			}

			preview := strings.Join(strings.Fields(lambdaSrc), " ")
			if len(preview) > 60 {
				preview = preview[:60] + "..."
			}

			out := g.outputFor(dir, file.Name.Name)
			out.entries = append(out.entries, &entry{
				key:     computeKey(fmt.Sprintf("%s.func%d", enclosing, i+1)),
				comment: fmt.Sprintf("%s at %s:%d — %s", method, filepath.Base(path), start.Line, preview),
				parsed:  parsed,
			})
		}

		// A goql lambda written inside another closure cannot be keyed: the compiler names
		// it under its parent (Outer.func2.func5), a scheme goql does not reproduce. Say so,
		// rather than emitting a key that would resolve to something else. In prod such a
		// call fails loudly with "no compiled body".
		for fl, method := range ormCalls {
			if !isTopLevel(funcDecl.Body, fl) {
				log.Printf("goqlc: skipping %s lambda at %s:%d — it is nested inside another "+
					"closure, which cannot be keyed; move it to the top level of its function",
					method, filepath.Base(path), g.fset.Position(fl.Pos()).Line)
			}
		}
	}

	return nil
}

// isTopLevel reports whether a literal is written directly in body rather than inside
// another function literal.
func isTopLevel(body *ast.BlockStmt, target *ast.FuncLit) bool {
	for _, fl := range goql.TopLevelFuncLits(body) {
		if fl == target {
			return true
		}
	}
	return false
}

func (g *generator) outputFor(dir, pkgName string) *pkgOutput {
	if out, ok := g.outputs[dir]; ok {
		return out
	}
	out := &pkgOutput{dir: dir, pkgName: pkgName}
	g.outputs[dir] = out
	return out
}

// lambdaAPI lists the goql functions that take a lambda to be parsed. The struct-based
// operations (Create/Search/Write/Remove) take entity values instead.
var lambdaAPI = map[string]bool{
	"Select": true,
	"Insert": true,
	"Update": true,
	"Delete": true,
	"Exists": true,
}

// findORMCalls returns the function literals passed directly to a goql call.
//
// Only literals written at the call site are found: one assigned to a variable first is
// invisible here, and such a call falls back to being unresolvable in a prod binary.
func findORMCalls(body *ast.BlockStmt) map[*ast.FuncLit]string {
	calls := make(map[*ast.FuncLit]string)
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calleeName(call.Fun)
		if !lambdaAPI[name] {
			return true
		}
		for _, arg := range call.Args {
			if fl, ok := arg.(*ast.FuncLit); ok {
				calls[fl] = name
			}
		}
		return true
	})
	return calls
}

// calleeName resolves the called function's name, unwrapping generic instantiation:
// goql.Select[Customer] parses as an IndexExpr wrapping the selector.
func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.IndexExpr:
		return calleeName(f.X)
	case *ast.IndexListExpr:
		return calleeName(f.X)
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.Ident:
		return f.Name
	}
	return ""
}

func computeKey(runtimeName string) string {
	sum := sha256.Sum256([]byte(runtimeName))
	return fmt.Sprintf("%x", sum[:8])
}

func (g *generator) emit(out *pkgOutput) error {
	var buf bytes.Buffer

	buf.WriteString("// Code generated by goqlc. DO NOT EDIT.\n")
	buf.WriteString("//go:build prod\n\n")
	fmt.Fprintf(&buf, "package %s\n\n", out.pkgName)
	buf.WriteString("import (\n")
	buf.WriteString("\t\"github.com/aekis-dev/goql\"\n")
	buf.WriteString("\t\"github.com/aekis-dev/goql/query\"\n")
	buf.WriteString(")\n\n")
	buf.WriteString("func init() {\n")

	for _, e := range out.entries {
		fmt.Fprintf(&buf, "\t// %s\n", e.comment)
		fmt.Fprintf(&buf, "\tgoql.RegisterQuery(%q,\n\t\t%s,\n\t)\n\n",
			e.key, emitParseQuery(e.parsed, 2))
	}

	buf.WriteString("}\n")

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("generated code is not valid Go: %w\n%s", err, buf.String())
	}

	return os.WriteFile(filepath.Join(out.dir, generatedFileName), formatted, 0o644)
}
