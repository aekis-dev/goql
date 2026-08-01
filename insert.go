package goql

import (
	"fmt"
	"log"
	"reflect"

	"github.com/aekis-dev/goql/models"
	"github.com/aekis-dev/goql/query"
)

// schemaOfParam resolves the registered model behind a lambda's nth parameter.
func schemaOfParam(funcType reflect.Type, index int) (*models.Model, error) {
	paramType := funcType.In(index)
	if paramType.Kind() == reflect.Ptr {
		paramType = paramType.Elem()
	}
	entity, ok := reflect.New(paramType).Interface().(models.Entity)
	if !ok {
		return nil, fmt.Errorf("type %v does not implement Entity interface", paramType)
	}
	schema, err := models.GetModel(entity)
	if err != nil {
		return nil, fmt.Errorf("failed to get models: %w", err)
	}
	return schema, nil
}

// isEntityParam reports whether a lambda's nth parameter is a pointer to a model.
func isEntityParam(funcType reflect.Type, index int) bool {
	if funcType.NumIn() <= index {
		return false
	}
	paramType := funcType.In(index)
	if paramType.Kind() != reflect.Ptr {
		return false
	}
	_, ok := reflect.New(paramType.Elem()).Interface().(models.Entity)
	return ok
}

// insertAny executes an Insert lambda: one INSERT … SELECT per assigning branch, inside a
// transaction so an if/else either lands completely or not at all.
func (ctx *Engine) insertAny(fn any) (int64, error) {
	fnType := reflect.TypeOf(fn)

	// Shape is validated at the API boundary; the destination comes from the parsed query,
	// the source from the lambda's second parameter.
	src, err := schemaOfParam(fnType, 1)
	if err != nil {
		return 0, fmt.Errorf("insert source: %w", err)
	}

	parsed, err := ctx.executor.ParseQuery(fn, "Insert")
	if err != nil {
		return 0, fmt.Errorf("failed to parse insert lambda: %w", err)
	}
	if err := checkInsertOptions(parsed.Body.Options); err != nil {
		return 0, err
	}

	inserts, err := ctx.dialect.LambdaInsert(parsed, src, parsed.Body.Options)
	if err != nil {
		return 0, fmt.Errorf("failed to build insert query: %w", err)
	}

	var totalAffected int64
	err = ctx.Transaction(func(tx *Engine) error {
		for _, q := range inserts {
			if tx.debugMode {
				log.Printf("Insert lambda SQL: %s\n Args: %v", q.SQL, q.Args)
			}
			result, err := tx.exec(q.SQL, q.Args...)
			if err != nil {
				return fmt.Errorf("failed to execute insert: %w", err)
			}
			affected, _ := result.RowsAffected()
			totalAffected += affected
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return totalAffected, nil
}

// checkInsertOptions rejects the modifiers that cannot mean anything for an INSERT … SELECT,
// rather than accepting and ignoring them. The projection comes from the assignments, and
// there is no result to preload relations into.
func checkInsertOptions(opts *query.Options) error {
	if opts == nil {
		return nil
	}
	if len(opts.Fields) > 0 {
		return fmt.Errorf("%w: Fields does not apply to Insert — the columns come from the "+
			"lambda's assignments", ErrInvalidOption)
	}
	if opts.PreloadSet {
		return fmt.Errorf("%w: Preload does not apply to Insert — it returns no entities",
			ErrInvalidOption)
	}
	return nil
}

// checkConflictOption rejects goql.Conflict on the calls that cannot honour it. Only Insert
// can, so accepting it elsewhere would silently drop it.
func checkConflictOption(opts *query.Options, call string) error {
	if opts != nil && opts.ConflictIgnore {
		return fmt.Errorf("%w: Conflict applies only to Insert, not %s", ErrInvalidOption, call)
	}
	return nil
}
