package query

import (
	"errors"
	"fmt"
	"reflect"
)

var (
	// ErrMissingParams means a lambda references a params field but no params value was
	// supplied at the call site.
	ErrMissingParams = errors.New("params value not supplied")

	// ErrInvalidParams means the supplied params value cannot provide a referenced field.
	ErrInvalidParams = errors.New("invalid params value")
)

// ParamRef is a placeholder for a field of the params struct supplied at the call site.
//
// Parsed bodies are cached and shared across calls (and compiled into the prod registry),
// so a call-time value can never be baked into the tree. The reference instead travels in
// a query's Args like any other bound value, and is substituted just before execution.
type ParamRef struct {
	Field string
}

// ResolveParams substitutes ParamRef placeholders in args with the corresponding fields
// of params. args is returned untouched when it holds no placeholders, which is the
// common case.
func ResolveParams(args []any, params any) ([]any, error) {
	hasRef := false
	for _, arg := range args {
		if _, ok := arg.(ParamRef); ok {
			hasRef = true
			break
		}
	}
	if !hasRef {
		return args, nil
	}

	if params == nil {
		return nil, fmt.Errorf("%w: the lambda reads fields from a params struct", ErrMissingParams)
	}

	v := reflect.ValueOf(params)
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil, fmt.Errorf("%w: params pointer is nil", ErrMissingParams)
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil, fmt.Errorf("%w: want a struct, got %T", ErrInvalidParams, params)
	}

	out := make([]any, len(args))
	copy(out, args)

	for i, arg := range out {
		ref, ok := arg.(ParamRef)
		if !ok {
			continue
		}
		field := v.FieldByName(ref.Field)
		if !field.IsValid() {
			return nil, fmt.Errorf("%w: %s has no field %s", ErrInvalidParams, v.Type(), ref.Field)
		}
		if !field.CanInterface() {
			return nil, fmt.Errorf("%w: %s.%s is not exported", ErrInvalidParams, v.Type(), ref.Field)
		}
		out[i] = field.Interface()
	}

	return out, nil
}
