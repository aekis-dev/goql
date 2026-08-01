package models

import (
	"reflect"
	"time"
)

// ColumnType is a column's logical type. Each dialect maps it to that engine's physical
// type, so one model definition targets every supported engine.
//
// A value outside the set below is emitted verbatim, the escape hatch for an
// engine-specific column type — at the cost of a model that only targets that engine.
type ColumnType string

// The goql type vocabulary. Parameterised types read their arguments from the field:
// Varchar uses Size, Decimal uses Precision and Scale, Timestamp uses Precision.
const (
	TypeInteger   ColumnType = "integer"
	TypeBigInt    ColumnType = "bigint"
	TypeReal      ColumnType = "real"
	TypeDouble    ColumnType = "double"
	TypeDecimal   ColumnType = "decimal"
	TypeText      ColumnType = "text"
	TypeVarchar   ColumnType = "varchar"
	TypeBoolean   ColumnType = "boolean"
	TypeTimestamp ColumnType = "timestamp"
	TypeBytes     ColumnType = "bytes"

	// TypeJSON marks a column whose Go value is marshalled to JSON on write and
	// unmarshalled on read. Its underlying value stays "jsonb" so models written before
	// the type vocabulary existed keep working.
	TypeJSON ColumnType = "jsonb"
)

// IsJSON reports whether a field round-trips through JSON. This is the single definition
// of that rule, which was previously a lowercase string comparison repeated at four call
// sites.
func (fs *Field) IsJSON() bool {
	return fs.Type == TypeJSON
}

// InferDBType picks a logical type for a Go type.
func InferDBType(goType reflect.Type) ColumnType {
	if goType == nil {
		return TypeText
	}
	// Nullable scalars are declared as pointers (*int, *bool, …) so callers can
	// distinguish "unset" from an explicit zero value. Infer from the element type.
	if goType.Kind() == reflect.Ptr {
		goType = goType.Elem()
	}

	switch goType.Kind() {
	case reflect.Bool:
		return TypeBoolean
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return TypeInteger
	case reflect.Int64, reflect.Uint64:
		return TypeBigInt
	case reflect.Float32:
		return TypeReal
	case reflect.Float64:
		return TypeDouble
	case reflect.String:
		return TypeText
	case reflect.Struct:
		if goType == reflect.TypeOf(time.Time{}) {
			return TypeTimestamp
		}
		return TypeJSON
	case reflect.Slice:
		if goType.Elem().Kind() == reflect.Uint8 {
			return TypeBytes
		}
		return TypeJSON
	case reflect.Map:
		return TypeJSON
	default:
		return TypeText
	}
}

// IsNumeric reports whether a column type holds a number, which is what an arithmetic
// aggregate needs. A type outside the core vocabulary is passed through verbatim by the
// dialects, so nothing can be assumed about it and it is not treated as numeric.
func (t ColumnType) IsNumeric() bool {
	switch t {
	case TypeInteger, TypeBigInt, TypeReal, TypeDouble, TypeDecimal:
		return true
	default:
		return false
	}
}
