package orm

import (
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v3"
	"reflect"
	"strings"
	"sync"
	"time"
)

// TableSchema holds complete schema information for a model
type TableSchema struct {
	Type       reflect.Type
	TableName  string
	Fields     map[string]*FieldSchema
	FieldsByDB map[string]*FieldSchema // DB column name -> FieldSchema
	PrimaryKey *FieldSchema
	Indexes    []*IndexSchema
	Relations  map[string]*RelationSchema
}

// FieldSchema represents the complete field configuration
type FieldSchema struct {
	// Basic info (set by parser, not from tags)
	FieldName string       `yaml:"-" json:"-"` // Go struct field name
	GoType    reflect.Type `yaml:"-" json:"-"` // Go type

	// Column definition
	Column    string `yaml:"column" json:"column"`
	Type      string `yaml:"type" json:"type"`
	Size      int    `yaml:"size" json:"size"`
	Precision int    `yaml:"precision" json:"precision"`
	Scale     int    `yaml:"scale" json:"scale"`

	// Constraints
	PrimaryKey    bool        `yaml:"primaryKey" json:"primaryKey"`
	AutoIncrement bool        `yaml:"autoIncrement" json:"autoIncrement"`
	Nullable      *bool       `yaml:"nullable" json:"nullable"` // Pointer to distinguish unset from false
	Unique        bool        `yaml:"unique" json:"unique"`
	Default       interface{} `yaml:"default" json:"default"`
	Check         string      `yaml:"check" json:"check"`
	Comment       string      `yaml:"comment" json:"comment"`

	// Indexes
	Index   interface{} `yaml:"index" json:"index"` // Can be string or IndexSchema
	Indexes []string    `yaml:"indexes" json:"indexes"`

	// Timestamps
	AutoCreateTime bool `yaml:"autoCreateTime" json:"autoCreateTime"`
	AutoUpdateTime bool `yaml:"autoUpdateTime" json:"autoUpdateTime"`

	// Relations
	Relation *RelationSchema `yaml:"relation" json:"relation"`

	// JSON Schema
	Schema interface{} `yaml:"schema" json:"schema"` // For JSON/JSONB columns

	// Tracking
	Ignore bool `yaml:"-" json:"-"` // Skip this field (tag: "-")
}

// GetColumnName returns the database column name
func (fc *FieldSchema) GetColumnName() string {
	if fc.Column != "" {
		return fc.Column
	}
	return toSnakeCase(fc.FieldName)
}

// GetDBType returns the database type
func (fc *FieldSchema) GetDBType() string {
	if fc.Type != "" {
		return fc.Type
	}
	// Infer from Go type if not specified
	return inferDBType(fc.GoType)
}

// IsNullable returns whether the field is nullable
func (fc *FieldSchema) IsNullable() bool {
	if fc.Nullable != nil {
		return *fc.Nullable
	}
	// Default behavior
	if fc.PrimaryKey {
		return false
	}
	return true
}

// GetDefault returns the default value as string
func (fc *FieldSchema) GetDefault() string {
	if fc.Default == nil {
		return ""
	}
	return fmt.Sprintf("%v", fc.Default)
}

// IndexSchema for complex index definitions
type IndexSchema struct {
	Name      string   `yaml:"name"`
	Type      string   `yaml:"type"` // btree, hash, gin, etc.
	Unique    bool     `yaml:"unique"`
	Where     string   `yaml:"where"`     // Partial index
	Fields    []string `yaml:"fields"`    // For composite indexes
	Composite bool     `yaml:"composite"` // Add this field

}

// RelationSchema for relationship definitions
type RelationSchema struct {
	Type           string `yaml:"type"`  // hasMany, hasOne, belongsTo, manyToMany
	Model          string `yaml:"model"` // Target model name
	ForeignKey     string `yaml:"foreignKey"`
	References     string `yaml:"references"`
	JoinTable      string `yaml:"joinTable"` // For manyToMany
	JoinForeignKey string `yaml:"joinForeignKey"`
	JoinReferences string `yaml:"joinReferences"`
	OnDelete       string `yaml:"onDelete"` // CASCADE, SET NULL, RESTRICT
	OnUpdate       string `yaml:"onUpdate"`
	EagerLoad      bool   `yaml:"eagerLoad"`
	Polymorphic    string `yaml:"polymorphic"` // Field name for polymorphic relations
}

type RelationType string

const (
	HasOne     RelationType = "has_one"
	HasMany    RelationType = "has_many"
	BelongsTo  RelationType = "belongs_to"
	ManyToMany RelationType = "many_to_many"
)

var (
	tagCache = make(map[string]*FieldSchema)
	tagMutex sync.RWMutex
)

// parseFieldTag parses YAML/JSON formatted colt struct tags
func parseFieldTag(tag string, field reflect.StructField) (*FieldSchema, error) {
	tagMutex.RLock()
	if config, exists := tagCache[tag]; exists {
		tagMutex.RUnlock()
		return config, nil
	}
	tagMutex.RUnlock()

	// Initialize with field metadata
	fc := &FieldSchema{
		FieldName: field.Name,
		GoType:    field.Type,
	}
	var req struct {
		Colt FieldSchema `yaml:"colt"`
	}

	// Handle special cases
	tag = strings.TrimSpace(tag)
	if tag == "-" {
		fc.Ignore = true
		return fc, nil
	}

	if tag == "" {
		// Use defaults
		return fc, nil
	}

	// Clean up the tag by replacing tabs with 4 spaces
	tag = strings.ReplaceAll(tag, "\t", "    ")

	// Detect format and parse
	var err error

	if strings.HasPrefix(tag, "{") {
		// JSON format
		err = json.Unmarshal([]byte(tag), &req)
	} else if strings.Contains(tag, ":") && (strings.Contains(tag, "\n") || strings.Contains(tag, "  ")) {
		// YAML format (has colons and newlines or multiple spaces)
		err = yaml.Unmarshal([]byte(tag), &req)
	} else {
		return nil, fmt.Errorf("invalid tag format: %s", tag)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to parse tag for field %s: %w", field.Name, err)
	}

	// Restore field metadata (in case unmarshaling overwrote it)
	req.Colt.FieldName = field.Name
	req.Colt.GoType = field.Type

	return &req.Colt, nil
}

// inferDBType returns the default database type for a Go type
func inferDBType(goType reflect.Type) string {
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
