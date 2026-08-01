package query

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/aekis-dev/goql/models"
)

// EntityCreate builds an INSERT query from an entity and its schema.
// Inspects scalar and M2O fields, skips AutoIncrement, O2M, M2M.
//
// Where the engine supports it, the generated primary key is returned by the statement
// itself; elsewhere the caller falls back to LastInsertId.
func (d *Dialect) EntityCreate(entity models.Entity, schema *models.Model) (*Query, error) {
	v := reflect.ValueOf(entity)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	t := v.Type()

	s := d.newStmt()
	var fields []string
	var values []any

	// Iterate in sorted order — schema.Fields is a map, and a randomly ordered column
	// list makes the emitted SQL unstable between runs.
	for _, fieldName := range sortedFieldNames(schema) {
		fieldSchema := schema.Fields[fieldName]
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
					fields = append(fields, d.QuoteIdent(fieldSchema.GetFKColumn()))
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
			if fieldSchema.IsJSON() {
				encoded, err := json.Marshal(fv.Interface())
				if err != nil {
					return nil, err
				}
				fields = append(fields, d.QuoteIdent(fieldSchema.ColumnName()))
				values = append(values, encoded)
				continue
			}
			fields = append(fields, d.QuoteIdent(fieldSchema.ColumnName()))
			values = append(values, fv.Interface())
		}
	}

	if len(fields) == 0 {
		return nil, fmt.Errorf("no fields to insert for %s", schema.TableName)
	}

	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		d.table(schema), strings.Join(fields, ", "), s.marks(len(fields)))

	// pq and pgx do not implement LastInsertId, so the key has to come back with the row.
	if d.SupportsReturning() && schema.PrimaryKey != nil {
		sql += " RETURNING " + d.primaryKey(schema)
	}

	return &Query{SQL: sql, Args: values}, nil
}
