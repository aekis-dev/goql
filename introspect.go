package goql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// LiveColumn is a column as the database actually has it.
type LiveColumn struct {
	Name    string
	Type    string
	NotNull bool
	Default string
}

// LiveTable is a table as the database actually has it.
type LiveTable struct {
	Name    string
	Columns map[string]*LiveColumn
}

// LiveSchema is the database's actual shape, read from the engine rather than assumed.
//
// It is the sole basis for a migration diff: there is no stored snapshot of what goql last
// believed, so the comparison is always against reality — at the cost of needing a
// reachable database to plan a migration.
type LiveSchema struct {
	Tables map[string]*LiveTable
}

// Table returns a table by name, or nil when the database does not have it.
func (s *LiveSchema) Table(name string) *LiveTable {
	return s.Tables[name]
}

// Introspect reads the live schema for the tables goql is asked about.
//
// Only the named tables are read. Tables the models do not declare are never inspected and
// never proposed for removal: a database usually holds tables goql knows nothing about, and
// offering to drop them would be reckless.
func (ctx *Engine) Introspect(c context.Context, tables []string) (*LiveSchema, error) {
	scoped := ctx.withCall(c, nil)

	existing, err := scoped.tableNames()
	if err != nil {
		return nil, err
	}

	schema := &LiveSchema{Tables: make(map[string]*LiveTable)}
	for _, name := range tables {
		if !existing[strings.ToLower(name)] {
			continue // not created yet
		}
		table, err := scoped.tableColumns(name)
		if err != nil {
			return nil, fmt.Errorf("introspect %s: %w", name, err)
		}
		schema.Tables[name] = table
	}
	return schema, nil
}

// tableNames lists the tables present, lowercased so comparison is case-insensitive —
// engines differ on identifier case folding.
func (ctx *Engine) tableNames() (map[string]bool, error) {
	rows, err := ctx.query(ctx.dialect.IntrospectTablesSQL())
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()

	names := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names[strings.ToLower(name)] = true
	}
	return names, rows.Err()
}

func (ctx *Engine) tableColumns(table string) (*LiveTable, error) {
	rows, err := ctx.query(ctx.dialect.IntrospectColumnsSQL(), table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := &LiveTable{Name: table, Columns: make(map[string]*LiveColumn)}
	for rows.Next() {
		var (
			name       string
			columnType sql.NullString
			notNull    bool
			def        sql.NullString
		)
		if err := rows.Scan(&name, &columnType, &notNull, &def); err != nil {
			return nil, err
		}
		out.Columns[name] = &LiveColumn{
			Name:    name,
			Type:    columnType.String,
			NotNull: notNull,
			Default: def.String,
		}
	}
	return out, rows.Err()
}
