// Tests for forge lint --suggest-buf-excepts. The interesting logic
// is the buf-output parser + threshold filter, both pure functions
// over strings; no external buf binary is required to exercise them.
package lint

import (
	"strings"
	"testing"
)

// TestParseBufLintRuleCounts pins the diagnostic-line regex against
// the two output shapes buf uses across versions: with and without a
// trailing message.
func TestParseBufLintRuleCounts(t *testing.T) {
	output := strings.Join([]string{
		`proto/v1/a.proto:1:1: PACKAGE_VERSION_SUFFIX: Package name should be suffixed.`,
		`proto/v1/b.proto:1:1: PACKAGE_VERSION_SUFFIX`,
		`proto/v1/c.proto:5:7: FIELD_LOWER_SNAKE_CASE: Field should be snake_case.`,
		`proto/v1/d.proto:6:7: FIELD_LOWER_SNAKE_CASE`,
		`proto/v1/e.proto:7:7: FIELD_LOWER_SNAKE_CASE`,
		`some non-diagnostic header line that should be ignored`,
		``,
	}, "\n")

	got := parseBufLintRuleCounts(output)

	// PACKAGE_VERSION_SUFFIX → {a.proto, b.proto}
	if files, ok := got["PACKAGE_VERSION_SUFFIX"]; !ok {
		t.Errorf("missing PACKAGE_VERSION_SUFFIX bucket")
	} else if len(files) != 2 {
		t.Errorf("PACKAGE_VERSION_SUFFIX = %d files, want 2 (%v)", len(files), files)
	}

	// FIELD_LOWER_SNAKE_CASE → {c, d, e}
	if files, ok := got["FIELD_LOWER_SNAKE_CASE"]; !ok {
		t.Errorf("missing FIELD_LOWER_SNAKE_CASE bucket")
	} else if len(files) != 3 {
		t.Errorf("FIELD_LOWER_SNAKE_CASE = %d files, want 3 (%v)", len(files), files)
	}

	// Non-matching lines don't leak into the map.
	if _, ok := got["NON"]; ok {
		t.Errorf("non-matching line crept into the map: %v", got)
	}
}

// TestSuggestionsAboveThreshold pins the strict > threshold semantics:
// a rule that fires on exactly N files does NOT make it past
// threshold=N — has to be more.
func TestSuggestionsAboveThreshold(t *testing.T) {
	counts := map[string]map[string]bool{
		"AT_THRESHOLD":    {"a.proto": true, "b.proto": true, "c.proto": true},                  // 3 files = threshold; excluded
		"ABOVE_THRESHOLD": {"a.proto": true, "b.proto": true, "c.proto": true, "d.proto": true}, // 4 files; included
		"BELOW_THRESHOLD": {"a.proto": true},                                                    // 1 file; excluded
	}

	got := suggestionsAboveThreshold(counts, 3)
	if len(got) != 1 {
		t.Fatalf("got %d suggestions, want 1 (only ABOVE_THRESHOLD qualifies)", len(got))
	}
	if got[0].Rule != "ABOVE_THRESHOLD" {
		t.Errorf("suggestion[0] = %q, want ABOVE_THRESHOLD", got[0].Rule)
	}
	if got[0].FileCount != 4 {
		t.Errorf("FileCount = %d, want 4", got[0].FileCount)
	}
}

// TestSuggestionsAboveThreshold_StableOrder pins alphabetic ordering
// so the YAML output is deterministic. Without sort, map-iteration
// order would leak into the rendered snippet and break diff-friendly
// CI artifacts.
func TestSuggestionsAboveThreshold_StableOrder(t *testing.T) {
	counts := map[string]map[string]bool{
		"Z_RULE": fileSet("a", "b", "c", "d"),
		"A_RULE": fileSet("a", "b", "c", "d"),
		"M_RULE": fileSet("a", "b", "c", "d"),
	}
	got := suggestionsAboveThreshold(counts, 3)
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	if got[0].Rule != "A_RULE" || got[1].Rule != "M_RULE" || got[2].Rule != "Z_RULE" {
		t.Errorf("not alphabetically sorted: %v", got)
	}
}

// TestPrintBufLintExceptYAML_EmptyHasFriendlyMessage verifies the
// no-suggestions branch prints something the user can read (a comment
// explaining why), rather than a bare empty YAML block.
func TestPrintBufLintExceptYAML_EmptyHasFriendlyMessage(t *testing.T) {
	var sb strings.Builder
	printBufLintExceptYAML(&sb, nil)
	out := sb.String()
	if !strings.Contains(out, "nothing to suggest") {
		t.Errorf("expected 'nothing to suggest' message, got:\n%s", out)
	}
	if strings.Contains(out, "except:") {
		t.Errorf("empty suggestions should not print an except: stanza; got:\n%s", out)
	}
}

// TestPrintBufLintExceptYAML_RendersExceptStanza pins the canonical
// buf.yaml shape: STANDARD use + each suggested rule indented under
// except:. The user pastes this verbatim.
func TestPrintBufLintExceptYAML_RendersExceptStanza(t *testing.T) {
	suggestions := []bufExceptSuggestion{
		{Rule: "PACKAGE_VERSION_SUFFIX", FileCount: 8},
		{Rule: "RPC_REQUEST_STANDARD_NAME", FileCount: 12},
	}
	var sb strings.Builder
	printBufLintExceptYAML(&sb, suggestions)
	out := sb.String()
	wants := []string{
		"lint:",
		"use:",
		"- STANDARD",
		"except:",
		"- PACKAGE_VERSION_SUFFIX  # fired on 8 file(s)",
		"- RPC_REQUEST_STANDARD_NAME  # fired on 12 file(s)",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in rendered YAML:\n%s", w, out)
		}
	}
}

// fileSet returns a map-as-set for the given file names. Trim the
// `.proto` suffix off the keys is unnecessary — the test asserts on
// counts, not file content, so any unique strings work.
func fileSet(names ...string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n+".proto"] = true
	}
	return out
}
