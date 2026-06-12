package orm

import (
	"database/sql"
	"fmt"
	"log"
	"reflect"
	"strings"
	"time"
	"unicode"

	"github.com/aekis-dev/goql/pkg/models"
)

// ChangeTracker implementation
func NewChangeTracker() *ChangeTracker {
	return &ChangeTracker{
		original: make(map[models.Entity]models.Entity),
		dirty:    make(map[models.Entity][]string),
	}
}

func (ct *ChangeTracker) Track(entity models.Entity) {
	ct.original[entity] = entity
}

// Helper functions for GoqlContext

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

// exec helper for GoqlContext
func (ctx *GoqlContext) exec(query string, args ...any) (sql.Result, error) {
	if ctx.debugMode {
		log.Printf("SQL: %s\n Args: %v\n", query, args)
	}

	if ctx.tx != nil {
		return ctx.tx.ExecContext(ctx.ctx, query, args...)
	}
	return ctx.db.ExecContext(ctx.ctx, query, args...)
}

// query helper for GoqlContext
func (ctx *GoqlContext) query(query string, args ...any) (*sql.Rows, error) {
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
		if trackable, ok := entity.(models.ChangeTrackable); ok {
			InitTracking(trackable)
		}

		results = append(results, entity)
	}

	return results, rows.Err()
}

// mapColumnsToEntity maps database columns to entity fields
func mapColumnsToEntity(entity any, columns []string, values []interface{}) error {
	schema, err := models.GetModel(entity.(models.Entity))
	if err != nil {
		return err
	}

	entityValue := reflect.ValueOf(entity).Elem()

	for i, column := range columns {
		if field, exists := schema.FieldsByDB[column]; exists {
			fieldValue := entityValue.FieldByName(field.Name)
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

	switch field.Kind() {

	case reflect.String:
		switch v := value.(type) {
		case string:
			field.SetString(v)
		case []byte:
			field.SetString(string(v))
		default:
			field.SetString(fmt.Sprintf("%v", v))
		}

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		switch v := value.(type) {
		case int64:
			field.SetInt(v)
		case int32:
			field.SetInt(int64(v))
		case int16:
			field.SetInt(int64(v))
		case int8:
			field.SetInt(int64(v))
		case int:
			field.SetInt(int64(v))
		case uint64:
			field.SetInt(int64(v))
		case uint32:
			field.SetInt(int64(v))
		case uint:
			field.SetInt(int64(v))
		case float64:
			field.SetInt(int64(v))
		case float32:
			field.SetInt(int64(v))
		case []byte:
			field.SetString(string(v)) // shouldn't happen but safe
		}

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		switch v := value.(type) {
		case uint64:
			field.SetUint(v)
		case uint32:
			field.SetUint(uint64(v))
		case uint16:
			field.SetUint(uint64(v))
		case uint8:
			field.SetUint(uint64(v))
		case uint:
			field.SetUint(uint64(v))
		case int64:
			field.SetUint(uint64(v))
		case int32:
			field.SetUint(uint64(v))
		case int:
			field.SetUint(uint64(v))
		case float64:
			field.SetUint(uint64(v))
		}

	case reflect.Float32, reflect.Float64:
		switch v := value.(type) {
		case float64:
			field.SetFloat(v)
		case float32:
			field.SetFloat(float64(v))
		case int64:
			field.SetFloat(float64(v))
		case int32:
			field.SetFloat(float64(v))
		case int16:
			field.SetFloat(float64(v))
		case int8:
			field.SetFloat(float64(v))
		case int:
			field.SetFloat(float64(v))
		case uint64:
			field.SetFloat(float64(v))
		case uint32:
			field.SetFloat(float64(v))
		case uint:
			field.SetFloat(float64(v))
		case []byte:
			// some drivers return DECIMAL as []byte
			var f float64
			if _, err := fmt.Sscanf(string(v), "%f", &f); err == nil {
				field.SetFloat(f)
			}
		}

	case reflect.Bool:
		switch v := value.(type) {
		case bool:
			field.SetBool(v)
		case int64:
			field.SetBool(v != 0)
		case int32:
			field.SetBool(v != 0)
		case int:
			field.SetBool(v != 0)
		case uint64:
			field.SetBool(v != 0)
		case []byte:
			field.SetBool(len(v) > 0 && v[0] != 0)
		}

	case reflect.Struct:
		if field.Type() == reflect.TypeOf(time.Time{}) {
			switch v := value.(type) {
			case time.Time:
				field.Set(reflect.ValueOf(v))
			case string:
				// SQLite returns timestamps as strings
				for _, layout := range []string{
					time.RFC3339Nano,
					time.RFC3339,
					"2006-01-02 15:04:05.999999999-07:00",
					"2006-01-02 15:04:05.999999999",
					"2006-01-02 15:04:05",
					"2006-01-02",
				} {
					if t, err := time.Parse(layout, v); err == nil {
						field.Set(reflect.ValueOf(t))
						break
					}
				}
			case []byte:
				// same as string
				for _, layout := range []string{
					time.RFC3339Nano,
					time.RFC3339,
					"2006-01-02 15:04:05.999999999-07:00",
					"2006-01-02 15:04:05.999999999",
					"2006-01-02 15:04:05",
					"2006-01-02",
				} {
					if t, err := time.Parse(layout, string(v)); err == nil {
						field.Set(reflect.ValueOf(t))
						break
					}
				}
			}
		}

	case reflect.Ptr:
		// Nullable fields — e.g. *time.Time, *string
		if value == nil {
			return nil
		}
		// Allocate the pointed-to type and recurse
		ptr := reflect.New(field.Type().Elem())
		if err := setFieldValue(ptr.Elem(), value); err != nil {
			return err
		}
		field.Set(ptr)

	case reflect.Slice:
		// []byte — raw binary
		if field.Type().Elem().Kind() == reflect.Uint8 {
			switch v := value.(type) {
			case []byte:
				field.SetBytes(v)
			case string:
				field.SetBytes([]byte(v))
			}
		}
	}

	return nil
}

// applyAutoTimestamps sets autoCreateTime and autoUpdateTime fields on the entity.
// isCreate=true sets both, isCreate=false sets only autoUpdateTime.
func applyAutoTimestamps(entity models.Entity, schema *models.Model, isCreate bool) {
	now := time.Now()

	v := reflect.ValueOf(entity)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	for _, fieldSchema := range schema.Fields {
		if fieldSchema.IsRelation() {
			continue
		}
		if !fieldSchema.AutoCreateTime && !fieldSchema.AutoUpdateTime {
			continue
		}
		if fieldSchema.AutoCreateTime && !isCreate {
			continue
		}

		fieldValue, found := getFieldValue(v, fieldSchema.Name)
		if !found || !fieldValue.CanSet() {
			continue
		}

		if fieldValue.Type() == reflect.TypeOf(time.Time{}) {
			fieldValue.Set(reflect.ValueOf(now))
		}
	}
}
