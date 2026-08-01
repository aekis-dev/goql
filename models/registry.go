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
		schema.FieldsByDB[f.ColumnName()] = &f
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
			schema.FieldsByDB[fieldSchema.ColumnName()] = fieldSchema
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
	defer r.mu.Unlock()

	// Overwriting silently hides a duplicate init() or a package imported under two
	// paths; a second registration of the same type is virtually always a mistake.
	if _, exists := r.schemas[modelType]; exists {
		return fmt.Errorf("%w: %s — AddModel must be called once per type", ErrDuplicateModel, modelType.Name())
	}

	// Parsed queries name their model by type name, so the name has to identify exactly one
	// model. Two packages each declaring `Invoice` would otherwise resolve to whichever the
	// registry happened to hit first — a wrong query, silently. Failing here means it
	// surfaces at init(), before anything runs.
	for existingType := range r.schemas {
		if existingType.Name() == modelType.Name() {
			return fmt.Errorf(
				"%w: two models are named %s (%s and %s) — goql identifies a model by its "+
					"type name, so rename one",
				ErrDuplicateModel, modelType.Name(), existingType.PkgPath(), modelType.PkgPath())
		}
	}

	r.schemas[modelType] = schema
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

		// Index accepts either a name — shared between fields to build a composite
		// index — or true, which derives one from the table and column so the common
		// single-column case needs no invented name.
		var indexName string
		switch v := field.Index.(type) {
		case string:
			indexName = v
		case bool:
			if v {
				indexName = fmt.Sprintf("idx_%s_%s", schema.TableName, field.ColumnName())
			}
		}
		if indexName == "" {
			continue
		}

		if idx, exists := indexMap[indexName]; exists {
			idx.Fields = append(idx.Fields, field.ColumnName())
			idx.Composite = len(idx.Fields) > 1
		} else {
			indexMap[indexName] = &Index{
				Name:   indexName,
				Fields: []string{field.ColumnName()},
				Unique: field.Unique,
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
		return nil, fmt.Errorf(
			"%w: %s — import the package that declares it so its init() calls "+
				"models.AddModel before the model is used",
			ErrNotRegistered, modelType.Name())
	}

	return schema, nil
}

// FindFieldByTable resolves a field by table and Go field name. Used by the compiled
// (prod) path, which refers to schema fields by name rather than holding pointers.
func FindFieldByTable(tableName, fieldName string) (*Field, error) {
	schemaRegistry.mu.RLock()
	defer schemaRegistry.mu.RUnlock()

	for _, schema := range schemaRegistry.schemas {
		if schema.TableName != tableName {
			continue
		}
		field, ok := schema.Fields[fieldName]
		if !ok {
			return nil, fmt.Errorf("%w: %s on table %s", ErrFieldNotFound, fieldName, tableName)
		}
		return field, nil
	}
	return nil, fmt.Errorf("no registered model uses table %s", tableName)
}

// SchemaByName resolves a model schema from its Go type name, which is how a parsed query
// names its model: the tree must survive into the generated prod registry as literals, so it
// cannot hold schema pointers. Type names are unique by construction — AddModel refuses a
// second model with the same name.
func SchemaByName(typeName string) (*Model, error) {
	schemaRegistry.mu.RLock()
	defer schemaRegistry.mu.RUnlock()

	for _, schema := range schemaRegistry.schemas {
		if schema.Type.Name() == typeName {
			return schema, nil
		}
	}
	return nil, fmt.Errorf("%w: no model is named %s — is the package declaring it imported?",
		ErrNotRegistered, typeName)
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
	return nil, fmt.Errorf("%w: no model uses type %q — call AddModel() before parsing", ErrNotRegistered, typeName)
}
