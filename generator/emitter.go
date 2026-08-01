//go:build !prod

package generator

import (
	"fmt"
	"strings"

	"github.com/aekis-dev/goql/query"
)

func indent(n int) string {
	return strings.Repeat("\t", n)
}

func emitParseNode(node *query.ParseNode, depth int) string {
	if node == nil {
		return "nil"
	}

	ind := indent(depth)
	ind1 := indent(depth + 1)

	if node.IsJoin() {
		return fmt.Sprintf("&query.ParseNode{\n%sJoinField: %s,\n%sJoinScope: %s,\n%s}",
			ind1, emitFieldRef(node.JoinField),
			ind1, emitParseNode(node.JoinScope, depth+1),
			ind)
	}

	// Logical branch — AND/OR with several children, or NOT with exactly one.
	if node.IsBranch() {
		children := make([]string, len(node.Children))
		for i, child := range node.Children {
			children[i] = emitParseNode(child, depth+2)
		}
		return fmt.Sprintf(
			"&query.ParseNode{\n%sLogicalOp: %q,\n%sChildren: []*query.ParseNode{\n%s%s,\n%s},\n%s}",
			ind1, node.LogicalOp,
			ind1,
			indent(depth+2), strings.Join(children, fmt.Sprintf(",\n%s", indent(depth+2))),
			ind1,
			ind)
	}

	// A nested query standing alone is an EXISTS term.
	if node.IsSubOnly() {
		return fmt.Sprintf("&query.ParseNode{\n%sSub: %s,\n%s}",
			ind1, emitParseQuery(node.Sub, depth+1), ind)
	}

	// Leaf comparison
	var fields []string
	if node.Left != nil {
		fields = append(fields, fmt.Sprintf("%sLeft: %s,", ind1, emitFieldRef(node.Left)))
	}
	if node.LeftValue != nil {
		fields = append(fields, fmt.Sprintf("%sLeftValue: %s,", ind1, emitValueRef(node.LeftValue)))
	}
	if node.RawColumn != "" {
		fields = append(fields, fmt.Sprintf("%sRawColumn: %q,", ind1, node.RawColumn))
	}
	fields = append(fields, fmt.Sprintf("%sOperator: %q,", ind1, node.Operator))
	if node.Right != nil {
		fields = append(fields, fmt.Sprintf("%sRight: %s,", ind1, emitValueRef(node.Right)))
	}
	if node.Sub != nil {
		fields = append(fields, fmt.Sprintf("%sSub: %s,", ind1, emitParseQuery(node.Sub, depth+1)))
	}
	if node.Agg != nil {
		fields = append(fields, fmt.Sprintf("%sAgg: %s,", ind1, emitSelect(node.Agg)))
	}
	if len(node.Values) > 0 {
		parts := make([]string, len(node.Values))
		for i, value := range node.Values {
			parts[i] = emitValueRef(value)
		}
		fields = append(fields, fmt.Sprintf("%sValues: []*query.ValueRef{%s},",
			ind1, strings.Join(parts, ", ")))
	}

	return fmt.Sprintf("&query.ParseNode{\n%s\n%s}", strings.Join(fields, "\n"), ind)
}

// emitParseQuery reconstructs a goql call: the model it names, the function it is, and its
// parsed body. The model is a name rather than a schema pointer precisely so it can be
// written out here as a literal.
func emitParseQuery(q *query.ParseQuery, depth int) string {
	if q == nil {
		return "nil"
	}
	ind := indent(depth)
	ind1 := indent(depth + 1)

	fields := []string{fmt.Sprintf("%sModel: %q,", ind1, q.Model)}
	if q.Func != "" {
		fields = append(fields, fmt.Sprintf("%sFunc: %q,", ind1, q.Func))
	}
	if q.From != "" {
		fields = append(fields, fmt.Sprintf("%sFrom: %q,", ind1, q.From))
	}
	fields = append(fields, fmt.Sprintf("%sBody: %s,", ind1, emitParsedBody(q.Body, depth+1)))

	return fmt.Sprintf("&query.ParseQuery{\n%s\n%s}", strings.Join(fields, "\n"), ind)
}

func emitFieldRef(ref *query.FieldRef) string {
	if ref == nil {
		return "nil"
	}
	// A CTE column names a query's projection, not a registered schema, so there is nothing
	// to resolve at init time — the names are the whole reference.
	if ref.CTETable != "" {
		return fmt.Sprintf("&query.FieldRef{CTETable: %q, CTEColumn: %q}", ref.CTETable, ref.CTEColumn)
	}

	tableName := ref.Field.TableSchema.TableName
	fieldName := ref.Field.Name

	if ref.Nested != nil {
		return fmt.Sprintf(
			"&query.FieldRef{Field: goql.ResolveField(%q, %q), Nested: %s}",
			tableName, fieldName, emitFieldRef(ref.Nested))
	}
	return fmt.Sprintf("&query.FieldRef{Field: goql.ResolveField(%q, %q)}", tableName, fieldName)
}

func emitValueRef(ref *query.ValueRef) string {
	if ref == nil {
		return "nil"
	}
	if ref.IsColumn {
		return fmt.Sprintf("&query.ValueRef{IsColumn: true, Field: %s}", emitFieldRef(ref.Field))
	}
	// A computed value is a tree of its own; %#v would write its pointers as a literal.
	if ref.Expr != nil {
		return fmt.Sprintf("&query.ValueRef{Expr: &query.ParseExpr{Op: %q, Text: %v, Left: %s, Right: %s}}",
			ref.Expr.Op, ref.Expr.Text,
			emitValueRef(ref.Expr.Left), emitValueRef(ref.Expr.Right))
	}
	// A params reference must be reconstructed as the placeholder type, not as its
	// literal Go value.
	if param, ok := ref.Value.(query.ParamRef); ok {
		return fmt.Sprintf("&query.ValueRef{Value: query.ParamRef{Field: %q}}", param.Field)
	}
	return fmt.Sprintf("&query.ValueRef{Value: %#v}", ref.Value)
}

func emitAssignments(assignments []*query.ParseAssign) string {
	if len(assignments) == 0 {
		return "nil"
	}
	parts := make([]string, len(assignments))
	for i, a := range assignments {
		parts[i] = fmt.Sprintf("{Field: %s, Value: %s}",
			emitFieldRef(a.Field), emitValueRef(a.Value))
	}
	return fmt.Sprintf("[]*query.ParseAssign{%s}", strings.Join(parts, ", "))
}

func emitRelationAssignments(relations []*query.ParseRelation) string {
	if len(relations) == 0 {
		return "nil"
	}
	parts := make([]string, len(relations))
	for i, ra := range relations {
		pks := make([]string, len(ra.RelatedPKs))
		for j, pk := range ra.RelatedPKs {
			pks[j] = fmt.Sprintf("%#v", pk)
		}
		parts[i] = fmt.Sprintf("{Field: %s, RelatedPKs: []any{%s}}",
			emitFieldRef(ra.Field), strings.Join(pks, ", "))
	}
	return fmt.Sprintf("[]*query.ParseRelation{%s}", strings.Join(parts, ", "))
}

func emitBranch(branch *query.ParseBranch, depth int) string {
	ind := indent(depth)
	ind1 := indent(depth + 1)

	return fmt.Sprintf(
		"&query.ParseBranch{\n%sCondition: %s,\n%sAssignments: %s,\n%sRelationAssignments: %s,\n%sSelects: %v,\n%s}",
		ind1, emitParseNode(branch.Condition, depth+1),
		ind1, emitAssignments(branch.Assignments),
		ind1, emitRelationAssignments(branch.RelationAssignments),
		ind1, branch.Selects,
		ind)
}

// emitOptions reconstructs the modifiers a lambda declared as parameters. They must be
// emitted: they live on the parsed body, so a prod binary that omitted them would silently
// run the query without its ORDER BY, LIMIT, projection or preloads.
func emitOptions(opts *query.Options, depth int) string {
	if opts == nil {
		return "nil"
	}

	ind := indent(depth)
	ind1 := indent(depth + 1)
	var fields []string

	if len(opts.Sorts) > 0 {
		terms := make([]string, len(opts.Sorts))
		for i, sort := range opts.Sorts {
			terms[i] = fmt.Sprintf("{By: %q, Desc: %v}", sort.By, sort.Desc)
		}
		fields = append(fields, fmt.Sprintf("Sorts: []query.SortSpec{%s}", strings.Join(terms, ", ")))
	}
	// Limit and Offset are pointers, so they carry "unset" distinctly from zero.
	if opts.Limit != nil {
		fields = append(fields, fmt.Sprintf("Limit: query.IntPtr(%d)", *opts.Limit))
	}
	if opts.Offset != nil {
		fields = append(fields, fmt.Sprintf("Offset: query.IntPtr(%d)", *opts.Offset))
	}
	if len(opts.Fields) > 0 {
		fields = append(fields, fmt.Sprintf("Fields: %s", emitStringSlice(opts.Fields)))
	}
	if opts.PreloadSet {
		fields = append(fields, fmt.Sprintf("Preload: %s", emitStringSlice(opts.Preload)))
		fields = append(fields, "PreloadSet: true")
	}
	if len(opts.GroupBy) > 0 {
		fields = append(fields, fmt.Sprintf("GroupBy: %s", emitStringSlice(opts.GroupBy)))
	}
	if opts.ConflictIgnore {
		fields = append(fields, "ConflictIgnore: true")
	}
	// An explicit join carries a whole condition tree, so it is emitted like one rather
	// than through %#v, which would write the node's pointers as a literal struct.
	if len(opts.Joins) > 0 {
		specs := make([]string, len(opts.Joins))
		for i, join := range opts.Joins {
			specs[i] = fmt.Sprintf("{Table: %q, Type: %q, CTE: %v, On: %s}",
				join.Table, join.Type, join.CTE, emitParseNode(join.On, depth+2))
		}
		fields = append(fields, fmt.Sprintf("Joins: []query.JoinSpec{%s}", strings.Join(specs, ", ")))
	}

	if len(fields) == 0 {
		return "&query.Options{}"
	}
	return fmt.Sprintf("&query.Options{\n%s%s,\n%s}",
		ind1, strings.Join(fields, fmt.Sprintf(",\n%s", ind1)), ind)
}

func emitStringSlice(values []string) string {
	if len(values) == 0 {
		return "nil"
	}
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	return fmt.Sprintf("[]string{%s}", strings.Join(quoted, ", "))
}

// emitProjection reconstructs an explicit projection — one entry per output column, with
// the result field it lands in.
func emitProjection(selects []*query.ParseSelect, depth int) string {
	if len(selects) == 0 {
		return "nil"
	}
	parts := make([]string, len(selects))
	for i, sel := range selects {
		parts[i] = emitSelect(sel)
	}
	return fmt.Sprintf("[]*query.ParseSelect{%s}", strings.Join(parts, ", "))
}

// emitSelect reconstructs one projected column or aggregate.
func emitSelect(sel *query.ParseSelect) string {
	if sel == nil {
		return "nil"
	}
	var fields []string
	if sel.Func != "" {
		fields = append(fields, fmt.Sprintf("Func: %q", sel.Func))
	}
	if sel.Field != nil {
		fields = append(fields, fmt.Sprintf("Field: %s", emitFieldRef(sel.Field)))
	}
	if sel.Value != nil {
		fields = append(fields, fmt.Sprintf("Value: %s", emitValueRef(sel.Value)))
	}
	if sel.Into != "" {
		fields = append(fields, fmt.Sprintf("Into: %q", sel.Into))
	}
	return fmt.Sprintf("&query.ParseSelect{%s}", strings.Join(fields, ", "))
}

// emitSet reconstructs a set operation and its branches, each a whole query.
func emitSet(set *query.ParseSet, depth int) string {
	if set == nil {
		return "nil"
	}
	ind := indent(depth)
	ind1 := indent(depth + 1)
	ind2 := indent(depth + 2)

	branches := make([]string, len(set.Branches))
	for i, branch := range set.Branches {
		branches[i] = emitParseQuery(branch, depth+2)
	}

	return fmt.Sprintf("&query.ParseSet{\n%sOp: %q,\n%sBranches: []*query.ParseQuery{\n%s%s,\n%s},\n%s}",
		ind1, set.Op,
		ind1,
		ind2, strings.Join(branches, fmt.Sprintf(",\n%s", ind2)),
		ind1, ind)
}

func emitParsedBody(body *query.ParseBody, depth int) string {
	ind := indent(depth)
	ind1 := indent(depth + 1)
	ind2 := indent(depth + 2)

	if len(body.Branches) == 0 {
		if body.Options == nil && len(body.Joined) == 0 && len(body.Select) == 0 &&
			body.Set == nil && len(body.With) == 0 {
			return "&query.ParseBody{}"
		}
		return fmt.Sprintf("&query.ParseBody{\n%sOptions: %s,\n%sJoined: %s,\n%sSelect: %s,\n%sSet: %s,\n%sWith: %s,\n%s}",
			ind1, emitOptions(body.Options, depth+1),
			ind1, emitStringSlice(body.Joined),
			ind1, emitProjection(body.Select, depth+1),
			ind1, emitSet(body.Set, depth+1),
			ind1, emitWith(body.With, depth+1), ind)
	}

	branches := make([]string, len(body.Branches))
	for i, branch := range body.Branches {
		branches[i] = emitBranch(branch, depth+2)
	}

	return fmt.Sprintf(
		"&query.ParseBody{\n%sBranches: []*query.ParseBranch{\n%s%s,\n%s},\n%sOptions: %s,\n%sJoined: %s,\n%sSelect: %s,\n%sSet: %s,\n%sWith: %s,\n%s}",
		ind1,
		ind2, strings.Join(branches, fmt.Sprintf(",\n%s", ind2)),
		ind1,
		ind1, emitOptions(body.Options, depth+1),
		ind1, emitStringSlice(body.Joined),
		ind1, emitProjection(body.Select, depth+1),
		ind1, emitSet(body.Set, depth+1),
		ind1, emitWith(body.With, depth+1),
		ind)
}

// emitWith reconstructs the common table expressions a body defines. Each is a whole query,
// so it is emitted like one.
func emitWith(with []*query.ParseCTE, depth int) string {
	if len(with) == 0 {
		return "nil"
	}
	parts := make([]string, len(with))
	for i, cte := range with {
		parts[i] = fmt.Sprintf("{Name: %q, Columns: %s, Recursive: %v, Query: %s}",
			cte.Name, emitStringSlice(cte.Columns), cte.Recursive,
			emitParseQuery(cte.Query, depth+1))
	}
	return fmt.Sprintf("[]*query.ParseCTE{%s}", strings.Join(parts, ", "))
}
