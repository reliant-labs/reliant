package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// TestPrintLeftAlone_PrintsDetailUnderItsPath: the exact edits a refusal
// carries (a stale starter test's rows and their replacement bodies) are
// printed under its path, indented, in order — one sentence cannot carry
// a code block.
func TestPrintLeftAlone_PrintsDetailUnderItsPath(t *testing.T) {
	var buf bytes.Buffer
	printLeftAlone(&buf, []checksums.LeftAlone{
		{Path: "frontends/web/src/hooks/run-service-hooks.test.tsx", Why: "two rows are stale",
			Detail: []string{"line 12: useX is now a useQuery — replace the row's body with:", "    renderHook(...)"}},
		{Path: "frontends/web/src/hooks/index.ts", Why: "hand-written"},
	})
	want := "⚠️  2 path(s) left as they are — forge did not write them:\n" +
		"   - frontends/web/src/hooks/run-service-hooks.test.tsx: two rows are stale\n" +
		"       line 12: useX is now a useQuery — replace the row's body with:\n" +
		"           renderHook(...)\n" +
		"   - frontends/web/src/hooks/index.ts: hand-written\n"
	if got := buf.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if strings.Count(buf.String(), "index.ts") != 1 {
		t.Error("a path with no detail prints one line")
	}
}
