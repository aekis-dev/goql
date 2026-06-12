package query

import (
	"fmt"
	"strings"

	"github.com/aekis-dev/goql/pkg/models"
)

// EntityDelete builds a DELETE query for a single entity by PK
func EntityDelete(pkColumn string, pkValue any, schema *models.Model) (*Query, error) {
	sql := fmt.Sprintf("DELETE FROM %s WHERE %s IN (?)",
		schema.TableName, pkColumn)
	return &Query{SQL: sql, Args: []any{pkValue}}, nil
}

// LambdaDelete builds a DELETE query from a parsed lambda predicate
func LambdaDelete(body *ParseBody, schema *models.Model) (*Query, error) {
	if body.Condition == nil {
		sql := fmt.Sprintf("DELETE FROM %s", schema.TableName)
		return &Query{SQL: sql}, nil
	}

	where, whereArgs := WhereClause(body.Condition)
	joins := CollectJoins(body.Condition, make(map[string]bool))

	var sql string
	if len(joins) > 0 {
		pkColumn := schema.PrimaryKey.GetColumnName()
		var joinClauses []string
		for _, ref := range joins {
			joinClauses = append(joinClauses, BuildJoinClause(ref))
		}
		sql = fmt.Sprintf(
			"DELETE FROM %s WHERE %s IN (SELECT %s.%s FROM %s %s WHERE %s)",
			schema.TableName,
			pkColumn,
			schema.TableName,
			pkColumn,
			schema.TableName,
			strings.Join(joinClauses, " "),
			where)
	} else {
		sql = fmt.Sprintf("DELETE FROM %s WHERE %s",
			schema.TableName, where)
	}

	return &Query{SQL: sql, Args: whereArgs}, nil
}
