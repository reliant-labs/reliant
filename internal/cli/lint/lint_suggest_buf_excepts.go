// `forge lint --suggest-buf-excepts` walks the project's .proto files,
// runs `buf lint`, and prints a buf.yaml `lint.except:` snippet
// suggesting which STANDARD rules to silence for a port-in-progress
// codebase.
//
// Migration projects (a Connect API spec ported from a pre-forge repo)
// routinely hit the same handful of STANDARD rules: PACKAGE_VERSION_SUFFIX
// when the original namespace was `foo.v1` vs the buf-canonical
// `foo.v1`, RPC_REQUEST_STANDARD_NAME when the RPCs predate the
// `<Method>Request` convention, FIELD_LOWER_SNAKE_CASE when fields
// were snake_case-with-acronyms, etc. The hint at the end of
// `forge lint` (printBufLintExceptHint) already nudges the user
// toward the four most common offenders, but the nudge is a static
// list — it can't tell which rules ACTUALLY fire in the user's tree.
//
// This command runs `buf lint` and aggregates the output by rule
// name, then prints the rules that fired across more than the
// `--threshold` (default 3) files. The heuristic is intentionally
// conservative: a single .proto with one violation is probably a
// real bug to fix; the same violation across many files is almost
// always a port-wide convention mismatch best resolved via except.
//
// The output is YAML-shaped so the user can paste it directly into
// buf.yaml. Nothing is mutated.
//
// FRICTION 2026-06-02: cp-forge proto port — author hit
// FIELD_LOWER_SNAKE_CASE on 40+ files and had to grep buf output
// manually to count rule occurrences before deciding what to except.
package lint

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// bufExceptSuggestThreshold is the per-rule file-count threshold above
// which a STANDARD rule earns an `except:` suggestion. Below the
// threshold, the rule is more likely a fixable bug than a
// port-convention mismatch.
//
// The default of 3 means a rule must fire on at least 4 distinct
// .proto files (strictly greater than 3) to make the suggestion list.
const bufExceptSuggestThreshold = 3

// runSuggestBufExcepts is the command entry point. Loads the project
// (optional — buf can run without forge.yaml), invokes `buf lint`,
// parses the output for STANDARD rule violations, and prints a
// buf.yaml `except:` snippet covering rules that fired on >threshold
// distinct files.
//
// Returns nil even when buf lint fails (the parser handles the
// non-zero exit). Returns an error only when buf can't be invoked at
// all (missing binary, not in a buf module).
func runSuggestBufExcepts(ctx context.Context) error {
	bufPath, err := exec.LookPath("buf")
	if err != nil {
		return fmt.Errorf("buf not found on PATH (install: https://buf.build/docs/installation): %w", err)
	}

	cmd := exec.CommandContext(ctx, bufPath, "lint")
	var stdout, stderr strings.Builder
	cmd.Stdout = io.MultiWriter(&stdout, &stderr) // some buf versions emit to either stream
	cmd.Stderr = &stderr
	_ = cmd.Run() // non-zero exit means lint failures — that's our signal

	combined := stdout.String() + stderr.String()
	counts := parseBufLintRuleCounts(combined)
	suggested := suggestionsAboveThreshold(counts, bufExceptSuggestThreshold)
	printBufLintExceptYAML(os.Stdout, suggested)
	return nil
}

// bufLintLineRegexp matches a single buf lint diagnostic line. The
// canonical buf output shape is:
//
//	<file>:<line>:<col>: <RULE_NAME>: <message>
//
// Or, on some versions:
//
//	<file>:<line>:<col>: <RULE_NAME>
//
// Both forms are accepted. The regex captures the file path and the
// rule name; line/col are intentionally ignored — the per-rule file
// count is what we tally.
var bufLintLineRegexp = regexp.MustCompile(`^([^:\s]+\.proto):\d+:\d+:\s*([A-Z][A-Z0-9_]+)\b`)

// parseBufLintRuleCounts walks buf lint's output and returns a map
// from rule name → set of distinct .proto file paths where the rule
// fired. The caller turns the file-count into a suggestion via the
// threshold check.
//
// Lines that don't match the canonical diagnostic shape are skipped
// silently — buf emits header/footer text the regex shouldn't try
// to interpret.
func parseBufLintRuleCounts(output string) map[string]map[string]bool {
	counts := make(map[string]map[string]bool)
	for _, line := range strings.Split(output, "\n") {
		m := bufLintLineRegexp.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		file, rule := m[1], m[2]
		if _, ok := counts[rule]; !ok {
			counts[rule] = make(map[string]bool)
		}
		counts[rule][file] = true
	}
	return counts
}

// suggestionsAboveThreshold returns the rules whose distinct-file
// count exceeds threshold. The returned slice is sorted alphabetically
// for deterministic output.
//
// We use strict > threshold (not >=) so the default threshold of 3
// requires the rule to fire on at least 4 distinct files — matching
// the task spec.
func suggestionsAboveThreshold(counts map[string]map[string]bool, threshold int) []bufExceptSuggestion {
	var out []bufExceptSuggestion
	for rule, files := range counts {
		if len(files) > threshold {
			out = append(out, bufExceptSuggestion{Rule: rule, FileCount: len(files)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rule < out[j].Rule })
	return out
}

// bufExceptSuggestion describes one rule that earned an except: entry.
// The file count is surfaced in the YAML comment so the user can
// triage by impact ("28 files? definitely an except") vs ("4 files?
// maybe fix instead").
type bufExceptSuggestion struct {
	Rule      string
	FileCount int
}

// printBufLintExceptYAML emits the buf.yaml-shaped snippet to w. Zero
// suggestions print a one-liner explaining there's nothing to do
// (matches `forge lint --suggest-excludes` behaviour).
func printBufLintExceptYAML(w io.Writer, suggestions []bufExceptSuggestion) {
	if len(suggestions) == 0 {
		_, _ = fmt.Fprintln(w, "# forge lint --suggest-buf-excepts: no STANDARD rule fired on more")
		_, _ = fmt.Fprintf(w, "# than %d distinct .proto file(s) — nothing to suggest.\n", bufExceptSuggestThreshold)
		_, _ = fmt.Fprintln(w, "# Either your proto tree is already clean, or each violation is a")
		_, _ = fmt.Fprintln(w, "# one-off bug worth fixing in place rather than excepting.")
		return
	}
	_, _ = fmt.Fprintln(w, "# forge lint --suggest-buf-excepts: STANDARD rules that fired on")
	_, _ = fmt.Fprintf(w, "# more than %d distinct .proto file(s). Paste into buf.yaml.\n", bufExceptSuggestThreshold)
	_, _ = fmt.Fprintln(w, "# Review each entry — a rule fired widely is usually a port-wide")
	_, _ = fmt.Fprintln(w, "# convention mismatch; one fired narrowly might be a fixable bug.")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "lint:")
	_, _ = fmt.Fprintln(w, "  use:")
	_, _ = fmt.Fprintln(w, "    - STANDARD")
	_, _ = fmt.Fprintln(w, "  except:")
	for _, s := range suggestions {
		_, _ = fmt.Fprintf(w, "    - %s  # fired on %d file(s)\n", s.Rule, s.FileCount)
	}
}
