package models

import (
	"fmt"
	"reflect"
	"sync"
	"time"
)

var baseModelFields map[string]*Field

func init() {
	baseModelFields = map[string]*Field{
		"ID": {
			Name:          "ID",
			Column:        "id",
			Type:          "integer",
			PrimaryKey:    true,
			AutoIncrement: true,
			NotNull:       true,
			GoType:        reflect.TypeOf(int64(0)),
		},
		"Created": {
			Name:           "Created",
			Column:         "goql_created",
			Type:           "timestamp",
			Precision:      6,
			AutoCreateTime: true,
			NotNull:        true,
			GoType:         reflect.TypeOf(time.Time{}),
			Default:        "CURRENT_TIMESTAMP",
		},
		"Updated": {
			Name:           "Updated",
			Column:         "goql_updated",
			Type:           "timestamp",
			Precision:      6,
			AutoUpdateTime: true,
			NotNull:        true,
			GoType:         reflect.TypeOf(time.Time{}),
			Default:        "CURRENT_TIMESTAMP",
		},
		"Deleted": {
			Name:      "Deleted",
			Column:    "goql_deleted",
			Type:      "timestamp",
			Precision: 6,
			GoType:    reflect.TypeOf((*time.Time)(nil)),
		},
	}
}

var (
	schemaRegistry = &Registry{
		schemas: make(map[reflect.Type]*Model),
	}
)

// Registry holds all registered model schemas
type Registry struct {
	mu      sync.RWMutex
	schemas map[reflect.Type]*Model
}

func AddModel(model Entity, table string, fields ...*Field) error {
	modelType := reflect.TypeOf(model)
	if modelType.Kind() == reflect.Ptr {
		modelType = modelType.Elem()
	}

	schema := &Model{
		Type:       modelType,
		TableName:  table,
		Fields:     make(map[string]*Field),
		FieldsByDB: make(map[string]*Field),
	}

	// Inject base Model fields
	for name, baseField := range baseModelFields {
		f := *baseField
		f.TableSchema = schema
		schema.Fields[name] = &f
		schema.FieldsByDB[f.GetColumnName()] = &f
		if f.PrimaryKey {
			schema.PrimaryKey = &f
		}
	}

	// Store user-provided field schemas
	for _, fieldSchema := range fields {
		// Resolve GoType and infer defaults from struct field
		if structField, ok := modelType.FieldByName(fieldSchema.Name); ok {
			fieldSchema.GoType = structField.Type

			// Infer column name from field name if not set
			if fieldSchema.Column == "" {
				fieldSchema.Column = toSnakeCase(fieldSchema.Name)
			}

			// Infer DB type from Go type if not set
			// Skip for relation fields — they don't have a DB type
			if fieldSchema.Type == "" && !fieldSchema.IsRelation() {
				fieldSchema.Type = InferDBType(structField.Type)
			}
		} else {
			return fmt.Errorf("field %s not found in struct %s", fieldSchema.Name, modelType.Name())
		}

		fieldSchema.TableSchema = schema
		schema.Fields[fieldSchema.Name] = fieldSchema

		switch fieldSchema.RelationKind() {
		case M2O:
			schema.FieldsByDB[fieldSchema.GetFKColumn()] = fieldSchema
		case O2M, M2M:
			// nothing on this table
		default:
			schema.FieldsByDB[fieldSchema.GetColumnName()] = fieldSchema
		}

		if fieldSchema.PrimaryKey {
			schema.PrimaryKey = fieldSchema
		}

		if err := validateRelationField(fieldSchema); err != nil {
			return fmt.Errorf("field %s: %w", fieldSchema.Name, err)
		}
	}

	buildIndexes(schema)

	return schemaRegistry.AddSchema(modelType, schema)
}

func (r *Registry) AddSchema(modelType reflect.Type, schema *Model) error {
	r.mu.Lock()
	r.schemas[modelType] = schema
	r.mu.Unlock()
	return nil
}

// validateRelationField checks that relation fields have the required tag data
func validateRelationField(fs *Field) error {
	switch fs.RelationKind() {
	case O2M:
		if fs.OneToMany.Ref == "" {
			return fmt.Errorf("one2many relation requires ref (FK column on the target table)")
		}
	case M2M:
		m := fs.ManyToMany
		if m.Table == "" {
			return fmt.Errorf("many2many relation requires table")
		}
		if m.Column == "" {
			return fmt.Errorf("many2many relation requires column (this model's FK in the join table)")
		}
		if m.Ref == "" {
			return fmt.Errorf("many2many relation requires ref (target model's FK in the join table)")
		}
	}
	return nil
}

// buildIndexes creates IndexSchema from field indexes
func buildIndexes(schema *Model) {
	indexMap := make(map[string]*Index)

	for _, field := range schema.Fields {
		if field.IsRelation() {
			continue
		}
		// Type assert field.Index to string and check if it's not empty
		if indexName, ok := field.Index.(string); ok && indexName != "" {
			if idx, exists := indexMap[indexName]; exists {
				idx.Fields = append(idx.Fields, field.GetColumnName())
				idx.Composite = len(idx.Fields) > 1
			} else {
				indexMap[indexName] = &Index{
					Name:   indexName,
					Fields: []string{field.GetColumnName()},
					Unique: field.Unique,
				}
			}
		}
	}

	// Convert map to slice
	for _, idx := range indexMap {
		schema.Indexes = append(schema.Indexes, idx)
	}
}

// GetModel retrieves models for a model
func GetModel(model Entity) (*Model, error) {
	return schemaRegistry.GetModel(model)
}

// GetModel retrieves cached models
func (r *Registry) GetModel(model Entity) (*Model, error) {
	modelType := reflect.TypeOf(model)
	if modelType.Kind() == reflect.Ptr {
		modelType = modelType.Elem()
	}

	r.mu.RLock()
	schema, exists := r.schemas[modelType]
	r.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("model %s not registered", modelType.Name())
	}

	return schema, nil
}

// FindModelByTypeName finds a registered model schema by its Go type name.
// Used by the executor to resolve lambda parameter types from source.
func FindModelByTypeName(typeName string) (Entity, error) {
	schemaRegistry.mu.RLock()
	defer schemaRegistry.mu.RUnlock()

	for _, schema := range schemaRegistry.schemas {
		if schema.Type.Name() == typeName {
			entity := reflect.New(schema.Type).Interface().(Entity)
			return entity, nil
		}
	}
	return nil, fmt.Errorf("no registered model found for type %q — call AddModel() before parsing", typeName)
}
