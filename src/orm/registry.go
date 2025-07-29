package orm

import (
	"fmt"
	"log"
	"reflect"
	"strings"
	"sync"
)

var (
	schemaRegistry = &Registry{
		schemas: make(map[reflect.Type]*TableSchema),
	}
)

// Registry holds all registered model schemas
type Registry struct {
	mu      sync.RWMutex
	schemas map[reflect.Type]*TableSchema
}

// Register registers a model with the ORM
func Register(models ...Entity) error {
	for _, model := range models {
		if err := schemaRegistry.Register(model); err != nil {
			return err
		}
	}
	return nil
}

// Register adds a model to the registry
func (r *Registry) Register(model Entity) error {
	modelType := reflect.TypeOf(model)
	if modelType.Kind() == reflect.Ptr {
		modelType = modelType.Elem()
	}

	// Check if already registered
	r.mu.RLock()
	if _, exists := r.schemas[modelType]; exists {
		r.mu.RUnlock()
		return nil
	}
	r.mu.RUnlock()

	// Parse schema
	schema, err := r.parseSchema(model, modelType)
	if err != nil {
		return fmt.Errorf("failed to parse schema for %s: %w", modelType.Name(), err)
	}

	// Store schema
	r.mu.Lock()
	r.schemas[modelType] = schema
	r.mu.Unlock()

	return nil
}

// parseSchema extracts schema information from a model
func (r *Registry) parseSchema(model Entity, modelType reflect.Type) (*TableSchema, error) {
	schema := &TableSchema{
		Type:       modelType,
		TableName:  model.TableName(),
		Fields:     make(map[string]*FieldSchema),
		FieldsByDB: make(map[string]*FieldSchema),
		Relations:  make(map[string]*RelationSchema),
	}

	// Parse fields (including embedded fields)
	r.parseFields(modelType, schema, "")

	// Build indexes from field information
	r.buildIndexes(schema)

	return schema, nil
}

func extractColtTag(rawTag string) string {
	// Find the colt:" part
	start := strings.Index(rawTag, `colt:"`)
	if start == -1 {
		return ""
	}

	// Move past 'colt:"'
	start += 6

	// Find the closing quote, accounting for escaped quotes
	end := start
	for end < len(rawTag) {
		if rawTag[end] == '"' && (end == 0 || rawTag[end-1] != '\\') {
			break
		}
		end++
	}

	if end >= len(rawTag) {
		return ""
	}

	return rawTag[start:end]
}

// parseFields recursively parses fields, handling embedded structs
func (r *Registry) parseFields(t reflect.Type, schema *TableSchema, prefix string) error {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		// Handle embedded structs
		if field.Anonymous {
			// Recursively parse embedded struct fields
			r.parseFields(field.Type, schema, prefix)
			continue
		}

		// Parse tag
		rawTag := string(field.Tag)

		fieldSchema, err := parseFieldTag(rawTag, field)
		if err != nil {
			log.Printf("Error parsing field %s: %v", field.Name, err)
			return err
		}

		if fieldSchema.Ignore {
			continue
		}

		// Store field schema
		fieldName := prefix + field.Name
		fieldSchema.FieldName = fieldName
		schema.Fields[fieldName] = fieldSchema
		schema.FieldsByDB[fieldSchema.GetColumnName()] = fieldSchema

		// Track primary key
		if fieldSchema.PrimaryKey {
			schema.PrimaryKey = fieldSchema
		}
	}
	return nil
}

// buildIndexes creates IndexSchema from field indexes
func (r *Registry) buildIndexes(schema *TableSchema) {
	indexMap := make(map[string]*IndexSchema)

	for _, field := range schema.Fields {
		// Type assert field.Index to string and check if it's not empty
		if indexName, ok := field.Index.(string); ok && indexName != "" {
			if idx, exists := indexMap[indexName]; exists {
				idx.Fields = append(idx.Fields, field.Column)
				idx.Composite = len(idx.Fields) > 1
			} else {
				indexMap[indexName] = &IndexSchema{
					Name:      indexName,
					Fields:    []string{field.Column},
					Unique:    field.Unique,
					Composite: false, // Single field initially
				}
			}
		}
	}

	// Convert map to slice
	for _, idx := range indexMap {
		schema.Indexes = append(schema.Indexes, idx)
	}
}

// GetSchema retrieves schema for a model type
func GetSchema(model Entity) (*TableSchema, error) {
	return schemaRegistry.GetSchema(model)
}

// GetSchema retrieves cached schema
func (r *Registry) GetSchema(model Entity) (*TableSchema, error) {
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

// MustGetSchema panics if schema not found
func MustGetSchema(model Entity) *TableSchema {
	schema, err := GetSchema(model)
	if err != nil {
		panic(err)
	}
	return schema
}
