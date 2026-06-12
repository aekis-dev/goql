package query

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/aekis-dev/goql/pkg/models"
)

// LambdaSearch builds a SELECT query from a parsed lambda predicate
func LambdaSearch(body *ParseBody, schema *models.Model) (*Query, error) {
	table_alias := schema.TableName[0:1]
	if body.Condition == nil {
		return &Query{
			SQL: fmt.Sprintf("SELECT %s.* FROM %s %s",
				table_alias, schema.TableName, table_alias),
			Args: nil,
		}, nil
	}

	joins := CollectJoins(body.Condition, make(map[string]bool))
	var joinClauses []string
	for _, joinNode := range joins {
		joinClauses = append(joinClauses, BuildJoinClause(joinNode))
	}

	where, args := WhereClause(body.Condition)

	var sql string
	if len(joinClauses) > 0 {
		sql = fmt.Sprintf("SELECT %s.* FROM %s %s %s WHERE %s",
			schema.TableName,
			schema.TableName, schema.TableName,
			strings.Join(joinClauses, " "),
			where)
	} else {
		sql = fmt.Sprintf("SELECT %s.* FROM %s %s WHERE %s",
			table_alias,
			schema.TableName, table_alias,
			where)
	}
	return &Query{SQL: sql, Args: args}, nil
}

// EntitySearch builds a SELECT query from one or more entities.
// Non-zero fields are used as WHERE conditions.
// Multiple entities produce OR conditions (IN clauses per column).
// A single entity with PK set searches by PK only.
func EntitySearch(entities []models.Entity, schema *models.Model) (*Query, error) {
	if len(entities) == 0 {
		return &Query{
			SQL:  fmt.Sprintf("SELECT %s.* FROM %s", schema.TableName, schema.TableName),
			Args: nil,
		}, nil
	}

	// Single entity with PK set — search by PK only
	if len(entities) == 1 {
		pkColumn, pkValue := entities[0].PrimaryKey()
		v := reflect.ValueOf(pkValue)
		if pkValue != nil && !isZeroValue(v) {
			return &Query{
				SQL:  fmt.Sprintf("SELECT %s.* FROM %s WHERE %s = ?", schema.TableName, schema.TableName, pkColumn),
				Args: []any{pkValue},
			}, nil
		}
	}

	// Collect non-zero field values across all entities
	// columnValues maps column name → unique values found across entities
	columnValues := make(map[string][]any)

	for _, entity := range entities {
		ev := reflect.ValueOf(entity)
		if ev.Kind() == reflect.Ptr {
			ev = ev.Elem()
		}
		et := ev.Type()

		for _, fieldSchema := range schema.Fields {
			if fieldSchema.PrimaryKey {
				continue
			}

			fieldValue, found := getFieldValue(ev, et, fieldSchema.Name)
			if !found || isZeroValue(fieldValue) {
				continue
			}

			switch fieldSchema.RelationKind() {
			case models.O2M, models.M2M:
				continue

			case models.M2O:
				if fieldValue.Kind() == reflect.Ptr && !fieldValue.IsNil() {
					if related, ok := fieldValue.Interface().(models.Entity); ok {
						_, relPK := related.PrimaryKey()
						col := fieldSchema.GetFKColumn()
						if !containsValue(columnValues[col], relPK) {
							columnValues[col] = append(columnValues[col], relPK)
						}
					}
				}

			default:
				col := fieldSchema.GetColumnName()
				val := fieldValue.Interface()
				if !containsValue(columnValues[col], val) {
					columnValues[col] = append(columnValues[col], val)
				}
			}
		}
	}

	if len(columnValues) == 0 {
		return &Query{
			SQL: fmt.Sprintf("SELECT %s.* FROM %s", schema.TableName, schema.TableName),
		}, nil
	}

	var conditions []string
	var args []any

	for col, vals := range columnValues {
		if len(vals) == 1 {
			// Single value — check for LIKE pattern
			if s, ok := vals[0].(string); ok && strings.Contains(s, "%") {
				conditions = append(conditions, fmt.Sprintf("%s LIKE ?", col))
			} else {
				conditions = append(conditions, fmt.Sprintf("%s = ?", col))
			}
			args = append(args, vals[0])
		} else {
			// Multiple values — IN clause
			placeholders := make([]string, len(vals))
			for i := range placeholders {
				placeholders[i] = "?"
			}
			conditions = append(conditions, fmt.Sprintf("%s IN (%s)",
				col, strings.Join(placeholders, ", ")))
			args = append(args, vals...)
		}
	}

	// Multiple entities — join with OR
	// Single entity without PK — join with AND
	joiner := " AND "
	if len(entities) > 1 {
		joiner = " OR "
	}

	sql := fmt.Sprintf("SELECT %s.* FROM %s WHERE %s",
		schema.TableName,
		schema.TableName,
		strings.Join(conditions, joiner))

	return &Query{SQL: sql, Args: args}, nil
}
