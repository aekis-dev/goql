package models

import (
	"fmt"
	"reflect"
	"strconv"
)

var entityType = reflect.TypeOf((*Entity)(nil)).Elem()

type RelationKind string

const (
	M2O RelationKind = "many2one"
	O2M RelationKind = "one2many"
	M2M RelationKind = "many2many"
)

// Field represents the complete field configuration
type Field struct {
	// Basic info (set by parse, not from tags)
	Name   string       `yaml:"name" json:"name"` // Go struct field name
	GoType reflect.Type `yaml:"-" json:"-"`       // Go type

	// Scalar column definition
	Column    string `yaml:"column" json:"column"`
	Type      string `yaml:"type" json:"type"`
	Size      int    `yaml:"size" json:"size"`
	Precision int    `yaml:"precision" json:"precision"`
	Scale     int    `yaml:"scale" json:"scale"`

	// Constraints
	PrimaryKey    bool        `yaml:"primaryKey" json:"primaryKey"`
	AutoIncrement bool        `yaml:"autoIncrement" json:"autoIncrement"`
	NotNull       bool        `yaml:"notNull" json:"notNull"`
	Unique        bool        `yaml:"unique" json:"unique"`
	Default       interface{} `yaml:"default" json:"default"`
	Checks        []string    `yaml:"checks" json:"checks"`
	Comment       string      `yaml:"comment" json:"comment"`
	Collation     string      `yaml:"collation" json:"collation"`

	// Timestamps
	AutoCreateTime bool `yaml:"autoCreateTime" json:"autoCreateTime"`
	AutoUpdateTime bool `yaml:"autoUpdateTime" json:"autoUpdateTime"`

	// Indexes
	Index   interface{} `yaml:"index" json:"index"` // Can be string or IndexSchema
	Indexes []string    `yaml:"indexes" json:"indexes"`

	// JSON Schema
	Schema interface{} `yaml:"models"         json:"models"` // For JSON/JSONB columns

	// Relation blocks — at most one will be non-nil
	OneToMany  *OneToMany  `yaml:"one2many"   json:"one2many"`
	ManyToMany *ManyToMany `yaml:"many2many"  json:"many2many"`

	TableSchema *Model `yaml:"-" json:"-"` // back-reference to the owning table
}

type OneToMany struct {
	Ref string `yaml:"ref" json:"ref"`
}

type ManyToMany struct {
	Table  string `yaml:"table"  json:"table"`
	Column string `yaml:"column" json:"column"`
	Ref    string `yaml:"ref"    json:"ref"`
}

// RelationKind returns the kind of relation this field represents,
// or empty string if it is a scalar field
func (fs *Field) RelationKind() RelationKind {
	if fs.ManyToMany != nil {
		return M2M
	}
	if fs.OneToMany != nil {
		return O2M
	}
	if fs.GoType != nil && fs.GoType.Kind() == reflect.Ptr {
		if reflect.New(fs.GoType.Elem()).Type().Implements(entityType) {
			return M2O
		}
	}
	return ""
}

// IsRelation returns true if this field represents a relation
func (fs *Field) IsRelation() bool {
	return fs.RelationKind() != ""
}

// TargetModel returns the reflect.Type of the related model,
// unwrapping pointer and slice as needed
func (fs *Field) TargetModel() reflect.Type {
	t := fs.GoType
	if t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t
}

// GetColumnName returns the database column name for scalar fields
func (fs *Field) GetColumnName() string {
	if fs.Column != "" {
		return strconv.Quote(fs.Column)
	}
	return strconv.Quote(toSnakeCase(fs.Name))
}

// GetDBType returns the database type for scalar fields
func (fs *Field) GetDBType() string {
	if fs.Type != "" {
		return fs.Type
	}
	// For many2one, default FK type
	if fs.RelationKind() == M2O {
		return "bigint"
	}
	return InferDBType(fs.GoType)
}

// GetFKColumn returns the FK column name for many2one fields,
// defaulting to snake_case(FieldName) + "_id"
func (fs *Field) GetFKColumn() string {
	if fs.Column != "" {
		return fs.Column
	}
	return toSnakeCase(fs.Name) + "_id"
}

// GetDefault returns the default value as string
func (fs *Field) GetDefault() string {
	if fs.Default == nil {
		return ""
	}
	return fmt.Sprintf("%v", fs.Default)
}
