package codegen

import (
	"fmt"
	"strings"
)

// A `flag:` on a sensitive config field.
//
// The loader never registers a CLI flag for a sensitive field
// (forge/pkg/config: a secret on a command line leaks through `ps` and shell
// history), so a sensitive field's `flag:` annotation names a flag the binary
// does not have. Nothing failed: the annotation was silently inert, and code
// written against it — the scaffold's own db.go read `--database-url` for
// years — fell back to the environment without a word (friction #26).
// SensitiveConfigFlags names each one, so generate can say so.

// SensitiveConfigFlags describes every config message that declares a `flag:`
// on a sensitive field, one entry per message, in message order. Empty when
// none does.
func SensitiveConfigFlags(messages []ConfigMessage) []string {
	var out []string
	for _, m := range messages {
		var fields []string
		for _, f := range m.Fields {
			if f.Sensitive && f.Flag != "" {
				fields = append(fields, fmt.Sprintf("%s (--%s)", f.Name, f.Flag))
			}
		}
		if len(fields) == 0 {
			continue
		}
		where := m.Name
		if m.ProtoFile != "" {
			where = m.ProtoFile + ": " + m.Name
		}
		out = append(out, fmt.Sprintf("%s declares a flag on sensitive field(s) %s, and no flag is ever registered for a sensitive field (a secret on a command line leaks through ps and shell history): delete their `flag:` lines; the values come from their env vars only",
			where, strings.Join(fields, ", ")))
	}
	return out
}
