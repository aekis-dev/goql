package goql

import (
	"errors"

	"github.com/aekis-dev/goql/models"
	"github.com/aekis-dev/goql/query"
)

// Sentinel errors returned (wrapped with context) by goql. Test them with errors.Is:
//
//	if errors.Is(err, goql.ErrCapturedVariable) { … }
var (
	// ErrUnsupportedExpr means a lambda contains a construct the parser cannot
	// translate into SQL.
	ErrUnsupportedExpr = errors.New("unsupported expression in lambda")

	// ErrInvalidLambda means a lambda has the wrong shape for the operation — a value
	// receiver where a pointer is required, a missing bool return, and so on.
	ErrInvalidLambda = errors.New("invalid lambda signature")

	// ErrCapturedVariable means a lambda references a variable from the enclosing
	// scope. Bodies are parsed rather than executed, so such a variable has no value
	// available; pass runtime values through a params struct parameter instead.
	ErrCapturedVariable = errors.New("captured variable has no value at parse time")

	// ErrNoAssignments means a write lambda assigns nothing, so there is nothing to do.
	ErrNoAssignments = errors.New("write lambda assigns nothing")

	// ErrNoCompiledBody means a `-tags prod` binary has no registry entry for a lambda,
	// which means the registry is missing or stale.
	ErrNoCompiledBody = errors.New("no compiled body for lambda")

	// ErrInvalidOption means a value passed as a query option is not one of the option
	// types, or an option was used in a way the parser cannot interpret.
	ErrInvalidOption = errors.New("invalid query option")

	// ErrRelationConstraint means a relation change cannot be applied because the schema
	// forbids it — a one2many row cannot be disassociated through a NOT NULL foreign key,
	// for instance.
	ErrRelationConstraint = errors.New("relation change not permitted by the schema")

	// ErrNotEntity means a type cannot be used as a model because it does not implement
	// models.Entity — usually a struct that forgot to embed goql.Model.
	ErrNotEntity = errors.New("type is not a goql entity")

	// ErrNotRegistered is re-exported from models, and the params errors from query, so
	// callers need only one import.
	ErrNotRegistered = models.ErrNotRegistered
	ErrMissingParams = query.ErrMissingParams
	ErrInvalidParams = query.ErrInvalidParams
)
