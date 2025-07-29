package orm

import (
	"database/sql"
	"fmt"
	"log"
	"reflect"
	"strings"
	"time"
	"unicode"
)

// ChangeTracker implementation
func NewChangeTracker() *ChangeTracker {
	return &ChangeTracker{
		original: make(map[Entity]Entity),
		dirty:    make(map[Entity][]string),
	}
}

func (ct *ChangeTracker) Track(entity Entity) {
	ct.original[entity] = entity
}

// Helper functions for ColtContext

func toSnakeCase(s string) string {
	var result strings.Builder
	for i, r := range s {
		if i > 0 && unicode.IsUpper(r) {
			result.WriteRune('_')
		}
		result.WriteRune(unicode.ToLower(r))
	}
	return result.String()
}

// isZeroValue checks if a reflect.Value is zero
func isZeroValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Ptr:
		return v.IsNil()
	case reflect.Struct:
		// Special handling for time.Time
		if t, ok := v.Interface().(time.Time); ok {
			return t.IsZero()
		}
		// For other structs, check if all exported fields are zero
		structType := v.Type()
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			structField := structType.Field(i)

			// Skip unexported fields to avoid panic
			if !structField.IsExported() {
				continue
			}

			// Skip fields that can't be accessed
			if !field.CanInterface() {
				continue
			}

			if !isZeroValue(field) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// containsValue checks if slice contains value
func containsValue(slice []any, value any) bool {
	for _, v := range slice {
		if reflect.DeepEqual(v, value) {
			return true
		}
	}
	return false
}

// exec helper for ColtContext
func (ctx *ColtContext) exec(query string, args ...any) (sql.Result, error) {
	if ctx.debugMode {
		log.Printf("SQL: %s\n Args: %v\n", query, args)
	}

	if ctx.tx != nil {
		return ctx.tx.ExecContext(ctx.ctx, query, args...)
	}
	return ctx.db.ExecContext(ctx.ctx, query, args...)
}

// query helper for ColtContext
func (ctx *ColtContext) query(query string, args ...any) (*sql.Rows, error) {
	if ctx.debugMode {
		log.Printf("SQL: %s\n Args: %v\n", query, args)
	}

	if ctx.tx != nil {
		return ctx.tx.QueryContext(ctx.ctx, query, args...)
	}
	return ctx.db.QueryContext(ctx.ctx, query, args...)
}

// scanRows implementation
func scanRows(rows *sql.Rows, entityType reflect.Type) ([]any, error) {
	var results []any

	// Get columns
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	for rows.Next() {
		// Create new entity instance
		entity := reflect.New(entityType).Interface()

		// Create scan destinations
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))

		for i := range columns {
			valuePtrs[i] = &values[i]
		}

		// Scan row
		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, err
		}

		// Map values to entity fields
		if err := mapColumnsToEntity(entity, columns, values); err != nil {
			return nil, err
		}

		// Initialize tracking if supported
		if trackable, ok := entity.(ChangeTrackable); ok {
			InitTracking(trackable)
		}

		results = append(results, entity)
	}

	return results, rows.Err()
}

// mapColumnsToEntity maps database columns to entity fields
func mapColumnsToEntity(entity any, columns []string, values []interface{}) error {
	schema, err := GetSchema(entity.(Entity))
	if err != nil {
		return err
	}

	entityValue := reflect.ValueOf(entity).Elem()

	for i, column := range columns {
		if field, exists := schema.FieldsByDB[column]; exists {
			fieldValue := entityValue.FieldByName(field.FieldName)
			if fieldValue.IsValid() && fieldValue.CanSet() {
				if err := setFieldValue(fieldValue, values[i]); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// setFieldValue sets a reflect.Value from a database value
func setFieldValue(field reflect.Value, value interface{}) error {
	if value == nil {
		return nil
	}

	// Handle common conversions
	switch field.Kind() {
	case reflect.String:
		if s, ok := value.(string); ok {
			field.SetString(s)
		} else {
			field.SetString(fmt.Sprintf("%v", value))
		}
	case reflect.Int, reflect.Int64:
		// Handle database integer types
		switch v := value.(type) {
		case int64:
			field.SetInt(v)
		case int:
			field.SetInt(int64(v))
		}
	case reflect.Uint, reflect.Uint64:
		switch v := value.(type) {
		case uint64:
			field.SetUint(v)
		case uint:
			field.SetUint(uint64(v))
		}
	case reflect.Bool:
		// Handle database boolean types
		switch v := value.(type) {
		case bool:
			field.SetBool(v)
		case int64:
			field.SetBool(v != 0)
		}
	case reflect.Float32, reflect.Float64:
		switch v := value.(type) {
		case float64:
			field.SetFloat(v)
		case float32:
			field.SetFloat(float64(v))
		}
	case reflect.Struct:
		// Handle time.Time
		if field.Type() == reflect.TypeOf(time.Time{}) {
			if t, ok := value.(time.Time); ok {
				field.Set(reflect.ValueOf(t))
			}
		}
	}

	return nil
}

func buildInsert(table string, fields []string) string {
	placeholders := make([]string, len(fields))
	for i := range placeholders {
		placeholders[i] = "?"
	}

	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		table,
		strings.Join(fields, ", "),
		strings.Join(placeholders, ", "))
}
