package query

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/aekis-dev/goql/pkg/models"
)

// EntityCreate builds an INSERT query from an entity and its schema.
// Inspects scalar and M2O fields, skips AutoIncrement, O2M, M2M.
func EntityCreate(entity models.Entity, schema *models.Model) (*Query, error) {
	v := reflect.ValueOf(entity)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	t := v.Type()

	var fields []string
	var values []any

	for _, fieldSchema := range schema.Fields {
		if fieldSchema.AutoIncrement {
			continue
		}
		switch fieldSchema.RelationKind() {
		case models.O2M, models.M2M:
			continue

		case models.M2O:
			fv, found := getFieldValue(v, t, fieldSchema.Name)
			if !found {
				continue
			}
			if fv.Kind() == reflect.Ptr && !fv.IsNil() {
				if related, ok := fv.Interface().(models.Entity); ok {
					_, pkValue := related.PrimaryKey()
					fields = append(fields, fieldSchema.GetFKColumn())
					values = append(values, pkValue)
				}
			}

		default:
			fv, found := getFieldValue(v, t, fieldSchema.Name)
			if !found {
				continue
			}
			if isZeroValue(fv) && !fieldSchema.NotNull {
				continue
			}
			if strings.ToLower(fieldSchema.Type) == "jsonb" {
				jfv, err := json.Marshal(fv.Interface())
				if err != nil {
					return nil, err
				}
				fields = append(fields, fieldSchema.GetColumnName())
				values = append(values, jfv)
				continue
			}
			fields = append(fields, fieldSchema.GetColumnName())
			values = append(values, fv.Interface())
		}
	}

	if len(fields) == 0 {
		return nil, fmt.Errorf("no fields to insert for %s", schema.TableName)
	}

	placeholders := make([]string, len(fields))
	for i := range placeholders {
		placeholders[i] = "?"
	}

	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		schema.TableName,
		strings.Join(fields, ", "),
		strings.Join(placeholders, ", "))

	return &Query{SQL: sql, Args: values}, nil
}
