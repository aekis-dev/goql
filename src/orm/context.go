package orm

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"reflect"
	"sort"
	"strings"
)

// ColtContext handles all database operations
type ColtContext struct {
	db        *sql.DB
	tx        *sql.Tx
	executor  QueryExecutor
	tracker   *ChangeTracker
	ctx       context.Context
	debugMode bool // Enable debug mode for logging SQL queries
}

// ChangeTracker monitors entity changes
type ChangeTracker struct {
	original map[Entity]Entity
	dirty    map[Entity][]string
}

func NewColtContext(db *sql.DB) *ColtContext {
	return &ColtContext{
		db:        db,
		executor:  getExecutor(os.Getenv("COLT_MODE")), // Changes based on build tags
		tracker:   NewChangeTracker(),
		ctx:       context.Background(),
		debugMode: true,
	}
}

// Search handles entity-based, slice-based, or predicate-based queries
func (ctx *ColtContext) Search(args ...any) ([]any, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("Query requires at least one argument")
	}

	if len(args) > 1 {
		return nil, fmt.Errorf("Query requires exactly one argument")
	}

	arg := args[0]
	argType := reflect.TypeOf(arg)
	argValue := reflect.ValueOf(arg)

	// Case 1: Single entity - use non-nil fields as WHERE conditions
	if entity, ok := arg.(Entity); ok {
		tableName := entity.TableName()
		entityValue := reflect.ValueOf(entity)
		if entityValue.Kind() == reflect.Ptr {
			entityValue = entityValue.Elem()
		}
		entityType := entityValue.Type()

		// Build WHERE clause from non-zero fields
		var conditions []string
		var values []any

		for i := 0; i < entityValue.NumField(); i++ {
			field := entityType.Field(i)
			fieldValue := entityValue.Field(i)

			// Skip fields with orm:"-" tag
			ormTag := field.Tag.Get("orm")
			if ormTag == "-" {
				continue
			}

			// Skip zero values
			if isZeroValue(fieldValue) {
				continue
			}

			// Get column name
			columnName := ormTag
			if columnName == "" {
				columnName = toSnakeCase(field.Name)
			}

			// Handle different field types
			switch fieldValue.Kind() {
			case reflect.String:
				// Support LIKE queries for strings with wildcards
				strVal := fieldValue.String()
				if strings.Contains(strVal, "%") {
					conditions = append(conditions, fmt.Sprintf("%s LIKE ?", columnName))
				} else {
					conditions = append(conditions, fmt.Sprintf("%s = ?", columnName))
				}
				values = append(values, strVal)

			case reflect.Slice:
				// Skip slices (usually relationships)
				continue

			default:
				conditions = append(conditions, fmt.Sprintf("%s = ?", columnName))
				values = append(values, fieldValue.Interface())
			}
		}

		// Build and execute query
		sql := fmt.Sprintf("SELECT * FROM %s", tableName)
		if len(conditions) > 0 {
			sql += " WHERE " + strings.Join(conditions, " AND ")
		}

		if ctx.debugMode {
			log.Printf("Query by entity SQL: %s\n Values: %v", sql, values)
		}

		rows, err := ctx.query(sql, values...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		return scanRows(rows, entityType)
	}

	// Case 2: Slice of entities - use IN clauses for non-nil fields
	if argValue.Kind() == reflect.Slice {
		sliceLen := argValue.Len()
		if sliceLen == 0 {
			return []any{}, nil
		}

		// Get first entity to determine type and table
		firstElem := argValue.Index(0).Interface()
		firstEntity, ok := firstElem.(Entity)
		if !ok {
			return nil, fmt.Errorf("slice elements must be entities")
		}

		tableName := firstEntity.TableName()

		// Collect values for each field across all entities
		fieldValues := make(map[string][]any)

		// Process all entities in the slice
		for i := 0; i < sliceLen; i++ {
			elem := argValue.Index(i).Interface()
			entity, ok := elem.(Entity)
			if !ok {
				return nil, fmt.Errorf("slice element %d is not an Entity", i)
			}

			entityValue := reflect.ValueOf(entity)
			if entityValue.Kind() == reflect.Ptr {
				entityValue = entityValue.Elem()
			}
			entityType := entityValue.Type()

			// Collect non-zero field values
			for j := 0; j < entityValue.NumField(); j++ {
				field := entityType.Field(j)
				fieldValue := entityValue.Field(j)

				ormTag := field.Tag.Get("orm")
				if ormTag == "-" {
					continue
				}

				if isZeroValue(fieldValue) {
					continue
				}

				columnName := ormTag
				if columnName == "" {
					columnName = toSnakeCase(field.Name)
				}

				// Skip slices (relationships)
				if fieldValue.Kind() == reflect.Slice {
					continue
				}

				// Add to field values map
				if _, exists := fieldValues[columnName]; !exists {
					fieldValues[columnName] = []any{}
				}

				// Only add unique values
				val := fieldValue.Interface()
				if !containsValue(fieldValues[columnName], val) {
					fieldValues[columnName] = append(fieldValues[columnName], val)
				}
			}
		}

		// Build WHERE clause with IN conditions
		var conditions []string
		var values []any

		for columnName, columnValues := range fieldValues {
			if len(columnValues) == 1 {
				// Single value - use equality
				conditions = append(conditions, fmt.Sprintf("%s = ?", columnName))
				values = append(values, columnValues[0])
			} else {
				// Multiple values - use IN
				placeholders := make([]string, len(columnValues))
				for i := range placeholders {
					placeholders[i] = "?"
				}
				conditions = append(conditions, fmt.Sprintf("%s IN (%s)",
					columnName, strings.Join(placeholders, ", ")))
				values = append(values, columnValues...)
			}
		}

		// Build and execute query
		sql := fmt.Sprintf("SELECT * FROM %s", tableName)
		if len(conditions) > 0 {
			sql += " WHERE " + strings.Join(conditions, " OR ") // OR because we want any match
		}

		if ctx.debugMode {
			log.Printf("Search by slice SQL: %s\n Values: %v", sql, values)
		}

		rows, err := ctx.query(sql, values...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		entityType := reflect.TypeOf(firstEntity)
		if entityType.Kind() == reflect.Ptr {
			entityType = entityType.Elem()
		}

		return scanRows(rows, entityType)
	}

	// Case 3: Predicate function (lambda)
	if argType.Kind() == reflect.Func {
		// Validate predicate function signature
		if argType.NumIn() != 1 || argType.NumOut() != 1 {
			return nil, fmt.Errorf("predicate must have signature func(T) bool")
		}

		if argType.Out(0).Kind() != reflect.Bool {
			return nil, fmt.Errorf("predicate must return bool")
		}

		// Get entity type from predicate parameter
		entityType := argType.In(0)

		// Create a temporary instance to get table name
		tempEntity := reflect.New(entityType).Interface()
		entity, ok := tempEntity.(Entity)
		if !ok {
			return nil, fmt.Errorf("type %v does not implement Entity interface", entityType)
		}

		tableName := entity.TableName()

		// Parse predicate to SQL WHERE clause
		whereClause := ctx.executor.ParsePredicate(arg)

		// Build SELECT statement
		sql := fmt.Sprintf("SELECT * FROM %s WHERE %s", tableName, whereClause)

		if ctx.debugMode {
			log.Printf("Search by lambda func SQL: %s", sql)
		}

		// Execute query
		rows, err := ctx.query(sql)
		if err != nil {
			return nil, fmt.Errorf("failed to execute query: %w", err)
		}
		defer rows.Close()

		return scanRows(rows, entityType)
	}

	return nil, fmt.Errorf("Query: unsupported argument type %T", arg)
}

// Create new entity
func (ctx *ColtContext) Create(args ...any) ([]any, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("Create requires at least one argument")
	}

	var results []any

	for _, arg := range args {
		argValue := reflect.ValueOf(arg)
		argType := reflect.TypeOf(arg)

		// Case 1: Single entity
		if entity, ok := arg.(Entity); ok {
			// Handle single entity
			results = append(results, entity)
			continue
		}

		// Case 2: Slice of entities
		if argValue.Kind() == reflect.Slice {
			sliceLen := argValue.Len()
			for i := 0; i < sliceLen; i++ {
				elem := argValue.Index(i)

				// Handle both value and pointer types
				var entity Entity
				var ok bool

				if elem.Kind() == reflect.Ptr {
					entity, ok = elem.Interface().(Entity)
				} else {
					// Create pointer to the value for Entity interface
					ptrElem := reflect.New(elem.Type())
					ptrElem.Elem().Set(elem)
					entity, ok = ptrElem.Interface().(Entity)
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
			// Try to create a pointer to the struct
			ptrValue := reflect.New(argType)
			ptrValue.Elem().Set(argValue)

			if entity, ok := ptrValue.Interface().(Entity); ok {
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
	err := ctx.Transaction(func(tx *ColtContext) error {
		for _, result := range results {
			entity := result.(Entity)

			// Mark as new for change tracking
			if trackable, ok := entity.(ChangeTrackable); ok {
				trackable.MarkNew()
			}

			// Get reflection info
			v := reflect.ValueOf(entity)
			if v.Kind() == reflect.Ptr {
				v = v.Elem()
			}
			t := v.Type()

			// Build INSERT SQL
			tableName := entity.TableName()
			var fields []string
			var values []any

			schema, err := GetSchema(entity)
			if err != nil {
				return fmt.Errorf("failed to get schema for %T: %w", entity, err)
			}

			for i := 0; i < v.NumField(); i++ {
				structField := t.Field(i)
				fieldValue := v.Field(i)

				// Skip if field should be ignored
				if fieldConfig, exists := schema.Fields[structField.Name]; exists {
					if fieldConfig.Ignore || fieldConfig.AutoIncrement {
						continue
					}

					// Skip zero values for nullable fields
					if isZeroValue(fieldValue) && fieldConfig.IsNullable() {
						continue
					}

					fields = append(fields, fieldConfig.GetColumnName())
					values = append(values, fieldValue.Interface())
				}
			}

			// Execute INSERT
			placeholders := make([]string, len(fields))
			for i := range placeholders {
				placeholders[i] = "?"
			}

			sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
				tableName,
				strings.Join(fields, ", "),
				strings.Join(placeholders, ", "))

			result, err := tx.exec(sql, values...)
			if err != nil {
				return fmt.Errorf("failed to create entity: %w", err)
			}

			if pk, err := result.LastInsertId(); err == nil {
				entity.SetPrimaryKey(pk)
			}

			// Initialize tracking after insert (captures the persisted state)
			if trackable, ok := entity.(ChangeTrackable); ok {
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

func (ctx *ColtContext) Write(args ...any) (int64, error) {
	if len(args) == 0 {
		return 0, fmt.Errorf("Write requires at least one argument")
	}

	var totalAffected int64

	// Use transaction for all operations
	err := ctx.Transaction(func(tx *ColtContext) error {
		// Process each argument individually
		for i, arg := range args {
			argType := reflect.TypeOf(arg)
			argValue := reflect.ValueOf(arg)

			switch {
			// Case 1: Single entity or slice of entities
			case isEntity(arg) || argValue.Kind() == reflect.Slice:
				var entities []Entity

				if isEntity(arg) {
					// Single entity
					entity := arg.(Entity)
					entities = append(entities, entity)
				} else {
					// Slice of entities
					sliceLen := argValue.Len()
					if sliceLen == 0 {
						continue
					}

					for j := 0; j < sliceLen; j++ {
						elem := argValue.Index(j)

						var entity Entity
						var ok bool

						if elem.Kind() == reflect.Ptr {
							entity, ok = elem.Interface().(Entity)
						} else {
							// Convert value to pointer
							ptrValue := reflect.New(elem.Type())
							ptrValue.Elem().Set(elem)
							entity, ok = ptrValue.Interface().(Entity)
						}

						if !ok {
							return fmt.Errorf("slice element %d is not an Entity", j)
						}

						entities = append(entities, entity)
					}
				}

				// Update entities using change tracking
				for _, entity := range entities {
					// Get changes
					changes := GetChanges(entity)
					if len(changes) == 0 {
						continue // No changes to update
					}

					// Get schema
					schema, err := GetSchema(entity)
					if err != nil {
						return fmt.Errorf("failed to get schema for entity: %w", err)
					}

					// Build UPDATE statement
					tableName := entity.TableName()
					var setClauses []string
					var values []any

					for fieldName, newValue := range changes {
						if field, exists := schema.Fields[fieldName]; exists {
							// Skip non-updatable fields
							if field.PrimaryKey || field.AutoIncrement {
								continue
							}

							columnName := field.GetColumnName()
							setClauses = append(setClauses, fmt.Sprintf("%s = ?", columnName))
							values = append(values, newValue)
						}
					}

					if len(setClauses) == 0 {
						continue // No updatable changes
					}

					// Add WHERE clause for primary key
					pkColumn, pkValue := entity.PrimaryKey()
					whereClause := fmt.Sprintf("%s = ?", pkColumn)
					values = append(values, pkValue)

					// Execute UPDATE
					sql := fmt.Sprintf("UPDATE %s SET %s WHERE %s",
						tableName,
						strings.Join(setClauses, ", "),
						whereClause)

					if ctx.debugMode {
						log.Printf("Update by entity SQL: %s\n Values: %v", sql, values)
					}
					result, err := tx.exec(sql, values...)
					if err != nil {
						return fmt.Errorf("failed to update entity: %w", err)
					}

					affected, err := result.RowsAffected()
					if err != nil {
						return fmt.Errorf("failed to get affected rows: %w", err)
					}

					totalAffected += affected

					// Clear changes after successful update
					if trackable, ok := entity.(ChangeTrackable); ok {
						trackable.ClearChanges()
					}
				}

			// Case 2: Lambda function with conditional logic
			case argType.Kind() == reflect.Func:
				// Validate function signature: func(T) with no return
				if argType.NumIn() != 1 || argType.NumOut() != 0 {
					return fmt.Errorf("conditional function at argument %d must have signature func(T)", i)
				}

				entityType := argType.In(0)
				if entityType.Kind() == reflect.Ptr {
					return fmt.Errorf("conditional function at argument %d parameter must be value type, not pointer", i)
				}

				// Create temporary entity to get table name
				tempEntity := reflect.New(entityType).Interface()
				entity, ok := tempEntity.(Entity)
				if !ok {
					return fmt.Errorf("type %v does not implement Entity interface", entityType)
				}

				tableName := entity.TableName()

				// Parse conditional function to extract conditional updates
				conditionalUpdates, err := tx.executor.ParseConditionalUpdates(arg)
				if err != nil {
					return fmt.Errorf("failed to parse conditional function at argument %d: %w", i, err)
				}

				// Execute each conditional update
				for _, update := range conditionalUpdates {
					sql := fmt.Sprintf("UPDATE %s SET %s WHERE %s",
						tableName,
						strings.Join(update.SetClauses, ", "),
						update.WhereClause)

					if ctx.debugMode {
						log.Printf("Update by lambda func SQL: %s\n Values: %v", sql, update.Values)
					}
					result, err := tx.exec(sql, update.Values...)
					if err != nil {
						return fmt.Errorf("failed to execute conditional update: %w", err)
					}

					affected, err := result.RowsAffected()
					if err != nil {
						return fmt.Errorf("failed to get affected rows: %w", err)
					}

					totalAffected += affected
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

// Helper function to check if arg is an Entity
func isEntity(arg any) bool {
	_, ok := arg.(Entity)
	return ok
}

// Delete handles single entity, slice of entities, or predicate-based deletion
func (ctx *ColtContext) Delete(args ...any) (int64, error) {
	if len(args) == 0 {
		return 0, fmt.Errorf("Delete requires at least one argument")
	}

	if len(args) > 1 {
		return 0, fmt.Errorf("Delete requires exactly one argument")
	}

	// Get the argument
	arg := args[0]
	argType := reflect.TypeOf(arg)
	argValue := reflect.ValueOf(arg)

	// Case 1: Entity or slice of entities
	if entity, ok := arg.(Entity); ok {
		// Single entity - convert to slice
		arg = []Entity{entity}
		argValue = reflect.ValueOf(arg)
	}

	if argValue.Kind() == reflect.Slice {
		// Handle slice of entities (either original slice or converted single entity)
		sliceLen := argValue.Len()
		if sliceLen == 0 {
			return 0, nil
		}

		// Collect all entities and their primary keys
		type entityKey struct {
			entity    Entity
			tableName string
			pkColumn  string
			pkValue   any
		}

		entityKeys := make([]entityKey, 0, sliceLen)

		// Verify all elements are entities and collect their keys
		for i := 0; i < sliceLen; i++ {
			elem := argValue.Index(i).Interface()
			entity, ok := elem.(Entity)
			if !ok {
				return 0, fmt.Errorf("slice element %d is not an Entity", i)
			}

			pkColumn, pkValue := entity.PrimaryKey()
			if pkValue == nil {
				return 0, fmt.Errorf("entity %d has nil primary key", i)
			}

			entityKeys = append(entityKeys, entityKey{
				entity:    entity,
				tableName: entity.TableName(),
				pkColumn:  pkColumn,
				pkValue:   pkValue,
			})
		}

		// Group entities by table for efficient deletion
		tableGroups := make(map[string][]entityKey)
		for _, ek := range entityKeys {
			tableGroups[ek.tableName] = append(tableGroups[ek.tableName], ek)
		}

		var totalAffected int64

		// Use transaction for multiple deletes
		err := ctx.Transaction(func(tx *ColtContext) error {
			for tableName, group := range tableGroups {
				if len(group) == 1 {
					// Single entity deletion
					sql := fmt.Sprintf("DELETE FROM %s WHERE %s = ?",
						tableName,
						group[0].pkColumn,
					)

					result, err := tx.exec(sql, group[0].pkValue)
					if err != nil {
						return fmt.Errorf("failed to delete entity from %s: %w", tableName, err)
					}

					affected, err := result.RowsAffected()
					if err != nil {
						return err
					}
					totalAffected += affected

				} else {
					// Batch deletion using IN clause
					// Collect all primary key values
					pkValues := make([]any, len(group))
					placeholders := make([]string, len(group))

					for i, ek := range group {
						pkValues[i] = ek.pkValue
						placeholders[i] = "?"
					}

					sql := fmt.Sprintf("DELETE FROM %s WHERE %s IN (%s)",
						tableName,
						group[0].pkColumn, // All entities in group have same pk column
						strings.Join(placeholders, ", "),
					)

					result, err := tx.exec(sql, pkValues...)
					if err != nil {
						return fmt.Errorf("failed to batch delete from %s: %w", tableName, err)
					}

					affected, err := result.RowsAffected()
					if err != nil {
						return err
					}
					totalAffected += affected
				}

				// Call any delete hooks if entities implement them
				for _, ek := range group {
					if deletable, ok := ek.entity.(interface{ OnDelete() }); ok {
						deletable.OnDelete()
					}
				}
			}

			return nil
		})

		return totalAffected, err
	}

	// Case 2: Predicate-based deletion
	if argType.Kind() == reflect.Func {
		// Validate predicate function signature
		if argType.NumIn() != 1 || argType.NumOut() != 1 {
			return 0, fmt.Errorf("predicate must have signature func(T) bool")
		}

		if argType.Out(0).Kind() != reflect.Bool {
			return 0, fmt.Errorf("predicate must return bool")
		}

		// Get entity type from predicate parameter
		entityType := argType.In(0)

		// Create a temporary instance to get table name
		tempEntity := reflect.New(entityType).Interface()
		entity, ok := tempEntity.(Entity)
		if !ok {
			return 0, fmt.Errorf("type %v does not implement Entity interface", entityType)
		}

		tableName := entity.TableName()

		// Parse predicate to SQL WHERE clause
		whereClause := ctx.executor.ParsePredicate(arg)

		// Build DELETE statement
		sql := fmt.Sprintf("DELETE FROM %s WHERE %s", tableName, whereClause)

		// Log in debug mode
		if ctx.debugMode {
			log.Printf("🗑️ Batch delete SQL: %s", sql)
		}

		// Execute batch delete
		result, err := ctx.exec(sql)
		if err != nil {
			return 0, fmt.Errorf("failed to execute batch delete: %w", err)
		}

		affected, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}

		// Log affected rows in debug mode
		if ctx.debugMode {
			log.Printf("🗑️ Deleted %d rows from %s", affected, tableName)
		}

		return affected, nil
	}

	return 0, fmt.Errorf("Delete: unsupported argument type %T", arg)
}

// Transaction support
func (ctx *ColtContext) Transaction(fn func(*ColtContext) error) error {
	tx, err := ctx.db.BeginTx(ctx.ctx, nil)
	if err != nil {
		return err
	}

	txCtx := &ColtContext{
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

// CreateTables creates database tables for all registered models
func (ctx *ColtContext) CreateTables(models ...Entity) error {
	return ctx.Transaction(func(tx *ColtContext) error {
		for _, model := range models {
			// Register the model first
			if err := Register(model); err != nil {
				return fmt.Errorf("failed to register model %T: %w", model, err)
			}

			// Get schema
			schema, err := GetSchema(model)
			if err != nil {
				return fmt.Errorf("failed to get schema for %T: %w", model, err)
			}

			// Generate CREATE TABLE SQL
			sql := ctx.generateCreateTableSQL(schema)

			// Execute CREATE TABLE
			if _, err := tx.exec(sql); err != nil {
				return fmt.Errorf("failed to create table %s: %w", schema.TableName, err)
			}

			// Create indexes
			for _, index := range schema.Indexes {
				indexSQL := ctx.generateCreateIndexSQL(schema.TableName, index)
				if _, err := tx.exec(indexSQL); err != nil {
					return fmt.Errorf("failed to create index %s: %w", index.Name, err)
				}
			}
		}
		return nil
	})
}

// generateCreateTableSQL generates CREATE TABLE statement from schema
func (ctx *ColtContext) generateCreateTableSQL(schema *TableSchema) string {
	var parts []string
	var primaryKeys []string

	// Sort fields for consistent output
	var fieldNames []string
	for name := range schema.Fields {
		fieldNames = append(fieldNames, name)
	}
	sort.Strings(fieldNames)

	// Generate column definitions
	for _, fieldName := range fieldNames {
		field := schema.Fields[fieldName]
		if field.Ignore {
			continue
		}

		columnDef := ctx.generateColumnDefinition(field)
		parts = append(parts, columnDef)

		if field.PrimaryKey {
			primaryKeys = append(primaryKeys, field.GetColumnName())
		}
	}

	// Add primary key constraint if multiple columns
	if len(primaryKeys) > 1 {
		parts = append(parts, fmt.Sprintf("PRIMARY KEY (%s)", strings.Join(primaryKeys, ", ")))
	}

	return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n  %s\n)",
		schema.TableName,
		strings.Join(parts, ",\n  "))
}

// generateColumnDefinition generates column definition from field schema
func (ctx *ColtContext) generateColumnDefinition(field *FieldSchema) string {
	var parts []string

	// Column name and type
	columnName := field.GetColumnName()
	dbType := field.GetDBType()

	// Handle size/precision for types
	if field.Size > 0 {
		dbType = fmt.Sprintf("%s(%d)", dbType, field.Size)
	} else if field.Precision > 0 {
		if field.Scale > 0 {
			dbType = fmt.Sprintf("%s(%d,%d)", dbType, field.Precision, field.Scale)
		} else {
			dbType = fmt.Sprintf("%s(%d)", dbType, field.Precision)
		}
	}

	parts = append(parts, columnName, dbType)

	// Primary key (for single column)
	if field.PrimaryKey {
		parts = append(parts, "PRIMARY KEY")
	}

	// Auto increment
	if field.AutoIncrement {
		parts = append(parts, "AUTOINCREMENT")
	}

	// Nullable
	if !field.IsNullable() {
		parts = append(parts, "NOT NULL")
	}

	// Unique
	if field.Unique {
		parts = append(parts, "UNIQUE")
	}

	// Default value
	if field.Default != nil {
		defaultVal := field.GetDefault()
		if defaultVal != "" {
			parts = append(parts, fmt.Sprintf("DEFAULT %s", defaultVal))
		}
	}

	// Check constraint
	if field.Check != "" {
		parts = append(parts, fmt.Sprintf("CHECK (%s)", field.Check))
	}

	return strings.Join(parts, " ")
}

// generateCreateIndexSQL generates CREATE INDEX statement
func (ctx *ColtContext) generateCreateIndexSQL(tableName string, index *IndexSchema) string {
	indexType := "INDEX"
	if index.Unique {
		indexType = "UNIQUE INDEX"
	}

	fields := index.Fields
	if len(fields) == 0 {
		return ""
	}

	sql := fmt.Sprintf("CREATE %s IF NOT EXISTS %s ON %s (%s)",
		indexType,
		index.Name,
		tableName,
		strings.Join(fields, ", "))

	if index.Where != "" {
		sql += fmt.Sprintf(" WHERE %s", index.Where)
	}

	return sql
}
