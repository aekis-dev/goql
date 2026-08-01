package goql

// Aggregates are written inside a lambda's projection, where they name a column to
// aggregate and the field of the result type it lands in:
//
//	rows, err := goql.Select[OrderTotals](ctx, e, func(t *OrderTotals, o *Order, from *goql.From) bool {
//	    from.Model = o
//	    t.Customer = o.Customer          // a plain column, and therefore a GROUP BY term
//	    t.Total    = goql.Sum(o.Total)   // SUM(total_amount) AS "Total"
//	    t.Orders   = goql.Count()        // COUNT(*)      AS "Orders"
//	    return o.Priority == "High"
//	})
//
// Like everything else in a lambda they are parsed, never executed — calling one outside a
// lambda returns the zero value, which is why they are useless anywhere else.
//
// They are package-level functions rather than methods on a carrier because a method cannot
// have type parameters ("method must have no type parameters"), and Min and Max must return
// what they were given: the minimum of a string column is a string.

// Sum totals a numeric column. The result keeps the column's own type, so a value declared
// as a decimal in the model stays one here.
func Sum[T any](column T) T { var zero T; return zero }

// Avg averages a numeric column. An average is fractional whatever the column is, so it is
// always a float64.
func Avg[T any](column T) float64 { return 0 }

// Min is the smallest value of a column, keeping the column's type — the earliest timestamp
// is a timestamp, the first name is a string.
func Min[T any](column T) T { var zero T; return zero }

// Max is the largest value of a column, keeping the column's type.
func Max[T any](column T) T { var zero T; return zero }

// Count counts rows, or the non-null values of a column when given one.
func Count(column ...any) int64 { return 0 }

// Set operations combine whole queries. Bind each branch to a name, then return the
// combination — the lambda's result *is* the combined query, so options declared alongside
// it (Sort, Limit) apply to the whole thing rather than to a branch:
//
//	goql.Select[Movement](ctx, e, func(m *Movement, sort *goql.Sort) bool {
//	    sort.By = "Amount"
//	    live, _     := goql.Select[Movement](ctx, e, …)
//	    archived, _ := goql.Select[Movement](ctx, e, …)
//	    return goql.Union(live, archived)
//	})
//
// Every branch is []*T, so the compiler checks that they have the same shape. They are not
// composable — these return bool, being the lambda's result — but they are variadic, which
// covers combining more than two.

// Union combines branches and removes duplicate rows, as SQL's bare UNION does.
func Union[T any](branches ...[]*T) bool { return false }

// UnionAll combines branches and keeps duplicates, which is cheaper: no sort to deduplicate.
func UnionAll[T any](branches ...[]*T) bool { return false }

// Intersect keeps the rows every branch produced. MySQL supports it from 8.0.31.
func Intersect[T any](branches ...[]*T) bool { return false }

// Except keeps the rows of the first branch that no later branch produced. MySQL supports it
// from 8.0.31.
func Except[T any](branches ...[]*T) bool { return false }
