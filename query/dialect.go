package query

import (
	"fmt"
	"strings"

	"github.com/aekis-dev/goql/models"
)

// Spec carries the pieces of SQL that genuinely differ between engines. Everything else —
// the WHERE tree, join collection, SET assembly — is shared, written once as methods on
// Dialect, so no engine gets its own copy to drift out of step.
type Spec interface {
	// Name identifies the engine in errors and logs.
	Name() string

	// QuoteIdent quotes a table, column or index name.
	QuoteIdent(name string) string

	// Placeholder renders the nth bound parameter, counting from 1.
	Placeholder(n int) string

	// TypeName renders a column's physical type from its logical one.
	TypeName(field *models.Field) string

	// AutoIncrementClause is appended to an auto-incrementing primary key.
	AutoIncrementClause() string

	// InsertIgnore renders an insert that skips rows that already exist, used for
	// many2many join rows.
	InsertIgnore(table string, columns []string, marks []string) string

	// InsertSelect renders INSERT … SELECT, optionally skipping rows that conflict with an
	// existing one. It is separate from InsertIgnore because the row source is a query
	// rather than a VALUES list, and because the engines put "ignore" in different places:
	// SQLite and MySQL modify the INSERT verb, Postgres appends a conflict clause.
	InsertSelect(table string, columns []string, selectSQL string, ignore bool) string

	// SupportsReturning reports whether INSERT … RETURNING is available. Where it is not,
	// goql falls back to LastInsertId.
	SupportsReturning() bool

	// SupportsUpdateFrom reports whether UPDATE … FROM is available. MySQL instead
	// requires UPDATE … JOIN … SET.
	SupportsUpdateFrom() bool

	// OpenEndedLimit renders a limit that excludes nothing, needed because an OFFSET
	// requires a LIMIT before it.
	OpenEndedLimit() string

	// EnableForeignKeysSQL returns the statement that turns on foreign key enforcement,
	// or "" where it is always on.
	EnableForeignKeysSQL() string

	// --- Schema evolution ---

	// AlterAddColumn adds a column, given a rendered column definition.
	AlterAddColumn(table, columnDef string) string

	// AlterRenameColumn renames a column, preserving its data.
	AlterRenameColumn(table, from, to string) string

	// AlterDropColumn removes a column and its data.
	AlterDropColumn(table, column string) string

	// AlterColumnType changes a column's type, or "" where the engine cannot.
	AlterColumnType(table, column, columnDef string) string

	// NormalizeType reduces a type to a canonical form for this engine, so a type the
	// model declares and the same type as introspection reports it compare equal.
	NormalizeType(declared string) string

	// SupportsTransactionalDDL reports whether DDL can be rolled back. MySQL commits each
	// statement as it runs, so a failed migration there cannot be undone.
	SupportsTransactionalDDL() bool

	// --- Introspection ---

	// IntrospectTablesSQL lists the tables in the current schema.
	IntrospectTablesSQL() string

	// IntrospectColumnsSQL lists one table's columns as
	// (name, type, notnull, default), bound to the table name.
	IntrospectColumnsSQL() string
}

// Dialect renders SQL for one engine: shared builders plus that engine's Spec.
type Dialect struct {
	Spec
}

// NewDialect wraps a Spec so the shared builders can be used.
func NewDialect(spec Spec) *Dialect {
	return &Dialect{Spec: spec}
}

// stmt is the per-statement emission state: table aliases, and the placeholder sequence,
// which matters because Postgres numbers its parameters.
//
// Placeholders are handed out in emission order, so every builder must append bound values
// in the same order it writes their markers.
type stmt struct {
	d     *Dialect
	alias *AliasMap
	n     int
}

func (d *Dialect) newStmt() *stmt {
	return &stmt{d: d, alias: NewAliasMap(d)}
}

// Statement is the exported handle on a statement's placeholder sequence, for callers
// outside this package that assemble SQL of their own.
type Statement struct{ s *stmt }

// NewStatement starts a placeholder sequence.
func (d *Dialect) NewStatement() *Statement { return &Statement{s: d.newStmt()} }

// Mark returns the next placeholder.
func (st *Statement) Mark() string { return st.s.mark() }

// Marks returns count placeholders, comma separated.
func (st *Statement) Marks(count int) string { return st.s.marks(count) }

// mark returns the next placeholder.
func (s *stmt) mark() string {
	s.n++
	return s.d.Placeholder(s.n)
}

// marks returns count placeholders, comma separated.
func (s *stmt) marks(count int) string {
	out := make([]string, count)
	for i := range out {
		out[i] = s.mark()
	}
	return strings.Join(out, ", ")
}

// column renders a field reference qualified for this statement.
func (s *stmt) column(ref *FieldRef) string {
	return fmt.Sprintf("%s.%s", s.alias.Alias(ref.TableName()), s.d.columnName(ref))
}

// columnName resolves the quoted column a field reference points at.
func (d *Dialect) columnName(ref *FieldRef) string {
	if ref.Nested != nil {
		return d.QuoteIdent(ref.Nested.Field.ColumnName())
	}
	if ref.Field.RelationKind() == models.M2O {
		return d.QuoteIdent(ref.Field.GetFKColumn())
	}
	return d.QuoteIdent(ref.Field.ColumnName())
}

// table renders a quoted table name.
func (d *Dialect) table(schema *models.Model) string {
	return d.QuoteIdent(schema.TableName)
}

// primaryKey renders a schema's quoted primary key column.
func (d *Dialect) primaryKey(schema *models.Model) string {
	if schema.PrimaryKey == nil {
		return ""
	}
	return d.QuoteIdent(schema.PrimaryKey.ColumnName())
}
