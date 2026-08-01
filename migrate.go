package goql

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aekis-dev/goql/models"
)

// ErrUnresolvedQuestions means a migration was asked to apply while ambiguities remain
// unanswered. Nothing is applied in that case.
var ErrUnresolvedQuestions = errors.New("migration has unanswered questions")

// migrationsTable records what has been applied. There are no migration files: the models
// are the source of truth and this table is the audit log of how the database was brought
// into line with them.
const migrationsTable = "goql_migrations"

// ChangeKind identifies what a migration step does.
type ChangeKind string

const (
	CreateTable    ChangeKind = "create_table"
	CreateIndex    ChangeKind = "create_index"
	AddColumn      ChangeKind = "add_column"
	RenameColumn   ChangeKind = "rename_column"
	DropColumn     ChangeKind = "drop_column"
	ChangeType     ChangeKind = "change_type"
	CreateJoinable ChangeKind = "create_join_table"
)

// Change is one statement a migration will run.
type Change struct {
	Kind   ChangeKind
	Table  string
	Column string
	Detail string
	SQL    string

	// Destructive marks a change that can lose data. Nothing destructive is ever planned
	// without the user having chosen it.
	Destructive bool
}

// Question asks the user something the schema cannot answer on its own. A column that
// disappeared and one that appeared look identical whether it was a rename or a
// drop-and-add; only intent distinguishes them, so goql asks rather than guesses.
type Question struct {
	ID      string
	Table   string
	Prompt  string
	Options []QuestionOption
}

// QuestionOption is one answer to a Question.
type QuestionOption struct {
	Value  string
	Label  string
	Detail string
}

// Plan is what a migration would do, plus what it needs answered first.
type Plan struct {
	Dialect   string
	Changes   []Change
	Questions []Question
}

// Empty reports whether there is nothing to do.
func (p *Plan) Empty() bool { return len(p.Changes) == 0 && len(p.Questions) == 0 }

// Summary reports what an applied migration did.
type Summary struct {
	Applied []Change
	// Failed is the change that stopped the migration, if any.
	Failed *Change
	Err    string
	// Rolled reports whether the engine could undo the applied statements.
	Rolled bool
}

// MigrationPlan compares the registered models against the live database and returns the
// changes needed, along with any ambiguity that must be resolved first.
//
// decisions may be nil on a first pass; supplying it resolves questions into concrete
// changes, which is how the interactive flow converges.
func (ctx *Engine) MigrationPlan(c context.Context, entities []models.Entity, decisions map[string]string) (*Plan, error) {
	schemas, err := schemasOf(entities)
	if err != nil {
		return nil, err
	}

	tables := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		tables = append(tables, schema.TableName)
	}

	live, err := ctx.Introspect(c, tables)
	if err != nil {
		return nil, err
	}

	return ctx.PlanAgainst(live, entities, decisions)
}

// PlanAgainst diffs the models against a schema you supply, rather than reading one from the
// database. Introspection is the usual source; this is the seam for inspecting a plan without
// a live server — and for testing each dialect's diff without running that engine.
func (ctx *Engine) PlanAgainst(live *LiveSchema, entities []models.Entity, decisions map[string]string) (*Plan, error) {
	schemas, err := schemasOf(entities)
	if err != nil {
		return nil, err
	}

	plan := &Plan{Dialect: ctx.dialect.Name()}
	for _, schema := range schemas {
		if err := ctx.planTable(schema, live, decisions, plan); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// planTable diffs one model against the database.
func (ctx *Engine) planTable(schema *models.Model, live *LiveSchema, decisions map[string]string, plan *Plan) error {
	d := ctx.dialect
	table := d.QuoteIdent(schema.TableName)

	existing := live.Table(schema.TableName)
	if existing == nil {
		// The whole table is new, so there is nothing ambiguous to ask about.
		create, err := d.CreateTable(schema)
		if err != nil {
			return err
		}
		plan.Changes = append(plan.Changes, Change{
			Kind:   CreateTable,
			Table:  schema.TableName,
			Detail: fmt.Sprintf("create table %s", schema.TableName),
			SQL:    create,
		})
		for _, indexSQL := range d.BuildCreateIndexes(schema) {
			plan.Changes = append(plan.Changes, Change{
				Kind:   CreateIndex,
				Table:  schema.TableName,
				Detail: fmt.Sprintf("index on %s", schema.TableName),
				SQL:    indexSQL,
			})
		}
		for _, fieldName := range sortedNames(schema.Fields) {
			field := schema.Fields[fieldName]
			if field.RelationKind() != models.M2M {
				continue
			}
			joinSQL, err := d.CreateJoinTable(field, schema)
			if err != nil {
				return err
			}
			plan.Changes = append(plan.Changes, Change{
				Kind:   CreateJoinable,
				Table:  field.ManyToMany.Table,
				Detail: fmt.Sprintf("create join table %s", field.ManyToMany.Table),
				SQL:    joinSQL,
			})
		}
		return nil
	}

	wanted := modelColumns(schema)

	// Columns the model wants that the database lacks, and vice versa.
	var appeared, disappeared []string
	for _, name := range sortedNames(wanted) {
		if _, ok := existing.Columns[name]; !ok {
			appeared = append(appeared, name)
		}
	}
	for name := range existing.Columns {
		if _, ok := wanted[name]; !ok {
			disappeared = append(disappeared, name)
		}
	}
	sort.Strings(disappeared)

	// A disappeared column may be a rename of an appeared one — indistinguishable from a
	// drop-and-add without knowing intent, so ask.
	consumed := make(map[string]bool)
	for _, old := range disappeared {
		id := fmt.Sprintf("%s.%s", schema.TableName, old)
		answer, answered := decisions[id]

		if !answered {
			plan.Questions = append(plan.Questions, renameQuestion(schema.TableName, old, appeared))
			continue
		}

		switch {
		case answer == "skip":
			// Leave the column alone.

		case answer == "drop":
			plan.Changes = append(plan.Changes, Change{
				Kind:        DropColumn,
				Table:       schema.TableName,
				Column:      old,
				Detail:      fmt.Sprintf("drop %s.%s — its data is discarded", schema.TableName, old),
				SQL:         d.AlterDropColumn(table, d.QuoteIdent(old)),
				Destructive: true,
			})

		case strings.HasPrefix(answer, "rename:"):
			to := strings.TrimPrefix(answer, "rename:")
			field, ok := wanted[to]
			if !ok {
				return fmt.Errorf("%w: %s has no column %s to rename %s into",
					models.ErrFieldNotFound, schema.TableName, to, old)
			}
			consumed[to] = true
			plan.Changes = append(plan.Changes, Change{
				Kind:   RenameColumn,
				Table:  schema.TableName,
				Column: to,
				Detail: fmt.Sprintf("rename %s.%s to %s — data is preserved", schema.TableName, old, to),
				SQL:    d.AlterRenameColumn(table, d.QuoteIdent(old), d.QuoteIdent(to)),
			})
			_ = field

		default:
			return fmt.Errorf("unrecognised answer %q for %s", answer, id)
		}
	}

	// Columns on both sides may still differ in type. Whether that loses data depends on
	// the direction of the change, which goql cannot tell, so it always asks.
	for _, name := range sortedNames(wanted) {
		current, present := existing.Columns[name]
		if !present {
			continue
		}
		field := wanted[name]
		want := d.TypeName(field)
		if d.TypesEqual(want, current.Type) {
			continue
		}

		id := fmt.Sprintf("%s.%s:type", schema.TableName, name)
		answer, answered := decisions[id]
		alter := d.AlterColumnType(table, d.QuoteIdent(name), want)

		if !answered {
			plan.Questions = append(plan.Questions,
				typeQuestion(schema.TableName, name, current.Type, want, alter != ""))
			continue
		}

		switch answer {
		case "skip":
			// Leave the column as the database has it.
		case "change":
			if alter == "" {
				return fmt.Errorf(
					"%s cannot change a column's type in place: rebuild %s manually to make %s a %s",
					d.Name(), schema.TableName, name, want)
			}
			plan.Changes = append(plan.Changes, Change{
				Kind:   ChangeType,
				Table:  schema.TableName,
				Column: name,
				Detail: fmt.Sprintf("change %s.%s from %s to %s — narrowing a type truncates data",
					schema.TableName, name, current.Type, want),
				SQL: alter,
				// Whether this loses data depends on the direction, which is not knowable
				// here, so it is flagged for a human to weigh.
				Destructive: true,
			})
		default:
			return fmt.Errorf("unrecognised answer %q for %s", answer, id)
		}
	}

	// Whatever was not claimed by a rename is genuinely new, and adding a column is safe.
	for _, name := range appeared {
		if consumed[name] {
			continue
		}
		field := wanted[name]
		plan.Changes = append(plan.Changes, Change{
			Kind:   AddColumn,
			Table:  schema.TableName,
			Column: name,
			Detail: fmt.Sprintf("add %s.%s", schema.TableName, name),
			SQL:    d.AlterAddColumn(table, ctx.columnDefinition(field)),
		})
	}

	return nil
}

// renameQuestion builds the prompt for a disappeared column.
func renameQuestion(table, column string, candidates []string) Question {
	q := Question{
		ID:     fmt.Sprintf("%s.%s", table, column),
		Table:  table,
		Prompt: fmt.Sprintf("Column %s.%s is no longer in the model.", table, column),
	}
	for _, candidate := range candidates {
		q.Options = append(q.Options, QuestionOption{
			Value:  "rename:" + candidate,
			Label:  fmt.Sprintf("renamed to %s", candidate),
			Detail: "keeps the existing data",
		})
	}
	q.Options = append(q.Options,
		QuestionOption{Value: "drop", Label: "drop the column", Detail: "discards its data"},
		QuestionOption{Value: "skip", Label: "leave it alone", Detail: "the column stays, unused"},
	)
	return q
}

// typeQuestion builds the prompt for a column whose type no longer matches the model.
func typeQuestion(table, column, have, want string, alterable bool) Question {
	q := Question{
		ID:     fmt.Sprintf("%s.%s:type", table, column),
		Table:  table,
		Prompt: fmt.Sprintf("Column %s.%s is %s in the database but %s in the model.", table, column, have, want),
	}

	if alterable {
		q.Options = append(q.Options, QuestionOption{
			Value:  "change",
			Label:  fmt.Sprintf("change it to %s", want),
			Detail: "narrowing a type truncates existing values",
		})
	} else {
		q.Options = append(q.Options, QuestionOption{
			Value:  "skip",
			Label:  "leave it as it is",
			Detail: "this engine cannot change a column type in place; rebuild the table to do it",
		})
		return q
	}

	q.Options = append(q.Options, QuestionOption{
		Value:  "skip",
		Label:  "leave it as it is",
		Detail: "the column keeps its current type",
	})
	return q
}

// Migrate applies a migration, resolving ambiguity with the given decisions.
//
// It re-plans from the live database rather than trusting a plan supplied by a caller, so a
// schema that moved since the plan was shown cannot be migrated against stale assumptions.
func (ctx *Engine) Migrate(c context.Context, entities []models.Entity, decisions map[string]string) (*Summary, error) {
	plan, err := ctx.MigrationPlan(c, entities, decisions)
	if err != nil {
		return nil, err
	}
	if len(plan.Questions) > 0 {
		return nil, fmt.Errorf("%w: %d unresolved", ErrUnresolvedQuestions, len(plan.Questions))
	}
	if len(plan.Changes) == 0 {
		return &Summary{}, nil
	}

	scoped := ctx.withCall(c, nil)
	if err := scoped.ensureMigrationsTable(); err != nil {
		return nil, err
	}

	summary := &Summary{}

	apply := func(e *Engine) error {
		for i := range plan.Changes {
			change := plan.Changes[i]
			if _, err := e.exec(change.SQL); err != nil {
				summary.Failed = &change
				summary.Err = err.Error()
				return fmt.Errorf("%s: %w", change.Detail, err)
			}
			if err := e.recordMigration(change); err != nil {
				return err
			}
			summary.Applied = append(summary.Applied, change)
		}
		return nil
	}

	// Where DDL is transactional a failure leaves nothing behind. On MySQL each statement
	// commits as it runs, so the summary reports how far it got instead.
	if ctx.dialect.SupportsTransactionalDDL() {
		if err := scoped.Transaction(apply); err != nil {
			summary.Rolled = true
			summary.Applied = nil
			return summary, err
		}
		return summary, nil
	}

	if err := apply(scoped); err != nil {
		return summary, err
	}
	return summary, nil
}

func (ctx *Engine) ensureMigrationsTable() error {
	d := ctx.dialect
	sql := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (%s %s, %s %s, %s %s, %s %s, %s %s)",
		d.QuoteIdent(migrationsTable),
		d.QuoteIdent("applied_at"), d.TypeName(&models.Field{Type: models.TypeTimestamp}),
		d.QuoteIdent("kind"), d.TypeName(&models.Field{Type: models.TypeText}),
		d.QuoteIdent("table_name"), d.TypeName(&models.Field{Type: models.TypeText}),
		d.QuoteIdent("detail"), d.TypeName(&models.Field{Type: models.TypeText}),
		d.QuoteIdent("statement"), d.TypeName(&models.Field{Type: models.TypeText}),
	)
	_, err := ctx.exec(sql)
	return err
}

func (ctx *Engine) recordMigration(change Change) error {
	d := ctx.dialect
	s := d.NewStatement()
	sql := fmt.Sprintf("INSERT INTO %s (%s, %s, %s, %s, %s) VALUES (%s)",
		d.QuoteIdent(migrationsTable),
		d.QuoteIdent("applied_at"), d.QuoteIdent("kind"), d.QuoteIdent("table_name"),
		d.QuoteIdent("detail"), d.QuoteIdent("statement"),
		s.Marks(5))
	_, err := ctx.exec(sql, time.Now(), string(change.Kind), change.Table, change.Detail, change.SQL)
	return err
}

// columnDefinition renders a column definition for an ALTER statement.
//
// A NOT NULL column cannot simply be added to a table with existing rows unless it has a
// default, so the constraint is dropped from the definition and left to be tightened
// deliberately.
func (ctx *Engine) columnDefinition(field *models.Field) string {
	relaxed := *field
	relaxed.NotNull = false
	relaxed.Unique = false
	return ctx.dialect.ColumnDefinition(&relaxed)
}

// modelColumns maps a model's column names to their fields, skipping relations that live on
// another table.
func modelColumns(schema *models.Model) map[string]*models.Field {
	out := make(map[string]*models.Field)
	for _, field := range schema.Fields {
		switch field.RelationKind() {
		case models.O2M, models.M2M:
			continue
		case models.M2O:
			out[field.GetFKColumn()] = field
		default:
			out[field.ColumnName()] = field
		}
	}
	return out
}

func schemasOf(entities []models.Entity) ([]*models.Model, error) {
	schemas := make([]*models.Model, 0, len(entities))
	for _, entity := range entities {
		schema, err := models.GetModel(entity)
		if err != nil {
			return nil, err
		}
		schemas = append(schemas, schema)
	}
	return schemas, nil
}

func sortedNames[T any](m map[string]T) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
