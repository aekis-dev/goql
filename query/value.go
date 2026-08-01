package query

import (
	"fmt"
)

// valueSQL renders any right-hand value — a column, a bound literal, or an arithmetic
// expression over them — and returns the values it binds, in emission order.
//
// Every site that used to branch on IsColumn goes through here, so an expression is
// available in each of them: a predicate, an UPDATE's SET, an INSERT … SELECT's value list
// and a projection.
func (d *Dialect) valueSQL(ref *ValueRef, s *stmt) (string, []any, error) {
	if ref == nil {
		return "", nil, fmt.Errorf("query: nil value reference")
	}

	switch {
	case ref.Expr != nil:
		return d.exprSQL(ref.Expr, s)
	case ref.IsColumn:
		return s.column(ref.Field), nil, nil
	default:
		return s.mark(), []any{ref.Value}, nil
	}
}

// exprSQL renders one arithmetic expression. Operands are rendered left to right so their
// placeholders and bound values stay in step.
func (d *Dialect) exprSQL(expr *ParseExpr, s *stmt) (string, []any, error) {
	left, leftArgs, err := d.valueSQL(expr.Left, s)
	if err != nil {
		return "", nil, err
	}
	right, rightArgs, err := d.valueSQL(expr.Right, s)
	if err != nil {
		return "", nil, err
	}
	args := append(leftArgs, rightArgs...)

	if expr.Text {
		return d.Concat(left, right), args, nil
	}

	// Parenthesised because the Go parser already decided the grouping; reproducing it
	// keeps a * (b + c) from being re-associated by the engine's own precedence.
	return fmt.Sprintf("(%s %s %s)", left, expr.Op, right), args, nil
}
