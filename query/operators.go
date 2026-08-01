package query

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrUnsupportedOperator means an operator string is not in the allowlist below.
var ErrUnsupportedOperator = errors.New("unsupported SQL operator")

// operatorArity describes how many right-hand values an operator takes.
type operatorArity int

const (
	arityOne  operatorArity = iota // col op ?
	arityList                      // col op (?, ?, …)
	arityNone                      // col op
)

// operators is the allowlist for goql.Condition.
//
// Operator strings only ever come from a literal in the caller's own source — lambda
// bodies are parsed, not executed, so no runtime value can reach here. The allowlist is
// therefore about catching typos ("LIK") at parse time rather than about injection.
var operators = map[string]operatorArity{
	"=":           arityOne,
	"!=":          arityOne,
	"<>":          arityOne,
	"<":           arityOne,
	"<=":          arityOne,
	">":           arityOne,
	">=":          arityOne,
	"LIKE":        arityOne,
	"NOT LIKE":    arityOne,
	"IN":          arityList,
	"NOT IN":      arityList,
	"IS NULL":     arityNone,
	"IS NOT NULL": arityNone,
}

// NormalizeOperator canonicalises an operator and checks it against the allowlist,
// verifying that the number of supplied values matches its arity.
func NormalizeOperator(op string, valueCount int) (string, error) {
	normalized := strings.ToUpper(strings.Join(strings.Fields(op), " "))
	// Comparison operators are not words, so leave them exactly as written.
	if _, ok := operators[op]; ok {
		normalized = op
	}

	arity, ok := operators[normalized]
	if !ok {
		return "", fmt.Errorf("%w: %q (supported: %s)", ErrUnsupportedOperator, op, supportedOperators())
	}

	switch arity {
	case arityNone:
		if valueCount != 0 {
			return "", fmt.Errorf("%w: %s takes no values, got %d",
				ErrUnsupportedOperator, normalized, valueCount)
		}
	case arityList:
		if valueCount == 0 {
			return "", fmt.Errorf("%w: %s needs at least one value",
				ErrUnsupportedOperator, normalized)
		}
	default:
		if valueCount != 1 {
			return "", fmt.Errorf("%w: %s takes exactly one value, got %d",
				ErrUnsupportedOperator, normalized, valueCount)
		}
	}

	return normalized, nil
}

// IsListOperator reports whether an operator takes a parenthesised value list.
func IsListOperator(op string) bool {
	return operators[op] == arityList
}

// IsNullaryOperator reports whether an operator takes no right-hand side.
func IsNullaryOperator(op string) bool {
	return operators[op] == arityNone
}

func supportedOperators() string {
	names := make([]string, 0, len(operators))
	for name := range operators {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
