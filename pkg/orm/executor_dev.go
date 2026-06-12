//go:build !prod

package orm

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"runtime"
	"strings"

	"github.com/aekis-dev/goql/pkg/models"
	"github.com/aekis-dev/goql/pkg/query"
)

// getExecutor returns the appropriate executor based on build tags
func getExecutor() QueryExecutor {
	return &DebugExecutor{cache: make(map[string]string)}
}

type DebugExecutor struct {
	cache map[string]string
}

// parseContext holds state threaded through the single-pass parse
type parseContext struct {
	schema    *models.Model
	paramName string
	sentinels map[string]*rangeSentinel
}

func newParseContext(schema *models.Model, paramName string) *parseContext {
	return &parseContext{
		schema:    schema,
		paramName: paramName,
		sentinels: make(map[string]*rangeSentinel),
	}
}

type rangeSentinel struct {
	relationRef *query.FieldRef
	condition   *query.ParseNode
}

// extractValue extracts the actual value from an AST expression
func (e *DebugExecutor) extractValue(expr ast.Expr, schema *models.Model, paramName string) (any, bool, error) {
	switch v := expr.(type) {

	case *ast.BasicLit:
		switch v.Kind {
		case token.STRING:
			inner := strings.Trim(v.Value, `"`)
			return inner, false, nil
		case token.INT:
			// Parse to int64 so emitter produces 1000 not "1000"
			var n int64
			fmt.Sscanf(v.Value, "%d", &n)
			return n, false, nil
		case token.FLOAT:
			// Parse to float64 so emitter produces 0.15 not "0.15"
			var f float64
			fmt.Sscanf(v.Value, "%f", &f)
			return f, false, nil
		default:
			return v.Value, false, nil
		}

	case *ast.Ident:
		// Handle boolean literals
		switch v.Name {
		case "true":
			return true, false, nil
		case "false":
			return false, false, nil
		default:
			return v.Name, false, nil
		}

	case *ast.SelectorExpr:
		// Field reference: c.Login, c.Customer.Country
		path := e.buildFieldPath(v, paramName)
		if len(path) == 0 {
			return nil, false, fmt.Errorf("could not resolve field path from expression")
		}

		// Simple field on this entity: c.Login → login
		if len(path) == 1 {
			if schema != nil {
				if field, exists := schema.Fields[path[0]]; exists {
					return fmt.Sprintf("%s.%s", schema.TableName, field.GetColumnName()), true, nil
				}
			}
			return toSnakeCase(path[0]), true, nil
		}

		// Relation field: c.Customer.Country → customers.country
		if len(path) == 2 {
			relationName, fieldName := path[0], path[1]
			tempEntity := reflect.New(entityType).Interface()
			if entity, ok := tempEntity.(models.Entity); ok {
				if schema, err := models.GetModel(entity); err == nil {
					if relationField, exists := schema.Fields[relationName]; exists {
						targetType := relationField.TargetModel()
						tempTarget := reflect.New(targetType).Interface()
						if targetEntity, ok := tempTarget.(models.Entity); ok {
							if targetSchema, err := models.GetModel(targetEntity); err == nil {
								if field, exists := targetSchema.Fields[fieldName]; exists {
									col := fmt.Sprintf("%s.%s", targetSchema.TableName, field.GetColumnName())
									return col, true, nil
								}
							}
						}
					}
				}
			}
		}

		return nil, false, fmt.Errorf("could not resolve field path: %v", path)

	case *ast.UnaryExpr:
		// Handle negation: -0.15
		val, isCol, err := e.extractValue(expr, schema, paramName)
		if err != nil {
			return nil, false, err
		}
		if isCol {
			return nil, false, fmt.Errorf("cannot negate a column reference")
		}
		return fmt.Sprintf("%s%v", v.Op, val), false, nil

	default:
		return nil, false, fmt.Errorf("unsupported assignment value type: %T", expr)
	}
}

func (e *DebugExecutor) ParseBody(fn any) (*query.ParseBody, error) {
	source := getFunctionSource(fn)
	funcLit, err := parseFuncLit(source)
	if err != nil {
		return nil, fmt.Errorf("failed to parse function: %w", err)
	}

	funcType := reflect.TypeOf(fn)
	entityType := funcType.In(0)
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
		return nil, fmt.Errorf("failed to get models: %w", err)
	}

	pctx := newParseContext(schema, funcParamName(funcLit))
	return e.parseBody(funcLit, pctx)
}

func (e *DebugExecutor) ParseBodyFromSource(source string) (*query.ParseBody, error) {
	funcLit, err := parseFuncLit(source)
	if err != nil {
		return nil, fmt.Errorf("failed to parse function: %w", err)
	}

	entity, err := resolveEntityTypeFromFuncLit(funcLit)
	if err != nil {
		return nil, err
	}
	schema, err := models.GetModel(entity)
	if err != nil {
		return nil, err
	}

	pctx := newParseContext(schema, funcParamName(funcLit))
	return e.parseBody(funcLit, pctx)
}

func (e *DebugExecutor) parseBody(funcLit *ast.FuncLit, pctx *parseContext) (*query.ParseBody, error) {
	result := &query.ParseBody{}
	var conditionRoots []*query.ParseNode

	for _, stmt := range funcLit.Body.List {
		switch s := stmt.(type) {

		case *ast.AssignStmt:
			e.trackSentinelDecl(s, pctx)
			assignment, relAssignment, err := e.tryParseAssignment(s, pctx)
			if err != nil {
				return nil, err
			}
			if assignment != nil {
				result.Assignments = append(result.Assignments, assignment)
			}
			if relAssignment != nil {
				result.RelationAssignments = append(result.RelationAssignments, relAssignment)
			}

		case *ast.RangeStmt:
			joinNode, err := e.parseRangeStmt(s, pctx)
			if err != nil {
				return nil, err
			}
			if joinNode != nil {
				conditionRoots = append(conditionRoots, joinNode)
			}

		case *ast.IfStmt:
			ifResult, err := e.parseIfBlock(s, pctx)
			if err != nil {
				return nil, err
			}
			if ifResult != nil {
				if ifResult.condition != nil {
					conditionRoots = append(conditionRoots, ifResult.condition)
				}
				result.Assignments = append(result.Assignments, ifResult.assignments...)
				result.RelationAssignments = append(result.RelationAssignments, ifResult.relAssignments...)
			}

		case *ast.ReturnStmt:
			if len(s.Results) != 1 || e.isAlwaysFalse(s.Results[0]) {
				continue
			}
			if e.isAlwaysTrue(s.Results[0]) {
				continue
			}
			node, err := e.exprToCondition(s.Results[0], pctx)
			if err != nil {
				return nil, err
			}
			conditionRoots = append(conditionRoots, node)
		}
	}

	switch len(conditionRoots) {
	case 0:
		result.Condition = nil
	case 1:
		result.Condition = conditionRoots[0]
	default:
		result.Condition = &query.ParseNode{LogicalOp: "OR", Children: conditionRoots}
	}

	return result, nil
}

type ifBlockResult struct {
	condition      *query.ParseNode
	assignments    []*query.ParseAssign
	relAssignments []*query.ParseRelation
}

func (e *DebugExecutor) parseIfBlock(ifStmt *ast.IfStmt, pctx *parseContext) (*ifBlockResult, error) {
	cond, err := e.exprToCondition(ifStmt.Cond, pctx)
	if err != nil {
		return nil, err
	}

	hasReturn := e.returnsTrue(ifStmt.Body)
	assignments, relAssignments, err := e.parseAssignmentList(ifStmt.Body.List, pctx)
	if err != nil {
		return nil, err
	}

	result := &ifBlockResult{}

	if hasReturn || len(assignments) > 0 || len(relAssignments) > 0 {
		result.condition = cond
		result.assignments = assignments
		result.relAssignments = relAssignments
	}

	// Handle else/else-if
	if ifStmt.Else != nil {
		elseResult, err := e.parseElseBlock(ifStmt.Else, pctx)
		if err != nil {
			return nil, err
		}
		if elseResult != nil {
			result.assignments = append(result.assignments, elseResult.assignments...)
			result.relAssignments = append(result.relAssignments, elseResult.relAssignments...)
		}
	}

	return result, nil
}

func (e *DebugExecutor) parseElseBlock(stmt ast.Stmt, pctx *parseContext) (*ifBlockResult, error) {
	switch els := stmt.(type) {
	case *ast.IfStmt:
		return e.parseIfBlock(els, pctx)
	case *ast.BlockStmt:
		assignments, relAssignments, err := e.parseAssignmentList(els.List, pctx)
		if err != nil {
			return nil, err
		}
		return &ifBlockResult{
			assignments:    assignments,
			relAssignments: relAssignments,
		}, nil
	}
	return nil, nil
}

func (e *DebugExecutor) parseAssignmentList(stmts []ast.Stmt, pctx *parseContext) ([]*query.ParseAssign, []*query.ParseRelation, error) {
	var assignments []*query.ParseAssign
	var relAssignments []*query.ParseRelation

	for _, stmt := range stmts {
		assignStmt, ok := stmt.(*ast.AssignStmt)
		if !ok {
			continue
		}
		a, ra, err := e.tryParseAssignment(assignStmt, pctx)
		if err != nil {
			return nil, nil, err
		}
		if a != nil {
			assignments = append(assignments, a)
		}
		if ra != nil {
			relAssignments = append(relAssignments, ra)
		}
	}
	return assignments, relAssignments, nil
}

func (e *DebugExecutor) tryParseAssignment(s *ast.AssignStmt, pctx *parseContext) (*query.ParseAssign, *query.ParseRelation, error) {
	if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
		return nil, nil, nil
	}
	lhs, ok := s.Lhs[0].(*ast.SelectorExpr)
	if !ok {
		return nil, nil, nil
	}

	fieldRef, err := e.resolveFieldRef(lhs, pctx.schema, pctx.paramName)
	if err != nil {
		return nil, nil, nil // not a models field — skip
	}

	switch fieldRef.Field.RelationKind() {
	case models.M2M, models.O2M:
		pks, err := e.extractRelatedPKs(s.Rhs[0])
		if err != nil {
			return nil, nil, fmt.Errorf("relation field %s: %w", fieldRef.Field.Name, err)
		}
		return nil, &query.ParseRelation{Field: fieldRef, RelatedPKs: pks}, nil

	default:
		valueRef, err := e.resolveValueRef(s.Rhs[0], pctx.schema, pctx.paramName)
		if err != nil {
			return nil, nil, fmt.Errorf("field %s: %w", fieldRef.Field.Name, err)
		}
		return &query.ParseAssign{Field: fieldRef, Value: valueRef}, nil, nil
	}
}

// trackSentinelDecl records boolean variables initialized to false
func (e *DebugExecutor) trackSentinelDecl(s *ast.AssignStmt, pctx *parseContext) {
	if s.Tok != token.DEFINE {
		return
	}
	if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
		return
	}
	ident, ok := s.Lhs[0].(*ast.Ident)
	if !ok {
		return
	}
	if e.isAlwaysFalse(s.Rhs[0]) {
		// register as potential sentinel — condition filled in by parseRangeStmt
		pctx.sentinels[ident.Name] = nil
	}
}

// parseRangeStmt handles: for _, t := range o.Tags { if t.X == val { sentinel = true } }
func (e *DebugExecutor) parseRangeStmt(rangeStmt *ast.RangeStmt, pctx *parseContext) (*query.ParseNode, error) {
	iterVar := ""
	if ident, ok := rangeStmt.Value.(*ast.Ident); ok {
		iterVar = ident.Name
	}
	if iterVar == "" || iterVar == "_" {
		return nil, nil
	}

	relationRef, err := e.resolveFieldRef(rangeStmt.X, pctx.schema, pctx.paramName)
	if err != nil {
		return nil, fmt.Errorf("range expression: %w", err)
	}
	if !relationRef.Field.IsRelation() {
		return nil, fmt.Errorf("range expression must be a relation field, got %s", relationRef.Field.Name)
	}

	targetType := relationRef.Field.TargetModel()
	tempTarget := reflect.New(targetType).Interface()
	targetEntity, ok := tempTarget.(models.Entity)
	if !ok {
		return nil, fmt.Errorf("target type %v does not implement Entity interface", targetType)
	}
	targetSchema, err := models.GetModel(targetEntity)
	if err != nil {
		return nil, fmt.Errorf("failed to get target models: %w", err)
	}

	innerCtx := newParseContext(targetSchema, iterVar)

	for _, stmt := range rangeStmt.Body.List {
		ifStmt, ok := stmt.(*ast.IfStmt)
		if !ok {
			continue
		}

		innerCondition, err := e.exprToCondition(ifStmt.Cond, innerCtx)
		if err != nil {
			return nil, fmt.Errorf("range body condition: %w", err)
		}

		joinNode := &query.ParseNode{
			JoinField: relationRef,
			JoinScope: innerCondition,
		}

		sentinelVar := e.findSentinelAssignment(ifStmt.Body)
		if sentinelVar != "" {
			pctx.sentinels[sentinelVar] = &rangeSentinel{
				relationRef: relationRef,
				condition:   joinNode,
			}
		} else if e.returnsTrue(ifStmt.Body) {
			// direct return true — bubble join node up
			return joinNode, nil
		}
	}

	return nil, nil
}

// findSentinelAssignment finds "varName = true" inside a block, returns varName
func (e *DebugExecutor) findSentinelAssignment(block *ast.BlockStmt) string {
	for _, stmt := range block.List {
		assignStmt, ok := stmt.(*ast.AssignStmt)
		if !ok {
			continue
		}
		if len(assignStmt.Lhs) == 1 && len(assignStmt.Rhs) == 1 {
			if ident, ok := assignStmt.Lhs[0].(*ast.Ident); ok {
				if e.isAlwaysTrue(assignStmt.Rhs[0]) {
					return ident.Name
				}
			}
		}
	}
	return ""
}

func (e *DebugExecutor) exprToCondition(expr ast.Expr, pctx *parseContext) (*query.ParseNode, error) {
	switch v := expr.(type) {
	case *ast.BinaryExpr:
		op := e.mapOperator(v.Op)

		// Logical operators → branch node
		if op == "AND" || op == "OR" {
			left, err := e.exprToCondition(v.X, pctx)
			if err != nil {
				return nil, err
			}
			right, err := e.exprToCondition(v.Y, pctx)
			if err != nil {
				return nil, err
			}
			return &query.ParseNode{
				LogicalOp: op,
				Children:  []*query.ParseNode{left, right},
			}, nil
		}

		// Check if left side is a sentinel variable being compared to true/false
		if ident, ok := v.X.(*ast.Ident); ok {
			if sentinel, ok := pctx.sentinels[ident.Name]; ok && sentinel != nil {
				// urgent_tag == true → use the sentinel condition directly
				// urgent_tag == false → negate it (not supported yet, skip)
				if e.isAlwaysTrue(v.Y) {
					return sentinel.condition, nil
				}
			}
		}

		left, err := e.resolveFieldRef(v.X, pctx.schema, pctx.paramName)
		if err != nil {
			return nil, fmt.Errorf("left side: %w", err)
		}
		right, err := e.resolveValueRef(v.Y, pctx.schema, pctx.paramName)
		if err != nil {
			return nil, fmt.Errorf("right side: %w", err)
		}
		return &query.ParseNode{
			Left:     left,
			Operator: op,
			Right:    right,
		}, nil

	case *ast.Ident:
		// sentinel variable substitution: urgent_tag → its registered join condition
		if sentinel, ok := pctx.sentinels[v.Name]; ok && sentinel != nil {
			return sentinel.condition, nil
		}
		return nil, fmt.Errorf("unexpected identifier %s in condition", v.Name)

	case *ast.ParenExpr:
		return e.exprToCondition(v.X, pctx)

	default:
		return nil, fmt.Errorf("unsupported expression type in condition: %T", expr)
	}
}

// resolveFieldRef resolves an AST expression to a FieldRef using the models
func (e *DebugExecutor) resolveFieldRef(expr ast.Expr, schema *models.Model, paramName string) (*query.FieldRef, error) {
	path := e.buildFieldPath(expr, paramName)

	if len(path) == 0 {
		return nil, fmt.Errorf("could not resolve field path")
	}

	// Simple field: o.Total → path = ["Total"]
	if len(path) == 1 {
		field, exists := schema.Fields[path[0]]
		if !exists {
			return nil, fmt.Errorf("field %s not found in models %s", path[0], schema.TableName)
		}
		return &query.FieldRef{Field: field}, nil
	}

	// Relation field: o.Customer.Country → path = ["Customer", "Country"]
	if len(path) == 2 {
		relationField, exists := schema.Fields[path[0]]
		if !exists {
			return nil, fmt.Errorf("field %s not found in models %s", path[0], schema.TableName)
		}
		if !relationField.IsRelation() {
			return nil, fmt.Errorf("field %s is not a relation field", path[0])
		}

		// Get target models
		targetType := relationField.TargetModel()
		tempTarget := reflect.New(targetType).Interface()
		targetEntity, ok := tempTarget.(models.Entity)
		if !ok {
			return nil, fmt.Errorf("target type %v does not implement Entity", targetType)
		}
		targetSchema, err := models.GetModel(targetEntity)
		if err != nil {
			return nil, fmt.Errorf("failed to get models for %v: %w", targetType, err)
		}

		nestedField, exists := targetSchema.Fields[path[1]]
		if !exists {
			return nil, fmt.Errorf("field %s not found in models %s", path[1], targetSchema.TableName)
		}

		return &query.FieldRef{
			Field:  relationField,
			Nested: &query.FieldRef{Field: nestedField},
		}, nil
	}

	return nil, fmt.Errorf("field path depth > 2 not supported: %v", path)
}

// resolveValueRef resolves an AST expression to a ValueRef
func (e *DebugExecutor) resolveValueRef(expr ast.Expr, schema *models.Model, paramName string) (*query.ValueRef, error) {
	// Check if it's a field reference first
	if _, ok := expr.(*ast.SelectorExpr); ok {
		fieldRef, err := e.resolveFieldRef(expr, schema, paramName)
		if err == nil {
			return &query.ValueRef{IsColumn: true, Field: fieldRef}, nil
		}
	}

	// Otherwise extract as literal
	val, isCol, err := e.extractValue(expr, schema, paramName)
	if err != nil {
		return nil, err
	}
	if isCol {
		// extractValue returned a column string — shouldn't happen here
		// since we checked for SelectorExpr above, but handle gracefully
		return &query.ValueRef{Value: val}, nil
	}
	return &query.ValueRef{Value: val}, nil
}

func (e *DebugExecutor) extractRelatedPKs(expr ast.Expr) ([]any, error) {
	compLit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, fmt.Errorf("expected a slice literal, got %T", expr)
	}

	var pks []any
	for _, elt := range compLit.Elts {
		// Each element is a struct literal: {ID: 1} or just {1}
		elemLit, ok := elt.(*ast.CompositeLit)
		if !ok {
			continue
		}
		for _, field := range elemLit.Elts {
			switch f := field.(type) {
			case *ast.KeyValueExpr:
				// {Model: orm.Model{ID: 1}} — find nested ID
				if innerLit, ok := f.Value.(*ast.CompositeLit); ok {
					for _, innerField := range innerLit.Elts {
						if kv, ok := innerField.(*ast.KeyValueExpr); ok {
							if ident, ok := kv.Key.(*ast.Ident); ok && ident.Name == "ID" {
								val, _, err := e.extractValue(kv.Value, nil, "")
								if err == nil {
									pks = append(pks, val)
								}
							}
						}
					}
				}
			}
		}
	}

	return pks, nil
}

// returnsTrue checks if a block statement returns true
func (e *DebugExecutor) returnsTrue(block *ast.BlockStmt) bool {
	for _, stmt := range block.List {
		if retStmt, ok := stmt.(*ast.ReturnStmt); ok {
			if len(retStmt.Results) == 1 {
				return e.isAlwaysTrue(retStmt.Results[0])
			}
		}
	}
	return false
}

// isAlwaysTrue checks if an expression always evaluates to true
func (e *DebugExecutor) isAlwaysTrue(expr ast.Expr) bool {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name == "true"
	case *ast.BasicLit:
		return v.Kind == token.INT && v.Value != "0"
	}
	return false
}

// isAlwaysFalse checks if an expression always evaluates to false
func (e *DebugExecutor) isAlwaysFalse(expr ast.Expr) bool {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name == "false"
	case *ast.BasicLit:
		return v.Kind == token.INT && v.Value == "0"
	}
	return false
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

// buildFieldPath recursively builds the field access path
func (e *DebugExecutor) buildFieldPath(expr ast.Expr, paramName string) []string {
	switch v := expr.(type) {
	case *ast.SelectorExpr:
		// Recursively build path: parent.field
		parentPath := e.buildFieldPath(v.X, paramName)
		return append(parentPath, v.Sel.Name)
	case *ast.Ident:
		// Skip the lambda parameter name — it's not part of the field path
		if v.Name == paramName {
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
	funcOffset := 0 // character offset where func( starts on the start line
	for i := start; i >= 0 && i >= start-10; i-- {
		line := lines[i]
		if idx := strings.Index(line, "func("); idx != -1 {
			start = i
			funcOffset = idx
			break
		}
	}

	// Extract the function body
	for i := start; i < len(lines); i++ {
		line := lines[i]

		// For the first line, strip everything before func(
		if i == start {
			line = line[funcOffset:]
		}

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

// parseFuncLit wraps source in a valid Go file and extracts the FuncLit node.
// parse.ParseExpr cannot handle function bodies with statements (for, if, return).
func parseFuncLit(source string) (*ast.FuncLit, error) {
	// Wrap in a minimal valid Go program so parse.ParseFile can handle it
	wrapped := "package p\nvar _ = " + source

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", wrapped, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to parse function source: %w", err)
	}

	// The FuncLit is the RHS of the var declaration
	var funcLit *ast.FuncLit
	ast.Inspect(file, func(n ast.Node) bool {
		if fl, ok := n.(*ast.FuncLit); ok {
			funcLit = fl
			return false
		}
		return true
	})

	if funcLit == nil {
		return nil, fmt.Errorf("no function literal found in source")
	}
	return funcLit, nil
}

// resolveEntityTypeFromFuncLit extracts the parameter type from a FuncLit
// and finds the matching registered models.
// Used by the generator — schemas must be registered before calling this.
func resolveEntityTypeFromFuncLit(funcLit *ast.FuncLit) (models.Entity, error) {
	if funcLit.Type.Params == nil || len(funcLit.Type.Params.List) == 0 {
		return nil, fmt.Errorf("function has no parameters")
	}

	param := funcLit.Type.Params.List[0]

	// Extract the type name — handles both "Customer" and "models.Customer"
	var typeName string
	switch t := param.Type.(type) {
	case *ast.Ident:
		// Same package: func(c Customer)
		typeName = t.Name
	case *ast.SelectorExpr:
		// External package: func(c models.Customer)
		typeName = t.Sel.Name
	default:
		return nil, fmt.Errorf("unsupported parameter type: %T", param.Type)
	}

	// Find matching models in registry by type name
	return models.FindModelByTypeName(typeName)
}

// funcParamName extracts the parameter name from a function literal.
// For func(c Customer) it returns "c".
// Falls back to empty string if the AST is malformed.
func funcParamName(fn *ast.FuncLit) string {
	if fn.Type.Params == nil || len(fn.Type.Params.List) == 0 {
		return ""
	}
	param := fn.Type.Params.List[0]
	if len(param.Names) == 0 {
		return ""
	}
	return param.Names[0].Name
}
