package models

import "errors"

// Sentinel errors for schema registration. Returned wrapped with context, so test them
// with errors.Is rather than comparing strings.
var (
	// ErrNotRegistered means a model was used before AddModel registered it — usually
	// because the package declaring it is never imported, so its init() never ran.
	ErrNotRegistered = errors.New("model not registered")

	// ErrDuplicateModel means AddModel was called twice for the same type.
	ErrDuplicateModel = errors.New("model already registered")

	// ErrFieldNotFound means a field name does not exist on a model.
	ErrFieldNotFound = errors.New("field not found")
)
