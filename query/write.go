package query

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aekis-dev/goql/models"
)

// LambdaWrite builds one UPDATE per assigning branch of a parsed lambda body.
//
// An if/else-if/else chain or a switch therefore yields several statements, each with its
// own mutually exclusive WHERE — previously every arm's assignments were merged into one
// UPDATE under the first arm's condition.
func (d *Dialect) LambdaWrite(q *ParseQuery) ([]*Query, error) {
	schema, err := q.Schema()
	if err != nil {
		return nil, err
	}
	body := q.Body

	// An UPDATE reaches other tables through the relation joins it derives, not through a
	// comma-joined FROM list, so an extra model parameter cannot be honoured here.
	if err := rejectJoined(body, "Update"); err != nil {
		return nil, err
	}

	var queries []*Query
	for _, branch := range body.WriteBranches() {
		if len(branch.Assignments) == 0 {
			// Relation-only branch: no columns to SET, handled by the caller.
			continue
		}
		q, err := d.lambdaWriteBranch(branch, schema)
		if err != nil {
			return nil, err
		}
		queries = append(queries, q)
	}
	return queries, nil
}

// lambdaWriteBranch builds the UPDATE for a single branch.
func (d *Dialect) lambdaWriteBranch(branch *ParseBranch, schema *models.Model) (*Query, error) {
	s := d.newStmt()
	// UPDATE cannot portably alias the table it modifies, so refer to it by name.
	s.alias.PinTableName(schema.TableName)

	var setClauses []string
	var args []any

	// Scalar assignments. The SET target is always unqualified — qualifying it is invalid
	// in standard SQL.
	for _, assignment := range branch.Assignments {
		field := assignment.Field.Field
		col := d.QuoteIdent(field.ColumnName())
		if field.RelationKind() == models.M2O {
			col = d.QuoteIdent(field.GetFKColumn())
		}

		if assignment.Value.IsColumn {
			setClauses = append(setClauses, fmt.Sprintf("%s = %s", col, s.column(assignment.Value.Field)))
			continue
		}

		setClauses = append(setClauses, fmt.Sprintf("%s = %s", col, s.mark()))

		if _, isParam := assignment.Value.Value.(ParamRef); isParam {
			// The value is only known at execution time, so it cannot be marshalled here.
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

	// Inject autoUpdateTime
	for _, fieldName := range sortedFieldNames(schema) {
		field := schema.Fields[fieldName]
		if field.AutoUpdateTime {
			setClauses = append(setClauses, fmt.Sprintf("%s = %s", d.QuoteIdent(field.ColumnName()), s.mark()))
			args = append(args, time.Now())
		}
	}

	if len(setClauses) == 0 {
		return nil, fmt.Errorf("no SET clauses found in lambda — no scalar assignments")
	}

	if branch.Condition == nil {
		return &Query{
			SQL:  fmt.Sprintf("UPDATE %s SET %s", d.table(schema), strings.Join(setClauses, ", ")),
			Args: args,
		}, nil
	}

	joins := CollectJoins(branch.Condition, make(map[string]bool))

	var fromTables []string
	var joinConditions []string
	for _, joinNode := range joins {
		from, joinCond, err := d.updateFromClause(joinNode, s)
		if err != nil {
			return nil, err
		}
		fromTables = append(fromTables, from)
		joinConditions = append(joinConditions, joinCond)
	}

	// Rendered after the joins so every table already has its alias assigned.
	where, whereArgs, err := d.whereClause(branch.Condition, s)
	if err != nil {
		return nil, err
	}
	args = append(args, whereArgs...)

	if len(fromTables) == 0 {
		return &Query{
			SQL: fmt.Sprintf("UPDATE %s SET %s WHERE %s",
				d.table(schema), strings.Join(setClauses, ", "), where),
			Args: args,
		}, nil
	}

	// MySQL has no UPDATE … FROM: it writes the join before SET instead.
	if !d.SupportsUpdateFrom() {
		return &Query{
			SQL: fmt.Sprintf("UPDATE %s JOIN %s ON %s SET %s WHERE %s",
				d.table(schema),
				strings.Join(fromTables, " JOIN "),
				strings.Join(joinConditions, " AND "),
				strings.Join(setClauses, ", "),
				where),
			Args: args,
		}, nil
	}

	allWhere := append(joinConditions, where)
	return &Query{
		SQL: fmt.Sprintf("UPDATE %s SET %s FROM %s WHERE %s",
			d.table(schema),
			strings.Join(setClauses, ", "),
			strings.Join(fromTables, ", "),
			strings.Join(allWhere, " AND ")),
		Args: args,
	}, nil
}

// EntityWrite builds an UPDATE query from a change map and schema.
// Inspects changes, maps field names to columns, skips PK/AutoIncrement/relations.
func (d *Dialect) EntityWrite(entity models.Entity, schema *models.Model, changes map[string]any) (*Query, error) {
	s := d.newStmt()
	_, pkValue := entity.PrimaryKey()

	var setClauses []string
	var args []any

	// Sorted for deterministic SET-clause order — changes is a map.
	changedFields := make([]string, 0, len(changes))
	for fieldName := range changes {
		changedFields = append(changedFields, fieldName)
	}
	sort.Strings(changedFields)

	for _, fieldName := range changedFields {
		newValue := changes[fieldName]
		field, exists := schema.Fields[fieldName]
		if !exists || field.PrimaryKey || field.AutoIncrement {
			continue
		}

		switch field.RelationKind() {
		case models.O2M, models.M2M:
			continue
		case models.M2O:
			setClauses = append(setClauses, fmt.Sprintf("%s = %s", d.QuoteIdent(field.GetFKColumn()), s.mark()))
			args = append(args, newValue)
		default:
			setClauses = append(setClauses, fmt.Sprintf("%s = %s", d.QuoteIdent(field.ColumnName()), s.mark()))
			if field.IsJSON() && newValue != nil {
				encoded, err := json.Marshal(newValue)
				if err != nil {
					return nil, fmt.Errorf("field %s: %w", fieldName, err)
				}
				args = append(args, encoded)
				continue
			}
			args = append(args, newValue)
		}
	}

	if len(setClauses) == 0 {
		return nil, nil // nothing to update
	}

	args = append(args, pkValue)
	return &Query{
		SQL: fmt.Sprintf("UPDATE %s SET %s WHERE %s = %s",
			d.table(schema), strings.Join(setClauses, ", "), d.primaryKey(schema), s.mark()),
		Args: args,
	}, nil
}
