package query

import (
	"fmt"
	"reflect"

	"github.com/aekis/goql/pkg/models"
)

// Query represents a built SQL query with its bound arguments
type Query struct {
	SQL  string
	Args []any
}

// JoinClause represents a JOIN needed to satisfy a condition
type JoinClause struct {
	SQL string // the full JOIN string e.g. "LEFT JOIN customers ON ..."
}

// FieldRef is a reference to a field in the models.
// Nested is non-nil when accessing through a relation (e.g. o.Customer.Country).
type FieldRef struct {
	Field  *models.Field
	Nested *FieldRef
}

// TableName returns the table name for this field reference
func (fr *FieldRef) TableName() string {
	if fr.Nested != nil {
		return fr.Nested.Field.TableSchema.TableName
	}
	return fr.Field.TableSchema.TableName
}

// ColumnName returns the resolved SQL column name for this field reference
func (fr *FieldRef) ColumnName() string {
	if fr.Nested != nil {
		return fr.Nested.Field.GetColumnName()
	}
	if fr.Field.RelationKind() == models.M2O {
		return fr.Field.GetFKColumn()
	}
	return fr.Field.GetColumnName()
}

// FullColumn returns "table.column" for use in SQL
func (fr *FieldRef) FullColumn() string {
	return fmt.Sprintf("%s.%s", fr.TableName(), fr.ColumnName())
}

// ValueRef represents the right-hand side of a comparison or assignment.
// Either a literal value or a reference to another field.
type ValueRef struct {
	IsColumn bool
	Field    *FieldRef // when IsColumn = true
	Value    any       // when IsColumn = false
}

// ParseNode is a node in the parsed condition tree.
// Leaf nodes represent a single comparison (Left Operator Right).
// Branch nodes combine child conditions with a logical operator (AND/OR).
type ParseNode struct {
	// Leaf — a single comparison: Left Operator Right
	Left     *FieldRef
	Operator string
	Right    *ValueRef

	// Branch — logical combination: Children joined by LogicalOp (AND/OR)
	LogicalOp string
	Children  []*ParseNode

	// Join — a relation that requires a JOIN with scoped conditions
	JoinField *FieldRef  // the relation field (o.Tags, c.Orders)
	JoinScope *ParseNode // conditions scoped to the joined table
}

// IsLeaf returns true if this node is a comparison, false if it's a logical branch
func (n *ParseNode) IsLeaf() bool {
	return n.LogicalOp == "" && n.JoinField == nil
}

func (n *ParseNode) IsBranch() bool {
	return n.LogicalOp != ""
}

func (n *ParseNode) IsJoin() bool {
	return n.JoinField != nil
}

// ParseAssign represents a field assignment in a Write lambda (e.g. c.Status = "Premium")
type ParseAssign struct {
	Field *FieldRef
	Value *ValueRef
}

type ParseRelation struct {
	Field      *FieldRef // the relation field (o.Tags)
	RelatedPKs []any     // PKs extracted from the literal slice value
}

type ParseBody struct {
	Condition           *ParseNode // nil = no WHERE (unconditional)
	Assignments         []*ParseAssign
	RelationAssignments []*ParseRelation
}

// getFieldValue finds a field by name, traversing embedded structs
func getFieldValue(v reflect.Value, t reflect.Type, name string) (reflect.Value, bool) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		fv := v.Field(i)
		if field.Name == name {
			return fv, true
		}
		if field.Anonymous {
			ft := field.Type
			if ft.Kind() == reflect.Ptr {
				if fv.IsNil() {
					continue
				}
				fv = fv.Elem()
				ft = ft.Elem()
			}
			if fv.Kind() == reflect.Struct {
				if found, ok := getFieldValue(fv, ft, name); ok {
					return found, true
				}
			}
		}
	}
	return reflect.Value{}, false
}

func isZeroValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Slice, reflect.Map, reflect.Array:
		return v.Len() == 0
	case reflect.Interface, reflect.Ptr:
		return v.IsNil()
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if f.IsExported() && !isZeroValue(v.Field(i)) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func containsValue(slice []any, value any) bool {
	for _, v := range slice {
		if reflect.DeepEqual(v, value) {
			return true
		}
	}
	return false
}
