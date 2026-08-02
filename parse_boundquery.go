//go:build !prod

package goql

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"

	"github.com/aekis-dev/goql/models"
	"github.com/aekis-dev/goql/query"
)

// bindQueryRef resolves `x.Query = someBinding` for either carrier.
//
// From.Query and Join.Query name the same thing — a query bound earlier in this lambda,
// read as a common table expression — so they resolve through one function. They were
// written separately in §20 and §21 and drifted: the join path never registered the
// definition, so a joined CTE rendered as a reference to a table that was never defined.
//
// Returns the definition to register. The caller decides what to do with it: From makes it
// the query's source, Join makes it a joined table.
func (e *DebugExecutor) bindQueryRef(rhs ast.Expr, carrier string, pctx *parseContext) (*query.ParseCTE, error) {
	name := baseIdentName(rhs)

	// A definition already registered under this name is reused rather than emitted twice,
	// so naming one binding from two carriers defines it once. Checked first, because
	// registering it removes the name from subqueries.
	for _, existing := range pctx.ctes {
		if existing.Name == name {
			return existing, nil
		}
	}

	sub, bound := pctx.subqueries[name]
	if !bound {
		return nil, fmt.Errorf(
			"%w: %s.Query must name a query bound in this lambda (x, _ := goql.Select[…]), got %s",
			ErrInvalidLambda, carrier, types.ExprString(rhs))
	}

	// Count/Exists and the aggregates yield a value, not rows to read from.
	if sub.Func != "" {
		return nil, fmt.Errorf(
			"%w: %s is a nested %s, which yields a value rather than rows to read from",
			ErrInvalidLambda, name, displayFunc(sub.Func))
	}

	columns, err := e.cteColumns(sub, name)
	if err != nil {
		return nil, err
	}

	// A self-reference carries a placeholder until now: the Go binding is what names the
	// CTE, and that name is only known here. Finding one is also what makes it recursive.
	recursive := nameSelfReferences(sub, name)

	cte := &query.ParseCTE{Name: name, Columns: columns, Query: sub, Recursive: recursive}
	pctx.ctes = append(pctx.ctes, cte)
	delete(pctx.subqueries, name)
	return cte, nil
}

// cteColumns reports the columns a bound query presents to whatever reads it.
//
// A query that projects names its own columns. One that does not — a plain
// `goql.Select[Tag](…)` — selects whole model rows, which a CTE cannot express, so the
// model's own columns are projected for it. That is what lets a plain filtered select be
// read from or joined without restating its columns.
func (e *DebugExecutor) cteColumns(sub *query.ParseQuery, name string) ([]string, error) {
	if len(sub.Body.Select) > 0 || sub.Body.Set != nil {
		return projectedColumns(sub.Body), nil
	}

	schema, err := sub.Schema()
	if err != nil {
		return nil, fmt.Errorf(
			"%w: %s selects whole rows and its model could not be resolved to project them: %v",
			ErrInvalidLambda, name, err)
	}

	sub.Body.Select = autoProjection(schema)
	if len(sub.Body.Select) == 0 {
		return nil, fmt.Errorf(
			"%w: %s has no columns to present — name them with goql.Fields",
			ErrInvalidLambda, name)
	}
	return projectedColumns(sub.Body), nil
}

// autoProjection lists a model's own columns as an explicit projection, in the same stable
// order the DDL uses so the generated SQL does not vary between runs.
func autoProjection(schema *models.Model) []*query.ParseSelect {
	names := make([]string, 0, len(schema.Fields))
	for fieldName, field := range schema.Fields {
		// A collection has no column on this table, so there is nothing to project.
		if kind := field.RelationKind(); kind == models.O2M || kind == models.M2M {
			continue
		}
		names = append(names, fieldName)
	}
	sort.Slice(names, func(i, j int) bool {
		fi, fj := schema.Fields[names[i]], schema.Fields[names[j]]
		if fi.PrimaryKey != fj.PrimaryKey {
			return fi.PrimaryKey
		}
		return names[i] < names[j]
	})

	selects := make([]*query.ParseSelect, 0, len(names))
	for _, fieldName := range names {
		selects = append(selects, &query.ParseSelect{
			Field: &query.FieldRef{Field: schema.Fields[fieldName]},
			Into:  fieldName,
		})
	}
	return selects
}
