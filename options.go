package goql

import (
	"fmt"

	"github.com/aekis-dev/goql/query"
)

// Query modifiers are expressed with the same types on both halves of the API.
//
// On the struct-based calls they are passed as trailing values:
//
//	goql.Search(ctx, e, models.Customer{Country: "USA"},
//	    goql.Sort{By: "Age", Desc: true}, goql.Limit{Value: 20})
//
// On the lambda-based calls they are declared as extra parameters and assigned inside
// the body, where they are recognised structurally — like everything else in a lambda,
// those assignments are parsed, not executed:
//
//	goql.Select[models.Customer](ctx, e, func(c *models.Customer, sort *goql.Sort, limit *goql.Limit) bool {
//	    sort.By = "Age"
//	    sort.Desc = true
//	    limit.Value = 20
//	    return c.Country == "USA"
//	})

// Sort orders results by a field. By is a Go field name, not a column name. Declare
// several to sort by more than one field; they apply in the order given.
type Sort struct {
	By   string
	Desc bool
}

// Limit caps the number of rows returned.
type Limit struct {
	Value int
}

// Offset skips rows before returning any. Engines require a limit alongside an offset,
// so goql emits an open-ended one when only Offset is given.
type Offset struct {
	Value int
}

// Fields restricts the projection to the named Go fields; the primary key is always
// included. Fields left out come back as Go zero values, which is indistinguishable
// from a column that is genuinely empty.
type Fields struct {
	Names []string
}

// Preload names the relation fields to load alongside the result. Each relation is
// fetched with one batched query for the whole result set, never one query per row.
//
// A model may mark relations to load by default with `Preload: true` on the field. When a
// query names any relations, its list **replaces** those defaults, so an empty Preload is
// how you ask for none.
type Preload struct {
	Fields []string
}

// Conflict controls what an Insert does with a row that collides with an existing one.
// Ignore skips it instead of failing.
//
// The name is deliberately narrow: a full upsert (a conflict target plus DO UPDATE SET) is
// a design of its own, and calling this OnConflict would half-claim it.
type Conflict struct {
	Ignore bool
}

// optionNames maps the carrier type names the parser recognises in a lambda signature.
// Keep in sync with the types above and with buildOptions.
var optionNames = map[string]bool{
	"Sort":     true,
	"Limit":    true,
	"Offset":   true,
	"Fields":   true,
	"Preload":  true,
	"Conflict": true,
	"From":     true,
	"Group":    true,
	"Join":     true,
}

// buildOptions folds trailing option values from a struct-based call into the query
// representation. Anything that is not a known option type is rejected rather than
// silently ignored.
func buildOptions(opts []any) (*query.Options, error) {
	if len(opts) == 0 {
		return nil, nil
	}

	out := &query.Options{}
	for _, opt := range opts {
		switch o := opt.(type) {
		case Sort:
			out.Sorts = append(out.Sorts, query.SortSpec{By: o.By, Desc: o.Desc})
		case *Sort:
			out.Sorts = append(out.Sorts, query.SortSpec{By: o.By, Desc: o.Desc})
		case Limit:
			v := o.Value
			out.Limit = &v
		case *Limit:
			v := o.Value
			out.Limit = &v
		case Offset:
			v := o.Value
			out.Offset = &v
		case *Offset:
			v := o.Value
			out.Offset = &v
		case Fields:
			out.Fields = append(out.Fields, o.Names...)
		case *Fields:
			out.Fields = append(out.Fields, o.Names...)
		case Preload:
			out.Preload = append(out.Preload, o.Fields...)
			out.PreloadSet = true
		case *Preload:
			out.Preload = append(out.Preload, o.Fields...)
			out.PreloadSet = true
		default:
			return nil, fmt.Errorf(
				"%w: %T is not a goql query option (want Sort, Limit, Offset, Fields or Preload)",
				ErrInvalidOption, opt)
		}
	}
	return out, nil
}

// Engine specs, re-exported from the query package so a caller needs one import:
//
//	e := goql.New(db, goql.SQLite{})
type (
	SQLite   = query.SQLite
	Postgres = query.Postgres
	MySQL    = query.MySQL
)

// From names the model a query reads from, for a lambda whose result type is not itself a
// model:
//
//	func(t *OrderTotals, o *Order, from *goql.From) bool { from.Model = o; … }
//
// Assign the lambda's own model parameter, not a fresh value: it points at the declaration
// rather than restating it, so the two cannot disagree. When the result type is a model, the
// query reads from that model and no From is needed.
type From struct {
	Model any

	// Query reads from a named query — a common table expression — instead of a table:
	//
	//	totals, _ := goql.Select[CustomerTotal](ctx, e, …)
	//	from.Query = totals
	//	from.Model = t          // t *CustomerTotal is a row of it
	//
	// The CTE is named after the Go variable it was bound to, so the name is stated once.
	// A CTE is evaluated before the outer query has a row, so its body may not reference
	// the outer query — that is a correlated subquery, which only IN and EXISTS can take.
	Query any
}

// JoinType is the kind of join an explicit Join performs.
type JoinType string

const (
	Inner JoinType = "INNER"
	Left  JoinType = "LEFT"
	Right JoinType = "RIGHT"
	Full  JoinType = "FULL"
)

// Join relates another declared model to the query explicitly, stating the condition
// rather than leaving it to be inferred:
//
//	func(i *Invoice, p *Payment, j *goql.Join) bool {
//	    j.Model = p
//	    j.On    = i.Ref == p.Ref
//	    j.Type  = goql.Left
//	    return p.Method == "card"
//	}
//
// Model names one of the lambda's own model parameters, the way From does — pointing at the
// declaration rather than restating it. On is the join condition; like every expression in a
// lambda it is parsed, not evaluated. Type defaults to Inner.
//
// Declare several *Join parameters to join several models. A comparison between two declared
// models is still read as a join condition without a Join carrier, which stays the shorter
// spelling for the inner-join case; an explicit Join is what makes the kind sayable.
//
// Not every engine has every kind: MySQL has no FULL JOIN, and SQLite gained RIGHT and FULL
// in 3.39. An unsupported kind is refused while building the statement.
type Join struct {
	Model any
	On    bool
	Type  JoinType

	// Query joins a named query instead of a table — a CTE bound in this lambda, or the
	// CTE being defined, which is what makes a recursive one recursive:
	//
	//	tree, _ := goql.Select[CatNode](ctx, e, func(t []*CatNode) bool {
	//	    …
	//	    children, _ := goql.Select[CatNode](ctx, e,
	//	        func(n *CatNode, prev *CatNode, c *Category, f *goql.From, j *goql.Join) bool {
	//	            j.Query, j.Model = t, prev     // ← join the CTE to itself
	//	            j.On = c.Parent.ID == prev.ID
	//	            …
	//	        })
	//	    return goql.UnionAll(roots, children)
	//	})
	//
	// The self-reference is declared as the combining lambda's own parameter — `t []*CatNode`,
	// the rows produced so far — so recursion is stated in the source rather than inferred.
	Query any
}

// Group names extra grouping keys for a projection, as Go field names:
//
//	g.By = []string{"Customer"}
//
// They are additive — a projected plain column is always a grouping key — so these are the
// keys you could not project: a relation, which has no scalar Go field, or a column you do
// not want in the result.
type Group struct {
	By []string
}
