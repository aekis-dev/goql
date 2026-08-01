package query

import (
	"fmt"
	"strings"
)

// Comparing "the type the model wants" against "the type the database reports" is the whole
// difficulty of detecting a type change. The two are written differently even when they mean
// the same thing: Postgres reports `character varying` where goql emits `varchar`, MySQL
// reports `tinyint(1)` for a boolean, and every engine differs on case.
//
// Only the engine knows its own canonical spellings, so normalisation is a Spec method.
// Normalising both sides and comparing the results avoids proposing migrations that would
// change nothing.

// splitType separates a type into its base name and its parenthesised arguments,
// lowercasing and collapsing whitespace: "NUMERIC(10, 2)" → ("numeric", "10,2").
//
// Text on *either* side of the parentheses belongs to the base name, because Postgres puts a
// precision in the middle: `timestamp(6) without time zone` is ("timestamp without time
// zone", "6"). Discarding the trailing words would make that compare equal to
// `timestamp(6) with time zone`, which is a different type.
func splitType(declared string) (base, args string) {
	text := strings.ToLower(strings.TrimSpace(declared))

	open := strings.Index(text, "(")
	if open == -1 {
		return strings.Join(strings.Fields(text), " "), ""
	}

	close := strings.LastIndex(text, ")")
	if close < open {
		close = len(text)
	}

	base = strings.Join(strings.Fields(text[:open]+" "+text[min(close+1, len(text)):]), " ")
	args = strings.ReplaceAll(text[open+1:close], " ", "")
	return base, args
}

// joinType reassembles a normalised type.
func joinType(base, args string) string {
	if args == "" {
		return base
	}
	return fmt.Sprintf("%s(%s)", base, args)
}

// normalizeWithAliases canonicalises a type using an engine's alias table.
func normalizeWithAliases(declared string, aliases map[string]string) string {
	base, args := splitType(declared)
	if canonical, ok := aliases[base]; ok {
		// An alias may carry its own arguments, as MySQL's boolean → tinyint(1) does.
		aliasBase, aliasArgs := splitType(canonical)
		if args == "" {
			args = aliasArgs
		}
		base = aliasBase
	}
	return joinType(base, args)
}

// TypesEqual reports whether a column's declared type and the type reported by
// introspection mean the same thing to this engine.
func (d *Dialect) TypesEqual(wanted, reported string) bool {
	return d.NormalizeType(wanted) == d.NormalizeType(reported)
}
