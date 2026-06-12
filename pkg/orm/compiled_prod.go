//go:build prod

package orm

import (
	"fmt"
	"sync"

	"github.com/aekis-dev/goql/pkg/models"
)

var (
	compiledMu     sync.RWMutex
	compiledBodies = map[string]*ParseBody{}
)

func RegisterBody(key string, body *ParseBody) {
	compiledMu.Lock()
	compiledBodies[key] = body
	compiledMu.Unlock()
}

// ResolveField looks up a FieldSchema by table and field name.
// Called at init() time from generated goql_registry_prod.go.
// Panics if the models is not registered — Register() must be called before init() runs.
func ResolveField(tableName, fieldName string) *models.Field {
	schemaRegistry.mu.RLock()
	defer schemaRegistry.mu.RUnlock()

	for _, schema := range schemaRegistry.schemas {
		if schema.TableName == tableName {
			if field, ok := schema.Fields[fieldName]; ok {
				return field
			}
		}
	}
	panic(fmt.Sprintf(
		"goql: ResolveField(%q, %q) — models not registered or field not found. "+
			"Ensure orm.Register() is called before init() resolves compiled queries.",
		tableName, fieldName))
}
