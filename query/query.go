package query

import (
	"fmt"

	"github.com/aekis-dev/goql/models"
)

// ParseQuery is one parsed goql call — the top-level one, or a nested one written inside a
// lambda body. Both are the same thing, so both are described the same way.
//
// It holds what identifies the call; ParseBody holds what was parsed out of the lambda.
// Everything here is a plain string because the tree has to be written into the generated
// prod registry as literals, which is also why the model is named rather than pointed to.
type ParseQuery struct {
	// Model is the Go type name of the model the lambda's first parameter names. The schema
	// and table are resolved from it. Type names are unique by construction: AddModel
	// refuses a second model with the same name.
	Model string

	// Func is the goql function that was called, empty for one that yields rows (Select).
	// It is the API's own name — Count, Exists, … — not a separate vocabulary.
	Func string

	Body *ParseBody
}

// Schema resolves the model this query runs against.
func (q *ParseQuery) Schema() (*models.Model, error) {
	if q == nil || q.Model == "" {
		return nil, fmt.Errorf("parsed query names no model")
	}
	return models.SchemaByName(q.Model)
}

// funcSpec describes a goql function as data: how many fields it takes through goql.Fields,
// whether those must be numeric, and whether it yields a single value. Adding an aggregate
// is an entry here — no new type, no new parser or builder case.
//
// minFields and maxFields are inclusive; -1 means no upper bound.
type funcSpec struct {
	minFields, maxFields int

	// numeric requires the named column to hold a number, which arithmetic aggregates do.
	// Without the check SQLite quietly answers 0 for SUM over text while Postgres errors —
	// the same code silently wrong on one engine and broken on the other.
	numeric bool

	// scalar marks a function that yields one value, so the options that shape a row set
	// cannot apply to it. They are refused rather than ignored.
	scalar bool
}

var funcSpecs = map[string]funcSpec{
	// Select projects whatever the caller names; nested, it must yield exactly one column,
	// which ValidateQuery enforces by position rather than here.
	"":       {minFields: 0, maxFields: -1},
	"Exists": {minFields: 0, maxFields: 0, scalar: true},
}

// KnownFunc reports whether name is a goql function a parsed query may carry.
func KnownFunc(name string) bool {
	_, ok := funcSpecs[name]
	return ok
}

// CheckFuncArity validates the number of fields a call names against the function it calls,
// the way NormalizeOperator validates an operator's operand count.
func CheckFuncArity(name string, fields int) error {
	spec, ok := funcSpecs[name]
	if !ok {
		return fmt.Errorf("%s is not a goql function that can be parsed", name)
	}
	if fields < spec.minFields {
		return fmt.Errorf("%s needs at least %d field(s), got %d",
			displayFunc(name), spec.minFields, fields)
	}
	if spec.maxFields >= 0 && fields > spec.maxFields {
		return fmt.Errorf("%s takes at most %d field(s), got %d — name them with goql.Fields",
			displayFunc(name), spec.maxFields, fields)
	}
	return nil
}

// ValidateQuery checks a parsed query against what its function allows: field count, field
// type, and which options make sense. It runs while parsing — at runtime in dev, at generate
// time for a prod registry — so a bad query is reported where it was written.
//
// nested says whether this query is written inside another lambda, which tightens one rule:
// a top-level Select projects as many columns as the caller names, but a nested one stands
// on one side of a comparison and must yield exactly one.
func ValidateQuery(q *ParseQuery, nested bool) error {
	spec, ok := funcSpecs[q.Func]
	if !ok {
		return fmt.Errorf("%s is not a goql function that can be parsed", q.Func)
	}

	fields := q.Fields()
	if err := CheckFuncArity(q.Func, len(fields)); err != nil {
		return err
	}
	if nested && q.Func == "" && len(fields) > 1 {
		return fmt.Errorf(
			"a nested Select yields one column, but goql.Fields names %d", len(fields))
	}

	if spec.numeric && len(fields) > 0 {
		schema, err := q.Schema()
		if err != nil {
			return err
		}
		field, found := schema.Fields[fields[0]]
		if !found {
			return fmt.Errorf("%w: %s on %s", models.ErrFieldNotFound, fields[0], schema.TableName)
		}
		if !field.LogicalType().IsNumeric() {
			return fmt.Errorf("%s needs a numeric column, but %s.%s is %s",
				displayFunc(q.Func), schema.TableName, field.ColumnName(), field.LogicalType())
		}
	}

	if spec.scalar {
		if err := rejectRowOptions(q); err != nil {
			return err
		}
	}
	return nil
}

// rejectRowOptions refuses the modifiers that shape a row set on a call that returns one
// value. Ignoring them would answer a question the caller did not ask.
func rejectRowOptions(q *ParseQuery) error {
	opts := q.Body.Options
	if opts == nil {
		return nil
	}
	switch {
	case len(opts.Sorts) > 0:
		return fmt.Errorf("Sort does not apply to %s, which returns a single value", displayFunc(q.Func))
	case opts.Limit != nil:
		return fmt.Errorf("Limit does not apply to %s, which returns a single value", displayFunc(q.Func))
	case opts.Offset != nil:
		return fmt.Errorf("Offset does not apply to %s, which returns a single value", displayFunc(q.Func))
	case opts.PreloadSet:
		return fmt.Errorf("Preload does not apply to %s, which returns no entities", displayFunc(q.Func))
	}
	return nil
}

func displayFunc(name string) string {
	if name == "" {
		return "Select"
	}
	return name
}

// Fields returns the field names this query yields, from the options its lambda declared.
func (q *ParseQuery) Fields() []string {
	if q == nil || q.Body == nil || q.Body.Options == nil {
		return nil
	}
	return q.Body.Options.Fields
}

// ParseSet is a set operation over whole queries: UNION and its relatives. It replaces the
// body's own conditions — the combination is the query.
type ParseSet struct {
	// Op is the SQL keyword: UNION, UNION ALL, INTERSECT, EXCEPT.
	Op string

	Branches []*ParseQuery
}

// setOps maps the goql marker to the SQL it renders as.
var setOps = map[string]string{
	"Union":     "UNION",
	"UnionAll":  "UNION ALL",
	"Intersect": "INTERSECT",
	"Except":    "EXCEPT",
}

// SetOp returns the SQL keyword for a goql set-operation marker, and whether it is one.
func SetOp(name string) (string, bool) {
	op, ok := setOps[name]
	return op, ok
}
