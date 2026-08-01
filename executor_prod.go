//go:build prod

package goql

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"runtime"

	"github.com/aekis-dev/goql/query"
)

func getExecutor() QueryExecutor {
	return &CompileExecutor{}
}

type CompileExecutor struct{}

// compiledKey derives the registry key for a lambda.
//
// It hashes the runtime function name (e.g. "main.main.func3"), which is the only
// handle a prod binary has: there is no source to read, so the key cannot be derived
// from the lambda's own contents. That name embeds a positional index, so reordering
// closures inside a function changes which body a lambda maps to — hence the entity
// type check in ParseBody, and the requirement to re-run `go generate` whenever
// lambdas are added, removed or moved.
func compiledKey(fn any) (string, error) {
	v := reflect.ValueOf(fn)
	if v.Kind() != reflect.Func {
		return "", fmt.Errorf("goql: expected a function, got %T", fn)
	}
	rf := runtime.FuncForPC(v.Pointer())
	if rf == nil {
		return "", fmt.Errorf("goql: could not resolve function pointer — binary may be stripped")
	}
	sum := sha256.Sum256([]byte(rf.Name()))
	return fmt.Sprintf("%x", sum[:8]), nil
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
