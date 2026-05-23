//go:build !prod

package orm

import "github.com/aekis/goql/pkg/query"
import "github.com/aekis/goql/pkg/models"

// In dev mode the registry is unused — DebugExecutor parses lambdas at runtime
func RegisterBody(key string, body *query.ParseBody) {} // dev stub

// ResolveField is only needed at prod init time — stub for dev
func ResolveField(tableName, fieldName string) *models.Field {
	return nil
}
