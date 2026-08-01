package query

import (
	"fmt"
	"strings"
)

// withClause renders the WITH prefix for a statement whose body defines common table
// expressions, and returns the values their definitions bind.
//
// The definitions are rendered before the statement that reads them, so their placeholders
// come first — which is also the order the engine reads them in.
func (d *Dialect) withClause(body *ParseBody, s *stmt) (string, []any, error) {
	if body == nil || len(body.With) == 0 || !d.SupportsCTE() {
		return "", nil, nil
	}

	parts := make([]string, 0, len(body.With))
	var args []any
	recursive := false
	for _, cte := range body.With {
		if err := checkRecursive(cte); err != nil {
			return "", nil, err
		}
		sql, cteArgs, err := d.cteBody(cte, s)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, fmt.Sprintf("%s AS (%s)", d.QuoteIdent(cte.Name), sql))
		args = append(args, cteArgs...)
		recursive = recursive || cte.Recursive
	}

	// RECURSIVE is a property of the WITH list, not of one member: a single recursive
	// definition makes the whole clause recursive and the others ride along.
	keyword := "WITH "
	if recursive {
		keyword = "WITH RECURSIVE "
	}
	return keyword + strings.Join(parts, ", ") + " ", args, nil
}

// cteBody renders one CTE's defining query.
//
// It gets its own aliases: it is an independent statement, and sharing the outer map would
// make a CTE over a table the outer query also reads collide. The placeholder counter is
// shared, because Postgres numbers parameters across the whole statement.
func (d *Dialect) cteBody(cte *ParseCTE, s *stmt) (string, []any, error) {
	outer := s.alias
	s.alias = NewAliasMap(d)
	defer func() { s.alias = outer }()

	// A recursive definition is a set operation — the anchor and the recursive term — so it
	// renders through the same builder a top-level union does.
	build := d.lambdaSearchIn
	if cte.Query.Body.Set != nil {
		build = d.lambdaSetSearchIn
	}
	sub, err := build(cte.Query, cte.Query.Body.Options, s)
	if err != nil {
		return "", nil, fmt.Errorf("common table expression %s: %w", cte.Name, err)
	}
	return sub.SQL, sub.Args, nil
}

// checkRecursive refuses what no engine allows in a recursive term. Emitting it would produce
// SQL the database rejects, with a message pointing at generated text the caller never wrote.
func checkRecursive(cte *ParseCTE) error {
	if !cte.Recursive {
		return nil
	}
	set := cte.Query.Body.Set
	if set == nil || len(set.Branches) < 2 {
		return fmt.Errorf(
			"recursive query %s needs an anchor branch and a recursive one, combined with "+
				"goql.Union or goql.UnionAll", cte.Name)
	}
	if set.Op != "UNION" && set.Op != "UNION ALL" {
		return fmt.Errorf("recursive query %s combines its branches with %s — only UNION and "+
			"UNION ALL are allowed", cte.Name, set.Op)
	}

	for i, branch := range set.Branches {
		body := branch.Body
		self := joinsCTE(body, cte.Name)
		if i == 0 && self {
			return fmt.Errorf("recursive query %s references itself in its first branch, which "+
				"is the anchor and must stand on its own", cte.Name)
		}
		if !self {
			continue
		}
		if body.Aggregated() {
			return fmt.Errorf("the recursive branch of %s aggregates, which no engine allows — "+
				"aggregate over the finished query instead", cte.Name)
		}
		if opts := body.Options; opts != nil {
			switch {
			case len(opts.GroupBy) > 0:
				return fmt.Errorf("the recursive branch of %s groups, which no engine allows", cte.Name)
			case len(opts.Sorts) > 0:
				return fmt.Errorf("the recursive branch of %s orders, which no engine allows — "+
					"order the finished query instead", cte.Name)
			case opts.Limit != nil || opts.Offset != nil:
				return fmt.Errorf("the recursive branch of %s limits, which no engine allows — "+
					"filter on a depth column to bound the recursion", cte.Name)
			}
		}
		if count := countCTEJoins(body, cte.Name); count > 1 {
			return fmt.Errorf("the recursive branch of %s references it %d times; every engine "+
				"allows exactly one", cte.Name, count)
		}
	}
	return nil
}

// joinsCTE reports whether a branch references the named query.
func joinsCTE(body *ParseBody, name string) bool {
	return countCTEJoins(body, name) > 0
}

func countCTEJoins(body *ParseBody, name string) int {
	if body.Options == nil {
		return 0
	}
	count := 0
	for _, join := range body.Options.Joins {
		if join.CTE && join.Table == name {
			count++
		}
	}
	return count
}

// derivedTable renders a CTE inline, as `(SELECT …) alias`, for an engine without WITH.
//
// The subquery is repeated at each use. That is the same result with a worse plan, which is
// preferable to a query that works on Postgres and fails on an older MySQL.
func (d *Dialect) derivedTable(body *ParseBody, name string, s *stmt) (string, []any, error) {
	for _, cte := range body.With {
		if cte.Name != name {
			continue
		}
		// A derived table cannot reference itself, so there is no fallback for recursion.
		if cte.Recursive {
			return "", nil, fmt.Errorf(
				"%s is recursive, which %s cannot express: WITH RECURSIVE is required and this "+
					"engine has no CTEs", name, d.Name())
		}
		sql, args, err := d.cteBody(cte, s)
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("(%s) %s", sql, s.alias.Alias(name)), args, nil
	}
	return "", nil, fmt.Errorf("query reads from %s, which is not defined in this statement", name)
}
