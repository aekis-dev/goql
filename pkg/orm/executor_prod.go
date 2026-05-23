//go:build prod

package orm

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"runtime"
)

func getExecutor() QueryExecutor {
	return &CompileExecutor{}
}

type CompileExecutor struct{}

func getStableKey(fn any) string {
	pc := reflect.ValueOf(fn).Pointer()
	f := runtime.FuncForPC(pc)
	if f == nil {
		panic("goql: could not resolve function pointer — binary may be stripped")
	}
	// f.Name() is stable across line changes e.g. "github.com/you/pkg.FuncName.func1"
	sum := sha256.Sum256([]byte(f.Name()))
	return fmt.Sprintf("%x", sum[:8])
}

func (e *CompileExecutor) ParseBody(fn any) (*ParseBody, error) {
	key := getStableKey(fn)

	compiledMu.RLock()
	body, ok := compiledBodies[key]
	compiledMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("goql: no compiled body for key %s — run goqlc", key)
	}
	return body, nil
}
