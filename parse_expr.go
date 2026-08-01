//go:build !prod

package goql

import (
	"fmt"
	"go/ast"
	"reflect"

	"github.com/aekis-dev/goql/models"
	"github.com/aekis-dev/goql/query"
)

// arithmeticOps are the operators that combine two values into one. Comparison and logical
// operators are conditions and take their own path.
var arithmeticOps = map[string]bool{
	"+": true,
	"-": true,
	"*": true,
	"/": true,
	"%": true,
}

// resolveArithmetic parses an expression like prev.Depth + 1 into a ParseExpr.
//
// Grouping is already decided: the Go parser built the tree, so goql only walks it and
// renders each node parenthesised. Nothing is stored anywhere — the expression is emitted
// inline and evaluated by the engine per row.
func (e *DebugExecutor) resolveArithmetic(expr *ast.BinaryExpr, pctx *parseContext) (*query.ValueRef, error) {
	left, err := e.resolveValueRef(expr.X, pctx)
	if err != nil {
		return nil, fmt.Errorf("left of %s: %w", expr.Op, err)
	}
	right, err := e.resolveValueRef(expr.Y, pctx)
	if err != nil {
		return nil, fmt.Errorf("right of %s: %w", expr.Op, err)
	}

	op := expr.Op.String()
	text := false
	if op == "+" {
		// Go spells concatenation "+", so which one this is has to be decided from the
		// operands. Getting it wrong is silent on MySQL, which coerces strings to numbers
		// rather than failing.
		text = isTextValue(left) || isTextValue(right)
	} else if isTextValue(left) || isTextValue(right) {
		return nil, fmt.Errorf("%w: %s cannot be applied to text", ErrUnsupportedExpr, op)
	}

	return &query.ValueRef{Expr: &query.ParseExpr{
		Op:    op,
		Left:  left,
		Right: right,
		Text:  text,
	}}, nil
}

// isTextValue reports whether a value is textual, so a "+" over it concatenates.
//
// A params-struct reference is not decidable here — the placeholder carries a name, not a
// type — so a concatenation of two params values reads as arithmetic. Writing one side as a
// column or a literal resolves it, which is what any real concatenation does anyway.
func isTextValue(ref *query.ValueRef) bool {
	switch {
	case ref == nil:
		return false

	case ref.Expr != nil:
		return ref.Expr.Text

	case ref.IsColumn:
		// A CTE column's type lives in the defining query's projection, not here.
		if ref.Field == nil || ref.Field.Field == nil {
			return false
		}
		field := ref.Field.Field
		if ref.Field.Nested != nil {
			field = ref.Field.Nested.Field
		}
		if field.GoType != nil && field.GoType.Kind() == reflect.String {
			return true
		}
		switch field.LogicalType() {
		case models.TypeText, models.TypeVarchar:
			return true
		}
		return false

	default:
		_, isString := ref.Value.(string)
		return isString
	}
}
