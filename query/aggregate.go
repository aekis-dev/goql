package query

import (
	"fmt"
	"strings"
)

// LambdaAggregate builds a scalar aggregate query — SUM(col) and whatever else the
// allowlist grows — from a parsed lambda predicate. The function and the column both come
// from the tree, so a new aggregate needs no new builder.
func (d *Dialect) LambdaAggregate(q *ParseQuery) (*Query, error) {
	schema, err := q.Schema()
	if err != nil {
		return nil, err
	}
	body := q.Body

	s := d.newStmt()
	alias := s.alias.Alias(schema.TableName)

	projection, err := d.projection(q, schema, alias)
	if err != nil {
		return nil, err
	}

	sql := fmt.Sprintf("SELECT %s FROM %s", projection, s.fromList(body, schema))
	var args []any

	if cond := body.SelectCondition(); cond != nil {
		joins, err := d.joinClauses(cond, s)
		if err != nil {
			return nil, err
		}
		where, whereArgs, err := d.whereClause(cond, s)
		if err != nil {
			return nil, err
		}
		if len(joins) > 0 {
			sql += " " + strings.Join(joins, " ")
		}
		sql += " WHERE " + where
		args = whereArgs
	}

	return &Query{SQL: sql, Args: args}, nil
}

// LambdaCount builds a COUNT query from a parsed lambda predicate.
//
// When the predicate reaches through a relation the joins can multiply rows, so the count
// is taken over distinct primary keys rather than raw rows.
func (d *Dialect) LambdaCount(q *ParseQuery) (*Query, error) {
	schema, err := q.Schema()
	if err != nil {
		return nil, err
	}
	body := q.Body

	s := d.newStmt()
	alias := s.alias.Alias(schema.TableName)

	cond := body.SelectCondition()
	if cond == nil {
		return &Query{
			SQL: fmt.Sprintf("SELECT COUNT(*) FROM %s", s.fromList(body, schema)),
		}, nil
	}

	joins, err := d.joinClauses(cond, s)
	if err != nil {
		return nil, err
	}
	where, args, err := d.whereClause(cond, s)
	if err != nil {
		return nil, err
	}

	// A joined participant is of unknown cardinality and may multiply rows. Joins derived
	// from the condition tree cannot: a relation predicate is an EXISTS, so what remains is
	// many2one path traversal, which yields at most one row per source row.
	counted := "COUNT(*)"
	if len(body.Joined) > 0 && schema.PrimaryKey != nil {
		counted = fmt.Sprintf("COUNT(DISTINCT %s.%s)", alias, d.primaryKey(schema))
	}

	sql := fmt.Sprintf("SELECT %s FROM %s", counted, s.fromList(body, schema))
	if len(joins) > 0 {
		sql += " " + strings.Join(joins, " ")
	}
	sql += " WHERE " + where

	return &Query{SQL: sql, Args: args}, nil
}

// LambdaExists builds a query that returns at most one row, for an existence check.
// `SELECT 1 … LIMIT 1` is used rather than EXISTS(…) because scanning a boolean differs
// between drivers, while "did a row come back" is uniform.
func (d *Dialect) LambdaExists(q *ParseQuery) (*Query, error) {
	schema, err := q.Schema()
	if err != nil {
		return nil, err
	}
	body := q.Body

	s := d.newStmt()
	s.alias.Alias(schema.TableName)

	sql := fmt.Sprintf("SELECT 1 FROM %s", s.fromList(body, schema))
	var args []any

	if cond := body.SelectCondition(); cond != nil {
		joins, err := d.joinClauses(cond, s)
		if err != nil {
			return nil, err
		}
		where, whereArgs, err := d.whereClause(cond, s)
		if err != nil {
			return nil, err
		}
		if len(joins) > 0 {
			sql += " " + strings.Join(joins, " ")
		}
		sql += " WHERE " + where
		args = whereArgs
	}

	return &Query{SQL: sql + " LIMIT 1", Args: args}, nil
}
