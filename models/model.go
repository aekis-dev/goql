package models

import (
	"reflect"
	"sort"
	"strings"
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

// PreloadFields lists the relation fields marked Preload, in a stable order, which are
// loaded by default on every read of this model.
func (m *Model) PreloadFields() []string {
	var names []string
	for name, field := range m.Fields {
		if field.Preload && field.IsRelation() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
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
