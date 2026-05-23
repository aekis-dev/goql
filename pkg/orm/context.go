package orm

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"reflect"

	"github.com/aekis/goql/pkg/models"
	"github.com/aekis/goql/pkg/query"
)

var entityType = reflect.TypeOf((*models.Entity)(nil)).Elem()

// GoqlContext handles all database operations
type GoqlContext struct {
	db        *sql.DB
	tx        *sql.Tx
	executor  QueryExecutor
	tracker   *ChangeTracker
	ctx       context.Context
	debugMode bool // Enable debug mode for logging SQL queries
}

// QueryExecutor interface
type QueryExecutor interface {
	ParseBody(fn any) (*query.ParseBody, error)
}

// ChangeTracker monitors entity changes
type ChangeTracker struct {
	original map[models.Entity]models.Entity
	dirty    map[models.Entity][]string
}

func NewGoqlContext(db *sql.DB) *GoqlContext {
	return &GoqlContext{
		db:        db,
		executor:  getExecutor(),
		tracker:   NewChangeTracker(),
		ctx:       context.Background(),
		debugMode: false,
	}
}

func (ctx *GoqlContext) WithDebug() *GoqlContext {
	ctx.debugMode = true
	return ctx
}

func (ctx *GoqlContext) EnableForeignKeys() error {
	_, err := ctx.db.Exec("PRAGMA foreign_keys = ON")
	return err
}

// Search handles entity-based, slice-based, or predicate-based queries
func (ctx *GoqlContext) Search(args ...any) ([]any, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("Search requires at least one argument")
	}

	arg := args[0]
	argType := reflect.TypeOf(arg)
	argValue := reflect.ValueOf(arg)

	// Normalize to []models.Entity for entity-based search
	var entities []models.Entity

	if entity, ok := arg.(models.Entity); ok {
		entities = []models.Entity{entity}
	} else if argValue.Kind() == reflect.Slice {
		for i := 0; i < argValue.Len(); i++ {
			elem := argValue.Index(i)
			var entity models.Entity
			var ok bool
			if elem.Kind() == reflect.Ptr {
				entity, ok = elem.Interface().(models.Entity)
			} else {
				ptrElem := reflect.New(elem.Type())
				ptrElem.Elem().Set(elem)
				entity, ok = ptrElem.Interface().(models.Entity)
			}
			if !ok {
				return nil, fmt.Errorf("slice element %d is not an Entity", i)
			}
			entities = append(entities, entity)
		}
	}

	if len(entities) > 0 {
		schema, err := models.GetModel(entities[0])
		if err != nil {
			return nil, fmt.Errorf("failed to get model for entity: %w", err)
		}

		q, err := query.EntitySearch(entities, schema)
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
		return scanRows(rows, entityType)
	}

	// Lambda predicate
	if argType.Kind() == reflect.Func {
		if argType.NumIn() != 1 || argType.NumOut() != 1 || argType.Out(0).Kind() != reflect.Bool {
			return nil, fmt.Errorf("predicate must have signature func(T) bool")
		}

		entityType := argType.In(0)
		tempEntity := reflect.New(entityType).Interface()
		entity, ok := tempEntity.(models.Entity)
		if !ok {
			return nil, fmt.Errorf("type %v does not implement Entity interface", entityType)
		}

		schema, err := models.GetModel(entity)
		if err != nil {
			return nil, fmt.Errorf("failed to get schema for entity: %w", err)
		}

		body, err := ctx.executor.ParseBody(arg)
		if err != nil {
			return nil, fmt.Errorf("failed to parse predicate: %w", err)
		}

		q, err := query.LambdaSearch(body, schema)
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
		return scanRows(rows, entityType)
	}

	return nil, fmt.Errorf("Search: unsupported argument type %T", arg)
}

// Create new entities
func (ctx *GoqlContext) Create(args ...any) ([]any, error) {
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
				elem := argValue.Index(i)
				var entity models.Entity
				var ok bool
				if elem.Kind() == reflect.Ptr {
					entity, ok = elem.Interface().(models.Entity)
				} else {
					// Create pointer to the value for Entity interface
					ptrElem := reflect.New(elem.Type())
					ptrElem.Elem().Set(elem)
					entity, ok = ptrElem.Interface().(models.Entity)
				}
				if !ok {
					return nil, fmt.Errorf("slice element %d of type %v is not an Entity", i, elem.Type())
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
		return nil, fmt.Errorf("argument of type %T is not an Entity", arg)
	}

	// Now process all entities for insertion
	if len(results) == 0 {
		return nil, nil
	}

	// Use transaction for multiple inserts
	err := ctx.Transaction(func(tx *GoqlContext) error {
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

			q, err := query.EntityCreate(entity, schema)
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
						q := query.JoinInsert(m)
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
						q := query.O2MUpdate(targetSchema, ref)
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
func (ctx *GoqlContext) Write(args ...any) (int64, error) {
	if len(args) == 0 {
		return 0, fmt.Errorf("Write requires at least one argument")
	}

	var totalAffected int64

	err := ctx.Transaction(func(tx *GoqlContext) error {
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
						elem := argValue.Index(j)
						var entity models.Entity
						var ok bool
						if elem.Kind() == reflect.Ptr {
							entity, ok = elem.Interface().(models.Entity)
						} else {
							ptrValue := reflect.New(elem.Type())
							ptrValue.Elem().Set(elem)
							entity, ok = ptrValue.Interface().(models.Entity)
						}
						if !ok {
							return fmt.Errorf("slice element %d is not an Entity", j)
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
					q, err := query.EntityWrite(entity, schema, changes)
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
							eq := query.JoinSelect(m)
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
									dq := query.JoinDelete(m)
									if _, err := tx.exec(dq.SQL, pkValue, pk); err != nil {
										return err
									}
								}
							}
							for pk := range newPKs {
								if !existingPKs[pk] {
									jq := query.JoinInsert(m)
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
							for i := 0; i < fieldValue.Len(); i++ {
								elem := fieldValue.Index(i)
								if elem.Kind() == reflect.Ptr {
									elem = elem.Elem()
								}
								ptrElem := reflect.New(elem.Type())
								ptrElem.Elem().Set(elem)
								if relatedEntity, ok := ptrElem.Interface().(models.Entity); ok {
									_, relatedPK := relatedEntity.PrimaryKey()
									if relatedPK == nil {
										continue
									}
									uq := query.O2MUpdate(targetSchema, ref)
									if _, err := tx.exec(uq.SQL, pkValue, relatedPK); err != nil {
										return err
									}
								}
							}
						}
					}

					if trackable, ok := entity.(models.ChangeTrackable); ok {
						trackable.ClearChanges()
					}
				}

			// Case 2: Lambda function with conditional logic
			case argType.Kind() == reflect.Func:
				// Validate function signature: func(T) with no return
				if argType.NumIn() != 1 || argType.NumOut() != 0 {
					return fmt.Errorf("write function at argument %d must have signature func(T)", i)
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

				body, err := tx.executor.ParseBody(arg)
				if err != nil {
					return fmt.Errorf("failed to parse function at argument %d: %w", i, err)
				}

				q, err := query.LambdaWrite(body, schema)
				if err != nil {
					// No scalar assignments — may still have relation assignments
					if len(body.RelationAssignments) == 0 {
						return fmt.Errorf("failed to build write query at argument %d: %w", i, err)
					}
				}

				if q != nil {
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

				// Handle relation assignments
				if len(body.RelationAssignments) > 0 {
					var where string
					var whereVals []any
					if body.Condition != nil {
						where, whereVals = query.WhereClause(body.Condition)
					} else {
						where = "1 = 1"
					}

					pq := query.SelectPKs(schema, where)
					pkRows, err := tx.query(pq.SQL, whereVals...)
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

					for _, ra := range body.RelationAssignments {
						field := ra.Field.Field
						newPKs := make(map[any]bool)
						for _, pk := range ra.RelatedPKs {
							newPKs[pk] = true
						}

						switch field.RelationKind() {
						case models.M2M:
							m := field.ManyToMany
							for _, pk := range affectedPKs {
								eq := query.JoinSelect(m)
								existingRows, err := tx.query(eq.SQL, pk)
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
										dq := query.JoinDelete(m)
										if _, err := tx.exec(dq.SQL, pk, epk); err != nil {
											return err
										}
									}
								}
								for npk := range newPKs {
									if !existingPKs[npk] {
										q := query.JoinInsert(m)
										if _, err := tx.exec(q.SQL, pk, npk); err != nil {
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
							for _, pk := range affectedPKs {
								for relatedPK := range newPKs {
									q := query.O2MUpdate(targetSchema, ref)
									if _, err := tx.exec(q.SQL, pk, relatedPK); err != nil {
										return err
									}
								}
							}
						}
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

// Delete handles entity-based or lambda-based deletion
func (ctx *GoqlContext) Delete(args ...any) (int64, error) {
	if len(args) == 0 {
		return 0, fmt.Errorf("Delete requires at least one argument")
	}

	var totalAffected int64

	err := ctx.Transaction(func(tx *GoqlContext) error {
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
						entity, ok := argValue.Index(j).Interface().(models.Entity)
						if !ok {
							return fmt.Errorf("slice element %d is not an Entity", j)
						}
						entities = append(entities, entity)
					}
				}

				// Group by table for efficient IN clause deletion
				type tableGroup struct {
					schema   *models.Model
					pkColumn string
					pkValues []any
				}
				groups := make(map[string]*tableGroup)

				for j, entity := range entities {
					pkColumn, pkValue := entity.PrimaryKey()
					if pkValue == nil {
						return fmt.Errorf("entity %d has nil primary key", j)
					}
					schema, err := models.GetModel(entity)
					if err != nil {
						return err
					}
					tbl := schema.TableName
					if _, exists := groups[tbl]; !exists {
						groups[tbl] = &tableGroup{pkColumn: pkColumn}
					}
					groups[tbl].pkValues = append(groups[tbl].pkValues, pkValue)
				}

				for tableName, grp := range groups {
					schema, err := models.GetModel(entities[0]) // need schema for the table
					q := query.EntityDeleteBatch(schema, grp.pkColumn, len(grp.pkValues))
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
				if argType.NumIn() != 1 || argType.NumOut() != 1 || argType.Out(0).Kind() != reflect.Bool {
					return fmt.Errorf("predicate at argument %d must have signature func(T) bool", i)
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
					return err
				}

				body, err := tx.executor.ParseBody(arg)
				if err != nil {
					return fmt.Errorf("failed to parse predicate at argument %d: %w", i, err)
				}

				q, err := query.LambdaDelete(body, schema)
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
func (ctx *GoqlContext) CreateTables(entities ...models.Entity) error {
	return ctx.Transaction(func(tx *GoqlContext) error {
		for _, model := range entities {
			schema, err := models.GetModel(model)
			if err != nil {
				return fmt.Errorf("failed to get schema for %T: %w", model, err)
			}

			tableSQL, err := query.CreateTable(schema)
			if err != nil {
				return fmt.Errorf("failed to build CREATE TABLE for %s: %w", schema.TableName, err)
			}
			if _, err := tx.exec(tableSQL); err != nil {
				return fmt.Errorf("failed to create table %s: %w", schema.TableName, err)
			}

			for _, indexSQL := range query.BuildCreateIndexes(schema) {
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
				joinSQL, err := query.CreateJoinTable(fieldSchema, schema)
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

// Transaction support
func (ctx *GoqlContext) Transaction(fn func(*GoqlContext) error) error {
	tx, err := ctx.db.BeginTx(ctx.ctx, nil)
	if err != nil {
		return err
	}

	txCtx := &GoqlContext{
		db:       ctx.db,
		tx:       tx,
		executor: ctx.executor,
		tracker:  ctx.tracker,
		ctx:      ctx.ctx,
	}

	if err := fn(txCtx); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

func isEntity(arg any) bool {
	_, ok := arg.(models.Entity)
	return ok
}
