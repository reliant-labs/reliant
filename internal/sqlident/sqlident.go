// Package sqlident reads postgres identifiers out of SQL text.
//
// forge reads migration DDL as text wherever no postgres is at hand: `forge
// project shapes`, `forge map`, the migration-context reader behind entity
// births, the read-only-fields and column-markers lints, and the
// unscoped_auth audit. Each used to spell its own identifier pattern, and
// each spelled only the bare case, `tasks`. pg_dump writes `CREATE TABLE
// public.tasks`; hand-written DDL writes `"public"."tasks"` or
// `public."Tasks"`. A pattern that stops at the first `.` read all of those
// as a table named `public`, and one that stops at a `"` missed the
// statement outright.
//
// This package is the one place that knows the grammar. A name is one to
// three parts separated by `.` (catalog.schema.name), with optional
// whitespace around each dot. A part is either unquoted — a letter or `_`,
// then letters, digits, `_` or `$` — which postgres folds to lower case, or
// double-quoted — any characters, `""` standing for one quote — which
// postgres keeps verbatim. Normalizing the same way postgres resolves the
// name is what lets a textual reader agree with the applied schema.
package sqlident

import "strings"

// Part is a regexp fragment matching one identifier part, quoted or not. It
// has no capture group, so it composes into a caller's pattern without
// renumbering that pattern's groups.
const Part = `(?:"(?:[^"]|"")+"|[A-Za-z_][A-Za-z0-9_$]*)`

// Qualified is a regexp fragment matching a name of one to three parts
// (`tasks`, `public.tasks`, `"public"."Tasks"`, `public.tasks.owner_id`). No
// capture group, like Part. The repetition is greedy, so a caller that
// captures it gets the whole qualified name; pass the capture to Parts or
// Last to read it.
const Qualified = `(?:` + Part + `\s*\.\s*){0,2}` + Part

// Parts splits a name matched by Qualified into its parts, each normalized
// as postgres resolves it: an unquoted part folded to lower case, a quoted
// one unquoted verbatim. `public."Tasks"` is ["public", "Tasks"]. Text that
// is not a name yields whatever parts precede the first character that
// cannot continue one.
func Parts(raw string) []string {
	var parts []string
	s := strings.TrimSpace(raw)
	for s != "" {
		var part string
		if s[0] == '"' {
			var b strings.Builder
			i := 1
			for i < len(s) {
				if s[i] == '"' {
					if i+1 < len(s) && s[i+1] == '"' {
						b.WriteByte('"')
						i += 2
						continue
					}
					break
				}
				b.WriteByte(s[i])
				i++
			}
			part = b.String()
			s = s[min(i+1, len(s)):]
		} else {
			end := strings.IndexByte(s, '.')
			if end < 0 {
				end = len(s)
			}
			part = strings.ToLower(strings.TrimSpace(s[:end]))
			s = s[end:]
		}
		parts = append(parts, part)
		s = strings.TrimSpace(s)
		if !strings.HasPrefix(s, ".") {
			break
		}
		s = strings.TrimSpace(s[1:])
	}
	return parts
}

// Last returns the normalized last part of a name: the object's own name,
// with any schema (and catalog) qualification dropped. `public.tasks`,
// `"public"."tasks"` and `tasks` all return "tasks".
func Last(raw string) string {
	parts := Parts(raw)
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}
