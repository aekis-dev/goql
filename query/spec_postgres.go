package query

import (
	"fmt"
	"strings"

	"github.com/aekis-dev/goql/models"
)

// Postgres renders SQL for PostgreSQL.
type Postgres struct{}

func (Postgres) Name() string { return "postgres" }

func (Postgres) QuoteIdent(name string) string { return quoteDouble(name) }

// Postgres numbers its parameters, so the marker depends on position.
func (Postgres) Placeholder(n int) string { return fmt.Sprintf("$%d", n) }

func (Postgres) TypeName(field *models.Field) string { return postgresType(field) }

// An identity column carries no extra clause; the type itself is changed instead.
func (Postgres) AutoIncrementClause() string { return "" }

func (Postgres) InsertIgnore(table string, columns, marks []string) string {
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT DO NOTHING",
		table, strings.Join(columns, ", "), strings.Join(marks, ", "))
}

// pq and pgx do not implement LastInsertId, so generated keys must be returned.
// Postgres puts the conflict rule at the end rather than in the INSERT verb.
func (Postgres) InsertSelect(table string, columns []string, selectSQL string, ignore bool) string {
	sql := fmt.Sprintf("INSERT INTO %s (%s) %s", table, strings.Join(columns, ", "), selectSQL)
	if ignore {
		sql += " ON CONFLICT DO NOTHING"
	}
	return sql
}

func (Postgres) SupportsReturning() bool { return true }

func (Postgres) SupportsUpdateFrom() bool { return true }

func (Postgres) OpenEndedLimit() string { return "LIMIT ALL" }

// Foreign keys are always enforced.
func (Postgres) EnableForeignKeysSQL() string { return "" }

func postgresType(field *models.Field) string {
	// LogicalType resolves an undeclared type from the Go type, and a foreign key to an
	// integer — switching on field.Type directly left relation columns as TEXT.
	switch field.LogicalType() {
	case models.TypeInteger:
		return "integer"
	case models.TypeBigInt:
		return "bigint"
	case models.TypeBoolean:
		return "boolean"
	case models.TypeReal:
		return "real"
	case models.TypeDouble:
		return "double precision"
	case models.TypeDecimal:
		if field.Precision > 0 {
			return fmt.Sprintf("numeric(%d, %d)", field.Precision, field.Scale)
		}
		return "numeric"
	case models.TypeText:
		return "text"
	case models.TypeVarchar:
		if field.Size > 0 {
			return fmt.Sprintf("varchar(%d)", field.Size)
		}
		return "text"
	case models.TypeTimestamp:
		if field.Precision > 0 {
			return fmt.Sprintf("timestamp(%d)", field.Precision)
		}
		return "timestamp"
	case models.TypeBytes:
		return "bytea"
	case models.TypeJSON:
		return "jsonb"
	}
	return rawTypePassthrough(field)
}

// --- Schema evolution ---

func (Postgres) AlterAddColumn(table, columnDef string) string {
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, columnDef)
}

func (Postgres) AlterRenameColumn(table, from, to string) string {
	return fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", table, from, to)
}

func (Postgres) AlterDropColumn(table, column string) string {
	return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", table, column)
}

func (Postgres) AlterColumnType(table, column, columnDef string) string {
	// columnDef carries the type only; USING lets Postgres cast existing values.
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s USING %s::%s",
		table, column, columnDef, column, columnDef)
}

func (Postgres) SupportsTransactionalDDL() bool { return true }

// --- Introspection ---

func (Postgres) IntrospectTablesSQL() string {
	return `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema()`
}

// format_type renders Postgres's own canonical spelling, including a precision in the place
// Postgres puts it (`timestamp(6) without time zone`). Reassembling that from
// information_schema parts is not possible, and guessing produced a phantom change on every
// timestamp column.
//
// to_regclass yields NULL rather than an error for a table that does not exist.
func (Postgres) IntrospectColumnsSQL() string {
	return `SELECT a.attname,
	               format_type(a.atttypid, a.atttypmod),
	               a.attnotnull,
	               COALESCE(pg_get_expr(d.adbin, d.adrelid), '')
	          FROM pg_attribute a
	          LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
	         WHERE a.attrelid = to_regclass($1) AND a.attnum > 0 AND NOT a.attisdropped`
}

// postgresTypeAliases maps the names information_schema reports onto the ones goql emits.
var postgresTypeAliases = map[string]string{
	"character varying":           "varchar",
	"character":                   "char",
	"timestamp without time zone": "timestamp",
	"timestamp with time zone":    "timestamptz",
	"time without time zone":      "time",
	"time with time zone":         "timetz",
	"double precision":            "double",
	"boolean":                     "bool",
	"decimal":                     "numeric",
	"int2":                        "smallint",
	"int4":                        "integer",
	"int8":                        "bigint",
	"float4":                      "real",
	"float8":                      "double",
	"bpchar":                      "char",
}

func (Postgres) NormalizeType(declared string) string {
	return normalizeWithAliases(declared, postgresTypeAliases)
}

// Postgres has every join kind.
func (Postgres) SupportsJoinType(string) bool { return true }

func (Postgres) Concat(left, right string) string { return fmt.Sprintf("(%s || %s)", left, right) }

func (Postgres) SupportsCTE() bool { return true }
