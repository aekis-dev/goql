package query

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aekis-dev/goql/pkg/models"
)

// LambdaWrite builds an UPDATE query from a parsed lambda body
func LambdaWrite(body *ParseBody, schema *models.Model) (*Query, error) {
	var setClauses []string
	var args []any

	// Scalar assignments
	for _, assignment := range body.Assignments {
		col := assignment.Field.Field.GetColumnName()
		if assignment.Field.Field.RelationKind() == models.M2O {
			col = assignment.Field.Field.GetFKColumn()
		}
		if assignment.Value.IsColumn {
			setClauses = append(setClauses, fmt.Sprintf("%s = %s", col, assignment.Value.Field.FullColumn()))
		} else {
			setClauses = append(setClauses, fmt.Sprintf("%s = ?", col))
			if strings.ToLower(assignment.Field.Field.Type) == "jsonb" && assignment.Value.Value != nil {
				jfv, err := json.Marshal(assignment.Value.Value)
				if err != nil {
					return nil, fmt.Errorf("field %s: %w", assignment.Field.Field.Name, err)
				}
				args = append(args, jfv)
			} else {
				args = append(args, assignment.Value.Value)
			}
		}
	}

	// Inject autoUpdateTime
	for _, field := range schema.Fields {
		if field.AutoUpdateTime {
			setClauses = append(setClauses, fmt.Sprintf("%s = ?", field.GetColumnName()))
			args = append(args, time.Now())
		}
	}

	if len(setClauses) == 0 {
		return nil, fmt.Errorf("no SET clauses found in lambda — no scalar assignments")
	}

	// Build WHERE and JOINs
	var sql string
	if body.Condition != nil {
		joins := CollectJoins(body.Condition, make(map[string]bool))
		where, whereArgs := WhereClause(body.Condition)
		args = append(args, whereArgs...)

		if len(joins) > 0 {
			var fromTables []string
			var joinConditions []string
			for _, joinNode := range joins {
				from, joinCond := BuildUpdateFromClause(joinNode, schema.TableName)
				fromTables = append(fromTables, from)
				joinConditions = append(joinConditions, joinCond)
			}
			allWhere := append(joinConditions, where)
			sql = fmt.Sprintf("UPDATE %s SET %s FROM %s WHERE %s",
				schema.TableName,
				strings.Join(setClauses, ", "),
				strings.Join(fromTables, ", "),
				strings.Join(allWhere, " AND "))
		} else {
			sql = fmt.Sprintf("UPDATE %s SET %s WHERE %s",
				schema.TableName,
				strings.Join(setClauses, ", "),
				where)
		}
	} else {
		sql = fmt.Sprintf("UPDATE %s SET %s",
			schema.TableName,
			strings.Join(setClauses, ", "))
	}

	return &Query{SQL: sql, Args: args}, nil
}

// EntityWrite builds an UPDATE query from a change map and schema.
// Inspects changes, maps field names to columns, skips PK/AutoIncrement/relations.
func EntityWrite(entity models.Entity, schema *models.Model, changes map[string]any) (*Query, error) {
	pkColumn, pkValue := entity.PrimaryKey()

	var setClauses []string
	var args []any

	for fieldName, newValue := range changes {
		field, exists := schema.Fields[fieldName]
		if !exists || field.PrimaryKey || field.AutoIncrement {
			continue
		}
		switch field.RelationKind() {
		case models.O2M, models.M2M:
			continue
		case models.M2O:
			setClauses = append(setClauses, fmt.Sprintf("%s = ?", field.GetFKColumn()))
			args = append(args, newValue)
		default:
			setClauses = append(setClauses, fmt.Sprintf("%s = ?", field.GetColumnName()))
			if strings.ToLower(field.Type) == "jsonb" && newValue != nil {
				jfv, err := json.Marshal(newValue)
				if err != nil {
					return nil, fmt.Errorf("field %s: %w", fieldName, err)
				}
				args = append(args, jfv)
			} else {
				args = append(args, newValue)
			}
		}
	}

	if len(setClauses) == 0 {
		return nil, nil // nothing to update
	}

	args = append(args, pkValue)
	sql := fmt.Sprintf("UPDATE %s SET %s WHERE %s = ?",
		schema.TableName,
		strings.Join(setClauses, ", "),
		pkColumn)
	return &Query{SQL: sql, Args: args}, nil
}
