package query

import (
	"fmt"
	"strings"

	"github.com/aekis-dev/goql/models"
)

// SQLite renders SQL for SQLite.
type SQLite struct{}

func (SQLite) Name() string { return "sqlite" }

func (SQLite) QuoteIdent(name string) string { return quoteDouble(name) }

func (SQLite) Placeholder(int) string { return "?" }

func (SQLite) TypeName(field *models.Field) string { return sqliteType(field) }

func (SQLite) AutoIncrementClause() string { return "AUTOINCREMENT" }

func (SQLite) InsertIgnore(table string, columns, marks []string) string {
	return fmt.Sprintf("INSERT OR IGNORE INTO %s (%s) VALUES (%s)",
		table, strings.Join(columns, ", "), strings.Join(marks, ", "))
}

func (SQLite) InsertSelect(table string, columns []string, selectSQL string, ignore bool) string {
	verb := "INSERT INTO"
	if ignore {
		verb = "INSERT OR IGNORE INTO"
	}
	return fmt.Sprintf("%s %s (%s) %s", verb, table, strings.Join(columns, ", "), selectSQL)
}

func (SQLite) SupportsReturning() bool { return false }

// UPDATE … FROM exists from SQLite 3.33.
func (SQLite) SupportsUpdateFrom() bool { return true }

func (SQLite) OpenEndedLimit() string { return "LIMIT -1" }

func (SQLite) EnableForeignKeysSQL() string { return "PRAGMA foreign_keys = ON" }

// sqliteType maps a logical type to SQLite's affinity-based vocabulary. SQLite accepts
// almost any type name, but using its own keeps the schema readable.
func sqliteType(field *models.Field) string {
	// LogicalType resolves an undeclared type from the Go type, and a foreign key to an
	// integer — switching on field.Type directly left relation columns as TEXT.
	switch field.LogicalType() {
	case models.TypeInteger, models.TypeBigInt, models.TypeBoolean:
		return "INTEGER"
	case models.TypeReal, models.TypeDouble:
		return "REAL"
	case models.TypeDecimal:
		if field.Precision > 0 {
			return fmt.Sprintf("NUMERIC(%d, %d)", field.Precision, field.Scale)
		}
		return "NUMERIC"
	case models.TypeText, models.TypeJSON:
		return "TEXT"
	case models.TypeVarchar:
		if field.Size > 0 {
			return fmt.Sprintf("VARCHAR(%d)", field.Size)
		}
		return "TEXT"
	case models.TypeTimestamp:
		return "TIMESTAMP"
	case models.TypeBytes:
		return "BLOB"
	}
	return rawTypePassthrough(field)
}

// rawTypePassthrough emits a type goql does not define verbatim, the escape hatch for
// engine-specific column types.
func rawTypePassthrough(field *models.Field) string {
	if field.Type != "" {
		return string(field.Type)
	}
	return "TEXT"
}

func quoteDouble(name string) string {
	// SQL escapes an embedded quote by doubling it, unlike Go's strconv.Quote which
	// backslash-escapes and would produce invalid SQL.
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// --- Schema evolution ---

func (SQLite) AlterAddColumn(table, columnDef string) string {
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, columnDef)
}

// RENAME COLUMN needs SQLite 3.25 or newer.
func (SQLite) AlterRenameColumn(table, from, to string) string {
	return fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", table, from, to)
}

// DROP COLUMN needs SQLite 3.35 or newer.
func (SQLite) AlterDropColumn(table, column string) string {
	return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", table, column)
}

// SQLite cannot change a column's type in place; the table has to be rebuilt.
func (SQLite) AlterColumnType(string, string, string) string { return "" }

func (SQLite) SupportsTransactionalDDL() bool { return true }

// --- Introspection ---

func (SQLite) IntrospectTablesSQL() string {
	return `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`
}

// pragma_table_info is the table-valued form, so the table name can be bound rather than
// interpolated as PRAGMA would require.
func (SQLite) IntrospectColumnsSQL() string {
	return `SELECT name, type, "notnull", dflt_value FROM pragma_table_info(?)`
}

// NormalizeType reduces a type to SQLite's column affinity, following the rules from the
// SQLite documentation in their documented order.
//
// Affinity is what SQLite actually enforces: it does not distinguish VARCHAR(100) from TEXT,
// and stores either identically. Comparing affinities means goql never proposes a type change
// that would have no effect.
func (SQLite) NormalizeType(declared string) string {
	text := strings.ToUpper(strings.TrimSpace(declared))

	switch {
	case text == "":
		return "blob"
	case strings.Contains(text, "INT"):
		return "integer"
	case strings.Contains(text, "CHAR"), strings.Contains(text, "CLOB"), strings.Contains(text, "TEXT"):
		return "text"
	case strings.Contains(text, "BLOB"):
		return "blob"
	case strings.Contains(text, "REAL"), strings.Contains(text, "FLOA"), strings.Contains(text, "DOUB"):
		return "real"
	default:
		return "numeric"
	}
}
