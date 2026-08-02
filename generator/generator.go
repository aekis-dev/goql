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
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"sort"
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
		keys:       make(map[string]string),
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

	// keys guards against two lambdas hashing to the same registry key, which the base
	// name plus line scheme makes possible across directories (two main.go, same line).
	// A collision is reported rather than silently resolving one lambda to the other body.
	keys map[string]string
}

// sortedLits returns the goql lambdas in source order, so the generated registry is stable
// between runs — ranging over the map directly would shuffle it.
func sortedLits(fset *token.FileSet, calls map[*ast.FuncLit]string) []*ast.FuncLit {
	lits := make([]*ast.FuncLit, 0, len(calls))
	for fl := range calls {
		lits = append(lits, fl)
	}
	sort.Slice(lits, func(i, j int) bool { return lits[i].Pos() < lits[j].Pos() })
	return lits
}

// firstStmtLine is the line the Go runtime may report for a closure instead of its `func`
// keyword: that of the first statement in its body.
func firstStmtLine(fset *token.FileSet, lit *ast.FuncLit) int {
	if lit.Body == nil {
		return fset.Position(lit.Pos()).Line
	}
	if len(lit.Body.List) == 0 {
		return fset.Position(lit.Body.Lbrace).Line
	}
	return fset.Position(lit.Body.List[0].Pos()).Line
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

		ormCalls := findORMCalls(funcDecl.Body)
		if len(ormCalls) == 0 {
			continue
		}

		// Every goql lambda in the function, nested ones included. Keys are positional
		// (goql.LambdaKey), so a literal written inside another closure is keyed like any
		// other — the funcN numbering this used to rely on could not name one.
		for _, fl := range sortedLits(g.fset, ormCalls) {
			method := ormCalls[fl]

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

			// The runtime attributes a closure's entry either to its `func` keyword or to
			// its first statement, depending on the body, and the generator cannot know
			// which. Emitting both anchors means the lookup hits whichever it reports.
			anchors := map[int]bool{start.Line: true, firstStmtLine(g.fset, fl): true}
			out := g.outputFor(dir, file.Name.Name)
			for line := range anchors {
				key := goql.LambdaKey(path, line)
				// The full path, not the base name the key uses: two files that share a
				// base name are exactly the collision this guards against, so recording
				// them under the same description would hide it.
				where := fmt.Sprintf("%s:%d", path, line)
				if prev, clash := g.keys[key]; clash && prev != where {
					return fmt.Errorf(
						"goqlc: %s and %s would share a registry key — keys are the file's "+
							"base name and a line, so move one of the lambdas to a different line",
						prev, where)
				}
				g.keys[key] = where
				out.entries = append(out.entries, &entry{
					key:     key,
					comment: fmt.Sprintf("%s at %s:%d — %s", method, filepath.Base(path), start.Line, preview),
					parsed:  parsed,
				})
			}
		}
	}

	return nil
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
