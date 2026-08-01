//go:build prod

package goql

import (
	"fmt"
	"sync"

	"github.com/aekis-dev/goql/models"
	"github.com/aekis-dev/goql/query"
)

var (
	compiledMu      sync.RWMutex
	compiledQueries = map[string]*query.ParseQuery{}
)

// RegisterBody records a pre-parsed lambda body. Called from the generated
// goql_registry_prod.go at init time.
func RegisterQuery(key string, parsed *query.ParseQuery) {
	compiledMu.Lock()
	compiledQueries[key] = parsed
	compiledMu.Unlock()
}

// ResolveField looks up a field by table and field name for the generated registry.
//
// It panics rather than returning an error because it is called inline from generated
// init() code, where there is no caller to hand an error to — and a miss means the
// generated registry disagrees with the registered models, which is not recoverable at
// runtime. Registration order matters: the models' init() must run before the
// generated registry's.
func ResolveField(tableName, fieldName string) *models.Field {
	field, err := models.FindFieldByTable(tableName, fieldName)
	if err != nil {
		panic(fmt.Sprintf(
			"goql: ResolveField(%q, %q): %v — the generated registry is out of sync with "+
				"the registered models; re-run `go generate` and ensure model packages are imported",
			tableName, fieldName, err))
	}
	return field
}
