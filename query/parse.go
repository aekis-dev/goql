package query

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/aekis-dev/goql/models"
)

// Query represents a built SQL query with its bound arguments
type Query struct {
	SQL  string
	Args []any
}

// JoinClause represents a JOIN needed to satisfy a condition
type JoinClause struct {
	SQL string // the full JOIN string e.g. "LEFT JOIN customers ON ..."
}

// FieldRef is a reference to a field in the models.
// Nested is non-nil when accessing through a relation (e.g. o.Customer.Country).
type FieldRef struct {
	Field  *models.Field
	Nested *FieldRef

	// CTETable and CTEColumn name a column of a common table expression, which has no
	// registered schema — its columns are whatever the defining query projected.
	CTETable  string
	CTEColumn string
}

// TableName returns the table name for this field reference
func (fr *FieldRef) TableName() string {
	if fr.CTETable != "" {
		return fr.CTETable
	}
	if fr.Nested != nil {
		return fr.Nested.Field.TableSchema.TableName
	}
	return fr.Field.TableSchema.TableName
}

// ValueRef represents the right-hand side of a comparison or assignment: a literal, a
// reference to another field, or an arithmetic expression over either.
type ValueRef struct {
	IsColumn bool
	Field    *FieldRef // when IsColumn = true
	Value    any       // when IsColumn = false and Expr = nil

	// Expr is set when the value is computed rather than read: prev.Depth + 1. Nothing is
	// stored — the expression is rendered inline and evaluated per row by the engine.
	Expr *ParseExpr
}

// ParseExpr is an arithmetic expression. Operands are themselves ValueRefs, so expressions
// nest; precedence and grouping are already resolved by the Go parser, which built the tree.
type ParseExpr struct {
	Op          string // + - * / %
	Left, Right *ValueRef

	// Text marks a "+" that concatenates rather than adds. It is decided while parsing,
	// from the Go types of the operands, because the engines disagree: SQLite and Postgres
	// spell concatenation ||, and MySQL needs CONCAT — where a bare + would silently
	// coerce both sides to numbers and yield 0.
	Text bool
}

// IsExpr reports whether this value is computed.
func (v *ValueRef) IsExpr() bool { return v != nil && v.Expr != nil }

// ParseNode is a node in the parsed condition tree.
// Leaf nodes represent a single comparison (Left Operator Right).
// Branch nodes combine child conditions with a logical operator (AND/OR).
type ParseNode struct {
	// Leaf — a single comparison: Left Operator Right
	Left     *FieldRef
	Operator string
	Right    *ValueRef

	// LeftValue replaces Left when the left-hand side is computed rather than a plain
	// column: o.Price * o.Qty > 100.
	LeftValue *ValueRef

	// RawColumn overrides Left with a fragment emitted verbatim, the escape hatch for
	// expressions goql cannot model — a JSON path, say. The caller owns its correctness
	// and its portability across engines.
	RawColumn string

	// Values carries the right-hand side for operators that take several (IN) or none
	// (IS NULL); Right covers the single-value case.
	Values []*ValueRef

	// Branch — logical combination: Children joined by LogicalOp (AND/OR)
	LogicalOp string
	Children  []*ParseNode

	// Join — a relation that requires a JOIN with scoped conditions
	JoinField *FieldRef  // the relation field (o.Tags, c.Orders)
	JoinScope *ParseNode // conditions scoped to the joined table

	// Sub is a nested goql call. With Left set it is the right-hand side of a comparison
	// (IN, or a scalar against COUNT); alone it is an EXISTS term.
	Sub *ParseQuery

	// Agg replaces Left with an aggregate over a column — SUM(total) > 1000. A condition
	// carrying one filters groups rather than rows, so it is emitted as HAVING.
	Agg *ParseSelect
}

// LogicalNot is the LogicalOp of a negation node, which has exactly one child.
const LogicalNot = "NOT"

// Negate wraps a condition in a NOT node. Used to give an else/default arm the
// condition "none of the preceding arms matched".
func Negate(node *ParseNode) *ParseNode {
	return &ParseNode{LogicalOp: LogicalNot, Children: []*ParseNode{node}}
}

// IsLeaf returns true if this node is a comparison, false if it's a logical branch
func (n *ParseNode) IsLeaf() bool {
	return n.LogicalOp == "" && n.JoinField == nil
}

// IsSubOnly reports a node that is nothing but a nested query, i.e. an EXISTS term.
func (n *ParseNode) IsSubOnly() bool {
	return n.Sub != nil && n.Left == nil && n.RawColumn == ""
}

func (n *ParseNode) IsBranch() bool {
	return n.LogicalOp != ""
}

func (n *ParseNode) IsJoin() bool {
	return n.JoinField != nil
}

// ParseAssign represents a field assignment in a Write lambda (e.g. c.Status = "Premium")
type ParseAssign struct {
	Field *FieldRef
	Value *ValueRef
}

type ParseRelation struct {
	Field      *FieldRef // the relation field (o.Tags)
	RelatedPKs []any     // PKs extracted from the literal slice value
}

// ParseBranch is one conditional arm of a lambda body — an if/else-if/else arm, a
// switch case, or the unconditional statements at the top level.
//
// Condition already carries the negation of every preceding arm in the same chain, so
// branches are mutually exclusive and independent: each can be emitted as its own
// statement in any order.
type ParseBranch struct {
	Condition           *ParseNode // nil = unconditional
	Assignments         []*ParseAssign
	RelationAssignments []*ParseRelation

	// Selects marks a branch whose body returns true, i.e. one that contributes to a
	// predicate's row set. Assignment-only branches leave it false.
	Selects bool
}

// ParseBody is the parsed form of one lambda: a flat list of branches.
//
// Write lambdas emit one UPDATE per branch that assigns something. Predicate lambdas
// (Search/Delete) OR the conditions of every selecting branch into a single WHERE.
type ParseBody struct {
	Branches []*ParseBranch

	// Options holds modifiers declared as extra lambda parameters and assigned in the
	// body (ordering, pagination, projection). Nil when the lambda declared none.
	Options *Options

	// Select is the projection when the query returns something other than model rows: one
	// entry per output column. Empty means "the model's own columns".
	Select []*ParseSelect

	// Set combines whole queries — UNION and its relatives. When set, it is the query: the
	// body's own branches describe nothing on their own.
	Set *ParseSet

	// With holds the common table expressions this body defines, in declaration order.
	With []*ParseCTE

	// Joined lists the tables of additional models the lambda declared as parameters and
	// actually referenced. They enter the FROM clause alongside the primary table, and a
	// comparison between two of them is the join condition — an inner join, since an
	// equality alone cannot say which side is optional.
	Joined []string
}

// SelectCondition combines the conditions of every selecting branch into one WHERE
// clause. A nil result means unconditional — either nothing selects, or a branch
// selects everything.
func (b *ParseBody) SelectCondition() *ParseNode {
	var roots []*ParseNode
	for _, branch := range b.Branches {
		if !branch.Selects {
			continue
		}
		// An unconditional selecting branch subsumes every other condition.
		if branch.Condition == nil {
			return nil
		}
		roots = append(roots, branch.Condition)
	}

	switch len(roots) {
	case 0:
		return nil
	case 1:
		return roots[0]
	default:
		return &ParseNode{LogicalOp: "OR", Children: roots}
	}
}

// WriteBranches returns the branches that assign something, i.e. those that produce an
// UPDATE statement.
func (b *ParseBody) WriteBranches() []*ParseBranch {
	var out []*ParseBranch
	for _, branch := range b.Branches {
		if len(branch.Assignments) > 0 || len(branch.RelationAssignments) > 0 {
			out = append(out, branch)
		}
	}
	return out
}

// getFieldValue finds a field by name, traversing embedded structs
func getFieldValue(v reflect.Value, t reflect.Type, name string) (reflect.Value, bool) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		fv := v.Field(i)
		if field.Name == name {
			return fv, true
		}
		if field.Anonymous {
			ft := field.Type
			if ft.Kind() == reflect.Ptr {
				if fv.IsNil() {
					continue
				}
				fv = fv.Elem()
				ft = ft.Elem()
			}
			if fv.Kind() == reflect.Struct {
				if found, ok := getFieldValue(fv, ft, name); ok {
					return found, true
				}
			}
		}
	}
	return reflect.Value{}, false
}

func isZeroValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Slice, reflect.Map, reflect.Array:
		return v.Len() == 0
	case reflect.Interface, reflect.Ptr:
		return v.IsNil()
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if f.IsExported() && !isZeroValue(v.Field(i)) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func containsValue(slice []any, value any) bool {
	for _, v := range slice {
		if reflect.DeepEqual(v, value) {
			return true
		}
	}
	return false
}

// rejectJoined refuses a lambda that declared extra models on a statement that cannot take
// a multi-table FROM list. Silently ignoring the parameter would run the statement against
// the wrong row set.
func rejectJoined(body *ParseBody, call string) error {
	if body.Options != nil && len(body.Options.Joins) > 0 {
		tables := make([]string, len(body.Options.Joins))
		for i, join := range body.Options.Joins {
			tables[i] = join.Table
		}
		return fmt.Errorf(
			"%s cannot take an explicit join (%s): it reaches other tables through declared "+
				"relations, not a join list",
			call, strings.Join(tables, ", "))
	}
	if len(body.Joined) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%s cannot join additional models (%s): only Select, Count, Exists and Insert can — "+
			"select the rows first, or reach the other table through a declared relation",
		call, strings.Join(body.Joined, ", "))
}

// ParseSelect is one column of a projection: a plain column, or an aggregate over one.
//
// It exists because a query may return something other than a model — a grouping key beside
// a SUM, say — and each output column has to say where it lands in the result type.
type ParseSelect struct {
	// Func is the aggregate applied to the column, empty for a plain one. A plain column is
	// also a GROUP BY term, which is SQL's own rule rather than a separate declaration.
	Func string

	// Field is the source column. Nil for COUNT(*), which counts rows rather than values.
	Field *FieldRef

	// Value is set instead of Field when the column is computed — an expression, or a
	// literal assigned straight into a result field.
	Value *ValueRef

	// Into names the field of the result type this column is scanned into. It is also the
	// column's SQL alias, so scanning never depends on column order.
	Into string
}

// Projection reports whether this body selects an explicit list of columns rather than the
// model's own rows.
func (b *ParseBody) Projection() bool { return len(b.Select) > 0 }

// Aggregated reports whether the projection computes anything over a group.
func (b *ParseBody) Aggregated() bool {
	for _, sel := range b.Select {
		if sel.Func != "" {
			return true
		}
	}
	return false
}

// GroupBy returns the projection's plain columns, which are grouping terms whenever
// something is aggregated. An aggregate-only projection is a single row and groups by
// nothing.
func (b *ParseBody) GroupBy() []*ParseSelect {
	if !b.Aggregated() {
		return nil
	}

	var groups []*ParseSelect
	for _, sel := range b.Select {
		if sel.Func == "" {
			groups = append(groups, sel)
		}
	}
	return groups
}

// HasAggregate reports whether any condition in this tree compares an aggregate.
func (n *ParseNode) HasAggregate() bool {
	if n == nil {
		return false
	}
	if n.Agg != nil {
		return true
	}
	for _, child := range n.Children {
		if child.HasAggregate() {
			return true
		}
	}
	if n.JoinScope != nil {
		return n.JoinScope.HasAggregate()
	}
	return false
}

// SplitHaving separates a condition tree into the part that filters rows (WHERE) and the
// part that filters groups (HAVING).
//
// The two are only separable across AND: SQL cannot OR a pre-aggregation filter with a
// post-aggregation one, and NOT of a mixed subtree is the same problem. Rather than guess at
// something that changes the answer, those are refused.
func SplitHaving(node *ParseNode) (where, having *ParseNode, err error) {
	if node == nil {
		return nil, nil, nil
	}
	if !node.HasAggregate() {
		return node, nil, nil
	}

	if node.IsLeaf() {
		return nil, node, nil
	}

	if node.LogicalOp != "AND" {
		return nil, nil, fmt.Errorf(
			"an aggregate condition can only be combined with AND: %s filters groups after "+
				"they are formed, and SQL cannot combine that with a row filter any other way",
			describeCombination(node))
	}

	var wheres, havings []*ParseNode
	for _, child := range node.Children {
		childWhere, childHaving, err := SplitHaving(child)
		if err != nil {
			return nil, nil, err
		}
		if childWhere != nil {
			wheres = append(wheres, childWhere)
		}
		if childHaving != nil {
			havings = append(havings, childHaving)
		}
	}
	return combineAnd(wheres), combineAnd(havings), nil
}

func combineAnd(nodes []*ParseNode) *ParseNode {
	switch len(nodes) {
	case 0:
		return nil
	case 1:
		return nodes[0]
	default:
		return &ParseNode{LogicalOp: "AND", Children: nodes}
	}
}

func describeCombination(node *ParseNode) string {
	if node.LogicalOp == LogicalNot {
		return "it is negated"
	}
	return "it is combined with " + node.LogicalOp
}
