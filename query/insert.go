package query

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aekis-dev/goql/models"
)

// LambdaInsert builds one INSERT … SELECT per assigning branch of a parsed lambda body.
//
// The lambda's first parameter is the destination model and its second is the source, so
// an assignment `a.Total = o.Total` supplies both halves of the statement at once: the
// left side names a destination column, the right side an expression selected from the
// source. Conditions filter the SELECT, exactly as they do for Select.
//
// Branches work as they do for Update — an if/else yields one statement per arm, each with
// its own mutually exclusive WHERE.
func (d *Dialect) LambdaInsert(q *ParseQuery, src *models.Model, opts *Options) ([]*Query, error) {
	dest, err := q.Schema()
	if err != nil {
		return nil, err
	}
	body := q.Body

	var queries []*Query
	for _, branch := range body.WriteBranches() {
		if len(branch.RelationAssignments) > 0 {
			// A relation link needs the primary key of a row that does not exist yet, and
			// INSERT … SELECT never reports the keys it generated.
			return nil, fmt.Errorf("relation assignments are not supported in an Insert " +
				"lambda: linking needs the primary key of a row that does not exist yet, " +
				"and INSERT … SELECT does not report the keys it generated — insert first, " +
				"then link")
		}
		if len(branch.Assignments) == 0 {
			continue
		}
		q, err := d.lambdaInsertBranch(body, branch, dest, src, opts)
		if err != nil {
			return nil, err
		}
		queries = append(queries, q)
	}
	if len(queries) == 0 {
		return nil, fmt.Errorf("no column assignments found in lambda")
	}
	return queries, nil
}

func (d *Dialect) lambdaInsertBranch(body *ParseBody, branch *ParseBranch, dest, src *models.Model, opts *Options) (*Query, error) {
	s := d.newStmt()
	// The source drives the SELECT, so it claims the preferred short alias. The destination
	// is named in full: an INSERT target cannot portably be aliased.
	alias := s.alias.Alias(src.TableName)

	var columns []string
	var selected []string
	var args []any

	for _, assignment := range branch.Assignments {
		field := assignment.Field.Field
		col := d.QuoteIdent(field.ColumnName())
		if field.RelationKind() == models.M2O {
			col = d.QuoteIdent(field.GetFKColumn())
		}
		columns = append(columns, col)

		// A source field is selected; anything else is bound and selected as a constant,
		// which is how literals and params-struct values reach every inserted row.
		if assignment.Value.IsColumn {
			selected = append(selected, s.column(assignment.Value.Field))
			continue
		}

		selected = append(selected, s.mark())

		if _, isParam := assignment.Value.Value.(ParamRef); isParam {
			if field.IsJSON() {
				return nil, fmt.Errorf(
					"field %s: a JSON column cannot be assigned from a params struct yet",
					field.Name)
			}
			args = append(args, assignment.Value.Value)
			continue
		}

		if field.IsJSON() && assignment.Value.Value != nil {
			encoded, err := json.Marshal(assignment.Value.Value)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", field.Name, err)
			}
			args = append(args, encoded)
			continue
		}
		args = append(args, assignment.Value.Value)
	}

	// Timestamps the destination maintains itself are filled in for every inserted row.
	for _, fieldName := range sortedFieldNames(dest) {
		field := dest.Fields[fieldName]
		if !field.AutoCreateTime && !field.AutoUpdateTime {
			continue
		}
		if assignsColumn(columns, d.QuoteIdent(field.ColumnName())) {
			continue
		}
		columns = append(columns, d.QuoteIdent(field.ColumnName()))
		selected = append(selected, "CURRENT_TIMESTAMP")
	}

	selectSQL := fmt.Sprintf("SELECT %s FROM %s",
		strings.Join(selected, ", "), s.fromList(body, src))

	if branch.Condition != nil {
		joins, err := d.joinClauses(branch.Condition, s)
		if err != nil {
			return nil, err
		}
		where, whereArgs, err := d.whereClause(branch.Condition, s)
		if err != nil {
			return nil, err
		}
		if len(joins) > 0 {
			selectSQL += " " + strings.Join(joins, " ")
		}
		selectSQL += " WHERE " + where
		args = append(args, whereArgs...)
	}

	// Rendered last so its placeholders follow those of the SELECT list and WHERE clause.
	tail, tailArgs, err := d.optionsTail(opts, src, alias, s)
	if err != nil {
		return nil, err
	}
	args = append(args, tailArgs...)

	ignore := opts != nil && opts.ConflictIgnore
	return &Query{
		SQL:  d.InsertSelect(d.table(dest), columns, selectSQL+tail, ignore),
		Args: args,
	}, nil
}

// assignsColumn reports whether a column is already in the insert's column list, so an
// explicit assignment wins over the automatic timestamp.
func assignsColumn(columns []string, column string) bool {
	for _, existing := range columns {
		if existing == column {
			return true
		}
	}
	return false
}
