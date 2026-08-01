package query

import (
	"fmt"

	"github.com/aekis-dev/goql/models"
)

// SelectWhereIn builds `SELECT * FROM table WHERE col IN (?, …)`, the batched form used to
// load a relation for many parent rows at once instead of one query per row.
func (d *Dialect) SelectWhereIn(schema *models.Model, column string, count int) *Query {
	s := d.newStmt()
	return &Query{
		SQL: fmt.Sprintf("SELECT * FROM %s WHERE %s IN (%s)",
			d.table(schema), column, s.marks(count)),
	}
}

// JoinRowsIn builds `SELECT col, ref FROM join_table WHERE col IN (?, …)`, which maps
// parent keys to target keys for a many2many relation.
func (d *Dialect) JoinRowsIn(m *models.ManyToMany, count int) *Query {
	s := d.newStmt()
	return &Query{
		SQL: fmt.Sprintf("SELECT %s, %s FROM %s WHERE %s IN (%s)",
			d.QuoteIdent(m.Column), d.QuoteIdent(m.Ref),
			d.QuoteIdent(m.Table), d.QuoteIdent(m.Column), s.marks(count)),
	}
}

// O2MDisassociate builds `UPDATE target SET fk = NULL WHERE fk = ?`, optionally excluding
// the rows that are being kept, so a one2many field that no longer lists a row clears its
// foreign key instead of leaving a stale link.
func (d *Dialect) O2MDisassociate(targetSchema *models.Model, fkColumn string, retained int) *Query {
	s := d.newStmt()
	sql := fmt.Sprintf("UPDATE %s SET %s = NULL WHERE %s = %s",
		d.table(targetSchema), d.QuoteIdent(fkColumn), d.QuoteIdent(fkColumn), s.mark())

	if retained > 0 {
		sql += fmt.Sprintf(" AND %s NOT IN (%s)", d.primaryKey(targetSchema), s.marks(retained))
	}

	return &Query{SQL: sql}
}

// O2MStale builds a query returning at most one row that still points at the parent but is
// not among the retained ones. It detects a disassociation that a NOT NULL foreign key
// cannot express, so the caller can report it instead of failing inside the driver.
func (d *Dialect) O2MStale(targetSchema *models.Model, fkColumn string, retained int) *Query {
	s := d.newStmt()
	sql := fmt.Sprintf("SELECT 1 FROM %s WHERE %s = %s",
		d.table(targetSchema), d.QuoteIdent(fkColumn), s.mark())

	if retained > 0 {
		sql += fmt.Sprintf(" AND %s NOT IN (%s)", d.primaryKey(targetSchema), s.marks(retained))
	}

	return &Query{SQL: sql + " LIMIT 1"}
}

// SelectKeyPairsIn builds `SELECT pk, column FROM table WHERE filter IN (?, …)`.
//
// Relation fields are left nil by the scanner — a foreign key is a key, not a row — so
// preloading reads the keys straight from the table instead of from scanned entities.
func (d *Dialect) SelectKeyPairsIn(schema *models.Model, column, filterColumn string, count int) *Query {
	s := d.newStmt()
	return &Query{
		SQL: fmt.Sprintf("SELECT %s, %s FROM %s WHERE %s IN (%s)",
			d.primaryKey(schema), column, d.table(schema), filterColumn, s.marks(count)),
	}
}
