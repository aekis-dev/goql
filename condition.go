package goql

// Condition expresses a comparison that Go's own operators cannot spell, such as LIKE,
// IN or IS NULL:
//
//	goql.Select[Customer](ctx, e, func(c *Customer) bool {
//	    return goql.Condition(c.Name, "LIKE", "%smith%")
//	})
//	goql.Select[Customer](ctx, e, func(c *Customer) bool {
//	    return goql.Condition(c.Status, "IN", "Active", "Premium")
//	})
//	goql.Select[Customer](ctx, e, func(c *Customer) bool {
//	    return goql.Condition(c.Deleted, "IS NULL")
//	})
//
// It coexists with native operators rather than replacing them — `c.Total > 100` keeps
// working, and the two can be combined with && and ||.
//
// The field argument is normally an entity field. It may instead be a string, which is
// emitted verbatim as the left-hand side; that is the escape hatch for expressions goql
// cannot model, such as a JSON path, and it is on the caller to write something the target
// engine understands:
//
//	goql.Condition("meta ->> 'colour'", "=", "red")
//
// Like every lambda body, calls to Condition are parsed from source and never executed.
// Calling one directly is a mistake, so it panics rather than returning a misleading zero.
func Condition(field any, op string, values ...any) bool {
	panic("goql: Condition is parsed from source, not executed — call it only inside a goql lambda")
}
