//go:build prod

package goql

import (
	"fmt"
	"reflect"
	"runtime"

	"github.com/aekis-dev/goql/query"
)

func getExecutor() QueryExecutor {
	return &CompileExecutor{}
}

type CompileExecutor struct{}

// compiledKey derives the registry key for a lambda: the base name of its source file and
// the line the runtime reports for it.
//
// It used to hash the runtime function name ("main.main.func3"). That name is only stable
// for a literal written directly in a function — the compiler rewrites it for a nested one,
// and differently depending on inlining, so a nested lambda could not be keyed at all:
//
//	                  default build                                default with -gcflags=all=-l
//	nested closure    main.TopLevel.TopLevel.func2.func4           main.TopLevel.func2.1
//
// The reported line is identical in both, and across -N, -N -l and -race. The file's base
// name rather than its path, because -trimpath rewrites paths and the base name survives it;
// goqlc errors if two lambdas in the build would share a key.
//
// The generator emits a key for both anchors the runtime may report — the `func` keyword and
// the first statement — because which one it picks depends on the body's shape.
func compiledKey(fn any) (string, error) {
	v := reflect.ValueOf(fn)
	if v.Kind() != reflect.Func {
		return "", fmt.Errorf("goql: expected a function, got %T", fn)
	}
	rf := runtime.FuncForPC(v.Pointer())
	if rf == nil {
		return "", fmt.Errorf("goql: could not resolve function pointer — binary may be stripped")
	}
	file, line := rf.FileLine(v.Pointer())
	return LambdaKey(file, line), nil
}

// The call is ignored here: the registry stores a query already parsed in the right mode,
// with its Func recorded.
func (e *CompileExecutor) ParseQuery(fn any, _ string) (*query.ParseQuery, error) {
	key, err := compiledKey(fn)
	if err != nil {
		return nil, err
	}

	compiledMu.RLock()
	body, ok := compiledQueries[key]
	compiledMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf(
			"goql: no compiled body for key %s — re-run `go generate` to refresh the registry", key)
	}

	return body, nil
}
