package orm

import (
	"crypto/md5"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"runtime"
	"strings"
)

// getExecutor returns the appropriate executor based on build tags
func getExecutor(mode string) QueryExecutor {
	if mode == "compile" {
		return &CompileExecutor{queries: loadPrecompiledQueries()}
	}
	return &DebugExecutor{cache: make(map[string]string)}
}

// QueryExecutor interface (already defined in context.go)
type QueryExecutor interface {
	ParsePredicate(predicate any) string
	ParseAggregation(expr any) string
	ParseConditionalUpdates(fn any) ([]ConditionalUpdate, error)
}

type ConditionalUpdate struct {
	TableName   string
	WhereClause string
	SetClauses  []string
	Values      []any
	JoinInfos   []*JoinInfo
}

// JoinInfo holds information about a join without building the SQL
type JoinInfo struct {
	TargetTable  string       // The table being joined
	SourceTable  string       // The source table (main table)
	JoinType     string       // INNER, LEFT, RIGHT, etc.
	ForeignKey   string       // Foreign key column in source table
	PrimaryKey   string       // Primary key column in target table
	RelationName string       // Name of the relation field
	RelationType RelationType // Type of relation (HasOne, BelongsTo, etc.)
}

// BuildSelectJoin builds JOIN clause for SELECT queries
func (ji *JoinInfo) BuildSelectJoin() string {
	return fmt.Sprintf("%s JOIN %s ON %s.%s = %s.%s",
		ji.JoinType,
		ji.TargetTable,
		ji.SourceTable,
		ji.ForeignKey,
		ji.TargetTable,
		ji.PrimaryKey)
}

// BuildUpdateFrom builds FROM clause for UPDATE queries
func (ji *JoinInfo) BuildUpdateFrom() string {
	return ji.TargetTable
}

// BuildUpdateWhere builds WHERE condition for UPDATE queries
func (ji *JoinInfo) BuildUpdateWhere(targetTable string) string {
	return fmt.Sprintf("%s.%s = %s.%s",
		targetTable,
		ji.ForeignKey,
		ji.TargetTable,
		ji.PrimaryKey)
}

type DebugExecutor struct {
	cache map[string]string
}

// ParseConditionalUpdates extracts conditional updates from lambda function
func (e *DebugExecutor) ParseConditionalUpdates(fn any) ([]ConditionalUpdate, error) {
	// Get function source and parse AST
	source := getFunctionSource(fn)

	// For now, create a simplified AST parser
	// This would need to be implemented to parse the actual function body
	// and extract if statements with conditions and assignments

	funcType := reflect.TypeOf(fn)
	entityType := funcType.In(0)

	// Create temp entity to get table name
	tempEntity := reflect.New(entityType).Interface()
	entity, ok := tempEntity.(Entity)
	if !ok {
		return nil, fmt.Errorf("type %v does not implement Entity interface", entityType)
	}

	tableName := entity.TableName()

	// Parse the function body to extract conditional statements
	updates, err := e.parseConditionalStatements(source, tableName, entityType)
	if err != nil {
		return nil, err
	}

	return updates, nil
}

// parseConditionalStatements parses if statements from function body
func (e *DebugExecutor) parseConditionalStatements(source, tableName string, entityType reflect.Type) ([]ConditionalUpdate, error) {
	// Parse source into AST
	node, err := parser.ParseExpr(source)
	if err != nil {
		return nil, fmt.Errorf("failed to parse function: %w", err)
	}

	var updates []ConditionalUpdate

	// Find function literal
	funcLit := findFuncLit(node)
	if funcLit == nil {
		return nil, fmt.Errorf("no function literal found")
	}

	// Process each statement in function body
	for _, stmt := range funcLit.Body.List {
		if ifStmt, ok := stmt.(*ast.IfStmt); ok {
			update, err := e.processIfStatement(ifStmt, tableName, entityType)
			if err != nil {
				return nil, err
			}
			if update != nil {
				updates = append(updates, *update)
			}
		}
	}

	return updates, nil
}

// processIfStatement converts if statement to ConditionalUpdate with JOIN support
func (e *DebugExecutor) processIfStatement(ifStmt *ast.IfStmt, tableName string, entityType reflect.Type) (*ConditionalUpdate, error) {
	// Analyze the condition to detect required joins
	joinInfos, err := e.analyzeRequiredJoins(ifStmt.Cond, entityType)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze joins: %w", err)
	}

	// Extract WHERE condition from if condition
	whereClause := e.exprToSQL(ifStmt.Cond, entityType)

	// Extract SET clauses from if body assignments
	var setClauses []string
	var values []any

	for _, stmt := range ifStmt.Body.List {
		if assignStmt, ok := stmt.(*ast.AssignStmt); ok {
			// Process assignment: o.Field = value
			if len(assignStmt.Lhs) == 1 && len(assignStmt.Rhs) == 1 {
				if selectorExpr, ok := assignStmt.Lhs[0].(*ast.SelectorExpr); ok {
					fieldName := selectorExpr.Sel.Name
					columnName := toSnakeCase(fieldName)

					// Extract value from right-hand side
					value, err := e.extractValue(assignStmt.Rhs[0])
					if err != nil {
						return nil, err
					}

					setClauses = append(setClauses, fmt.Sprintf("%s = ?", columnName))
					values = append(values, value)
				}
			}
		}
	}

	if len(setClauses) == 0 {
		return nil, nil // No valid assignments found
	}

	return &ConditionalUpdate{
		TableName:   tableName,
		WhereClause: whereClause,
		SetClauses:  setClauses,
		Values:      values,
		JoinInfos:   joinInfos, // Use JoinInfos instead of JoinClauses
	}, nil
}

// analyzeRequiredJoins analyzes an expression to find required joins
func (e *DebugExecutor) analyzeRequiredJoins(expr ast.Expr, entityType reflect.Type) ([]*JoinInfo, error) {
	var joins []*JoinInfo
	var err error

	ast.Inspect(expr, func(n ast.Node) bool {
		if err != nil {
			return false
		}

		// Look for selector expressions like c.Customer.Country
		if sel, ok := n.(*ast.SelectorExpr); ok {
			// Check if this is a relation access (nested selector)
			if innerSel, ok := sel.X.(*ast.SelectorExpr); ok {
				// This is a chained selector like c.Customer.Country
				relationName := innerSel.Sel.Name // "Customer"

				joinInfo, joinErr := e.generateJoinClause(entityType, relationName)
				if joinErr != nil {
					err = joinErr
					return false
				}

				if joinInfo != nil {
					// Check if we already have this join
					found := false
					for _, existing := range joins {
						if existing.SourceTable == joinInfo.SourceTable &&
							existing.TargetTable == joinInfo.TargetTable {
							found = true
							break
						}
					}

					if !found {
						joins = append(joins, joinInfo)
					}
				}
			}
		}

		return true
	})

	return joins, err
}

// generateJoinClause generates join information using schema relations
func (e *DebugExecutor) generateJoinClause(entityType reflect.Type, relationName string) (*JoinInfo, error) {
	// Create temporary entity to get schema
	tempEntity := reflect.New(entityType).Interface()
	entity, ok := tempEntity.(Entity)
	if !ok {
		return nil, fmt.Errorf("type %v does not implement Entity interface", entityType)
	}

	// Get schema for the source entity
	sourceSchema, err := GetSchema(entity)
	if err != nil {
		return nil, fmt.Errorf("failed to get schema for %v: %w", entityType, err)
	}

	// Find the relation in the schema
	relation, exists := sourceSchema.Relations[relationName]
	if !exists {
		return nil, fmt.Errorf("relation %s not found in schema for %v", relationName, entityType)
	}

	sourceTable := sourceSchema.TableName

	// Get the target entity type from the source schema field
	field, fieldExists := sourceSchema.Fields[relationName]
	if !fieldExists {
		return nil, fmt.Errorf("field %s not found in schema for relation", relationName)
	}

	// Get the actual type of the relation field
	targetType := field.GoType
	if targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
	}

	// Create temporary target entity to get its schema
	tempTargetEntity := reflect.New(targetType).Interface()
	targetEntity, ok := tempTargetEntity.(Entity)
	if !ok {
		return nil, fmt.Errorf("target type %v does not implement Entity interface", targetType)
	}

	// Get schema for the target entity
	targetSchema, err := GetSchema(targetEntity)
	if err != nil {
		return nil, fmt.Errorf("failed to get target schema for %v: %w", targetType, err)
	}

	targetTable := targetSchema.TableName

	// Build join info based on relation type
	joinInfo := &JoinInfo{
		SourceTable: sourceTable,
		TargetTable: targetTable,
		ForeignKey:  sourceSchema.Fields[relation.ForeignKey].GetColumnName(),
		PrimaryKey:  targetSchema.PrimaryKey.GetColumnName(),
		JoinType:    "LEFT JOIN", // Default join type
	}

	return joinInfo, nil
}

// extractValue extracts the actual value from an AST expression
func (e *DebugExecutor) extractValue(expr ast.Expr) (any, error) {
	switch v := expr.(type) {
	case *ast.BasicLit:
		switch v.Kind {
		case token.STRING:
			return strings.Trim(v.Value, `"`), nil
		case token.INT:
			return v.Value, nil
		case token.FLOAT:
			return v.Value, nil
		default:
			return v.Value, nil
		}
	case *ast.Ident:
		// Handle boolean literals
		switch v.Name {
		case "true":
			return true, nil
		case "false":
			return false, nil
		default:
			return v.Name, nil
		}
	default:
		return fmt.Sprintf("%v", expr), nil
	}
}

// ParsePredicate converts a lambda function into a SQL WHERE clause
func (e *DebugExecutor) ParsePredicate(predicate any) string {
	// Extract the function source code
	source := getFunctionSource(predicate)

	// Parse the source code into an AST
	node, err := parser.ParseExpr(source)
	if err != nil {
		panic(fmt.Sprintf("failed to parse predicate: %v", err))
	}

	// Handle different AST node types
	switch n := node.(type) {
	case *ast.FuncLit:
		// Direct function literal
		//return e.astToSQL(n)
	case *ast.Ident:
		// Variable reference - this shouldn't happen for inline lambdas
		panic(fmt.Sprintf("unexpected identifier in predicate: %s", n.Name))
	default:
		// Try to find the function literal within the expression
		if funcLit := findFuncLit(n); funcLit != nil {
			//return e.astToSQL(funcLit)
		}
		panic(fmt.Sprintf("unsupported predicate AST node type: %T", n))
	}
	return ""
}

// findFuncLit recursively searches for a function literal in an AST node
func findFuncLit(node ast.Node) *ast.FuncLit {
	var funcLit *ast.FuncLit

	ast.Inspect(node, func(n ast.Node) bool {
		if fl, ok := n.(*ast.FuncLit); ok {
			funcLit = fl
			return false // Stop searching
		}
		return true // Continue searching
	})

	return funcLit
}

// ParseAggregation parses aggregation expressions
func (e *DebugExecutor) ParseAggregation(expr any) string {
	// This would parse expressions like Sum(c.Orders.TotalPrice)
	// For now, returning placeholder
	return "SUM(total_price)"
}

// astToSQL converts an AST function literal to a SQL WHERE clause
func (e *DebugExecutor) astToSQL(fn *ast.FuncLit, entityType reflect.Type) string {
	// Extract the body of the function
	body := fn.Body.List
	if len(body) != 1 {
		panic("predicate function must have a single return statement")
	}

	// Handle the return statement
	retStmt, ok := body[0].(*ast.ReturnStmt)
	if !ok {
		panic("predicate function must return a value")
	}

	// Convert the return expression to SQL
	return e.exprToSQL(retStmt.Results[0], entityType)
}

// exprToSQL converts an AST expression to a SQL fragment
func (e *DebugExecutor) exprToSQL(expr ast.Expr, entityType reflect.Type) string {
	switch v := expr.(type) {
	case *ast.BinaryExpr:
		// Handle binary expressions (e.g., a == b, a && b)
		left := e.exprToSQL(v.X, entityType)
		right := e.exprToSQL(v.Y, entityType)
		op := e.mapOperator(v.Op)
		return fmt.Sprintf("(%s %s %s)", left, op, right)

	case *ast.Ident:
		// Handle identifiers (e.g., field names)
		return v.Name

	case *ast.SelectorExpr:
		// Build the field path
		path := e.buildFieldPath(v)

		if len(path) == 0 {
			return v.Sel.Name
		}

		// Get entity schema
		tempEntity := reflect.New(entityType).Interface()
		entity, ok := tempEntity.(Entity)
		if !ok {
			return v.Sel.Name // Fallback
		}

		schema, err := GetSchema(entity)
		if err != nil {
			return v.Sel.Name // Fallback
		}

		// For simple field access (e.g., o.Name)
		if len(path) == 1 {
			fieldName := path[0]
			if field, exists := schema.Fields[fieldName]; exists {
				return field.GetColumnName()
			}
			return fieldName // Fallback
		}

		// For relation field access (e.g., o.Customer.Country)
		if len(path) == 2 {
			relationName := path[0]
			fieldName := path[1]

			// Get relation field from schema
			if relationField, exists := schema.Fields[relationName]; exists {
				// Get target entity type
				targetType := relationField.GoType
				if targetType.Kind() == reflect.Ptr {
					targetType = targetType.Elem()
				}

				// Get target schema
				tempTargetEntity := reflect.New(targetType).Interface()
				if targetEntity, ok := tempTargetEntity.(Entity); ok {
					if targetSchema, err := GetSchema(targetEntity); err == nil {
						if field, exists := targetSchema.Fields[fieldName]; exists {
							return fmt.Sprintf("%s.%s", targetSchema.TableName, field.GetColumnName())
						}
					}
				}
			}
		}

		return v.Sel.Name // Fallback

	case *ast.BasicLit:
		// Handle basic literals (e.g., strings, numbers)
		return v.Value

	default:
		panic(fmt.Sprintf("unsupported expression type: %T", expr))
	}
}

// buildFieldPath recursively builds the field access path
func (e *DebugExecutor) buildFieldPath(expr ast.Expr) []string {
	switch v := expr.(type) {
	case *ast.SelectorExpr:
		// Recursively build path: parent.field
		parentPath := e.buildFieldPath(v.X)
		return append(parentPath, v.Sel.Name)
	case *ast.Ident:
		// Base case: just the identifier
		if v.Name == "o" || v.Name == "c" { // Skip parameter names
			return []string{}
		}
		return []string{v.Name}
	default:
		return []string{}
	}
}

// mapOperator maps Go operators to SQL operators
func (e *DebugExecutor) mapOperator(op token.Token) string {
	switch op {
	case token.EQL:
		return "="
	case token.LSS:
		return "<"
	case token.GTR:
		return ">"
	case token.NEQ:
		return "!="
	case token.LEQ:
		return "<="
	case token.GEQ:
		return ">="
	case token.LAND:
		return "AND"
	case token.LOR:
		return "OR"
	default:
		panic(fmt.Sprintf("unsupported operator: %s", op))
	}
}

// getColumnName maps Go field name to database column
func (e *DebugExecutor) getColumnName(entityType reflect.Type, fieldName string) string {
	// Get schema for entity type
	tempEntity := reflect.New(entityType).Interface()
	if entity, ok := tempEntity.(Entity); ok {
		schema, err := GetSchema(entity)
		if err == nil {
			if field, exists := schema.Fields[fieldName]; exists {
				return field.GetColumnName()
			}
		}
	}

	// Fallback to snake_case
	return toSnakeCase(fieldName)
}

// handleSum handles Sum aggregation
func (e *DebugExecutor) handleSum(call *ast.CallExpr, entityType reflect.Type) string {
	if len(call.Args) < 2 {
		return "0"
	}

	// First arg is collection, second is field name
	if lit, ok := call.Args[1].(*ast.BasicLit); ok && lit.Kind == token.STRING {
		fieldName := strings.Trim(lit.Value, `"`)
		return fmt.Sprintf("SUM(%s)", toSnakeCase(fieldName))
	}

	return "0"
}

// handleCount handles Count aggregation
func (e *DebugExecutor) handleCount(call *ast.CallExpr, entityType reflect.Type) string {
	return "COUNT(*)"
}

// handleAny handles Any (EXISTS) subqueries
func (e *DebugExecutor) handleAny(call *ast.CallExpr, entityType reflect.Type) string {
	// This would handle expressions like:
	// c.Orders.Any(func(o Order) bool { return o.Total > 100 })
	return "EXISTS (SELECT 1 FROM orders WHERE customer_id = customers.id AND total > 100)"
}

// handleAll handles All (NOT EXISTS with negation) subqueries
func (e *DebugExecutor) handleAll(call *ast.CallExpr, entityType reflect.Type) string {
	return "NOT EXISTS (SELECT 1 FROM orders WHERE customer_id = customers.id AND NOT (total > 100))"
}

// getFunctionSource extracts function source code (simplified)
func getFunctionSource(fn any) string {
	fnValue := reflect.ValueOf(fn)
	if fnValue.Kind() != reflect.Func {
		return ""
	}

	// Get function pointer
	fnPtr := fnValue.Pointer()

	// Get runtime function info
	runtimeFunc := runtime.FuncForPC(fnPtr)
	if runtimeFunc == nil {
		return ""
	}

	// Get file and line info
	file, startLine := runtimeFunc.FileLine(fnPtr)

	// Read the source file
	content, err := os.ReadFile(file)
	if err != nil {
		return ""
	}

	lines := strings.Split(string(content), "\n")
	if startLine <= 0 || startLine > len(lines) {
		return ""
	}

	// Find the function definition starting from startLine
	var funcLines []string
	braceCount := 0
	parenCount := 0
	inFunction := false

	// Look for the function starting point (may need to go back a few lines)
	start := startLine - 1
	for i := start; i >= 0 && i >= start-10; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.Contains(line, "func(") {
			start = i
			break
		}
	}

	// Extract the function body
	for i := start; i < len(lines); i++ {
		line := lines[i]

		// Count braces and parentheses to find function boundaries
		for j, char := range line {
			switch char {
			case '(':
				parenCount++
			case ')':
				parenCount--
			case '{':
				braceCount++
				inFunction = true
			case '}':
				braceCount--
				if inFunction && braceCount == 0 {
					// Include the closing brace but stop here
					funcLines = append(funcLines, line[:j+1])
					result := strings.Join(funcLines, "\n")

					// Clean up trailing comma or other syntax from function call
					result = strings.TrimSpace(result)
					if strings.HasSuffix(result, ",") {
						result = result[:len(result)-1]
					}
					if strings.HasSuffix(result, ")") && !strings.Contains(result, "func(") {
						// Remove trailing parenthesis if it's not part of the function signature
						lastFunc := strings.LastIndex(result, "func(")
						if lastFunc != -1 {
							afterFunc := result[lastFunc:]
							if !strings.Contains(afterFunc, ")") {
								result = result[:len(result)-1]
							}
						}
					}

					return result
				}
			case ',':
				// If we're at the end of the function and find a comma, it's likely trailing syntax
				if inFunction && braceCount == 0 && parenCount == 0 {
					funcLines = append(funcLines, line[:j])
					result := strings.Join(funcLines, "\n")
					return strings.TrimSpace(result)
				}
			}
		}

		// If we haven't found the end yet, add the whole line
		if inFunction && braceCount > 0 {
			funcLines = append(funcLines, line)
		} else if !inFunction {
			funcLines = append(funcLines, line)
		}
	}

	result := strings.Join(funcLines, "\n")

	// Final cleanup - remove trailing comma if present
	result = strings.TrimSpace(result)
	if strings.HasSuffix(result, ",") {
		result = result[:len(result)-1]
	}

	return result
}

type CompileExecutor struct {
	queries map[string]string
}

// This would be in a generated file
var precompiledConditionalUpdates = map[string][]ConditionalUpdate{
	"a1b2c3d4": {
		{
			TableName:   "customers",
			WhereClause: "age > 40 AND status != 'Premium'",
			SetClauses:  []string{"status = ?", "discount = ?"},
			Values:      []any{"Senior", 0.15},
		},
	},
	// ... more pre-compiled conditional updates
}

// ParseConditionalUpdates returns pre-compiled conditional updates
func (e *CompileExecutor) ParseConditionalUpdates(fn any) ([]ConditionalUpdate, error) {
	key := getPredicateKey(fn)

	// In production, conditional updates would be pre-compiled
	// For now, return a placeholder
	if updates, exists := precompiledConditionalUpdates[key]; exists {
		return updates, nil
	}

	panic(fmt.Sprintf("Conditional update not pre-compiled. Key: %s\nRun 'go generate' to compile queries", key))
}

//func (e *CompileExecutor) ParseQuery[T Entity](predicate func(T) bool) string {
//	return e.ParsePredicate(predicate)
//}

// ParsePredicate returns pre-compiled SQL for the predicate
func (e *CompileExecutor) ParsePredicate(predicate any) string {
	key := getPredicateKey(predicate)

	if sql, exists := e.queries[key]; exists {
		return sql
	}

	// Panic in production if query not pre-compiled
	panic(fmt.Sprintf("Query not pre-compiled. Key: %s\nRun 'go generate' to compile queries", key))
}

// ParseAggregation returns pre-compiled aggregation SQL
func (e *CompileExecutor) ParseAggregation(expr any) string {
	// In production, aggregations would also be pre-compiled
	return "SUM(total_price)"
}

// getPredicateKey generates unique key for predicate function
func getPredicateKey(fn any) string {
	pc := reflect.ValueOf(fn).Pointer()
	f := runtime.FuncForPC(pc)
	file, line := f.FileLine(pc)

	h := md5.New()
	h.Write([]byte(fmt.Sprintf("%s:%d", file, line)))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// loadPrecompiledQueries loads pre-generated queries
func loadPrecompiledQueries() map[string]string {
	// This would be generated by go generate
	return precompiledQueries
}

// This would be in a generated file
var precompiledQueries = map[string]string{
	"a1b2c3d4": "country = 'USA' AND status = 'active'",
	"e5f6g7h8": "age > 18",
	// ... more pre-compiled queries
}
