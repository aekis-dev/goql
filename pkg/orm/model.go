package orm

import (
	"reflect"
	"sync"
	"time"

	"github.com/aekis-dev/goql/pkg/models"
)

// Model is the base struct for all ORM entities
type Model struct {
	ID        int64
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
	// Change tracking fields (not persisted)
	mu       sync.RWMutex   `goql:"-" json:"-"`
	original map[string]any `goql:"-" json:"-"`
	changes  map[string]any `goql:"-" json:"-"`
	isNew    bool           `goql:"-" json:"-"`
}

// PrimaryKey returns the default primary key
func (m *Model) PrimaryKey() (string, any) {
	return "id", m.ID
}

func (m *Model) SetPrimaryKey(pk int64) {
	v := reflect.ValueOf(m).Elem()
	schema, err := models.GetModel(m)
	if err != nil || schema == nil {
		// Fallback to setting ID field directly
		m.ID = pk
		return
	}

	if schema.PrimaryKey != nil {
		fieldName := schema.PrimaryKey.Name
		field := v.FieldByName(fieldName)
		if field.IsValid() && field.CanSet() {
			field.SetInt(pk)
		}
	} else {
		// No primary key field found in models, fallback to ID
		m.ID = pk
	}
}

// ClearChanges resets change tracking
func (m *Model) ClearChanges() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.changes = make(map[string]any)
	m.isNew = false
}

// MarkNew marks entity as new (not yet persisted)
func (m *Model) MarkNew() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.isNew = true
}

// IsNew returns whether this entity has been persisted
func (m *Model) IsNew() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.isNew
}

func InitTracking(entity models.Entity) {
	if model := getModelFromEntity(entity); model != nil {
		model.mu.Lock()
		defer model.mu.Unlock()
		model.original = captureEntityValues(entity)
	}

}

// GetChanges returns changes for the given entity
func GetChanges(entity models.Entity) map[string]any {
	if model := getModelFromEntity(entity); model != nil {
		model.mu.RLock()
		defer model.mu.RUnlock()

		if model.original == nil {
			return make(map[string]any)
		}

		return detectEntityChanges(entity, model.original)
	}

	return make(map[string]any)
}

// getModelFromEntity extracts the embedded Model from an entity
func getModelFromEntity(entity models.Entity) *Model {
	entityValue := reflect.ValueOf(entity)
	if entityValue.Kind() == reflect.Ptr {
		entityValue = entityValue.Elem()
	}

	// Look for the Model field
	for i := 0; i < entityValue.NumField(); i++ {
		field := entityValue.Field(i)
		if field.Type() == reflect.TypeOf(Model{}) {
			if field.CanAddr() {
				return field.Addr().Interface().(*Model)
			}
		}
	}
	return nil
}

// captureEntityValues captures all field values from an entity
func captureEntityValues(entity models.Entity) map[string]any {
	values := make(map[string]any)

	entityValue := reflect.ValueOf(entity)
	if entityValue.Kind() == reflect.Ptr {
		entityValue = entityValue.Elem()
	}

	entityType := entityValue.Type()

	// Capture all field values from the complete entity
	for i := 0; i < entityValue.NumField(); i++ {
		field := entityType.Field(i)
		fieldValue := entityValue.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		// Handle embedded Model struct - extract its fields individually
		if field.Type == reflect.TypeOf(Model{}) {
			modelValue := fieldValue
			modelType := modelValue.Type()

			// Capture each field from the Model struct
			for j := 0; j < modelValue.NumField(); j++ {
				modelField := modelType.Field(j)
				modelFieldValue := modelValue.Field(j)

				// Skip unexported fields and mutex
				if !modelField.IsExported() || modelField.Name == "mu" {
					continue
				}

				// Store Model fields with their field names
				values[modelField.Name] = modelFieldValue.Interface()
			}
		} else {
			// Regular field - store directly
			values[field.Name] = fieldValue.Interface()
		}
	}

	return values
}

// detectEntityChanges compares current entity values with original values
func detectEntityChanges(entity models.Entity, original map[string]any) map[string]any {
	changes := make(map[string]any)

	entityValue := reflect.ValueOf(entity)
	if entityValue.Kind() == reflect.Ptr {
		entityValue = entityValue.Elem()
	}

	entityType := entityValue.Type()

	// Compare current values with original values
	for i := 0; i < entityValue.NumField(); i++ {
		field := entityType.Field(i)
		fieldValue := entityValue.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		// Handle embedded Model struct - compare its fields individually
		if field.Type == reflect.TypeOf(Model{}) {
			modelValue := fieldValue
			modelType := modelValue.Type()

			// Compare each field of the embedded Model
			for j := 0; j < modelValue.NumField(); j++ {
				modelField := modelType.Field(j)
				modelFieldValue := modelValue.Field(j)

				if !modelField.IsExported() {
					continue
				}

				// Get original value for this Model field
				if originalValue, exists := original[modelField.Name]; exists {
					currentValue := modelFieldValue.Interface()

					// Compare values
					if !reflect.DeepEqual(currentValue, originalValue) {
						changes[modelField.Name] = currentValue
					}
				}
			}
		} else {
			// Handle regular entity fields
			if originalValue, exists := original[field.Name]; exists {
				currentValue := fieldValue.Interface()

				// Compare values
				if !reflect.DeepEqual(currentValue, originalValue) {
					changes[field.Name] = currentValue
				}
			}
		}
	}

	return changes
}

func getEntityFields(entityValue reflect.Value, entityType reflect.Type) []reflect.StructField {
	var fields []reflect.StructField

	for i := 0; i < entityType.NumField(); i++ {
		field := entityType.Field(i)

		// Check if field is embedded (anonymous)
		if field.Anonymous {
			// Get the embedded struct type
			embeddedType := field.Type
			if embeddedType.Kind() == reflect.Ptr {
				embeddedType = embeddedType.Elem()
			}

			// Recursively get fields from embedded struct
			embeddedFields := getEntityFields(reflect.Value{}, embeddedType)
			fields = append(fields, embeddedFields...)
		} else {
			fields = append(fields, field)
		}
	}

	return fields
}

func getFieldValue(entityValue reflect.Value, fieldName string) (reflect.Value, bool) {
	entityType := entityValue.Type()

	for i := 0; i < entityType.NumField(); i++ {
		field := entityType.Field(i)
		fieldValue := entityValue.Field(i)

		if field.Name == fieldName {
			return fieldValue, true
		}

		// Check embedded structs
		if field.Anonymous {
			if field.Type.Kind() == reflect.Ptr && !fieldValue.IsNil() {
				fieldValue = fieldValue.Elem()
			}

			if fieldValue.IsValid() && fieldValue.Kind() == reflect.Struct {
				if embeddedValue, found := getFieldValue(fieldValue, fieldName); found {
					return embeddedValue, true
				}
			}
		}
	}

	return reflect.Value{}, false
}
