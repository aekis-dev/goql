//go:build !prod

package goql

import "github.com/aekis-dev/goql/query"
import "github.com/aekis-dev/goql/models"

// In dev mode the registry is unused — DebugExecutor parses lambdas at runtime
func RegisterQuery(key string, parsed *query.ParseQuery) {} // dev stub

// ResolveField is only needed at prod init time — stub for dev
func ResolveField(tableName, fieldName string) *models.Field {
	return nil
}
