//go:build !prod

package goql

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/aekis-dev/goql/models"
	"github.com/aekis-dev/goql/query"
)

// getExecutor returns the appropriate executor based on build tags
func getExecutor() QueryExecutor {
	return &DebugExecutor{cache: make(map[string]*query.ParseQuery)}
}

// DebugExecutor parses lambda bodies from source at runtime. Parsed bodies are cached
// per lambda: the cache is shared across goroutines, hence the mutex.
type DebugExecutor struct {
	mu    sync.RWMutex
	cache map[string]*query.ParseQuery
}

// parseContext holds state threaded through the single-pass parse
type parseContext struct {
	schema    *models.Model
	paramName string

	// destSchema and destParamName are set only for an Insert lambda, whose first parameter
	// is the destination model and whose second is the source. Assignment targets resolve
	// against the destination; every value and condition still resolves against schema
	// above, so the whole predicate and value machinery is reused untouched.
	destSchema    *models.Model
	destParamName string

	// participants maps a lambda parameter name to the model it stands for: the primary
	// entity, plus any additional models declared for a join. A comparison between fields
	// of two different participants is a join condition rather than a WHERE term.
	participants map[string]*models.Model

	// joined lists the tables of the additional participants actually referenced, in
	// declaration order, so the builder knows what to put in the FROM clause.
	joined []string

	// participantPaths maps a participant bound as the far row of a path join to that
	// path, so references through it alias to that occurrence of the table rather than to
	// the table at large.
	participantPaths map[string]string

	// correlated holds tables that belong to an enclosing lambda. A nested body may refer
	// to them, but they must not enter its own FROM list — the outer statement has them.
	correlated map[string]bool

	// resultName and resultType describe a projection's destination — the lambda's first
	// parameter when the query's result type is not a model. Empty otherwise.
	resultName string
	resultType reflect.Type

	// subqueries maps a name bound by `x, _ := goql.Select[…](…)` to its parsed form.
	subqueries map[string]*query.ParseQuery

	// subErrors holds names bound to the error half of a nested call. Nothing runs, so
	// there is no error to inspect; naming one and testing it is reported clearly.
	subErrors map[string]bool

	// Extra lambda parameters, classified by type rather than position so they may be
	// declared in any order: optionParams maps a parameter name to its carrier type
	// ("Sort", "Limit", …).
	optionParams map[string]string

	// One SortSpec per *Sort parameter, kept in declaration order so several sorts
	// compose predictably.
	sortSpecs map[string]*query.SortSpec
	sortOrder []string

	// One JoinSpec per *Join parameter, likewise in declaration order.
	joinSpecs map[string]*query.JoinSpec
	joinOrder []string

	// outerNames are the enclosing lambda's model parameters, for a nested projection that
	// deliberately does not inherit them.
	outerNames map[string]bool

	// rowParams are pointer parameters whose type is neither an option carrier nor a
	// registered model: candidates for standing in as a row of a CTE.
	rowParams map[string]bool

	// cte is the common table expression this lambda reads from, once from.Query names one.
	cte *query.ParseCTE

	// ctes holds every definition bound in this lambda, whether read from or joined, in
	// declaration order. A definition appears once however many carriers name it.
	ctes []*query.ParseCTE

	// cteRows maps a parameter standing for a row of a named query to that query's name and
	// columns — the one from.Query reads from, and any joined with join.Query.
	cteRows map[string]*query.ParseCTE

	// selfHandle is the parameter of a combining lambda that stands for the CTE being
	// defined: `t []*CatNode`, the rows produced so far. Joining it is what makes a
	// recursive CTE recursive, stated rather than inferred.
	selfHandle string

	// selfColumns are the columns the self-reference presents, taken from the first branch
	// parsed — the anchor term, which is where SQL takes a recursive CTE's shape from too.
	selfColumns []string

	// recursive is set once a branch joins the CTE being defined.
	recursive bool

	opts *query.Options

	// paramsName is the name of the lambda's params-struct parameter, if it declared
	// one; paramsType is its Go type when known, so field references can be checked at
	// parse time rather than failing on the first call.
	paramsName string
	paramsType reflect.Type
}

// newInsertParseContext builds the context for an Insert lambda. The source model is the
// primary schema, so conditions and values parse exactly as they do for Select; the
// destination is carried alongside for assignment targets.
func newInsertParseContext(dest *models.Model, destParam string, src *models.Model, srcParam string) *parseContext {
	pctx := newParseContext(src, srcParam)
	pctx.destSchema = dest
	pctx.destParamName = destParam
	return pctx
}

// paramNameAt returns the name of the nth parameter, or "" when it is unnamed.
func paramNameAt(params []lambdaParam, index int) string {
	if index >= len(params) {
		return ""
	}
	return params[index].name
}

func newParseContext(schema *models.Model, paramName string) *parseContext {
	pctx := &parseContext{
		schema:       schema,
		paramName:    paramName,
		optionParams: make(map[string]string),
		sortSpecs:    make(map[string]*query.SortSpec),
		joinSpecs:    make(map[string]*query.JoinSpec),
		rowParams:    make(map[string]bool),
		cteRows:      make(map[string]*query.ParseCTE),
		participants: make(map[string]*models.Model),
	}
	// A projection lambda starts with no model: which one it reads from is stated by
	// from.Model. Registering a nil under the empty name would put a hole in the map.
	if schema != nil && paramName != "" {
		pctx.participants[paramName] = schema
	}
	return pctx
}

// inherited marks a table as belonging to an enclosing lambda, so referencing it correlates
// rather than joins.
func (pctx *parseContext) inherited(table string) {
	if pctx.correlated == nil {
		pctx.correlated = make(map[string]bool)
	}
	pctx.correlated[table] = true
}

// addParticipant registers an extra model declared as a lambda parameter, so fields
// reached through it resolve against that model rather than the primary one.
func (pctx *parseContext) addParticipant(name string, schema *models.Model) {
	if name == "" || name == "_" {
		return
	}
	pctx.participants[name] = schema
}

// baseIdentName returns the identifier a selector chain starts from: "o" for o.Customer.Country.
func baseIdentName(expr ast.Expr) string {
	base := expr
	for {
		sel, ok := base.(*ast.SelectorExpr)
		if !ok {
			break
		}
		base = sel.X
	}
	if ident, ok := base.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// ownerOf resolves the model a field expression belongs to, by looking up the identifier
// the selector chain starts from. Falls back to the primary model, which is what an
// expression written without a receiver (a sentinel variable, say) means.
func (pctx *parseContext) ownerOf(expr ast.Expr) (*models.Model, string) {
	if name := baseIdentName(expr); name != "" {
		if schema, found := pctx.participants[name]; found {
			return schema, name
		}
	}
	return pctx.schema, pctx.paramName
}

// assignTarget reports which model an assignment's left-hand side must resolve against, and
// whether this statement is one goql should look at.
//
// An Insert assigns to its destination; every other lambda assigns to its single model.
// Naming a *different* declared model is a mistake, not a statement to skip: writing
// `o.Priority = "x"` in an Insert lambda describes a mutation of the source that no
// INSERT … SELECT performs, and silently dropping it would run a statement the caller did
// not ask for.
func (pctx *parseContext) assignTarget(lhs ast.Expr) (schema *models.Model, paramName string, ok bool, err error) {
	target, targetParam := pctx.schema, pctx.paramName
	if pctx.destSchema != nil {
		target, targetParam = pctx.destSchema, pctx.destParamName
	}

	base := baseIdentName(lhs)
	if base == targetParam {
		return target, targetParam, true, nil
	}

	if other, declared := pctx.participants[base]; declared {
		if pctx.destSchema != nil {
			return nil, "", false, fmt.Errorf(
				"%w: %s.%s assigns to the source model %s — an Insert lambda assigns to its "+
					"destination (%s), and the source is only read from",
				ErrInvalidLambda, base, selectorTail(lhs), other.TableName, targetParam)
		}
		return nil, "", false, fmt.Errorf(
			"%w: %s.%s assigns to %s, but this lambda writes to %s — an additional model may "+
				"only be read from, to relate it to the one being written",
			ErrInvalidLambda, base, selectorTail(lhs), other.TableName, target.TableName)
	}

	// Not rooted at any declared model: a sentinel variable or an unrelated statement.
	return target, targetParam, true, nil
}

// selectorTail returns the last name in a selector chain, for error messages.
func selectorTail(expr ast.Expr) string {
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	return ""
}

// noteJoined records that an extra participant was actually referenced, so its table joins
// the FROM clause. Declaration order is preserved and each table listed once.
func (pctx *parseContext) noteJoined(schema *models.Model) {
	if schema == nil || pctx.schema == nil || schema.TableName == pctx.schema.TableName {
		return
	}
	if pctx.correlated[schema.TableName] {
		return
	}
	for _, existing := range pctx.joined {
		if existing == schema.TableName {
			return
		}
	}
	pctx.joined = append(pctx.joined, schema.TableName)
}

// classifyParams records the lambda's extra parameters. The leading entity parameters —
// one normally, two for an Insert — are skipped; every other one is identified by its type.
func (pctx *parseContext) classifyParams(funcLit *ast.FuncLit) error {
	if funcLit.Type.Params == nil {
		return nil
	}

	entityParams := 1
	if pctx.destSchema != nil {
		entityParams = 2
	}

	for i, param := range flatParams(funcLit) {
		if i < entityParams {
			continue // the entity, or destination and source for an Insert
		}

		// An option carrier is a pointer to one of the known option types; a pointer to
		// another registered model is a join participant. Anything else is the params
		// struct, which carries call-time values.
		typeName, isPointer := pointerTypeName(param.typ)
		if isPointer && !optionNames[typeName] {
			if schema := modelByTypeName(typeName); schema != nil {
				pctx.addParticipant(param.name, schema)
				continue
			}
		}
		// A slice of pointers stands for the rows a query has produced so far: the CTE being
		// defined, referred to from inside its own definition.
		if sliceOfPointers(param.typ) {
			if param.name != "_" {
				pctx.selfHandle = param.name
			}
			continue
		}
		// A pointer to something that is neither an option nor a model stands for a row of
		// a query — a CTE's row handle. A params struct is passed by value.
		if isPointer && !optionNames[typeName] {
			if param.name != "_" {
				pctx.rowParams[param.name] = true
			}
			continue
		}
		if !isPointer || !optionNames[typeName] {
			if pctx.paramsName != "" {
				return fmt.Errorf("%w: a lambda may declare at most one params struct",
					ErrInvalidParams)
			}
			if param.name != "_" {
				pctx.paramsName = param.name
			}
			continue
		}

		if param.name == "_" {
			continue
		}
		pctx.optionParams[param.name] = typeName
		switch typeName {
		case "Sort":
			pctx.sortSpecs[param.name] = &query.SortSpec{}
			pctx.sortOrder = append(pctx.sortOrder, param.name)
		case "Join":
			pctx.joinSpecs[param.name] = &query.JoinSpec{}
			pctx.joinOrder = append(pctx.joinOrder, param.name)
		}
	}
	return nil
}

// options assembles the parsed modifiers, or nil when the lambda declared none.
func (pctx *parseContext) options() (*query.Options, error) {
	if pctx.opts == nil && len(pctx.sortOrder) == 0 && len(pctx.joinOrder) == 0 {
		return nil, nil
	}
	out := pctx.opts
	if out == nil {
		out = &query.Options{}
	}
	for _, name := range pctx.sortOrder {
		spec := pctx.sortSpecs[name]
		if spec.By != "" {
			out.Sorts = append(out.Sorts, *spec)
		}
	}
	for _, name := range pctx.joinOrder {
		spec := pctx.joinSpecs[name]
		switch {
		case spec.Table == "" && spec.On == nil:
			// Declared and never used. Harmless on its own, but it reads as an intent that
			// was not carried out, so say so rather than emitting a query without the join.
			return nil, fmt.Errorf("%w: join parameter %s is declared but never assigned — "+
				"set %s.Field, or %s.Model and %s.On, or remove it",
				ErrInvalidOption, name, name, name, name)
		case spec.Table == "":
			return nil, fmt.Errorf("%w: %s.Model is not set — a join needs the model it relates",
				ErrInvalidOption, name)
		case len(spec.Hops) > 0 && spec.Path != "" && !pctx.pathBound(spec.Path):
			// A path supplies the condition, but the row it arrives at needs a handle or
			// nothing can refer to it.
			return nil, fmt.Errorf(
				"%w: %s.Field joins %s but %s.Model is not set — the joined row needs a handle "+
					"to be referenced by", ErrInvalidOption, name, spec.Table, name)
		case len(spec.Hops) == 0 && spec.On == nil:
			return nil, fmt.Errorf("%w: %s.On is not set — a join needs the condition relating "+
				"%s, which goql will not guess", ErrInvalidOption, name, spec.Table)
		}
		out.Joins = append(out.Joins, *spec)
	}
	if out.IsEmpty() {
		return nil, nil
	}
	return out, nil
}

// pointerTypeName returns the name of a pointer type's element, handling both
// "*Sort" and "*goql.Sort".
func pointerTypeName(expr ast.Expr) (string, bool) {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return "", false
	}
	switch t := star.X.(type) {
	case *ast.Ident:
		return t.Name, true
	case *ast.SelectorExpr:
		return t.Sel.Name, true
	}
	return "", false
}

// tryParseOptionAssignment recognises assignments to an option carrier, such as
// `sort.By = "Age"` or `limit.Value = 20`. It reports whether the statement was one.
func (e *DebugExecutor) tryParseOptionAssignment(s *ast.AssignStmt, pctx *parseContext) (bool, error) {
	if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
		return false, nil
	}
	sel, ok := s.Lhs[0].(*ast.SelectorExpr)
	if !ok {
		return false, nil
	}
	base, ok := sel.X.(*ast.Ident)
	if !ok {
		return false, nil
	}
	carrier, ok := pctx.optionParams[base.Name]
	if !ok {
		return false, nil
	}

	if pctx.opts == nil {
		pctx.opts = &query.Options{}
	}
	field := sel.Sel.Name

	switch carrier {
	case "Sort":
		spec := pctx.sortSpecs[base.Name]
		switch field {
		case "By":
			name, err := e.optionString(s.Rhs[0])
			if err != nil {
				return true, err
			}
			spec.By = name
		case "Desc":
			desc, err := e.optionBool(s.Rhs[0])
			if err != nil {
				return true, err
			}
			spec.Desc = desc
		default:
			return true, fmt.Errorf("%w: Sort has no field %s", ErrInvalidOption, field)
		}

	case "Limit", "Offset":
		if field != "Value" {
			return true, fmt.Errorf("%w: %s has no field %s", ErrInvalidOption, carrier, field)
		}
		n, err := e.optionInt(s.Rhs[0])
		if err != nil {
			return true, err
		}
		if carrier == "Limit" {
			pctx.opts.Limit = &n
		} else {
			pctx.opts.Offset = &n
		}

	case "Fields":
		if field != "Names" {
			return true, fmt.Errorf("%w: Fields has no field %s", ErrInvalidOption, field)
		}
		names, err := e.optionStringSlice(s.Rhs[0])
		if err != nil {
			return true, err
		}
		pctx.opts.Fields = append(pctx.opts.Fields, names...)

	case "Preload":
		if field != "Fields" {
			return true, fmt.Errorf("%w: Preload has no field %s", ErrInvalidOption, field)
		}
		names, err := e.optionStringSlice(s.Rhs[0])
		if err != nil {
			return true, err
		}
		pctx.opts.Preload = append(pctx.opts.Preload, names...)
		pctx.opts.PreloadSet = true

	case "Conflict":
		if field != "Ignore" {
			return true, fmt.Errorf("%w: Conflict has no field %s", ErrInvalidOption, field)
		}
		ignore, err := e.optionBool(s.Rhs[0])
		if err != nil {
			return true, err
		}
		pctx.opts.ConflictIgnore = ignore

	case "From":
		return true, e.setFromModel(field, s.Rhs[0], pctx)

	case "Join":
		return true, e.setJoin(pctx.joinSpecs[base.Name], field, s.Rhs[0], pctx)

	case "Group":
		if field != "By" {
			return true, fmt.Errorf("%w: Group has no field %s", ErrInvalidOption, field)
		}
		names, err := e.optionStringSlice(s.Rhs[0])
		if err != nil {
			return true, err
		}
		pctx.opts.GroupBy = append(pctx.opts.GroupBy, names...)

	default:
		// A carrier the parser knows by name but does not handle would otherwise be
		// accepted and dropped, which is how from.Model silently did nothing.
		return true, fmt.Errorf("%w: %s is not handled by the parser", ErrInvalidOption, carrier)
	}

	return true, nil
}

func (e *DebugExecutor) optionString(expr ast.Expr) (string, error) {
	val, isCol, err := e.extractValue(expr, nil, "")
	if err != nil {
		return "", err
	}
	s, ok := val.(string)
	if isCol || !ok {
		return "", fmt.Errorf("%w: expected a string literal", ErrInvalidOption)
	}
	return s, nil
}

func (e *DebugExecutor) optionBool(expr ast.Expr) (bool, error) {
	val, _, err := e.extractValue(expr, nil, "")
	if err != nil {
		return false, err
	}
	b, ok := val.(bool)
	if !ok {
		return false, fmt.Errorf("%w: expected true or false", ErrInvalidOption)
	}
	return b, nil
}

func (e *DebugExecutor) optionInt(expr ast.Expr) (int, error) {
	val, _, err := e.extractValue(expr, nil, "")
	if err != nil {
		return 0, err
	}
	n, ok := val.(int64)
	if !ok {
		return 0, fmt.Errorf("%w: expected an integer literal", ErrInvalidOption)
	}
	return int(n), nil
}

func (e *DebugExecutor) optionStringSlice(expr ast.Expr) ([]string, error) {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, fmt.Errorf("%w: expected a []string literal", ErrInvalidOption)
	}
	names := make([]string, 0, len(lit.Elts))
	for _, elt := range lit.Elts {
		name, err := e.optionString(elt)
		if err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}

// extractValue extracts the actual value from an AST expression
func (e *DebugExecutor) extractValue(expr ast.Expr, schema *models.Model, paramName string) (any, bool, error) {
	switch v := expr.(type) {

	case *ast.BasicLit:
		switch v.Kind {
		case token.STRING:
			inner, err := strconv.Unquote(v.Value)
			if err != nil {
				return nil, false, fmt.Errorf("invalid string literal %s: %w", v.Value, err)
			}
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
			// Returning v.Name here silently compiled `c.Age > minAge` into
			// `age > 'minAge'`. Lambda bodies are parsed from source and never
			// executed, so an outer variable has no value available at parse time.
			return nil, false, fmt.Errorf(
				"%w: %q — lambda bodies are parsed, not executed, so pass runtime "+
					"values through a params struct parameter instead",
				ErrCapturedVariable, v.Name)
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
					return fmt.Sprintf("%s.%s", schema.TableName, field.ColumnName()), true, nil
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
									col := fmt.Sprintf("%s.%s", targetSchema.TableName, field.ColumnName())
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
		val, isCol, err := e.extractValue(v.X, schema, paramName)
		if err != nil {
			return nil, false, err
		}
		if isCol {
			return nil, false, fmt.Errorf("cannot negate a column reference")
		}
		// Negate the value itself — formatting it back into a string would bind a
		// numeric literal as text.
		switch v.Op {
		case token.SUB:
			switch n := val.(type) {
			case int64:
				return -n, false, nil
			case float64:
				return -n, false, nil
			default:
				return nil, false, fmt.Errorf("cannot negate non-numeric literal %v", val)
			}
		case token.ADD:
			return val, false, nil
		default:
			return nil, false, fmt.Errorf("unsupported unary operator %s in value position", v.Op)
		}

	default:
		return nil, false, fmt.Errorf("%w: assignment value of type %T", ErrUnsupportedExpr, expr)
	}
}

func (e *DebugExecutor) ParseQuery(fn any, call string) (*query.ParseQuery, error) {
	return e.parseLambda(fn, call)
}

func (e *DebugExecutor) parseLambda(fn any, call string) (*query.ParseQuery, error) {
	// Insert is the one call whose first parameter is a destination rather than the row
	// being matched.
	insert := call == "Insert"

	id, err := lambdaID(fn)
	if err != nil {
		return nil, err
	}

	// Parsing a lambda means reading and parsing its whole source file, so results are
	// memoized: a lambda's identity is fixed for the lifetime of the process.
	if body, ok := e.cachedBody(id.cacheKey()); ok {
		return body, nil
	}

	funcLit, err := e.locateFuncLit(id, reflect.TypeOf(fn))
	if err != nil {
		return nil, err
	}

	params := flatParams(funcLit)
	funcType := reflect.TypeOf(fn)

	// A first parameter that is not a model is a projection's result type: the query returns
	// something other than model rows, and the model it reads from is named in the body with
	// from.Model.
	if !insert && !isEntityParam(funcType, 0) {
		return e.parseProjectionLambda(fn, funcType, funcLit, params, id)
	}

	schema, err := schemaOfParam(funcType, 0)
	if err != nil {
		return nil, err
	}

	// A second entity parameter means an Insert: the first is the destination, the second
	// the source the SELECT reads from. Classification is by type, as it is for options.
	var pctx *parseContext
	entityParams := 1
	if insert {
		if !isEntityParam(funcType, 1) {
			return nil, fmt.Errorf("%w: an Insert lambda takes a source model as its second "+
				"parameter", ErrInvalidLambda)
		}
		src, err := schemaOfParam(funcType, 1)
		if err != nil {
			return nil, err
		}
		pctx = newInsertParseContext(schema, paramNameAt(params, 0), src, paramNameAt(params, 1))
		entityParams = 2
	} else {
		pctx = newParseContext(schema, paramNameAt(params, 0))
	}

	if err := pctx.classifyParams(funcLit); err != nil {
		return nil, err
	}
	// The concrete params type is known for a runtime call, so field references can be
	// checked while parsing instead of failing on the first execution.
	if pctx.paramsType, err = paramsTypeFrom(funcType, entityParams); err != nil {
		return nil, err
	}

	body, err := e.parseBody(funcLit, pctx)
	if err != nil {
		return nil, err
	}

	parsed := &query.ParseQuery{Model: schema.Type.Name(), Func: recordedFunc(call), Body: body}
	pctx.applyCTE(parsed)
	if err := query.ValidateQuery(parsed, false); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidLambda, err)
	}

	e.storeBody(id.cacheKey(), parsed)
	return parsed, nil
}

func (e *DebugExecutor) cachedBody(key string) (*query.ParseQuery, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	body, ok := e.cache[key]
	return body, ok
}

func (e *DebugExecutor) storeBody(key string, body *query.ParseQuery) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cache == nil {
		e.cache = make(map[string]*query.ParseQuery)
	}
	e.cache[key] = body
}

// ParseQueryFromSource parses a lambda from its source text, for the generator — which
// knows which goql function each lambda was passed to.
func (e *DebugExecutor) ParseQueryFromSource(source, call string) (*query.ParseQuery, error) {
	return e.parseSource(source, call)
}

func (e *DebugExecutor) parseSource(source, call string) (*query.ParseQuery, error) {
	insert := call == "Insert"

	funcLit, err := parseFuncLit(source)
	if err != nil {
		return nil, fmt.Errorf("failed to parse function: %w", err)
	}

	params := flatParams(funcLit)

	// A first parameter that is not a model is a projection's result type. Ahead of time
	// there is no reflect.Type to consult, so "is a model" means "its type name resolves to
	// a registered one" — the same rule the runtime applies, expressed against the AST.
	if !insert && sourceModelFromParam(params, 0) == nil {
		return e.parseProjectionSource(funcLit, params, nil)
	}

	entity, err := resolveEntityTypeFromFuncLit(funcLit)
	if err != nil {
		return nil, err
	}
	schema, err := models.GetModel(entity)
	if err != nil {
		return nil, err
	}

	// Ahead of time there is no reflect.Type to consult, so the second parameter is an
	// entity if its type name resolves to a registered model — the same rule the runtime
	// applies, expressed against the AST.
	var pctx *parseContext
	if insert {
		src := sourceModelFromParam(params, 1)
		if src == nil {
			return nil, fmt.Errorf("%w: an Insert lambda takes a source model as its second "+
				"parameter", ErrInvalidLambda)
		}
		pctx = newInsertParseContext(schema, paramNameAt(params, 0), src, paramNameAt(params, 1))
	} else {
		pctx = newParseContext(schema, paramNameAt(params, 0))
	}

	if err := pctx.classifyParams(funcLit); err != nil {
		return nil, err
	}
	body, err := e.parseBody(funcLit, pctx)
	if err != nil {
		return nil, err
	}
	parsed := &query.ParseQuery{Model: schema.Type.Name(), Func: recordedFunc(call), Body: body}
	pctx.applyCTE(parsed)
	if err := query.ValidateQuery(parsed, false); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidLambda, err)
	}
	return parsed, nil
}

func (e *DebugExecutor) parseBody(funcLit *ast.FuncLit, pctx *parseContext) (*query.ParseBody, error) {
	result := &query.ParseBody{}

	// Assignments written at the top level are unconditional; they collect into a
	// single branch with no condition.
	var uncondAssigns []*query.ParseAssign
	var uncondRelAssigns []*query.ParseRelation

	// Conditions of every preceding arm in the body, so a guard-clause predicate
	// (`if A { return false }` followed by `return true`) means "not A" rather than
	// "everything".
	var bodyPriors []*query.ParseNode

	for _, stmt := range expandAssignments(funcLit.Body.List) {
		switch s := stmt.(type) {

		case *ast.AssignStmt:
			handled, err := e.tryParseOptionAssignment(s, pctx)
			if err != nil {
				return nil, err
			}
			if handled {
				continue
			}

			handled, err = e.tryParseSubqueryDecl(s, pctx)
			if err != nil {
				return nil, err
			}
			if handled {
				continue
			}

			projected, err := e.tryParseProjection(s, pctx)
			if err != nil {
				return nil, err
			}
			if projected != nil {
				result.Select = append(result.Select, projected)
				continue
			}

			assignment, relAssignment, err := e.tryParseAssignment(s, pctx)
			if err != nil {
				return nil, err
			}
			if assignment != nil {
				uncondAssigns = append(uncondAssigns, assignment)
			}
			if relAssignment != nil {
				uncondRelAssigns = append(uncondRelAssigns, relAssignment)
			}

		case *ast.RangeStmt:
			return nil, rangeRetired(s)

		case *ast.IfStmt:
			branches, arms, err := e.parseIfChain(s, pctx)
			if err != nil {
				return nil, err
			}
			result.Branches = append(result.Branches, branches...)
			bodyPriors = append(bodyPriors, arms...)

		case *ast.SwitchStmt:
			branches, arms, err := e.parseSwitchStmt(s, pctx)
			if err != nil {
				return nil, err
			}
			result.Branches = append(result.Branches, branches...)
			bodyPriors = append(bodyPriors, arms...)

		case *ast.ReturnStmt:
			if len(s.Results) != 1 || e.isAlwaysFalse(s.Results[0]) {
				continue
			}
			if e.isAlwaysTrue(s.Results[0]) {
				// `return true` selects whatever no preceding arm already handled.
				result.Branches = append(result.Branches, &query.ParseBranch{
					Condition: exclusiveCondition(bodyPriors, nil),
					Selects:   true,
				})
				continue
			}
			// A set operation is the query rather than a condition within it.
			set, err := e.tryParseSetOperation(s.Results[0], pctx)
			if err != nil {
				return nil, err
			}
			if set != nil {
				result.Set = set
				continue
			}

			node, err := e.exprToCondition(s.Results[0], pctx)
			if err != nil {
				return nil, err
			}
			result.Branches = append(result.Branches, &query.ParseBranch{
				Condition: exclusiveCondition(bodyPriors, node),
				Selects:   true,
			})
		}
	}

	if len(uncondAssigns) > 0 || len(uncondRelAssigns) > 0 {
		result.Branches = append(result.Branches, &query.ParseBranch{
			Assignments:         uncondAssigns,
			RelationAssignments: uncondRelAssigns,
		})
	}

	opts, err := pctx.options()
	if err != nil {
		return nil, err
	}
	result.Options = opts
	result.Joined = pctx.joined
	return result, nil
}

// parseIfChain flattens an if / else-if / else chain into independent branches.
// Each arm's condition is ANDed with the negation of every preceding arm, so `else`
// means "none of the above" and the branches stay mutually exclusive.
//
// The second return value is each arm's own condition, which the caller carries
// forward as context for statements that follow the chain.
func (e *DebugExecutor) parseIfChain(ifStmt *ast.IfStmt, pctx *parseContext) ([]*query.ParseBranch, []*query.ParseNode, error) {
	var branches []*query.ParseBranch
	var priors []*query.ParseNode

	var stmt ast.Stmt = ifStmt
	for stmt != nil {
		switch s := stmt.(type) {
		case *ast.IfStmt:
			if s.Init != nil {
				return nil, nil, fmt.Errorf("if with an init statement is not supported in a lambda")
			}
			cond, err := e.exprToCondition(s.Cond, pctx)
			if err != nil {
				return nil, nil, err
			}
			branch, err := e.branchFromBlock(s.Body, exclusiveCondition(priors, cond), pctx)
			if err != nil {
				return nil, nil, err
			}
			if branch != nil {
				branches = append(branches, branch)
			}
			priors = append(priors, cond)
			stmt = s.Else

		case *ast.BlockStmt: // trailing else
			branch, err := e.branchFromBlock(s, exclusiveCondition(priors, nil), pctx)
			if err != nil {
				return nil, nil, err
			}
			if branch != nil {
				branches = append(branches, branch)
			}
			// A trailing else is exhaustive: nothing after the chain is reachable
			// through a condition, so no priors escape.
			return branches, nil, nil

		default:
			stmt = nil
		}
	}

	return branches, priors, nil
}

// parseSwitchStmt flattens a switch into branches, handling both forms:
// a tagless switch whose cases are boolean expressions, and a tag switch whose cases
// are values compared against the tag. `default` becomes "none of the cases matched".
func (e *DebugExecutor) parseSwitchStmt(sw *ast.SwitchStmt, pctx *parseContext) ([]*query.ParseBranch, []*query.ParseNode, error) {
	if sw.Init != nil {
		return nil, nil, fmt.Errorf("switch with an init statement is not supported in a lambda")
	}

	// A tag switch compares each case value against this field.
	var tagRef *query.FieldRef
	if sw.Tag != nil {
		ref, err := e.resolveFieldRef(sw.Tag, pctx.schema, pctx.paramName)
		if err != nil {
			return nil, nil, fmt.Errorf("switch tag: %w", err)
		}
		tagRef = ref
	}

	var branches []*query.ParseBranch
	var priors []*query.ParseNode
	var defaultClause *ast.CaseClause

	for _, stmt := range sw.Body.List {
		clause, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}
		if len(clause.List) == 0 {
			// Deferred until every case condition is known, whatever its position.
			defaultClause = clause
			continue
		}

		cond, err := e.caseCondition(clause, tagRef, pctx)
		if err != nil {
			return nil, nil, err
		}
		body := &ast.BlockStmt{List: clause.Body}
		branch, err := e.branchFromBlock(body, exclusiveCondition(priors, cond), pctx)
		if err != nil {
			return nil, nil, err
		}
		if branch != nil {
			branches = append(branches, branch)
		}
		priors = append(priors, cond)
	}

	if defaultClause != nil {
		body := &ast.BlockStmt{List: defaultClause.Body}
		branch, err := e.branchFromBlock(body, exclusiveCondition(priors, nil), pctx)
		if err != nil {
			return nil, nil, err
		}
		if branch != nil {
			branches = append(branches, branch)
		}
		// A default clause is exhaustive — nothing escapes as a prior.
		return branches, nil, nil
	}

	return branches, priors, nil
}

// caseCondition builds the condition for one case clause. Multiple values in a single
// case are ORed together.
func (e *DebugExecutor) caseCondition(clause *ast.CaseClause, tagRef *query.FieldRef, pctx *parseContext) (*query.ParseNode, error) {
	var terms []*query.ParseNode

	for _, expr := range clause.List {
		if tagRef == nil {
			// Tagless switch — each case entry is a boolean expression.
			node, err := e.exprToCondition(expr, pctx)
			if err != nil {
				return nil, err
			}
			terms = append(terms, node)
			continue
		}
		// Tag switch — each case entry is a value compared against the tag.
		valueRef, err := e.resolveValueRef(expr, pctx)
		if err != nil {
			return nil, err
		}
		terms = append(terms, &query.ParseNode{
			Left:     tagRef,
			Operator: "=",
			Right:    valueRef,
		})
	}

	if len(terms) == 1 {
		return terms[0], nil
	}
	return &query.ParseNode{LogicalOp: "OR", Children: terms}, nil
}

// branchFromBlock builds a branch from one arm's body. Returns nil when the arm does
// nothing observable (for example `else { return false }`), though its condition still
// counts towards the negation carried by later arms.
func (e *DebugExecutor) branchFromBlock(block *ast.BlockStmt, cond *query.ParseNode, pctx *parseContext) (*query.ParseBranch, error) {
	assignments, relAssignments, err := e.parseAssignmentList(block.List, pctx)
	if err != nil {
		return nil, err
	}
	selects := e.returnsTrue(block)

	if !selects && len(assignments) == 0 && len(relAssignments) == 0 {
		return nil, nil
	}

	return &query.ParseBranch{
		Condition:           cond,
		Assignments:         assignments,
		RelationAssignments: relAssignments,
		Selects:             selects,
	}, nil
}

// exclusiveCondition ANDs an arm's own condition with the negation of all preceding
// arms. With no own condition (an else/default arm) the result is just the negations.
func exclusiveCondition(priors []*query.ParseNode, own *query.ParseNode) *query.ParseNode {
	var terms []*query.ParseNode
	for _, prior := range priors {
		terms = append(terms, query.Negate(prior))
	}
	if own != nil {
		terms = append(terms, own)
	}

	switch len(terms) {
	case 0:
		return nil
	case 1:
		return terms[0]
	default:
		return &query.ParseNode{LogicalOp: "AND", Children: terms}
	}
}

func (e *DebugExecutor) parseAssignmentList(stmts []ast.Stmt, pctx *parseContext) ([]*query.ParseAssign, []*query.ParseRelation, error) {
	var assignments []*query.ParseAssign
	var relAssignments []*query.ParseRelation

	for _, stmt := range stmts {
		assignStmt, ok := stmt.(*ast.AssignStmt)
		if !ok {
			continue
		}
		if isOptionAssignment(assignStmt, pctx) {
			return nil, nil, fmt.Errorf(
				"%w: query options apply to the whole query, so set them at the top level "+
					"of the lambda rather than inside a branch", ErrInvalidOption)
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

	// An Insert lambda assigns to the destination model; everything else assigns to the one
	// model it was given. Assigning to any other declared model is refused, not skipped.
	targetSchema, targetParam, ok, err := pctx.assignTarget(lhs)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, nil
	}

	fieldRef, err := e.resolveFieldRef(lhs, targetSchema, targetParam)
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
		valueRef, err := e.resolveValueRef(s.Rhs[0], pctx)
		if err != nil {
			return nil, nil, fmt.Errorf("field %s: %w", fieldRef.Field.Name, err)
		}
		return &query.ParseAssign{Field: fieldRef, Value: valueRef}, nil, nil
	}
}

func (e *DebugExecutor) exprToCondition(expr ast.Expr, pctx *parseContext) (*query.ParseNode, error) {
	switch v := expr.(type) {
	case *ast.BinaryExpr:
		op, err := e.mapOperator(v.Op)
		if err != nil {
			return nil, err
		}

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

		// An aggregate on the left filters groups: SUM(total) > 1000 becomes HAVING.
		if agg, err := e.aggregateOperand(v.X, pctx); err != nil {
			return nil, err
		} else if agg != nil {
			right, err := e.resolveValueRef(v.Y, pctx)
			if err != nil {
				return nil, fmt.Errorf("right side: %w", err)
			}
			return &query.ParseNode{Agg: agg, Operator: op, Right: right}, nil
		}
		if agg, err := e.aggregateOperand(v.Y, pctx); err != nil {
			return nil, err
		} else if agg != nil {
			return nil, fmt.Errorf(
				"%w: put the aggregate on the left of the comparison, so the condition reads "+
					"as the group being filtered", ErrUnsupportedExpr)
		}

		// A computed left-hand side: o.Price * o.Qty > 100.
		if binary, ok := v.X.(*ast.BinaryExpr); ok && arithmeticOps[binary.Op.String()] {
			leftValue, err := e.resolveArithmetic(binary, pctx)
			if err != nil {
				return nil, fmt.Errorf("left side: %w", err)
			}
			right, err := e.resolveValueRef(v.Y, pctx)
			if err != nil {
				return nil, fmt.Errorf("right side: %w", err)
			}
			return &query.ParseNode{LeftValue: leftValue, Operator: op, Right: right}, nil
		}

		left, err := e.resolveFieldRefIn(v.X, pctx)
		if err != nil {
			return nil, fmt.Errorf("left side: %w", err)
		}
		right, err := e.resolveValueRef(v.Y, pctx)
		if err != nil {
			return nil, fmt.Errorf("right side: %w", err)
		}
		return &query.ParseNode{
			Left:     left,
			Operator: op,
			Right:    right,
		}, nil

	case *ast.Ident:
		// A name bound to a nested Exists earlier in the body.
		if sub, found := pctx.subqueries[v.Name]; found {
			if sub.Func != "Exists" {
				return nil, fmt.Errorf(
					"%w: %s is a nested %s, which produces a value rather than a condition",
					ErrUnsupportedExpr, v.Name, displayFunc(sub.Func))
			}
			return &query.ParseNode{Sub: sub}, nil
		}
		return nil, fmt.Errorf("unexpected identifier %s in condition", v.Name)

	case *ast.CallExpr:
		// A relation filter is a correlated EXISTS over a collection.
		if node, handled, err := e.filterCall(v, pctx); handled {
			return node, err
		}

		// A nested Exists is a condition in its own right: EXISTS (SELECT 1 …).
		sub, err := e.subqueryFor(v, pctx)
		if err != nil {
			return nil, err
		}
		if sub != nil {
			if sub.Func != "Exists" {
				return nil, fmt.Errorf(
					"%w: a nested %s produces a value, so it belongs on one side of a "+
						"comparison rather than standing alone", ErrUnsupportedExpr,
					displayFunc(sub.Func))
			}
			return &query.ParseNode{Sub: sub}, nil
		}
		return e.parseConditionCall(v, pctx)

	case *ast.ParenExpr:
		return e.exprToCondition(v.X, pctx)

	case *ast.UnaryExpr:
		if v.Op != token.NOT {
			return nil, fmt.Errorf(
				"%w: unary %s is not a condition", ErrUnsupportedExpr, v.Op)
		}
		inner, err := e.exprToCondition(v.X, pctx)
		if err != nil {
			return nil, err
		}
		return query.Negate(inner), nil

	default:
		return nil, fmt.Errorf("%w: condition of type %T", ErrUnsupportedExpr, expr)
	}
}

// resolveFieldRef resolves an AST expression to a FieldRef using the models
// resolveFieldRefIn resolves a field expression against whichever declared model it is
// reached through, which is the primary one unless the lambda declared extra models to join.
func (e *DebugExecutor) resolveFieldRefIn(expr ast.Expr, pctx *parseContext) (*query.FieldRef, error) {
	if name := baseIdentName(expr); pctx.subErrors[name] {
		return nil, fmt.Errorf(
			"%w: %s is the error of a nested goql call, which is never executed and so never "+
				"fails here — discard it with _ and handle the error returned by the "+
				"enclosing call, which reports anything wrong with the subquery",
			ErrInvalidLambda, name)
	}

	// A column of the CTE this query reads from.
	if ref, isCTE, err := pctx.cteColumn(expr); isCTE {
		return ref, err
	}

	// A model of the enclosing query, deliberately not inherited here: this body may be read
	// from as a CTE, which is evaluated before the outer query produces a row.
	if name := baseIdentName(expr); pctx.outerNames[name] {
		return nil, fmt.Errorf(
			"%w: %s belongs to the enclosing query — a nested projection cannot reference it, "+
				"because a query read from with from.Query is evaluated before the outer one",
			ErrInvalidLambda, name)
	}

	// The destination row of an INSERT … SELECT does not exist yet, so it cannot be read
	// from. Say that, rather than failing later with a confusing "field a not found".
	if pctx.destSchema != nil && baseIdentName(expr) == pctx.destParamName {
		return nil, fmt.Errorf(
			"%w: %s.%s reads the Insert destination, which has no rows yet — conditions and "+
				"values come from the source model",
			ErrInvalidLambda, pctx.destParamName, selectorTail(expr))
	}

	schema, paramName := pctx.ownerOf(expr)
	ref, err := e.resolveFieldRef(expr, schema, paramName)
	if err != nil {
		return nil, err
	}

	// A handle bound to the far row of a path join carries that path, so its columns render
	// under the join's alias rather than a fresh one for the same table.
	if path, bound := pctx.participantPaths[paramName]; bound {
		ref.AliasPath = path
		return ref, nil
	}

	pctx.noteJoined(schema)
	return ref, nil
}

func (e *DebugExecutor) resolveFieldRef(expr ast.Expr, schema *models.Model, paramName string) (*query.FieldRef, error) {
	path := e.buildFieldPath(expr, paramName)

	if len(path) == 0 {
		return nil, fmt.Errorf("could not resolve field path")
	}

	// A lambda whose parameter type is not a registered model is read as a projection, which
	// then has no model to resolve against. Reaching here with no schema means the type was
	// never registered — most often because the package declaring it was not imported, so
	// its init() never ran AddModel. Without this the parser dereferenced nil and crashed.

	if schema == nil {
		return nil, fmt.Errorf(
			"%w: the lambda's parameter type is not a registered model, so %s cannot be "+
				"resolved — import the package that declares the model so its init() runs "+
				"models.AddModel, or set from.Model if this is a projection",
			models.ErrNotRegistered, strings.Join(path, "."))
	}

	// Simple field: o.Total → path = ["Total"]
	if len(path) == 1 {
		field, exists := schema.Fields[path[0]]
		if !exists {
			return nil, fmt.Errorf("field %s not found in models %s", path[0], schema.TableName)
		}
		return &query.FieldRef{Field: field}, nil
	}

	// A path traverses one relation per segment: o.Customer.Company.Country.Code walks
	// orders → customers → companies → countries and reads a column of the last.
	//
	// Depth is unbounded because the relations are declared on the models, so every hop is
	// derived rather than invented. The Go type checker keeps this to many2one on its own: a
	// collection is a slice, so o.Tags.Name does not compile — which is what guarantees a
	// path can never multiply rows.
	root := &query.FieldRef{}
	ref := root
	current := schema

	for i, segment := range path[:len(path)-1] {
		relationField, exists := current.Fields[segment]
		if !exists {
			return nil, fmt.Errorf("field %s not found in models %s", segment, current.TableName)
		}
		if !relationField.IsRelation() {
			return nil, fmt.Errorf(
				"field %s is not a relation field, so %s cannot be reached through it",
				segment, strings.Join(path[i+1:], "."))
		}

		targetSchema, err := query.RelationTargetSchema(relationField)
		if err != nil {
			return nil, err
		}

		ref.Field = relationField
		ref.Nested = &query.FieldRef{}
		ref, current = ref.Nested, targetSchema
	}

	terminal := path[len(path)-1]
	terminalField, exists := current.Fields[terminal]
	if !exists {
		return nil, fmt.Errorf("field %s not found in models %s", terminal, current.TableName)
	}
	ref.Field = terminalField

	// The primary key of a many2one target is the value the foreign key column already holds,
	// so o.Customer.ID is customers.id only in spelling — it is orders.customer_id. Collapsing
	// it avoids a join that could only ever compare a column to itself, and it is what makes a
	// foreign key comparable to a plain value.
	if last := lastRelation(root); last != nil && terminalField.PrimaryKey &&
		last.Field.RelationKind() == models.M2O {
		last.Nested = nil
	}

	return root, nil
}

// lastRelation returns the reference holding the final relation of a path, or nil when the
// path traverses none.
func lastRelation(ref *query.FieldRef) *query.FieldRef {
	if ref.Nested == nil {
		return nil
	}
	for ref.Nested.Nested != nil {
		ref = ref.Nested
	}
	return ref
}

// resolveValueRef resolves an AST expression to a ValueRef
func (e *DebugExecutor) resolveValueRef(expr ast.Expr, pctx *parseContext) (*query.ValueRef, error) {
	schema, paramName := pctx.schema, pctx.paramName

	// A reference into the params struct: the value is only known at the call site, so
	// emit a placeholder that is substituted just before execution.
	if ref, err := e.tryParamRef(expr, pctx); ref != nil || err != nil {
		return ref, err
	}

	// Parentheses carry no meaning of their own: the grouping they expressed is already in
	// the shape of the tree the Go parser built.
	if paren, ok := expr.(*ast.ParenExpr); ok {
		return e.resolveValueRef(paren.X, pctx)
	}

	// An arithmetic expression: prev.Depth + 1, o.Price * o.Qty. Nothing is stored — the
	// expression renders inline and the engine evaluates it per row.
	if binary, ok := expr.(*ast.BinaryExpr); ok && arithmeticOps[binary.Op.String()] {
		return e.resolveArithmetic(binary, pctx)
	}

	// A bare identifier naming a declared model means that model's row: `o.Customer == c`
	// compares the foreign key against the other model's primary key. This is how a nested
	// predicate correlates with the enclosing one.
	if ident, ok := expr.(*ast.Ident); ok {
		if other, declared := pctx.participants[ident.Name]; declared {
			if other.PrimaryKey == nil {
				return nil, fmt.Errorf("%w: %s has no primary key to compare against",
					ErrUnsupportedExpr, other.TableName)
			}
			return &query.ValueRef{IsColumn: true, Field: &query.FieldRef{Field: other.PrimaryKey}}, nil
		}
	}

	// A model of the enclosing query, which a nested projection deliberately does not
	// inherit. Reported here too: the field-reference path below discards its error.
	if name := baseIdentName(expr); pctx.outerNames[name] {
		return nil, fmt.Errorf(
			"%w: %s belongs to the enclosing query — a nested projection cannot reference it, "+
				"because a query read from with from.Query is evaluated before the outer one",
			ErrInvalidLambda, name)
	}

	// Check if it's a field reference first. A reference through another declared model
	// makes this comparison a join condition, which is why resolution is by receiver.
	if _, ok := expr.(*ast.SelectorExpr); ok {
		fieldRef, err := e.resolveFieldRefIn(expr, pctx)
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
				// {Model: goql.Model{ID: 1}} — find nested ID
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
	for _, stmt := range expandAssignments(block.List) {
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
func (e *DebugExecutor) mapOperator(op token.Token) (string, error) {
	switch op {
	case token.EQL:
		return "=", nil
	case token.LSS:
		return "<", nil
	case token.GTR:
		return ">", nil
	case token.NEQ:
		return "!=", nil
	case token.LEQ:
		return "<=", nil
	case token.GEQ:
		return ">=", nil
	case token.LAND:
		return "AND", nil
	case token.LOR:
		return "OR", nil
	default:
		return "", fmt.Errorf("%w: operator %s in predicate", ErrUnsupportedExpr, op)
	}
}

// runtimeLambdaID identifies a function literal by the source position the runtime reports
// for it, with the compiler's positional numbering kept only to break a tie.
//
// The line is the primary key because it is the one identifier that survives everything the
// compiler does. Verified across -N, -l, -N -l and -race, and through a //line directive
// (where the runtime and go/parser report the same rewritten position):
//
//	                        default build                                     -gcflags=all=-l
//	top-level closure       main.TopLevel.func2                               (identical)
//	nested closure          main.TopLevel.TopLevel.func2.func4                main.TopLevel.func2.1
//	two levels deep         main.TopLevel.TopLevel.func3.…func5.func6         main.TopLevel.func3.1.1
//	created in a call       main.main.Outer.run.main.Outer.func1.func2        main.Outer.func1.1
//
// Only the first row is stable, which is why keying on the funcN index supported top-level
// literals and nothing else. The reported *line* is identical in every one of those cases,
// so it identifies a nested literal as readily as a top-level one.
type runtimeLambdaID struct {
	file string
	line int

	// enclosing and index reproduce the old scheme, used only when a line carries more
	// than one literal. They are meaningful for a top-level literal, whose runtime name
	// the compiler does not rewrite.
	enclosing string // e.g. "DoThing" or "(*Service).Sync"
	index     int    // 1-based, matching the runtime's funcN suffix; 0 when not top-level
}

func (id runtimeLambdaID) cacheKey() string {
	return fmt.Sprintf("%s#%d#%s#%d", id.file, id.line, id.enclosing, id.index)
}

// lambdaID resolves a function value to the literal it was compiled from.
//
// It uses the runtime's own name for the closure (e.g. "main.main.func3") rather than
// its line number, because a line cannot distinguish two lambdas written on one line.
func lambdaID(fn any) (runtimeLambdaID, error) {
	v := reflect.ValueOf(fn)
	if v.Kind() != reflect.Func {
		return runtimeLambdaID{}, fmt.Errorf("goql: expected a function, got %T", fn)
	}

	pc := v.Pointer()
	rf := runtime.FuncForPC(pc)
	if rf == nil {
		return runtimeLambdaID{}, fmt.Errorf("goql: could not resolve function pointer")
	}
	file, line := rf.FileLine(pc)
	name := rf.Name()

	marker := strings.LastIndex(name, ".func")
	if marker == -1 {
		return runtimeLambdaID{}, fmt.Errorf(
			"goql: %s is not a function literal — pass a lambda written at the call site", name)
	}

	id := runtimeLambdaID{file: file, line: line}

	// A trailing integer means the compiler left the name in its top-level form
	// ("pkg.DoThing.func2"). Anything else is a nested literal, whose name the compiler
	// rewrites — see the type's comment. The line identifies it either way; this only
	// records what the old scheme knew, for the same-line tie-break.
	if index, err := strconv.Atoi(name[marker+len(".func"):]); err == nil {
		// Strip the package qualifier, which may itself contain dots and slashes
		// ("github.com/you/app.DoThing" → "DoThing").
		qualified := name[:marker]
		if slash := strings.LastIndex(qualified, "/"); slash != -1 {
			qualified = qualified[slash+1:]
		}
		if dot := strings.Index(qualified, "."); dot != -1 {
			qualified = qualified[dot+1:]
		}
		id.enclosing, id.index = qualified, index
	}

	return id, nil
}

// locateFuncLit parses the lambda's source file and returns the literal identified by
// id. This replaces an earlier hand-rolled brace/paren scanner, which desynchronized on
// braces inside strings and could not tell apart two literals on the same line.
func (e *DebugExecutor) locateFuncLit(id runtimeLambdaID, want reflect.Type) (*ast.FuncLit, error) {
	fset := token.NewFileSet()
	// Source comes from a registered tree when one is embedded, and from disk otherwise, so
	// the same locator serves a binary that carries its own sources.
	src, err := readSource(id.file)
	if err != nil {
		return nil, fmt.Errorf("goql: failed to read %s: %w", id.file, err)
	}

	file, err := parser.ParseFile(fset, id.file, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("goql: failed to parse %s: %w", id.file, err)
	}

	// The runtime attributes a closure's entry either to its `func` keyword or to its first
	// statement, depending on the body — both were observed. So candidates are literals
	// whose source range covers the reported line, narrowed by those two anchors.
	var covering, anchored []*ast.FuncLit
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.FuncLit)
		if !ok {
			return true
		}
		start, end := fset.Position(lit.Pos()).Line, fset.Position(lit.End()).Line
		if id.line < start || id.line > end {
			return true
		}
		covering = append(covering, lit)
		if start == id.line || firstStmtLine(fset, lit) == id.line {
			anchored = append(anchored, lit)
		}
		return true
	})

	candidates := anchored
	if len(candidates) == 0 {
		candidates = covering
	}

	// Two literals genuinely share an anchor when one opens on the line where the other's
	// first statement sits — exactly what a lambda passed to a call inside a closure looks
	// like. Their *signatures* differ, though, and the runtime value gives us the one we
	// are looking for, so match on that before falling back to anything positional.
	if len(candidates) > 1 {
		if matching := signatureMatches(candidates, want); len(matching) > 0 {
			candidates = matching
		}
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}

	// A lambda whose first statement contains another literal of the same shape shares its
	// anchor — `return goql.Filter(o.Tags, func(t *Tag) bool { … })`. The goql lambda is the
	// enclosing one, and an inner literal is parsed as part of its body rather than looked
	// up on its own, so the outermost candidate is the right one.
	if outer := outermost(candidates); outer != nil {
		return outer, nil
	}
	onLine := candidates

	// Several literals share the line. Fall back to the compiler's positional numbering,
	// which is meaningful for a top-level literal — that is the case the old scheme
	// handled, and TestParse_TwoLambdasOnOneLine covers it.
	if len(onLine) > 1 && id.index >= 1 {
		for _, decl := range file.Decls {
			funcDecl, ok := decl.(*ast.FuncDecl)
			if !ok || funcDecl.Body == nil || EnclosingFuncName(funcDecl) != id.enclosing {
				continue
			}
			lits := TopLevelFuncLits(funcDecl.Body)
			if id.index <= len(lits) {
				return lits[id.index-1], nil
			}
		}
	}

	if len(onLine) > 1 {
		return nil, fmt.Errorf(
			"goql: %s:%d holds %d function literals and this one is nested, so its position "+
				"cannot be told apart — put them on separate lines",
			id.file, id.line, len(onLine))
	}

	return nil, fmt.Errorf(
		"goql: could not locate a function literal at %s:%d — the source on disk may no "+
			"longer match the running binary",
		id.file, id.line)
}

// EnclosingFuncName renders a function declaration the way the Go runtime names it, so
// that goqlc's ahead-of-time numbering and dev-mode lookup agree exactly.
// Methods carry their receiver: "(*Service).Sync" or "Service.Sync".
// TopLevelFuncLits returns the function literals declared directly in body — not those
// nested inside another literal — in source order.
//
// This is how the compiler numbers the funcN suffix: closures written at the top level of a
// function get 1..n in source order, and nested ones continue the same counter but are named
// under their parent (`Outer.func2.…`). Counting every literal flat, as this used to, made a
// nested closure shift every later sibling's index — so a lookup silently resolved to a
// *different* lambda's body. Verified against the runtime's own names.
// firstStmtLine returns the line the Go runtime reports for a closure: that of the first
// statement in its body, or of the opening brace when the body is empty.
func firstStmtLine(fset *token.FileSet, lit *ast.FuncLit) int {
	if lit.Body == nil {
		return fset.Position(lit.Pos()).Line
	}
	if len(lit.Body.List) == 0 {
		return fset.Position(lit.Body.Lbrace).Line
	}
	return fset.Position(lit.Body.List[0].Pos()).Line
}

// signatureMatches keeps the literals whose declared signature matches the function value
// actually passed: same parameter and result counts, and the same type names.
//
// This is what tells a goql lambda apart from the closure it is written inside — a
// Transaction's func(*goql.Engine) error against a predicate's func(*Order) bool — when both
// anchor on the same source line.
func signatureMatches(lits []*ast.FuncLit, want reflect.Type) []*ast.FuncLit {
	if want == nil || want.Kind() != reflect.Func {
		return nil
	}

	var kept []*ast.FuncLit
	for _, lit := range lits {
		params := 0
		var names []string
		if lit.Type.Params != nil {
			for _, field := range lit.Type.Params.List {
				n := len(field.Names)
				if n == 0 {
					n = 1
				}
				params += n
				for i := 0; i < n; i++ {
					names = append(names, typeBaseName(field.Type))
				}
			}
		}
		results := 0
		if lit.Type.Results != nil {
			for _, field := range lit.Type.Results.List {
				if n := len(field.Names); n > 0 {
					results += n
				} else {
					results++
				}
			}
		}
		if params != want.NumIn() || results != want.NumOut() {
			continue
		}

		same := true
		for i := 0; i < want.NumIn() && same; i++ {
			if names[i] != "" && names[i] != reflectBaseName(want.In(i)) {
				same = false
			}
		}
		if same {
			kept = append(kept, lit)
		}
	}
	return kept
}

// typeBaseName reduces a type expression to the identifier that names it, dropping
// pointers, slices and package qualifiers: *models.Order and []*Order both give "Order".
// It returns "" for shapes it cannot reduce, which are then not used for matching.
func typeBaseName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return typeBaseName(t.X)
	case *ast.ArrayType:
		return typeBaseName(t.Elt)
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

// reflectBaseName is typeBaseName's counterpart for a runtime type.
func reflectBaseName(t reflect.Type) string {
	for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	return t.Name()
}

// outermost returns the one literal that encloses every other candidate, or nil when no
// single candidate does — two literals written side by side on one line, for instance.
func outermost(lits []*ast.FuncLit) *ast.FuncLit {
	if len(lits) < 2 {
		return nil
	}
	for _, candidate := range lits {
		enclosesAll := true
		for _, other := range lits {
			if other == candidate {
				continue
			}
			if !(candidate.Pos() <= other.Pos() && other.End() <= candidate.End()) {
				enclosesAll = false
				break
			}
		}
		if enclosesAll {
			return candidate
		}
	}
	return nil
}

func TopLevelFuncLits(body *ast.BlockStmt) []*ast.FuncLit {
	var lits []*ast.FuncLit
	ast.Inspect(body, func(n ast.Node) bool {
		funcLit, ok := n.(*ast.FuncLit)
		if !ok {
			return true
		}
		lits = append(lits, funcLit)
		// Do not descend: anything inside belongs to this literal, not to the function.
		return false
	})
	return lits
}

func EnclosingFuncName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	switch t := fn.Recv.List[0].Type.(type) {
	case *ast.Ident:
		return t.Name + "." + fn.Name.Name
	case *ast.StarExpr:
		if ident, ok := t.X.(*ast.Ident); ok {
			return "(*" + ident.Name + ")." + fn.Name.Name
		}
	}
	return fn.Name.Name
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

	// The entity parameter must be a pointer, so the body reads as the mutation the
	// generated statement performs. Handles "*Customer" and "*models.Customer".
	star, ok := param.Type.(*ast.StarExpr)
	if !ok {
		return nil, fmt.Errorf(
			"%w: entity parameter must be a pointer (func(x *T) …), got %s",
			ErrInvalidLambda, types.ExprString(param.Type))
	}

	var typeName string
	switch t := star.X.(type) {
	case *ast.Ident:
		// Same package: func(c *Customer)
		typeName = t.Name
	case *ast.SelectorExpr:
		// External package: func(c *models.Customer)
		typeName = t.Sel.Name
	default:
		return nil, fmt.Errorf("%w: unsupported parameter type %T", ErrInvalidLambda, star.X)
	}

	// Find matching models in registry by type name
	return models.FindModelByTypeName(typeName)
}

// modelByTypeName resolves a type name to a registered model, or nil when none matches.
// This is the AST-only counterpart of the runtime's reflect-based check: an option carrier
// or a params struct simply does not resolve.
func modelByTypeName(typeName string) *models.Model {
	entity, err := models.FindModelByTypeName(typeName)
	if err != nil {
		return nil
	}
	schema, err := models.GetModel(entity)
	if err != nil {
		return nil
	}
	return schema
}

// sourceModelFromParam resolves the nth parameter to a registered model, or nil when it is
// not a pointer to one.
func sourceModelFromParam(params []lambdaParam, index int) *models.Model {
	if index >= len(params) {
		return nil
	}
	typeName, isPointer := pointerTypeName(params[index].typ)
	if !isPointer || optionNames[typeName] {
		return nil
	}
	return modelByTypeName(typeName)
}

// lambdaParam is one parameter of a lambda, flattened out of its declaration group.
// Grouping matters for an Insert of a model into itself, written func(dst, src *Order):
// two parameters sharing one type expression.
type lambdaParam struct {
	name string
	typ  ast.Expr
}

// flatParams lists a lambda's parameters in declaration order, one entry per name.
func flatParams(fn *ast.FuncLit) []lambdaParam {
	if fn.Type.Params == nil {
		return nil
	}
	var out []lambdaParam
	for _, group := range fn.Type.Params.List {
		if len(group.Names) == 0 {
			// An unnamed parameter still occupies a position.
			out = append(out, lambdaParam{typ: group.Type})
			continue
		}
		for _, name := range group.Names {
			out = append(out, lambdaParam{name: name.Name, typ: group.Type})
		}
	}
	return out
}

// funcParamName extracts the first parameter's name from a function literal.
// For func(c *Customer) it returns "c". Empty when the AST is malformed.
func funcParamName(fn *ast.FuncLit) string {
	params := flatParams(fn)
	if len(params) == 0 {
		return ""
	}
	return params[0].name
}

// isOptionAssignment reports whether a statement assigns to an option carrier, used to
// reject option assignments nested inside conditional arms.
func isOptionAssignment(s *ast.AssignStmt, pctx *parseContext) bool {
	if len(s.Lhs) != 1 {
		return false
	}
	sel, ok := s.Lhs[0].(*ast.SelectorExpr)
	if !ok {
		return false
	}
	base, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, isOption := pctx.optionParams[base.Name]
	return isOption
}

// tryParamRef recognises a reference into the lambda's params struct, such as
// `p.MinTotal`. It returns (nil, nil) when the expression is not one.
//
// When the params type is known — it is for a runtime call, but not when parsing from
// source — the field is checked here, so a typo fails at parse time instead of on the
// first call.
func (e *DebugExecutor) tryParamRef(expr ast.Expr, pctx *parseContext) (*query.ValueRef, error) {
	if pctx.paramsName == "" {
		return nil, nil
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return nil, nil
	}
	base, ok := sel.X.(*ast.Ident)
	if !ok || base.Name != pctx.paramsName {
		return nil, nil
	}

	if pctx.paramsType != nil {
		t := pctx.paramsType
		if t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		field, found := t.FieldByName(sel.Sel.Name)
		if !found {
			return nil, fmt.Errorf("%w: %s has no field %s",
				ErrInvalidParams, t.Name(), sel.Sel.Name)
		}
		if !field.IsExported() {
			return nil, fmt.Errorf("%w: %s.%s is not exported",
				ErrInvalidParams, t.Name(), sel.Sel.Name)
		}
	}

	return &query.ValueRef{Value: query.ParamRef{Field: sel.Sel.Name}}, nil
}

// parseConditionCall translates a goql.Condition(field, op, values…) call into a
// condition node. Everything is read from source: the operator is a literal, checked
// against the allowlist here so a typo fails at parse time.
func (e *DebugExecutor) parseConditionCall(call *ast.CallExpr, pctx *parseContext) (*query.ParseNode, error) {
	if name := calleeName(call.Fun); name != "Condition" {
		return nil, fmt.Errorf("%w: call to %s in a condition", ErrUnsupportedExpr, types.ExprString(call.Fun))
	}
	if len(call.Args) < 2 {
		return nil, fmt.Errorf(
			"%w: Condition needs a field and an operator", ErrUnsupportedExpr)
	}

	node := &query.ParseNode{}

	// The field is normally an entity field, but a string literal is emitted verbatim.
	if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
		raw, err := strconv.Unquote(lit.Value)
		if err != nil {
			return nil, fmt.Errorf("invalid string literal %s: %w", lit.Value, err)
		}
		node.RawColumn = raw
	} else {
		fieldRef, err := e.resolveFieldRefIn(call.Args[0], pctx)
		if err != nil {
			return nil, fmt.Errorf("Condition field: %w", err)
		}
		node.Left = fieldRef
	}

	opLit, ok := call.Args[1].(*ast.BasicLit)
	if !ok || opLit.Kind != token.STRING {
		return nil, fmt.Errorf("%w: the Condition operator must be a string literal",
			ErrUnsupportedExpr)
	}
	rawOp, err := strconv.Unquote(opLit.Value)
	if err != nil {
		return nil, fmt.Errorf("invalid operator literal %s: %w", opLit.Value, err)
	}

	valueExprs := call.Args[2:]
	op, err := query.NormalizeOperator(rawOp, len(valueExprs))
	if err != nil {
		return nil, err
	}
	node.Operator = op

	// A single value may be a nested query rather than a bound value: IN (SELECT …), or a
	// comparison against a nested COUNT.
	if len(valueExprs) == 1 {
		sub, err := e.subqueryFor(valueExprs[0], pctx)
		if err != nil {
			return nil, err
		}
		if sub != nil {
			if sub.Func == "Exists" {
				return nil, fmt.Errorf(
					"%w: a nested Exists is a condition on its own, not a value — write it "+
						"as a term rather than inside Condition", ErrUnsupportedExpr)
			}
			node.Sub = sub
			return node, nil
		}
	}

	values := make([]*query.ValueRef, 0, len(valueExprs))
	for _, expr := range valueExprs {
		value, err := e.resolveValueRef(expr, pctx)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}

	switch {
	case query.IsNullaryOperator(op):
		// nothing to bind
	case query.IsListOperator(op):
		node.Values = values
	default:
		node.Right = values[0]
	}

	return node, nil
}

// calleeName resolves the name of a called function, ignoring its qualifier so both
// `goql.Condition` and a dot-imported `Condition` are recognised.
func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.Ident:
		return f.Name
	case *ast.IndexExpr:
		return calleeName(f.X)
	}
	return ""
}

// recordedFunc is the Func a parsed query carries for a given call. Calls that yield rows
// record nothing — that is what an empty Func means — while the rest record their own name,
// so the tree says what it is rather than relying on which builder happens to read it.
func recordedFunc(call string) string {
	switch call {
	case "Exists":
		return call
	default:
		return ""
	}
}

// bindCTERow records the parameter standing for a row of a named query. References through
// it resolve to that query's projected columns rather than to a schema.
func (pctx *parseContext) bindCTERow(name string, cte *query.ParseCTE) {
	pctx.cteRows[name] = cte
	delete(pctx.rowParams, name)
}

// cteColumn resolves a reference through the CTE row handle — t.Total — to the column the
// defining query projected under that name. The check is possible because the CTE's columns
// are known: they are its projection.
func (pctx *parseContext) cteColumn(expr ast.Expr) (*query.FieldRef, bool, error) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return nil, false, nil
	}
	base, ok := sel.X.(*ast.Ident)
	if !ok {
		return nil, false, nil
	}
	cte, bound := pctx.cteRows[base.Name]
	if !bound {
		return nil, false, nil
	}

	name := sel.Sel.Name
	for _, column := range cte.Columns {
		if column == name {
			return &query.FieldRef{CTETable: cte.Name, CTEColumn: column}, true, nil
		}
	}
	return nil, true, fmt.Errorf("%w: %s does not select %s — it selects %s",
		models.ErrFieldNotFound, cte.Name, name, strings.Join(cte.Columns, ", "))
}

// applyCTE records the common table expression this query reads from, if any. The definition
// travels with the body, so it survives into the generated prod registry like everything else.
func (pctx *parseContext) applyCTE(parsed *query.ParseQuery) {
	if pctx.cte != nil {
		parsed.From = pctx.cte.Name
	}
	// Every definition bound in this lambda is emitted, including ones only joined —
	// otherwise the statement references a table that was never defined.
	parsed.Body.With = append(parsed.Body.With, pctx.ctes...)
}

// outerParamNames collects the enclosing lambda's model parameter names.
func outerParamNames(outer *parseContext) map[string]bool {
	if outer == nil {
		return nil
	}
	names := make(map[string]bool, len(outer.participants))
	for name := range outer.participants {
		names[name] = true
	}
	return names
}

// sliceOfPointers reports a parameter written []*T, the shape a goql call returns and
// therefore the shape of a handle on a query's rows.
func sliceOfPointers(expr ast.Expr) bool {
	array, ok := expr.(*ast.ArrayType)
	if !ok || array.Len != nil {
		return false
	}
	_, isPointer := array.Elt.(*ast.StarExpr)
	return isPointer
}

// expandAssignments rewrites `a.X, a.Y = b.X, b.Y` into one statement per target.
//
// It is ordinary Go and means exactly that, but every handler matches a single left-hand
// side — so without this a tuple assignment matched none of them and was silently dropped,
// which is the failure mode this project keeps removing. A declaration binding two names
// from one call (`x, _ := goql.Select(…)`) has a single right-hand side and is left alone.
func expandAssignments(stmts []ast.Stmt) []ast.Stmt {
	needsSplit := false
	for _, stmt := range stmts {
		if assign, ok := stmt.(*ast.AssignStmt); ok &&
			len(assign.Lhs) > 1 && len(assign.Lhs) == len(assign.Rhs) {
			needsSplit = true
			break
		}
	}
	if !needsSplit {
		return stmts
	}

	out := make([]ast.Stmt, 0, len(stmts)+2)
	for _, stmt := range stmts {
		assign, ok := stmt.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) < 2 || len(assign.Lhs) != len(assign.Rhs) {
			out = append(out, stmt)
			continue
		}
		for i := range assign.Lhs {
			out = append(out, &ast.AssignStmt{
				Lhs:    []ast.Expr{assign.Lhs[i]},
				TokPos: assign.TokPos,
				Tok:    assign.Tok,
				Rhs:    []ast.Expr{assign.Rhs[i]},
			})
		}
	}
	return out
}
