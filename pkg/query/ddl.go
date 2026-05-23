package query

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/aekis/goql/pkg/models"
)

// CreateTable builds a CREATE TABLE IF NOT EXISTS statement
func CreateTable(schema *models.Model) (string, error) {
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
			parts = append(parts, buildFKColumnDef(field))
		default:
			parts = append(parts, buildColumnDef(field))
			if field.PrimaryKey {
				primaryKeys = append(primaryKeys, field.GetColumnName())
			}
		}
	}

	if len(primaryKeys) > 1 {
		parts = append(parts, fmt.Sprintf("PRIMARY KEY (%s)", strings.Join(primaryKeys, ", ")))
	}

	return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n  %s\n)",
		schema.TableName,
		strings.Join(parts, ",\n  ")), nil
}

func buildColumnDef(field *models.Field) string {
	parts := []string{field.GetColumnName(), field.GetDBType()}
	if field.PrimaryKey {
		parts = append(parts, "PRIMARY KEY")
		if field.AutoIncrement {
			parts = append(parts, "AUTOINCREMENT")
		}
	} else {
		if field.NotNull {
			parts = append(parts, "NOT NULL")
		}
		if field.Unique {
			parts = append(parts, "UNIQUE")
		}
	}
	if field.GetDefault() != "" {
		parts = append(parts, fmt.Sprintf("DEFAULT %s", field.GetDefault()))
	}
	for _, check := range field.Checks {
		parts = append(parts, fmt.Sprintf("CHECK (%s)", check))
	}
	if field.Collation != "" {
		parts = append(parts, fmt.Sprintf("COLLATE %s", field.Collation))
	}
	return strings.Join(parts, " ")
}

func buildFKColumnDef(field *models.Field) string {
	parts := []string{field.GetFKColumn(), field.GetDBType()}
	if field.NotNull {
		parts = append(parts, "NOT NULL")
	}
	return strings.Join(parts, " ")
}

// BuildCreateIndexes returns all CREATE INDEX statements for a schema
func BuildCreateIndexes(schema *models.Model) []string {
	var sqls []string
	for _, idx := range schema.Indexes {
		unique := ""
		if idx.Unique {
			unique = "UNIQUE "
		}
		sqls = append(sqls, fmt.Sprintf(
			"CREATE %sINDEX IF NOT EXISTS %s ON %s (%s)",
			unique, idx.Name, schema.TableName,
			strings.Join(idx.Fields, ", ")))
	}
	return sqls
}

// CreateJoinTable builds a CREATE TABLE for a many2many join table
func CreateJoinTable(field *models.Field, sourceSchema *models.Model) (string, error) {
	if field.ManyToMany == nil {
		return "", fmt.Errorf("field %s is not a many2many field", field.Name)
	}

	m := field.ManyToMany
	targetType := field.TargetModel()
	tempTarget := reflect.New(targetType).Interface()
	targetEntity, ok := tempTarget.(models.Entity)
	if !ok {
		return "", fmt.Errorf("many2many target %v does not implement Entity", targetType)
	}
	targetSchema, err := models.GetModel(targetEntity)
	if err != nil {
		return "", fmt.Errorf("failed to get schema for join table target %v: %w", targetType, err)
	}

	return fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (
  %s INTEGER NOT NULL,
  %s INTEGER NOT NULL,
  PRIMARY KEY (%s, %s),
  FOREIGN KEY (%s) REFERENCES %s(%s) ON DELETE CASCADE ON UPDATE CASCADE,
  FOREIGN KEY (%s) REFERENCES %s(%s) ON DELETE CASCADE ON UPDATE CASCADE
)`,
		m.Table,
		m.Column, m.Ref,
		m.Column, m.Ref,
		m.Column, sourceSchema.TableName, sourceSchema.PrimaryKey.GetColumnName(),
		m.Ref, targetSchema.TableName, targetSchema.PrimaryKey.GetColumnName(),
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
