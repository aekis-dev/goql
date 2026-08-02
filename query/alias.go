package query

import (
	"fmt"
	"strings"

	"github.com/aekis-dev/goql/models"
)

// AliasMap assigns a short SQL alias to each table participating in one query.
//
// Aliases are decided at SQL-build time, not at parse time: the parsed condition tree
// stays alias-agnostic because aliasing is purely an emission concern. All builders for a
// single statement share one AliasMap so the SELECT list, JOIN clauses and WHERE clause
// agree on every alias.
//
// Statements where an alias is not portable (UPDATE, the outer DELETE) pin their tables to
// the full table name with PinTableName, so the same condition tree renders correctly there
// too.
type AliasMap struct {
	d       *Dialect
	byTable map[string]string
	used    map[string]bool
}

func NewAliasMap(d *Dialect) *AliasMap {
	return &AliasMap{
		d:       d,
		byTable: make(map[string]string),
		used:    make(map[string]bool),
	}
}

// Alias returns the alias for a table, assigning one on first use. The table's first
// letter is preferred, with a numeric suffix to break collisions (customers → c,
// carts → c2), which also keeps future self-joins unambiguous.
func (am *AliasMap) Alias(table string) string {
	return am.AliasFor("", table)
}

// AliasFor returns the alias for a table reached by a relation path, assigning one on first
// use. The path is what identifies the occurrence: two paths reaching the same table are two
// different rows and must not share an alias. An empty path is the table itself — the query's
// own, a participant, or a CTE.
func (am *AliasMap) AliasFor(path, table string) string {
	key := table
	if path != "" {
		key = table + "\x00" + path
	}

	if existing, ok := am.byTable[key]; ok {
		return existing
	}

	base := "t"
	for _, r := range strings.ToLower(table) {
		if r >= 'a' && r <= 'z' {
			base = string(r)
			break
		}
	}

	candidate := base
	for n := 2; am.used[candidate]; n++ {
		candidate = fmt.Sprintf("%s%d", base, n)
	}

	am.byTable[key] = candidate
	am.used[candidate] = true
	return candidate
}

// Assigned reports whether a table already has an alias in this statement.
func (am *AliasMap) Assigned(table string) bool {
	_, ok := am.byTable[table]
	return ok
}

// PinTableName makes a table render as its own quoted name rather than a short alias, for
// statements that cannot portably alias the table they modify.
func (am *AliasMap) PinTableName(table string) {
	am.byTable[table] = am.d.QuoteIdent(table)
}

// From renders a table reference for a FROM or JOIN clause: `"customers" c`, or just
// `"customers"` when the table has been pinned.
func (am *AliasMap) From(table string) string {
	return am.FromFor("", table)
}

// FromFor renders a table reference for a table reached by a relation path.
func (am *AliasMap) FromFor(path, table string) string {
	alias := am.AliasFor(path, table)
	quoted := am.d.QuoteIdent(table)
	if alias == quoted {
		return quoted
	}
	return fmt.Sprintf("%s %s", quoted, alias)
}

// fromList renders the FROM clause: the primary table, plus any additional models the
// lambda declared and referenced. Extra participants are comma-joined, which every
// supported engine reads as an inner join once the WHERE clause relates them — and the
// relating comparison is exactly what the caller wrote.
func (s *stmt) fromList(body *ParseBody, schema *models.Model) string {
	tables := []string{s.alias.From(schema.TableName)}
	seen := map[string]bool{schema.TableName: true}
	for _, table := range body.Joined {
		// A participant naming the primary table is already in the list; listing it twice
		// would be a cross join against itself.
		if seen[table] {
			continue
		}
		// An explicit join brings its table in with its own ON condition, so naming it here
		// too would cross-join it before that condition applied.
		if body.Options.JoinsTable(table) {
			continue
		}
		seen[table] = true
		tables = append(tables, s.alias.From(table))
	}
	return strings.Join(tables, ", ")
}
