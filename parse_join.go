//go:build !prod

package goql

import (
	"fmt"
	"go/ast"
	"go/types"

	"github.com/aekis-dev/goql/query"
)

// joinTypes are the kinds an explicit join may take. The value is the SQL keyword.
var joinTypes = map[string]string{
	"Inner": "INNER",
	"Left":  "LEFT",
	"Right": "RIGHT",
	"Full":  "FULL",
}

// setJoin records one field of an explicit join carrier: `j.Model = p`, `j.On = a == b` or
// `j.Type = goql.Left`.
//
// The condition is parsed here rather than evaluated, like every other expression in a
// lambda. Model names one of the lambda's own model parameters — pointing at the declaration
// instead of restating it, so the two cannot disagree.
func (e *DebugExecutor) setJoin(spec *query.JoinSpec, field string, rhs ast.Expr, pctx *parseContext) error {
	if spec == nil {
		return fmt.Errorf("%w: join carrier is not declared as a lambda parameter", ErrInvalidOption)
	}

	switch field {
	case "Query":
		return e.setJoinQuery(spec, rhs, pctx)

	case "Field":
		return e.setJoinField(spec, rhs, pctx)

	case "Model":
		name := baseIdentName(rhs)

		// With a relation path joined, Model names the handle for the row at its far end.
		if len(spec.Hops) > 0 {
			return e.bindJoinPathRow(spec, name, rhs, pctx)
		}

		// With a query joined, Model names the handle for one of its rows.
		if spec.CTE {
			if name == "" || !pctx.rowParams[name] {
				return fmt.Errorf(
					"%w: join.Model must name the lambda parameter standing for a row of %s, got %s",
					ErrInvalidLambda, spec.Table, types.ExprString(rhs))
			}
			pctx.bindCTERow(name, &query.ParseCTE{Name: spec.Table, Columns: pctx.joinColumns(spec.Table)})
			return nil
		}

		schema, declared := pctx.participants[name]
		if !declared {
			return fmt.Errorf(
				"%w: join.Model must be one of the lambda's model parameters, got %s",
				ErrInvalidLambda, types.ExprString(rhs))
		}
		if schema.TableName == pctx.schema.TableName {
			return fmt.Errorf(
				"%w: join.Model names %s, which the query already reads from — a table cannot "+
					"be joined to itself without a second alias",
				ErrInvalidLambda, schema.TableName)
		}
		spec.Table = schema.TableName

	case "On":
		condition, err := e.exprToCondition(rhs, pctx)
		if err != nil {
			return fmt.Errorf("join condition: %w", err)
		}
		spec.On = condition

	case "Type":
		name := joinTypeName(rhs)
		keyword, known := joinTypes[name]
		if !known {
			return fmt.Errorf(
				"%w: %s is not a join type — use goql.Inner, goql.Left, goql.Right or goql.Full",
				ErrInvalidOption, types.ExprString(rhs))
		}
		spec.Type = keyword

	default:
		return fmt.Errorf("%w: Join has no field %s", ErrInvalidOption, field)
	}
	return nil
}

// setJoinField handles `j.Field = o.Customer.Country` — a join declared by naming the
// relation path that reaches it, rather than by writing out the condition.
//
// The models already declare how each hop relates, so the ON clauses are derived. That also
// makes the join reusable in a way a path in a predicate is not: the far row gets a handle,
// so it can be sorted by, projected, or kept with LEFT semantics.
func (e *DebugExecutor) setJoinField(spec *query.JoinSpec, rhs ast.Expr, pctx *parseContext) error {
	ref, err := e.resolveFieldRefIn(rhs, pctx)
	if err != nil {
		return fmt.Errorf("join field: %w", err)
	}

	terminal := ref.Terminal().Field
	if terminal == nil || !terminal.IsRelation() {
		return fmt.Errorf(
			"%w: join.Field must name a relation to join through, and %s is a plain column",
			ErrInvalidOption, types.ExprString(rhs))
	}

	targetSchema, err := query.RelationTargetSchema(terminal)
	if err != nil {
		return err
	}

	// Hops() stops at the reference holding the terminal, because in a predicate that one
	// names a column. Here it is the last relation, so it is a hop of its own.
	hops := ref.Hops()
	sourcePath := ref.Path()
	targetPath := terminal.Name
	if sourcePath != "" {
		targetPath = sourcePath + "." + terminal.Name
	}
	hops = append(hops, query.FieldHop{
		SourcePath: sourcePath,
		Field:      terminal,
		TargetPath: targetPath,
	})

	spec.Hops = hops
	spec.Path = targetPath
	spec.Table = targetSchema.TableName
	return nil
}

// bindJoinPathRow binds the handle named by join.Model to the row a path join reaches, so
// references through it resolve against that occurrence of the table rather than the table
// at large.
func (e *DebugExecutor) bindJoinPathRow(spec *query.JoinSpec, name string, rhs ast.Expr, pctx *parseContext) error {
	schema, declared := pctx.participants[name]
	if !declared {
		return fmt.Errorf(
			"%w: join.Model must be one of the lambda's model parameters, got %s",
			ErrInvalidLambda, types.ExprString(rhs))
	}
	if schema.TableName != spec.Table {
		return fmt.Errorf(
			"%w: join.Model names a %s, but join.Field arrives at %s",
			ErrInvalidLambda, schema.TableName, spec.Table)
	}
	if pctx.participantPaths == nil {
		pctx.participantPaths = map[string]string{}
	}
	pctx.participantPaths[name] = spec.Path
	return nil
}

// joinTypeName reads the constant a join type was assigned from, written either qualified
// (goql.Left) or bare (Left) depending on the caller's imports.
func joinTypeName(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	}
	return ""
}

// selfCTEName is the placeholder a recursive self-reference carries until the CTE is bound to
// a name. The binding is what names it, and that happens after this body is parsed.
const selfCTEName = "\x00self"

// setJoinQuery handles `j.Query = t` — joining a named query rather than a table. The query
// is one bound in this lambda, or the handle on the CTE being defined, which is what makes a
// CTE recursive.
func (e *DebugExecutor) setJoinQuery(spec *query.JoinSpec, rhs ast.Expr, pctx *parseContext) error {
	name := baseIdentName(rhs)

	if name != "" && name == pctx.selfHandle {
		if len(pctx.selfColumns) == 0 {
			return fmt.Errorf(
				"%w: %s is joined before anything has been selected into it — the first branch "+
					"of a recursive query is its anchor and cannot reference itself",
				ErrInvalidLambda, name)
		}
		pctx.recursive = true
		spec.Table, spec.CTE = selfCTEName, true
		return nil
	}

	// Resolved by the same function from.Query uses, which is also what registers the
	// definition so a WITH clause is emitted for it.
	cte, err := e.bindQueryRef(rhs, "join", pctx)
	if err != nil {
		return err
	}
	spec.Table, spec.CTE = cte.Name, true
	return nil
}

// joinColumns returns the columns a joined query presents. For the CTE being defined they
// are the anchor branch's, which is where SQL takes a recursive CTE's shape from too.
func (pctx *parseContext) joinColumns(table string) []string {
	if table == selfCTEName {
		return pctx.selfColumns
	}
	// Binding a query moves it out of subqueries and into ctes, so the registered
	// definition — which already carries its column list — is what to consult.
	for _, cte := range pctx.ctes {
		if cte.Name == table {
			return cte.Columns
		}
	}
	for name, sub := range pctx.subqueries {
		if name != table {
			continue
		}
		return projectedColumns(sub.Body)
	}
	return nil
}

// pathBound reports whether a handle was bound to the row a path join reaches.
func (pctx *parseContext) pathBound(path string) bool {
	for _, bound := range pctx.participantPaths {
		if bound == path {
			return true
		}
	}
	return false
}
