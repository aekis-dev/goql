package query

import (
	"fmt"
	"strings"

	"github.com/aekis-dev/goql/models"
)

// subQuery renders a nested query and its bound values.
//
// The nested query shares the enclosing statement's alias map and placeholder counter, which
// is what makes correlation work: a condition inside the nested body may reference the outer
// table, and both render with the same aliases.
func (d *Dialect) subQuery(sub *ParseQuery, s *stmt) (string, []any, error) {
	schema, err := sub.Schema()
	if err != nil {
		return "", nil, fmt.Errorf("subquery: %w", err)
	}

	// A subquery over a table the outer statement already uses would need its own alias to
	// be unambiguous, and both would currently render as the same one.
	if s.alias.Assigned(schema.TableName) {
		return "", nil, fmt.Errorf(
			"subquery over %s is not supported here: the enclosing query already uses that "+
				"table, and both would render with the same alias", schema.TableName)
	}
	alias := s.alias.Alias(schema.TableName)

	projection, err := d.projection(sub, schema, alias)
	if err != nil {
		return "", nil, err
	}

	sql := fmt.Sprintf("SELECT %s FROM %s", projection, s.alias.From(schema.TableName))
	var args []any

	if cond := sub.Body.SelectCondition(); cond != nil {
		joins, err := d.joinClauses(cond, s)
		if err != nil {
			return "", nil, err
		}
		where, whereArgs, err := d.whereClause(cond, s)
		if err != nil {
			return "", nil, err
		}
		if len(joins) > 0 {
			sql += " " + strings.Join(joins, " ")
		}
		sql += " WHERE " + where
		args = append(args, whereArgs...)
	}

	tail, tailArgs, err := d.optionsTail(sub.Body.Options, schema, alias, s)
	if err != nil {
		return "", nil, err
	}
	sql += tail
	args = append(args, tailArgs...)

	if sub.Func == "Exists" {
		return fmt.Sprintf("EXISTS (%s)", sql), args, nil
	}
	return fmt.Sprintf("(%s)", sql), args, nil
}

// projection renders what a query yields: the goql function applied to the fields its lambda
// named, or the primary key when it named none. Shared by nested queries and top-level
// aggregates, so both agree on what a Func means.
func (d *Dialect) projection(sub *ParseQuery, schema *models.Model, alias string) (string, error) {
	fields := sub.Fields()
	if err := CheckFuncArity(sub.Func, len(fields)); err != nil {
		return "", err
	}

	var column string
	if len(fields) > 0 {
		col, err := d.columnFor(schema, alias, fields[0])
		if err != nil {
			return "", err
		}
		column = col
	}

	switch sub.Func {
	case "Exists":
		// "Did a row come back" needs no column, and asking for one only constrains the
		// planner.
		return "1", nil

	case "Count":
		if column == "" {
			return "COUNT(*)", nil
		}
		return fmt.Sprintf("COUNT(%s)", column), nil

	case "":
		if column != "" {
			return column, nil
		}
		// A subquery feeding IN compares against a key, so the primary key is the default.
		if schema.PrimaryKey == nil {
			return "", fmt.Errorf("subquery over %s: no primary key to project, name a field "+
				"with goql.Fields", schema.TableName)
		}
		return fmt.Sprintf("%s.%s", alias, d.primaryKey(schema)), nil

	default:
		// Every other function is an aggregate over the named column.
		if column == "" {
			return "", fmt.Errorf("%s needs a field, named with goql.Fields", sub.Func)
		}
		return fmt.Sprintf("%s(%s)", strings.ToUpper(sub.Func), column), nil
	}
}
