package models

import (
	"reflect"
	"strings"
	"time"
	"unicode"
)

// Model holds complete models information for a model
type Model struct {
	Type       reflect.Type
	TableName  string
	Fields     map[string]*Field
	FieldsByDB map[string]*Field
	PrimaryKey *Field
	Indexes    []*Index
}

// inferDBType returns the default database type for a Go type
func InferDBType(goType reflect.Type) string {
	if goType == nil {
		return "text"
	}
	switch goType.Kind() {
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int32:
		return "integer"
	case reflect.Int64:
		return "bigint"
	case reflect.Uint, reflect.Uint32:
		return "integer"
	case reflect.Uint64:
		return "bigint"
	case reflect.Float32:
		return "real"
	case reflect.Float64:
		return "double precision"
	case reflect.String:
		return "text"
	case reflect.Struct:
		if goType == reflect.TypeOf(time.Time{}) {
			return "timestamp"
		}
		return "jsonb"
	case reflect.Slice:
		if goType.Elem().Kind() == reflect.Uint8 {
			return "bytea"
		}
		return "jsonb"
	default:
		return "text"
	}
}

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
