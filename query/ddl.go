package query

import (
	"fmt"
	"sort"
	"strings"

	"github.com/aekis-dev/goql/models"
)

// CreateTable builds a CREATE TABLE IF NOT EXISTS statement
func (d *Dialect) CreateTable(schema *models.Model) (string, error) {
	if schema.PrimaryKey == nil {
		return "", fmt.Errorf("schema %s has no primary key", schema.TableName)
	}

	var parts []string
	var primaryKeys []string

	for _, fieldName := range sortedFieldNames(schema) {
		field := schema.Fields[fieldName]
		switch field.RelationKind() {
		case models.O2M, models.M2M:
			continue
		case models.M2O:
			parts = append(parts, d.fkColumnDef(field))
		default:
			parts = append(parts, d.columnDef(field))
			if field.PrimaryKey {
				primaryKeys = append(primaryKeys, d.QuoteIdent(field.ColumnName()))
			}
		}
	}

	if len(primaryKeys) > 1 {
		parts = append(parts, fmt.Sprintf("PRIMARY KEY (%s)", strings.Join(primaryKeys, ", ")))
	}

	return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n  %s\n)",
		d.table(schema), strings.Join(parts, ",\n  ")), nil
}

// ColumnDefinition renders a column definition, for ALTER statements built elsewhere.
func (d *Dialect) ColumnDefinition(field *models.Field) string {
	return d.columnDef(field)
}

func (d *Dialect) columnDef(field *models.Field) string {
	parts := []string{d.QuoteIdent(field.ColumnName()), d.TypeName(field)}

	if field.PrimaryKey {
		parts = append(parts, "PRIMARY KEY")
		if field.AutoIncrement {
			if clause := d.AutoIncrementClause(); clause != "" {
				parts = append(parts, clause)
			}
		}
	} else {
		if field.NotNull {
			parts = append(parts, "NOT NULL")
		}
		if field.Unique {
			parts = append(parts, "UNIQUE")
		}
	}

	if def := d.defaultClause(field); def != "" {
		parts = append(parts, def)
	}
	for _, check := range field.Checks {
		parts = append(parts, fmt.Sprintf("CHECK (%s)", check))
	}
	if field.Collation != "" {
		parts = append(parts, "COLLATE "+field.Collation)
	}

	return strings.Join(parts, " ")
}

// defaultClause renders a column default.
//
// A string default is a literal and must be quoted — emitting it bare produced
// `DEFAULT Active`, which SQLite tolerates but Postgres reads as a column reference. Known
// SQL expressions are passed through unquoted, since `DEFAULT 'CURRENT_TIMESTAMP'` would
// store the words rather than the time.
func (d *Dialect) defaultClause(field *models.Field) string {
	if field.Default == nil {
		return ""
	}

	switch v := field.Default.(type) {
	case string:
		if v == "" {
			return ""
		}
		if isSQLExpression(v) {
			return "DEFAULT " + v
		}
		return "DEFAULT " + quoteLiteral(v)
	case bool:
		if v {
			return "DEFAULT TRUE"
		}
		return "DEFAULT FALSE"
	default:
		return fmt.Sprintf("DEFAULT %v", v)
	}
}

// sqlExpressionDefaults are the expression defaults goql recognises. Anything else that is
// a string is treated as a literal.
var sqlExpressionDefaults = map[string]bool{
	"CURRENT_TIMESTAMP": true,
	"CURRENT_DATE":      true,
	"CURRENT_TIME":      true,
	"NOW()":             true,
	"NULL":              true,
}

func isSQLExpression(value string) bool {
	return sqlExpressionDefaults[strings.ToUpper(strings.TrimSpace(value))]
}

// quoteLiteral quotes a string literal, doubling any embedded quote.
func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func (d *Dialect) fkColumnDef(field *models.Field) string {
	parts := []string{d.QuoteIdent(field.GetFKColumn()), d.TypeName(field)}
	if field.NotNull {
		parts = append(parts, "NOT NULL")
	}
	return strings.Join(parts, " ")
}

// BuildCreateIndexes returns all CREATE INDEX statements for a schema
func (d *Dialect) BuildCreateIndexes(schema *models.Model) []string {
	var sqls []string
	for _, idx := range schema.Indexes {
		unique := ""
		if idx.Unique {
			unique = "UNIQUE "
		}
		columns := make([]string, len(idx.Fields))
		for i, column := range idx.Fields {
			columns[i] = d.QuoteIdent(column)
		}
		sqls = append(sqls, fmt.Sprintf("CREATE %sINDEX IF NOT EXISTS %s ON %s (%s)",
			unique, d.QuoteIdent(idx.Name), d.table(schema), strings.Join(columns, ", ")))
	}
	return sqls
}

// CreateJoinTable builds a CREATE TABLE for a many2many join table
func (d *Dialect) CreateJoinTable(field *models.Field, sourceSchema *models.Model) (string, error) {
	if field.ManyToMany == nil {
		return "", fmt.Errorf("field %s is not a many2many field", field.Name)
	}

	m := field.ManyToMany
	targetSchema, err := RelationTargetSchema(field)
	if err != nil {
		return "", err
	}

	joinTable := d.QuoteIdent(m.Table)
	sourceCol := d.QuoteIdent(m.Column)
	targetCol := d.QuoteIdent(m.Ref)
	keyType := d.TypeName(&models.Field{Type: models.TypeBigInt})

	return fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (
  %s %s NOT NULL,
  %s %s NOT NULL,
  PRIMARY KEY (%s, %s),
  FOREIGN KEY (%s) REFERENCES %s(%s) ON DELETE CASCADE ON UPDATE CASCADE,
  FOREIGN KEY (%s) REFERENCES %s(%s) ON DELETE CASCADE ON UPDATE CASCADE
)`,
		joinTable,
		sourceCol, keyType,
		targetCol, keyType,
		sourceCol, targetCol,
		sourceCol, d.table(sourceSchema), d.primaryKey(sourceSchema),
		targetCol, d.table(targetSchema), d.primaryKey(targetSchema),
	), nil
}

func sortedFieldNames(schema *models.Model) []string {
	names := make([]string, 0, len(schema.Fields))
	for name := range schema.Fields {
		names = append(names, name)
	}
	// stable order: PK first, then alphabetical
	sort.Slice(names, func(i, j int) bool {
		fi, fj := schema.Fields[names[i]], schema.Fields[names[j]]
		if fi.PrimaryKey != fj.PrimaryKey {
			return fi.PrimaryKey
		}
		return names[i] < names[j]
	})
	return names
}
