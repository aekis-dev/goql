package orm

import (
	"reflect"
	"sync"
	"time"
	"unsafe"
)

// Model is the base struct for all ORM entities
type Model struct {
	ID int64 `colt:
        column: id
        primaryKey: true
        autoIncrement: true
        type: integer`

	CreatedAt time.Time `colt:
        column: created_at
        type: timestamp
        precision: 6
        autoCreateTime: true
        nullable: false
        default: CURRENT_TIMESTAMP`

	UpdatedAt time.Time `colt:
        column: updated_at
        type: timestamp  
        precision: 6
        autoUpdateTime: true
        nullable: false
        default: CURRENT_TIMESTAMP`

	DeletedAt *time.Time `colt:
        column: deleted_at
        type: timestamp
        precision: 6
        index: idx_deleted_at
        nullable: true`

	// Change tracking fields (not persisted)
	mu       sync.RWMutex   `colt:"-" json:"-"`
	original map[string]any `colt:"-" json:"-"`
	changes  map[string]any `colt:"-" json:"-"`
	isNew    bool           `colt:"-" json:"-"`
}

// TableName must be overridden by embedding structs
func (m *Model) TableName() string {
	panic("TableName() must be implemented by the embedding struct")
}

// PrimaryKey returns the default primary key
func (m *Model) PrimaryKey() (string, any) {
	return "id", m.ID
}

func (m *Model) SetPrimaryKey(pk int64) {
	v := reflect.ValueOf(m).Elem()
	schema, err := GetSchema(m)
	if err != nil || schema == nil {
		// Fallback to setting ID field directly
		m.ID = pk
		return
	}

	if schema.PrimaryKey != nil {
		fieldName := schema.PrimaryKey.FieldName
		field := v.FieldByName(fieldName)
		if field.IsValid() && field.CanSet() {
			field.SetInt(pk)
		}
	} else {
		// No primary key field found in schema, fallback to ID
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

func InitTracking(entity Entity) {
	if model := getModelFromEntity(entity); model != nil {
		model.mu.Lock()
		defer model.mu.Unlock()
		model.original = captureEntityValues(entity)
	}

}

// GetChanges returns changes for the given entity
func GetChanges(entity Entity) map[string]any {
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
func getModelFromEntity(entity Entity) *Model {
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

// getEmbeddingEntity finds the struct that embeds this Model
func getEmbeddingEntity(m *Model) Entity {
	// Use runtime stack inspection to find the calling context
	// or traverse up the memory structure to find the embedding struct

	// For now, we'll use a more direct approach by checking if the Model
	// is part of a larger struct by examining memory layout
	modelPtr := unsafe.Pointer(m)

	// Try to find the embedding entity by checking common patterns
	// This is a simplified approach - in practice you might need more sophisticated logic
	return findEmbeddingEntityByReflection(modelPtr)
}

// captureEntityValues captures all field values from an entity
func captureEntityValues(entity Entity) map[string]any {
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
func detectEntityChanges(entity Entity, original map[string]any) map[string]any {
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

// findEmbeddingEntityByReflection attempts to find the embedding entity
func findEmbeddingEntityByReflection(modelPtr unsafe.Pointer) Entity {
	// This is a simplified implementation
	// In practice, you might need more sophisticated logic to traverse
	// the memory layout and find the embedding struct

	// For now, return nil to indicate we couldn't find the embedding entity
	// The caller should handle this case
	return nil
}
