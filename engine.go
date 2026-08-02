package goql

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"reflect"

	"github.com/aekis-dev/goql/models"
	"github.com/aekis-dev/goql/query"
)

var entityType = reflect.TypeOf((*models.Entity)(nil)).Elem()

// Engine handles all database operations
type Engine struct {
	db        *sql.DB
	tx        *sql.Tx
	executor  QueryExecutor
	tracker   *ChangeTracker
	ctx       context.Context
	dialect   *query.Dialect
	debugMode bool // Enable debug mode for logging SQL queries

	// params is the call-time params struct, if the current call supplied one. It is
	// set on a per-call copy of the Engine, never on the shared one.
	params any
}

// QueryExecutor interface
type QueryExecutor interface {
	// ParseQuery returns the parsed form of a goql call: the model it names, the function
	// it is, and the body of its lambda.
	//
	// call is the goql function the lambda was passed to. It cannot be inferred from the
	// lambda — an Insert destination and a predicate that joins another model have the same
	// signature — and it is what the parsed query records as its Func.
	ParseQuery(fn any, call string) (*query.ParseQuery, error)
}

// ChangeTracker monitors entity changes
type ChangeTracker struct {
	original map[models.Entity]models.Entity
	dirty    map[models.Entity][]string
}

// New opens an Engine over db for the given engine.
//
// The dialect is explicit because database/sql exposes no driver name, so guessing it would
// mean a fragile type-switch that breaks on wrapped drivers:
//
//	e := goql.New(db, goql.SQLite{})
//	e := goql.New(db, goql.Postgres{})
//	e := goql.New(db, goql.MySQL{})
func New(db *sql.DB, spec query.Spec) *Engine {
	return &Engine{
		db:        db,
		executor:  getExecutor(),
		tracker:   NewChangeTracker(),
		ctx:       context.Background(),
		dialect:   query.NewDialect(spec),
		debugMode: false,
	}
}

// Dialect exposes the engine's SQL dialect, for callers building statements by hand.
func (ctx *Engine) Dialect() *query.Dialect { return ctx.dialect }

func (ctx *Engine) WithDebug() *Engine {
	ctx.debugMode = true
	return ctx
}

// withCall returns an Engine scoped to one call, carrying that call's context (so each
// query has its own deadline and cancellation) and its params struct.
func (ctx *Engine) withCall(c context.Context, params any) *Engine {
	if (c == nil || c == ctx.ctx) && params == nil {
		return ctx
	}
	scoped := *ctx
	if c != nil {
		scoped.ctx = c
	}
	scoped.params = params
	return &scoped
}

// EnableForeignKeys turns on foreign key enforcement where the engine requires asking;
// on engines that always enforce them it does nothing.
func (ctx *Engine) EnableForeignKeys() error {
	stmt := ctx.dialect.EnableForeignKeysSQL()
	if stmt == "" {
		return nil
	}
	_, err := ctx.db.Exec(stmt)
	return err
}

// getByKeys selects rows by primary key. Scanning and preloading follow the same rules as
// every other read, so a model's declared Preload defaults apply and a Preload option
// replaces them.
func (ctx *Engine) getByKeys(entity models.Entity, keys []any, opts *query.Options) ([]any, error) {
	schema, err := models.GetModel(entity)
	if err != nil {
		return nil, fmt.Errorf("failed to get model for entity: %w", err)
	}

	q, err := ctx.dialect.PrimaryKeySearch(keys, schema, opts)
	if err != nil {
		return nil, err
	}

	if ctx.debugMode {
		log.Printf("Get by key SQL: %s\n Args: %v", q.SQL, q.Args)
	}

	rows, err := ctx.query(q.SQL, q.Args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entityType := reflect.TypeOf(entity)
	if entityType.Kind() == reflect.Ptr {
		entityType = entityType.Elem()
	}

	results, err := scanRows(rows, entityType)
	if err != nil {
		return nil, err
	}
	if err := ctx.preloadRelations(results, schema, effectivePreload(opts, schema)); err != nil {
		return nil, err
	}
	return results, nil
}

// Search handles entity-based, slice-based, or predicate-based queries
func (ctx *Engine) searchAny(arg any, opts *query.Options) ([]any, error) {
	argType := reflect.TypeOf(arg)
	argValue := reflect.ValueOf(arg)

	// Normalize to []models.Entity for entity-based search
	var entities []models.Entity

	if argValue.Kind() == reflect.Slice {
		for i := 0; i < argValue.Len(); i++ {
			entity, ok := asEntity(argValue.Index(i))
			if !ok {
				return nil, fmt.Errorf("%w: slice element %d", ErrNotEntity, i)
			}
			entities = append(entities, entity)
		}
	} else if argType.Kind() != reflect.Func {
		if entity, ok := asEntity(argValue); ok {
			entities = append(entities, entity)
		}
	}

	if len(entities) > 0 {
		schema, err := models.GetModel(entities[0])
		if err != nil {
			return nil, fmt.Errorf("failed to get model for entity: %w", err)
		}

		q, err := ctx.dialect.EntitySearch(entities, schema, opts)
		if err != nil {
			return nil, err
		}

		if ctx.debugMode {
			log.Printf("Search by entity SQL: %s\n Args: %v", q.SQL, q.Args)
		}

		entityType := reflect.TypeOf(entities[0])
		if entityType.Kind() == reflect.Ptr {
			entityType = entityType.Elem()
		}

		rows, err := ctx.query(q.SQL, q.Args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		results, err := scanRows(rows, entityType)
		if err != nil {
			return nil, err
		}
		if err := ctx.preloadRelations(results, schema, effectivePreload(opts, schema)); err != nil {
			return nil, err
		}
		return results, nil
	}

	// Lambda predicate
	if argType.Kind() == reflect.Func {
		if argType.NumIn() == 0 {
			return nil, fmt.Errorf("%w: predicate takes no parameters", ErrInvalidLambda)
		}

		entityType := argType.In(0)
		if entityType.Kind() == reflect.Ptr {
			entityType = entityType.Elem()
		}
		tempEntity := reflect.New(entityType).Interface()
		entity, ok := tempEntity.(models.Entity)
		if !ok {
			return nil, fmt.Errorf("type %v does not implement Entity interface", entityType)
		}

		schema, err := models.GetModel(entity)
		if err != nil {
			return nil, fmt.Errorf("failed to get schema for entity: %w", err)
		}

		parsed, err := ctx.executor.ParseQuery(arg, "Select")
		if err != nil {
			return nil, fmt.Errorf("failed to parse predicate: %w", err)
		}

		// Modifiers declared inside the lambda take effect unless the caller passed
		// some explicitly.
		effective := opts
		if effective == nil {
			effective = parsed.Body.Options
		}
		if err := checkConflictOption(effective, "Select"); err != nil {
			return nil, err
		}

		q, err := ctx.dialect.LambdaSearch(parsed, effective)
		if err != nil {
			return nil, fmt.Errorf("failed to build search query: %w", err)
		}

		if ctx.debugMode {
			log.Printf("Search by lambda SQL: %s\n Args: %v", q.SQL, q.Args)
		}

		rows, err := ctx.query(q.SQL, q.Args...)
		if err != nil {
			return nil, fmt.Errorf("failed to execute query: %w", err)
		}
		defer rows.Close()

		results, err := scanRows(rows, entityType)
		if err != nil {
			return nil, err
		}
		if err := ctx.preloadRelations(results, schema, effectivePreload(effective, schema)); err != nil {
			return nil, err
		}
		return results, nil
	}

	return nil, fmt.Errorf("Search: unsupported argument type %T", arg)
}

// Create new entities
func (ctx *Engine) createAny(args ...any) ([]any, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("Create requires at least one argument")
	}

	var results []any

	for _, arg := range args {
		argValue := reflect.ValueOf(arg)
		argType := reflect.TypeOf(arg)

		// Case 1: Single entity
		if entity, ok := arg.(models.Entity); ok {
			results = append(results, entity)
			continue
		}

		// Case 2: Slice of entities
		if argValue.Kind() == reflect.Slice {
			for i := 0; i < argValue.Len(); i++ {
				entity, ok := asEntity(argValue.Index(i))
				if !ok {
					return nil, fmt.Errorf("%w: slice element %d", ErrNotEntity, i)
				}
				results = append(results, entity)
			}
			continue
		}

		// Case 3: Check if it's a struct that should be converted to Entity
		if argValue.Kind() == reflect.Struct {
			ptrValue := reflect.New(argType)
			ptrValue.Elem().Set(argValue)
			if entity, ok := ptrValue.Interface().(models.Entity); ok {
				results = append(results, entity)
				continue
			}
		}
		return nil, fmt.Errorf("%w: argument of type %T", ErrNotEntity, arg)
	}

	// Now process all entities for insertion
	if len(results) == 0 {
		return nil, nil
	}

	// Use transaction for multiple inserts
	err := ctx.Transaction(func(tx *Engine) error {
		for _, result := range results {
			entity := result.(models.Entity)

			// Mark as new for change tracking
			if trackable, ok := entity.(models.ChangeTrackable); ok {
				trackable.MarkNew()
			}

			// Get reflection info
			v := reflect.ValueOf(entity)
			if v.Kind() == reflect.Ptr {
				v = v.Elem()
			}

			schema, err := models.GetModel(entity)
			if err != nil {
				return fmt.Errorf("failed to get schema for %T: %w", entity, err)
			}

			applyAutoTimestamps(entity, schema, true)

			q, err := ctx.dialect.EntityCreate(entity, schema)
			if err != nil {
				return fmt.Errorf("failed to build insert query: %w", err)
			}

			if ctx.debugMode {
				log.Printf("Create SQL: %s\n Args: %v", q.SQL, q.Args)
			}

			dbResult, err := tx.exec(q.SQL, q.Args...)
			if err != nil {
				return fmt.Errorf("failed to create entity: %w", err)
			}

			if pk, err := dbResult.LastInsertId(); err == nil {
				entity.SetPrimaryKey(pk)
			}

			// Handle relation fields
			_, pkValue := entity.PrimaryKey()
			for _, fieldSchema := range schema.Fields {
				fieldValue, found := getFieldValue(v, fieldSchema.Name)
				if !found || !fieldValue.IsValid() || fieldValue.Kind() != reflect.Slice || fieldValue.Len() == 0 {
					continue
				}

				switch fieldSchema.RelationKind() {
				case models.M2M:
					m := fieldSchema.ManyToMany
					for i := 0; i < fieldValue.Len(); i++ {
						elem := fieldValue.Index(i)
						if elem.Kind() == reflect.Ptr {
							elem = elem.Elem()
						}
						ptrElem := reflect.New(elem.Type())
						ptrElem.Elem().Set(elem)
						relatedEntity, ok := ptrElem.Interface().(models.Entity)
						if !ok {
							return fmt.Errorf("many2many element does not implement Entity")
						}
						_, relatedPK := relatedEntity.PrimaryKey()
						q := tx.dialect.JoinInsert(m)
						if _, err := tx.exec(q.SQL, pkValue, relatedPK); err != nil {
							return fmt.Errorf("failed to insert into join table %s: %w", m.Table, err)
						}
					}

				case models.O2M:
					ref := fieldSchema.OneToMany.Ref
					targetType := fieldSchema.TargetModel()
					tempTarget := reflect.New(targetType).Interface()
					targetEntity, ok := tempTarget.(models.Entity)
					if !ok {
						return fmt.Errorf("one2many target does not implement Entity")
					}
					targetSchema, err := models.GetModel(targetEntity)
					if err != nil {
						return fmt.Errorf("failed to get target schema for one2many: %w", err)
					}
					for i := 0; i < fieldValue.Len(); i++ {
						elem := fieldValue.Index(i)
						if elem.Kind() == reflect.Ptr {
							elem = elem.Elem()
						}
						ptrElem := reflect.New(elem.Type())
						ptrElem.Elem().Set(elem)
						relatedEntity, ok := ptrElem.Interface().(models.Entity)
						if !ok {
							return fmt.Errorf("one2many element does not implement Entity")
						}
						_, relatedPK := relatedEntity.PrimaryKey()
						if relatedPK == nil {
							continue
						}
						q := tx.dialect.O2MUpdate(targetSchema, ref)
						if _, err := tx.exec(q.SQL, pkValue, relatedPK); err != nil {
							return fmt.Errorf("failed to update one2many FK: %w", err)
						}
					}
				}
			}

			if trackable, ok := entity.(models.ChangeTrackable); ok {
				InitTracking(trackable)
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return results, nil
}

// Write handles entity-based or lambda-based updates
func (ctx *Engine) writeAny(args ...any) (int64, error) {
	if len(args) == 0 {
		return 0, fmt.Errorf("Write requires at least one argument")
	}

	var totalAffected int64

	err := ctx.Transaction(func(tx *Engine) error {
		for i, arg := range args {
			argType := reflect.TypeOf(arg)
			argValue := reflect.ValueOf(arg)

			switch {
			// Case 1: Single entity or slice of entities
			case isEntity(arg) || argValue.Kind() == reflect.Slice:
				var entities []models.Entity

				if isEntity(arg) {
					entities = append(entities, arg.(models.Entity))
				} else {
					for j := 0; j < argValue.Len(); j++ {
						entity, ok := asEntity(argValue.Index(j))
						if !ok {
							return fmt.Errorf("%w: slice element %d", ErrNotEntity, j)
						}
						entities = append(entities, entity)
					}
				}

				for _, entity := range entities {
					schema, err := models.GetModel(entity)
					if err != nil {
						return fmt.Errorf("failed to get schema for entity: %w", err)
					}

					applyAutoTimestamps(entity, schema, false)

					v := reflect.ValueOf(entity)
					if v.Kind() == reflect.Ptr {
						v = v.Elem()
					}

					changes := GetChanges(entity)
					_, pkValue := entity.PrimaryKey()

					// Build scalar SET clauses from changes
					q, err := ctx.dialect.EntityWrite(entity, schema, changes)
					if err != nil {
						return fmt.Errorf("failed to build update query: %w", err)
					}
					if q != nil {
						if err != nil {
							return fmt.Errorf("failed to build update query: %w", err)
						}
						if ctx.debugMode {
							log.Printf("Write entity SQL: %s\n Args: %v", q.SQL, q.Args)
						}
						result, err := tx.exec(q.SQL, q.Args...)
						if err != nil {
							return fmt.Errorf("failed to update entity: %w", err)
						}
						affected, _ := result.RowsAffected()
						totalAffected += affected
					}

					// Handle relation fields
					for _, fieldSchema := range schema.Fields {
						switch fieldSchema.RelationKind() {
						case models.M2M:
							fieldValue, found := getFieldValue(v, fieldSchema.Name)
							if !found || !fieldValue.IsValid() || fieldValue.Kind() != reflect.Slice || fieldValue.IsNil() {
								continue
							}
							m := fieldSchema.ManyToMany
							eq := tx.dialect.JoinSelect(m)
							existingRows, err := tx.query(eq.SQL, pkValue)
							if err != nil {
								return fmt.Errorf("failed to query existing associations: %w", err)
							}
							existingPKs := make(map[any]bool)
							for existingRows.Next() {
								var pk any
								if err := existingRows.Scan(&pk); err != nil {
									existingRows.Close()
									return err
								}
								existingPKs[pk] = true
							}
							existingRows.Close()

							newPKs := make(map[any]bool)
							for i := 0; i < fieldValue.Len(); i++ {
								elem := fieldValue.Index(i)
								if elem.Kind() == reflect.Ptr {
									elem = elem.Elem()
								}
								ptrElem := reflect.New(elem.Type())
								ptrElem.Elem().Set(elem)
								if relatedEntity, ok := ptrElem.Interface().(models.Entity); ok {
									_, relatedPK := relatedEntity.PrimaryKey()
									if relatedPK != nil {
										newPKs[relatedPK] = true
									}
								}
							}
							for pk := range existingPKs {
								if !newPKs[pk] {
									dq := tx.dialect.JoinDelete(m)
									if _, err := tx.exec(dq.SQL, pkValue, pk); err != nil {
										return err
									}
								}
							}
							for pk := range newPKs {
								if !existingPKs[pk] {
									jq := tx.dialect.JoinInsert(m)
									if _, err := tx.exec(jq.SQL, pkValue, pk); err != nil {
										return err
									}
								}
							}

						case models.O2M:
							fieldValue, found := getFieldValue(v, fieldSchema.Name)
							if !found || !fieldValue.IsValid() || fieldValue.Kind() != reflect.Slice || fieldValue.IsNil() {
								continue
							}
							ref := fieldSchema.OneToMany.Ref
							targetType := fieldSchema.TargetModel()
							tempTarget := reflect.New(targetType).Interface()
							targetEntity, ok := tempTarget.(models.Entity)
							if !ok {
								return fmt.Errorf("one2many target does not implement Entity")
							}
							targetSchema, err := models.GetModel(targetEntity)
							if err != nil {
								return err
							}
							var relatedPKs []any
							for i := 0; i < fieldValue.Len(); i++ {
								elem := fieldValue.Index(i)
								if elem.Kind() == reflect.Ptr {
									elem = elem.Elem()
								}
								ptrElem := reflect.New(elem.Type())
								ptrElem.Elem().Set(elem)
								if relatedEntity, ok := ptrElem.Interface().(models.Entity); ok {
									if _, relatedPK := relatedEntity.PrimaryKey(); relatedPK != nil {
										relatedPKs = append(relatedPKs, relatedPK)
									}
								}
							}
							if err := tx.syncO2M(targetSchema, ref, pkValue, relatedPKs); err != nil {
								return err
							}
						}
					}

					if trackable, ok := entity.(models.ChangeTrackable); ok {
						trackable.ClearChanges()
					}
				}

			// Case 2: Lambda function with conditional logic
			case argType.Kind() == reflect.Func:
				// Shape is validated at the API boundary, which also accounts for option
				// and params parameters; only the entity parameter is needed here.
				if argType.NumIn() == 0 {
					return fmt.Errorf("%w: write lambda takes no parameters", ErrInvalidLambda)
				}

				entityType := argType.In(0)
				if entityType.Kind() == reflect.Ptr {
					entityType = entityType.Elem()
				}

				tempEntity := reflect.New(entityType).Interface()
				entity, ok := tempEntity.(models.Entity)
				if !ok {
					return fmt.Errorf("type %v does not implement Entity interface", entityType)
				}

				schema, err := models.GetModel(entity)
				if err != nil {
					return fmt.Errorf("failed to get schema for %v: %w", entityType, err)
				}

				parsed, err := tx.executor.ParseQuery(arg, "Update")
				if err != nil {
					return fmt.Errorf("failed to parse function at argument %d: %w", i, err)
				}
				body := parsed.Body

				if len(body.WriteBranches()) == 0 {
					return fmt.Errorf("write function at argument %d assigns nothing", i)
				}
				if err := checkConflictOption(body.Options, "Update"); err != nil {
					return err
				}

				// Each branch is an independent statement with its own SET list and its
				// own mutually exclusive WHERE, so an if/else or switch produces several.
				updates, err := ctx.dialect.LambdaWrite(parsed)
				if err != nil {
					return fmt.Errorf("failed to build write query at argument %d: %w", i, err)
				}
				for _, q := range updates {
					if ctx.debugMode {
						log.Printf("Write lambda SQL: %s\n Args: %v", q.SQL, q.Args)
					}
					result, err := tx.exec(q.SQL, q.Args...)
					if err != nil {
						return fmt.Errorf("failed to execute write: %w", err)
					}
					affected, _ := result.RowsAffected()
					totalAffected += affected
				}

				// Relation assignments are applied per branch, scoped to the rows that
				// branch selects.
				for _, branch := range body.WriteBranches() {
					if len(branch.RelationAssignments) == 0 {
						continue
					}
					if err := tx.applyRelationAssignments(schema, branch); err != nil {
						return err
					}
				}

			default:
				return fmt.Errorf("unsupported argument type %T at position %d", arg, i)
			}
		}
		return nil
	})

	if err != nil {
		return 0, err
	}
	return totalAffected, nil
}

// applyRelationAssignments syncs the relation fields assigned by one write branch,
// scoped to the rows that branch's condition selects.
func (ctx *Engine) applyRelationAssignments(schema *models.Model, branch *query.ParseBranch) error {
	pq, err := ctx.dialect.SelectPKsWhere(schema, branch.Condition)
	if err != nil {
		return fmt.Errorf("failed to build PK query for relation update: %w", err)
	}

	pkRows, err := ctx.query(pq.SQL, pq.Args...)
	if err != nil {
		return fmt.Errorf("failed to query PKs for relation update: %w", err)
	}
	var affectedPKs []any
	for pkRows.Next() {
		var pk any
		if err := pkRows.Scan(&pk); err != nil {
			pkRows.Close()
			return err
		}
		affectedPKs = append(affectedPKs, pk)
	}
	pkRows.Close()
	if err := pkRows.Err(); err != nil {
		return err
	}

	for _, ra := range branch.RelationAssignments {
		field := ra.Field.Field
		newPKs := make(map[any]bool)
		for _, pk := range ra.RelatedPKs {
			newPKs[pk] = true
		}

		switch field.RelationKind() {
		case models.M2M:
			m := field.ManyToMany
			for _, pk := range affectedPKs {
				eq := ctx.dialect.JoinSelect(m)
				existingRows, err := ctx.query(eq.SQL, pk)
				if err != nil {
					return err
				}
				existingPKs := make(map[any]bool)
				for existingRows.Next() {
					var epk any
					if err := existingRows.Scan(&epk); err != nil {
						existingRows.Close()
						return err
					}
					existingPKs[epk] = true
				}
				existingRows.Close()

				for epk := range existingPKs {
					if !newPKs[epk] {
						dq := ctx.dialect.JoinDelete(m)
						if _, err := ctx.exec(dq.SQL, pk, epk); err != nil {
							return err
						}
					}
				}
				for npk := range newPKs {
					if !existingPKs[npk] {
						jq := ctx.dialect.JoinInsert(m)
						if _, err := ctx.exec(jq.SQL, pk, npk); err != nil {
							return err
						}
					}
				}
			}

		case models.O2M:
			ref := field.OneToMany.Ref
			targetType := field.TargetModel()
			tempTarget := reflect.New(targetType).Interface()
			targetEntity, ok := tempTarget.(models.Entity)
			if !ok {
				return fmt.Errorf("one2many target does not implement Entity")
			}
			targetSchema, err := models.GetModel(targetEntity)
			if err != nil {
				return err
			}
			relatedPKs := make([]any, 0, len(newPKs))
			for relatedPK := range newPKs {
				relatedPKs = append(relatedPKs, relatedPK)
			}
			for _, pk := range affectedPKs {
				if err := ctx.syncO2M(targetSchema, ref, pk, relatedPKs); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// Delete handles entity-based or lambda-based deletion
func (ctx *Engine) deleteAny(args ...any) (int64, error) {
	if len(args) == 0 {
		return 0, fmt.Errorf("Delete requires at least one argument")
	}

	var totalAffected int64

	err := ctx.Transaction(func(tx *Engine) error {
		for i, arg := range args {
			argType := reflect.TypeOf(arg)
			argValue := reflect.ValueOf(arg)

			switch {
			// Case 1: Single entity or slice of entities
			case isEntity(arg) || argValue.Kind() == reflect.Slice:
				var entities []models.Entity
				if isEntity(arg) {
					entities = append(entities, arg.(models.Entity))
				} else {
					for j := 0; j < argValue.Len(); j++ {
						entity, ok := asEntity(argValue.Index(j))
						if !ok {
							return fmt.Errorf("%w: slice element %d", ErrNotEntity, j)
						}
						entities = append(entities, entity)
					}
				}

				// Group by table for efficient IN clause deletion
				type tableGroup struct {
					schema   *models.Model
					pkValues []any
				}
				groups := make(map[string]*tableGroup)

				for j, entity := range entities {
					_, pkValue := entity.PrimaryKey()
					if pkValue == nil {
						return fmt.Errorf("entity %d has nil primary key", j)
					}
					schema, err := models.GetModel(entity)
					if err != nil {
						return err
					}
					tbl := schema.TableName
					if _, exists := groups[tbl]; !exists {
						// Capture the schema of *this* table — resolving it later from
						// entities[0] would use the wrong schema for every other group.
						groups[tbl] = &tableGroup{schema: schema}
					}
					groups[tbl].pkValues = append(groups[tbl].pkValues, pkValue)
				}

				for tableName, grp := range groups {
					q := ctx.dialect.EntityDeleteBatch(grp.schema, len(grp.pkValues))
					if ctx.debugMode {
						log.Printf("Delete entity SQL: %s\n Args: %v", q.SQL, grp.pkValues)
					}
					result, err := tx.exec(q.SQL, grp.pkValues...)
					if err != nil {
						return fmt.Errorf("failed to delete from %s: %w", tableName, err)
					}
					affected, _ := result.RowsAffected()
					totalAffected += affected
				}

			// Case 2: Lambda function for predicate-based deletion
			case argType.Kind() == reflect.Func:
				if argType.NumIn() == 0 {
					return fmt.Errorf("%w: predicate takes no parameters", ErrInvalidLambda)
				}

				entityType := argType.In(0)
				if entityType.Kind() == reflect.Ptr {
					entityType = entityType.Elem()
				}

				// The model comes from the parsed query, which is the single place it is
				// recorded.
				parsed, err := tx.executor.ParseQuery(arg, "Delete")
				if err != nil {
					return fmt.Errorf("failed to parse predicate at argument %d: %w", i, err)
				}
				body := parsed.Body

				if err := checkConflictOption(body.Options, "Delete"); err != nil {
					return err
				}

				q, err := ctx.dialect.LambdaDelete(parsed)
				if err != nil {
					return fmt.Errorf("failed to build delete query: %w", err)
				}

				if ctx.debugMode {
					log.Printf("Delete lambda SQL: %s\n Args: %v", q.SQL, q.Args)
				}

				result, err := tx.exec(q.SQL, q.Args...)
				if err != nil {
					return fmt.Errorf("failed to execute batch delete at argument %d: %w", i, err)
				}
				affected, _ := result.RowsAffected()
				totalAffected += affected

			default:
				return fmt.Errorf("unsupported argument type %T at position %d", arg, i)
			}
		}
		return nil
	})

	if err != nil {
		return 0, err
	}
	return totalAffected, nil
}

// CreateTables creates database tables for all registered models
func (ctx *Engine) CreateTables(entities ...models.Entity) error {
	return ctx.Transaction(func(tx *Engine) error {
		for _, model := range entities {
			schema, err := models.GetModel(model)
			if err != nil {
				return fmt.Errorf("failed to get schema for %T: %w", model, err)
			}

			tableSQL, err := ctx.dialect.CreateTable(schema)
			if err != nil {
				return fmt.Errorf("failed to build CREATE TABLE for %s: %w", schema.TableName, err)
			}
			if _, err := tx.exec(tableSQL); err != nil {
				return fmt.Errorf("failed to create table %s: %w", schema.TableName, err)
			}

			for _, indexSQL := range ctx.dialect.BuildCreateIndexes(schema) {
				if indexSQL == "" {
					continue
				}
				if _, err := tx.exec(indexSQL); err != nil {
					return fmt.Errorf("failed to create index on %s: %w", schema.TableName, err)
				}
			}

			for _, fieldSchema := range schema.Fields {
				if fieldSchema.RelationKind() != models.M2M {
					continue
				}
				joinSQL, err := ctx.dialect.CreateJoinTable(fieldSchema, schema)
				if err != nil {
					return fmt.Errorf("failed to build join table SQL: %w", err)
				}
				if _, err := tx.exec(joinSQL); err != nil {
					return fmt.Errorf("failed to create join table %s: %w", fieldSchema.ManyToMany.Table, err)
				}
			}
		}
		return nil
	})
}

// Transaction runs fn inside a database transaction, committing when it returns nil and
// rolling back otherwise.
//
// A nested call joins the surrounding transaction rather than opening a second one. That
// matters twice over: Create, Write, Delete and Insert each wrap themselves in a
// transaction, so without joining, work done inside a user transaction would commit
// independently of it — and the second BeginTx would hold a second pooled connection while
// the first is still held, deadlocking any pool smaller than the nesting depth.
//
// The rollback is deferred, so a panic in fn releases the connection on its way out
// instead of leaking it for the lifetime of the process.
func (ctx *Engine) Transaction(fn func(*Engine) error) error {
	if ctx.tx != nil {
		// Already in a transaction: run in it. An error propagates to whichever call
		// opened it, which is what rolls the whole thing back.
		return fn(ctx)
	}

	tx, err := ctx.db.BeginTx(ctx.ctx, nil)
	if err != nil {
		return err
	}

	// Copy the Engine rather than rebuilding it, so per-call state (the params struct,
	// debug mode) survives into the transaction. Listing fields by hand silently dropped
	// them.
	txCtx := *ctx
	txCtx.tx = tx

	committed := false
	defer func() {
		if !committed {
			// Covers the error return and the panic alike. Rolling back an already
			// finished transaction reports sql.ErrTxDone, which is not interesting here.
			tx.Rollback()
		}
	}()

	if err := fn(&txCtx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func isEntity(arg any) bool {
	_, ok := arg.(models.Entity)
	return ok
}

// insertReturning runs an INSERT that reports its generated primary key, for engines
// without LastInsertId.
func (ctx *Engine) insertReturning(q *query.Query) (int64, error) {
	rows, err := ctx.query(q.SQL, q.Args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, err
		}
		return 0, fmt.Errorf("insert returned no primary key")
	}

	var pk int64
	if err := rows.Scan(&pk); err != nil {
		return 0, err
	}
	return pk, rows.Err()
}
