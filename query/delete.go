package query

import (
	"fmt"

	"github.com/aekis-dev/goql/models"
)

// EntityDelete builds a DELETE query for a single entity by PK
func (d *Dialect) EntityDelete(pkValue any, schema *models.Model) (*Query, error) {
	s := d.newStmt()
	return &Query{
		SQL: fmt.Sprintf("DELETE FROM %s WHERE %s IN (%s)",
			d.table(schema), d.primaryKey(schema), s.mark()),
		Args: []any{pkValue},
	}, nil
}

// LambdaDelete builds a DELETE query from a parsed lambda predicate
func (d *Dialect) LambdaDelete(q *ParseQuery) (*Query, error) {
	schema, err := q.Schema()
	if err != nil {
		return nil, err
	}
	body := q.Body

	if err := rejectJoined(body, "Delete"); err != nil {
		return nil, err
	}

	cond := body.SelectCondition()
	if cond == nil {
		return &Query{SQL: fmt.Sprintf("DELETE FROM %s", d.table(schema))}, nil
	}

	// A predicate reaching through a relation needs a JOIN, which DELETE cannot do
	// portably — scope it with a subquery over the primary key instead.
	if joins := CollectJoins(cond, make(map[string]bool)); len(joins) > 0 {
		sub, err := d.SelectPKsWhere(schema, cond)
		if err != nil {
			return nil, err
		}
		return &Query{
			SQL: fmt.Sprintf("DELETE FROM %s WHERE %s IN (%s)",
				d.table(schema), d.primaryKey(schema), sub.SQL),
			Args: sub.Args,
		}, nil
	}

	s := d.newStmt()
	// DELETE cannot portably alias its target table.
	s.alias.PinTableName(schema.TableName)
	where, args, err := d.whereClause(cond, s)
	if err != nil {
		return nil, err
	}

	return &Query{
		SQL:  fmt.Sprintf("DELETE FROM %s WHERE %s", d.table(schema), where),
		Args: args,
	}, nil
}
