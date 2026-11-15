package pgtest

import (
	"fmt"
	"sort"
	"strings"
)

// pgCtlOptions is the `pg_ctl start -o` value for a server on port with the
// given postgresql.conf overrides, spelled as embedded-postgres spells it:
// every value double-quoted, because on Windows pg_ctl hands the string to
// CMD, which delimits with double quotes only. Keys are sorted so the command
// line is deterministic.
func pgCtlOptions(port uint32, params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	opts := []string{fmt.Sprintf("-p %d", port)}
	for _, k := range keys {
		opts = append(opts, fmt.Sprintf(`-c %s="%s"`, k, params[k]))
	}
	return strings.Join(opts, " ")
}
