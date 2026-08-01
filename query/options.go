package query

import (
	"fmt"
	"strings"

	"github.com/aekis-dev/goql/models"
)

// SortSpec is one ORDER BY term. By is a Go field name, resolved to a column against
// the model's schema.
type SortSpec struct {
	By   string
	Desc bool
}

// Options carries the per-query modifiers shared by the struct-based and lambda-based
// APIs: projection, ordering and pagination. A nil *Options means "no modifiers".
type Options struct {
	Sorts  []SortSpec
	Limit  *int
	Offset *int

	// Fields restricts the projection to these Go field names. Empty selects every
	// column. Unselected fields come back as Go zero values, indistinguishable from a
	// column that is genuinely empty.
	Fields []string

	// Preload names the relation fields to load with the result. PreloadSet tells an
	// explicit empty list ("load nothing") apart from an absent one ("use the model's
	// defaults"), since a query's list replaces those defaults entirely.
	Preload    []string
	PreloadSet bool

	// GroupBy names extra grouping keys, as Go field names. They are additive: a projected
	// plain column is always a key, and these are the ones you could not project — a
	// relation, or a column you do not want in the result.
	GroupBy []string

	// ConflictIgnore skips rows that collide with an existing one instead of failing.
	// Only Insert honours it; the other calls reject it rather than ignore it.
	ConflictIgnore bool
}

// columnFor resolves a Go field name to its qualified, quoted column.
func (d *Dialect) columnFor(schema *models.Model, qualifier, fieldName string) (string, error) {
	field, ok := schema.Fields[fieldName]
	if !ok {
		return "", fmt.Errorf("%w: %s on %s", models.ErrFieldNotFound, fieldName, schema.TableName)
	}
	if field.IsRelation() {
		if field.RelationKind() != models.M2O {
			return "", fmt.Errorf("field %s is a relation and has no column on %s",
				fieldName, schema.TableName)
		}
		return fmt.Sprintf("%s.%s", qualifier, d.QuoteIdent(field.GetFKColumn())), nil
	}
	return fmt.Sprintf("%s.%s", qualifier, d.QuoteIdent(field.ColumnName())), nil
}

// selectList renders the projection for a SELECT: every column, or just the requested
// fields. The primary key is always included so scanned rows remain identifiable.
func (d *Dialect) selectList(o *Options, schema *models.Model, qualifier string) (string, error) {
	if o == nil || len(o.Fields) == 0 {
		return qualifier + ".*", nil
	}

	seen := make(map[string]bool, len(o.Fields)+1)
	var cols []string

	if schema.PrimaryKey != nil {
		pk := fmt.Sprintf("%s.%s", qualifier, d.primaryKey(schema))
		cols = append(cols, pk)
		seen[pk] = true
	}

	for _, fieldName := range o.Fields {
		col, err := d.columnFor(schema, qualifier, fieldName)
		if err != nil {
			return "", err
		}
		if seen[col] {
			continue
		}
		seen[col] = true
		cols = append(cols, col)
	}

	return strings.Join(cols, ", "), nil
}

// optionsTail renders the ORDER BY / LIMIT / OFFSET suffix along with its bound arguments.
func (d *Dialect) optionsTail(o *Options, schema *models.Model, qualifier string, s *stmt) (string, []any, error) {
	if o == nil {
		return "", nil, nil
	}

	var sql strings.Builder
	var args []any

	if len(o.Sorts) > 0 {
		terms := make([]string, 0, len(o.Sorts))
		for _, sort := range o.Sorts {
			col, err := d.columnFor(schema, qualifier, sort.By)
			if err != nil {
				return "", nil, err
			}
			if sort.Desc {
				col += " DESC"
			}
			terms = append(terms, col)
		}
		sql.WriteString(" ORDER BY " + strings.Join(terms, ", "))
	}

	if o.Limit != nil {
		sql.WriteString(" LIMIT " + s.mark())
		args = append(args, *o.Limit)
	}
	if o.Offset != nil {
		// Every supported engine requires a limit before an offset, so an open-ended one
		// is emitted when the caller gave none.
		if o.Limit == nil {
			sql.WriteString(" " + d.OpenEndedLimit())
		}
		sql.WriteString(" OFFSET " + s.mark())
		args = append(args, *o.Offset)
	}

	return sql.String(), args, nil
}

// IntPtr returns a pointer to n. Limit and Offset are pointers so "unset" is distinct from
// zero; the generated prod registry needs a way to write one as an expression.
func IntPtr(n int) *int { return &n }

// groupKeys returns the explicitly named grouping keys, tolerating absent options.
func (o *Options) groupKeys() []string {
	if o == nil {
		return nil
	}
	return o.GroupBy
}

// IsEmpty reports whether any modifier is set. It lives here, beside the fields, because the
// parser used to repeat this check and a field added there but forgotten here is silently
// dropped — which is how GroupBy first went missing.
func (o *Options) IsEmpty() bool {
	if o == nil {
		return true
	}
	return len(o.Sorts) == 0 &&
		o.Limit == nil &&
		o.Offset == nil &&
		len(o.Fields) == 0 &&
		!o.PreloadSet &&
		len(o.GroupBy) == 0 &&
		!o.ConflictIgnore
}
