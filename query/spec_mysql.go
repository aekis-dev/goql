package query

import (
	"fmt"
	"strings"

	"github.com/aekis-dev/goql/models"
)

// MySQL renders SQL for MySQL and MariaDB.
type MySQL struct{}

func (MySQL) Name() string { return "mysql" }

// MySQL quotes identifiers with backticks unless ANSI_QUOTES is set, which cannot be
// assumed, so backticks are used.
func (MySQL) QuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func (MySQL) Placeholder(int) string { return "?" }

func (MySQL) TypeName(field *models.Field) string { return mysqlType(field) }

func (MySQL) AutoIncrementClause() string { return "AUTO_INCREMENT" }

func (MySQL) InsertIgnore(table string, columns, marks []string) string {
	return fmt.Sprintf("INSERT IGNORE INTO %s (%s) VALUES (%s)",
		table, strings.Join(columns, ", "), strings.Join(marks, ", "))
}

func (MySQL) InsertSelect(table string, columns []string, selectSQL string, ignore bool) string {
	verb := "INSERT INTO"
	if ignore {
		verb = "INSERT IGNORE INTO"
	}
	return fmt.Sprintf("%s %s (%s) %s", verb, table, strings.Join(columns, ", "), selectSQL)
}

func (MySQL) SupportsReturning() bool { return false }

// MySQL has no UPDATE … FROM; a joined update is written UPDATE … JOIN … SET.
func (MySQL) SupportsUpdateFrom() bool { return false }

// MySQL needs a concrete limit before an offset; this is the documented idiom.
func (MySQL) OpenEndedLimit() string { return "LIMIT 18446744073709551615" }

func (MySQL) EnableForeignKeysSQL() string { return "" }

func mysqlType(field *models.Field) string {
	// LogicalType resolves an undeclared type from the Go type, and a foreign key to an
	// integer — switching on field.Type directly left relation columns as TEXT.
	switch field.LogicalType() {
	case models.TypeInteger:
		return "INT"
	case models.TypeBigInt:
		return "BIGINT"
	case models.TypeBoolean:
		return "BOOLEAN"
	case models.TypeReal:
		return "FLOAT"
	case models.TypeDouble:
		return "DOUBLE"
	case models.TypeDecimal:
		if field.Precision > 0 {
			return fmt.Sprintf("DECIMAL(%d, %d)", field.Precision, field.Scale)
		}
		return "DECIMAL"
	case models.TypeText:
		return "TEXT"
	case models.TypeVarchar:
		if field.Size > 0 {
			return fmt.Sprintf("VARCHAR(%d)", field.Size)
		}
		// MySQL cannot index a TEXT column without a prefix length, so a sized VARCHAR
		// is the safer default for an unsized one.
		return "VARCHAR(255)"
	case models.TypeTimestamp:
		if field.Precision > 0 {
			return fmt.Sprintf("DATETIME(%d)", min(field.Precision, 6))
		}
		return "DATETIME"
	case models.TypeBytes:
		return "BLOB"
	case models.TypeJSON:
		return "JSON"
	}
	return rawTypePassthrough(field)
}

// --- Schema evolution ---

func (MySQL) AlterAddColumn(table, columnDef string) string {
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, columnDef)
}

// RENAME COLUMN needs MySQL 8.0 or newer; earlier versions used CHANGE.
func (MySQL) AlterRenameColumn(table, from, to string) string {
	return fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", table, from, to)
}

func (MySQL) AlterDropColumn(table, column string) string {
	return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", table, column)
}

func (MySQL) AlterColumnType(table, column, columnDef string) string {
	return fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s", table, column, columnDef)
}

// Each DDL statement commits as it runs, so a failed migration cannot be rolled back.
func (MySQL) SupportsTransactionalDDL() bool { return false }

// --- Introspection ---

func (MySQL) IntrospectTablesSQL() string {
	return `SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE()`
}

func (MySQL) IntrospectColumnsSQL() string {
	return `SELECT column_name, column_type, is_nullable = 'NO', column_default
	          FROM information_schema.columns
	         WHERE table_schema = DATABASE() AND table_name = ?`
}

// mysqlTypeAliases maps MySQL's own spellings onto a single canonical form. BOOLEAN is an
// alias for TINYINT(1) in MySQL, which is what introspection reports.
var mysqlTypeAliases = map[string]string{
	"boolean":          "tinyint(1)",
	"bool":             "tinyint(1)",
	"integer":          "int",
	"numeric":          "decimal",
	"dec":              "decimal",
	"double precision": "double",
	"real":             "double",
}

func (MySQL) NormalizeType(declared string) string {
	return normalizeWithAliases(declared, mysqlTypeAliases)
}
