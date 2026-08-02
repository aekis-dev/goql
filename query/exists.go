package query

import (
	"fmt"
	"strings"

	"github.com/aekis-dev/goql/models"
)

// existsClause renders a correlated EXISTS over a collection relation.
//
// The subquery shares the enclosing statement's alias map and placeholder counter, which is
// what makes the correlation render correctly: the outer table already has an alias (or is
// pinned to its own name in an UPDATE/DELETE) and the same one is used on both sides.
//
// A JOIN is deliberately not used. A join is a clause on the statement and is applied before
// the WHERE, so it both multiplies rows and eliminates rows that have no related row at all —
// even when those rows satisfy another arm of a disjunction. EXISTS is an expression and can
// do neither.
func (d *Dialect) existsClause(re *RelationExists, s *stmt) (string, []any, error) {
	field := re.Relation.Field
	sourceSchema := field.TableSchema

	targetSchema, err := RelationTargetSchema(field)
	if err != nil {
		return "", nil, err
	}

	// The related table needs an alias of its own inside the subquery. The alias map is
	// keyed by table, so a relation whose target is a table the enclosing statement already
	// reads would render both under one alias.
	if s.alias.Assigned(targetSchema.TableName) {
		return "", nil, fmt.Errorf(
			"goql.Filter over %s is not supported here: the enclosing query already uses that "+
				"table, and both would render with the same alias", targetSchema.TableName)
	}

	var from, correlation string
	switch field.RelationKind() {
	case models.O2M:
		// The related row carries the foreign key back to the source row.
		from = s.alias.From(targetSchema.TableName)
		correlation = fmt.Sprintf("%s.%s = %s.%s",
			s.alias.Alias(targetSchema.TableName), d.QuoteIdent(field.OneToMany.Ref),
			s.alias.Alias(sourceSchema.TableName), d.primaryKey(sourceSchema))

	case models.M2M:
		m := field.ManyToMany
		if s.alias.Assigned(m.Table) {
			return "", nil, fmt.Errorf(
				"goql.Filter over %s is not supported here: the enclosing query already uses "+
					"the join table %s", targetSchema.TableName, m.Table)
		}
		// The bridge table correlates back to the source row; the target is joined to it.
		from = fmt.Sprintf("%s INNER JOIN %s ON %s.%s = %s.%s",
			s.alias.From(m.Table),
			s.alias.From(targetSchema.TableName),
			s.alias.Alias(targetSchema.TableName), d.primaryKey(targetSchema),
			s.alias.Alias(m.Table), d.QuoteIdent(m.Ref))
		correlation = fmt.Sprintf("%s.%s = %s.%s",
			s.alias.Alias(m.Table), d.QuoteIdent(m.Column),
			s.alias.Alias(sourceSchema.TableName), d.primaryKey(sourceSchema))

	default:
		return "", nil, fmt.Errorf(
			"goql: cannot filter over %s: it is not a collection relation", field.Name)
	}

	conditions := []string{correlation}
	var args []any

	if re.Condition != nil {
		// A condition inside the filter may itself reach through a many2one of the related
		// model, which joins within the subquery and so must precede the WHERE clause.
		joins, err := d.joinClauses(re.Condition, s)
		if err != nil {
			return "", nil, err
		}
		where, whereArgs, err := d.whereClause(re.Condition, s)
		if err != nil {
			return "", nil, err
		}
		if len(joins) > 0 {
			from += " " + strings.Join(joins, " ")
		}
		// Parenthesised: AND binds tighter than OR, so a predicate that is itself a
		// disjunction would otherwise leave its later arms outside the correlation —
		// making the EXISTS true for every row.
		conditions = append(conditions, "("+where+")")
		args = whereArgs
	}

	return fmt.Sprintf("EXISTS (SELECT 1 FROM %s WHERE %s)",
		from, strings.Join(conditions, " AND ")), args, nil
}
