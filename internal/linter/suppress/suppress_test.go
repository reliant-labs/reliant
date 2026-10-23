package suppress

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

func errFinding(rule string, line int) finding.Finding {
	return finding.Finding{
		Rule:     rule,
		Severity: finding.SeverityError,
		File:     "internal/handlers/api/service.go",
		Line:     line,
		Message:  "boom",
	}
}

func warnFinding(rule string, line int) finding.Finding {
	f := errFinding(rule, line)
	f.Severity = finding.SeverityWarning
	return f
}

func TestLineDirectiveSuppressesOnlyItsLine(t *testing.T) {
	src := strings.Join([]string{
		"package api", // 1
		"// forge:lint-disable-next-line my-rule: known", // 2
		"offending()",     // 3
		"alsoOffending()", // 4
	}, "\n")

	res := Apply(src, []finding.Finding{errFinding("my-rule", 3), errFinding("my-rule", 4)})

	if len(res.Suppressed) != 1 || res.Suppressed[0].Line != 3 {
		t.Fatalf("expected only line 3 suppressed, got %+v", res.Suppressed)
	}
	if len(res.Kept) != 1 || res.Kept[0].Line != 4 {
		t.Fatalf("expected line 4 kept, got %+v", res.Kept)
	}
}

func TestFileDirectiveSuppressesWholeFileIncludingFileLevelFindings(t *testing.T) {
	src := "// forge:lint-disable-file my-rule: CLI project, no handlers\npackage api\ncode()\n"

	// Line 0 is how forge linters spell a file-level finding.
	res := Apply(src, []finding.Finding{errFinding("my-rule", 0), errFinding("my-rule", 99)})

	if len(res.Kept) != 0 {
		t.Fatalf("file-scope directive should suppress everything, kept %+v", res.Kept)
	}
	if len(res.Suppressed) != 2 {
		t.Fatalf("expected 2 suppressions, got %d", len(res.Suppressed))
	}
}

// A line-scoped directive must NOT silence a file-level (line 0)
// finding — there is no line for it to attach to, and letting it match
// would make a narrow directive quietly the broadest one.
func TestLineDirectiveDoesNotSuppressFileLevelFinding(t *testing.T) {
	src := "package api\n// forge:lint-disable-next-line my-rule: x\ncode()\n"

	res := Apply(src, []finding.Finding{errFinding("my-rule", 0)})

	if len(res.Kept) != 1 {
		t.Fatalf("file-level finding must survive a line directive, got %+v", res.Suppressed)
	}
}

func TestBlockDirectiveCoversRangeAndStopsAtEnable(t *testing.T) {
	src := strings.Join([]string{
		"package api",                           // 1
		"// forge:lint-disable my-rule: legacy", // 2
		"a()",                                   // 3
		"b()",                                   // 4
		"// forge:lint-enable my-rule",          // 5
		"c()",                                   // 6
	}, "\n")

	res := Apply(src, []finding.Finding{
		errFinding("my-rule", 3), errFinding("my-rule", 4), errFinding("my-rule", 6),
	})

	if len(res.Suppressed) != 2 {
		t.Fatalf("expected lines 3,4 suppressed, got %+v", res.Suppressed)
	}
	if len(res.Kept) != 1 || res.Kept[0].Line != 6 {
		t.Fatalf("line 6 is past forge:lint-enable and must survive, got %+v", res.Kept)
	}
}

func TestUnclosedBlockRunsToEndOfFile(t *testing.T) {
	src := "package api\n// forge:lint-disable my-rule: legacy\na()\nb()\n"

	res := Apply(src, []finding.Finding{errFinding("my-rule", 3), errFinding("my-rule", 4)})

	if len(res.Kept) != 0 {
		t.Fatalf("unclosed block should cover to EOF, kept %+v", res.Kept)
	}
}

// golangci's positional semantics: trailing on code = this line;
// alone on a line = the next one. Both must work, because the whole
// point of honoring //nolint is that existing muscle memory applies.
func TestNolintTrailingAppliesToSameLine(t *testing.T) {
	src := "package api\noffending() //nolint:my-rule // deliberate\nother()\n"

	res := Apply(src, []finding.Finding{errFinding("my-rule", 2), errFinding("my-rule", 3)})

	if len(res.Suppressed) != 1 || res.Suppressed[0].Line != 2 {
		t.Fatalf("trailing nolint should cover its own line, got %+v", res.Suppressed)
	}
}

func TestNolintOnOwnLineAppliesToNextLine(t *testing.T) {
	src := "package api\n//nolint:my-rule // deliberate\noffending()\n"

	res := Apply(src, []finding.Finding{errFinding("my-rule", 3)})

	if len(res.Suppressed) != 1 {
		t.Fatalf("standalone nolint should cover the next line, got kept %+v", res.Kept)
	}
}

func TestNolintReasonIsExtracted(t *testing.T) {
	src := "package api\noffending() //nolint:my-rule // set by the reconciler\n"

	res := Apply(src, []finding.Finding{errFinding("my-rule", 2)})

	if len(res.Suppressed) != 1 {
		t.Fatal("expected suppression")
	}
	if got := res.Suppressed[0].Reason; got != "set by the reconciler" {
		t.Fatalf("reason = %q, want %q", got, "set by the reconciler")
	}
	if len(res.Violations) != 0 {
		t.Fatalf("a reason was given; no violation expected, got %+v", res.Violations)
	}
}

func TestMultipleRulesInOneDirective(t *testing.T) {
	src := "package api\n// forge:lint-disable-next-line rule-a, rule-b: both known\ncode()\n"

	res := Apply(src, []finding.Finding{errFinding("rule-a", 3), errFinding("rule-b", 3), errFinding("rule-c", 3)})

	if len(res.Suppressed) != 2 {
		t.Fatalf("expected rule-a and rule-b suppressed, got %+v", res.Suppressed)
	}
	if len(res.Kept) != 1 || res.Kept[0].Rule != "rule-c" {
		t.Fatalf("rule-c was not named and must survive, got %+v", res.Kept)
	}
}

func TestWildcardSuppressesEveryRule(t *testing.T) {
	src := "// forge:lint-disable-file *: vendored third-party source\npackage api\n"

	res := Apply(src, []finding.Finding{errFinding("rule-a", 1), errFinding("rule-b", 2)})

	if len(res.Kept) != 0 {
		t.Fatalf("wildcard should suppress all, kept %+v", res.Kept)
	}
}

// The reason requirement is the teeth of the whole design: silencing a
// gating rule without saying why must itself be reported.
func TestSuppressingErrorWithoutReasonIsReported(t *testing.T) {
	src := "package api\n// forge:lint-disable-next-line my-rule\ncode()\n"

	res := Apply(src, []finding.Finding{errFinding("my-rule", 3)})

	if len(res.Suppressed) != 1 {
		t.Fatal("finding should still be suppressed")
	}
	if len(res.Violations) != 1 {
		t.Fatalf("expected a missing-reason violation, got %+v", res.Violations)
	}
	if res.Violations[0].Rule != RuleMissingReason {
		t.Fatalf("violation rule = %q", res.Violations[0].Rule)
	}
	// It points at the DIRECTIVE (line 2), not the finding (line 3) —
	// the directive is what the author has to edit.
	if res.Violations[0].Line != 2 {
		t.Fatalf("violation should point at the directive line 2, got %d", res.Violations[0].Line)
	}
}

// The missing-reason violation must GATE. As a warning it was advice: a
// bare `forge:lint-disable-next-line <error-rule>` silenced the error and
// left only a non-gating note behind, so any gate could be turned green
// with no reason at all — the exact outcome the requirement exists to stop.
func TestMissingReasonViolationGates(t *testing.T) {
	src := "package api\n// forge:lint-disable-next-line my-rule\ncode()\n"

	res := Apply(src, []finding.Finding{errFinding("my-rule", 3)})

	if len(res.Violations) != 1 {
		t.Fatalf("expected one missing-reason violation, got %+v", res.Violations)
	}
	if got := res.Violations[0].Severity; got != finding.SeverityError {
		t.Fatalf("missing-reason violation severity = %q, want error (a reasonless suppression must not turn a gate green)", got)
	}
}

// A next-line directive covers the next line of CODE, through any comment
// lines between. gofmt produces exactly that shape: it moves `//forge:`
// directive lines to the end of a doc comment and separates them from
// prose with a blank `//`, and a directive for a declaration naturally
// sits in that declaration's doc comment.
func TestNextLineSpansCommentLinesToCode(t *testing.T) {
	src := strings.Join([]string{
		"package api", // 1
		"//forge:lint-disable-next-line my-rule: reviewed", // 2
		"//",                       // 3
		"// Prose gofmt put here.", // 4
		"func Target() {}",         // 5
	}, "\n")

	res := Apply(src, []finding.Finding{errFinding("my-rule", 5)})

	if len(res.Kept) != 0 {
		t.Fatalf("directive should reach the declaration through its doc comment; kept %+v", res.Kept)
	}
}

// The span stops at the first line of code, and never crosses a blank
// line: it must not reach anything the author could not see directly
// beneath the directive.
func TestNextLineStopsAtCodeAndBlankLines(t *testing.T) {
	src := strings.Join([]string{
		"package api", // 1
		"// forge:lint-disable-next-line my-rule: one", // 2
		"a()", // 3
		"b()", // 4
		"// forge:lint-disable-next-line my-rule: two", // 5
		"",    // 6
		"c()", // 7
	}, "\n")

	res := Apply(src, []finding.Finding{errFinding("my-rule", 3), errFinding("my-rule", 4), errFinding("my-rule", 7)})

	if len(res.Kept) != 2 || res.Kept[0].Line != 4 || res.Kept[1].Line != 7 {
		t.Fatalf("span must stop at the first code line and not cross a blank line; kept %+v", res.Kept)
	}
}

// `#` and `--` count as comment lines only as comments: a TypeScript
// private field or a decrement is code, and ends the span.
func TestCommentOnlyLineRecognition(t *testing.T) {
	for raw, want := range map[string]bool{
		"// prose":        true,
		"  //forge:x":     true,
		"# yaml comment":  true,
		"#":               true,
		"-- sql comment":  true,
		"#count = 0":      false,
		"--i":             false,
		"code() // trail": false,
		"":                false,
	} {
		if got := isCommentOnlyLine(raw); got != want {
			t.Errorf("isCommentOnlyLine(%q) = %v, want %v", raw, got, want)
		}
	}
}

// ApplyFiles is Apply across many files: each finding is judged against
// ITS file's directives, and a file that cannot be read keeps its findings
// (an unreadable file is not evidence of a suppression).
func TestApplyFilesResolvesEachFindingAgainstItsOwnFile(t *testing.T) {
	files := map[string]string{
		"root/a.proto": "// forge:lint-disable-next-line my-rule: server default\n  optional string x = 1;\n",
		"root/b.proto": "  optional string y = 1;\n",
	}
	read := func(path string) ([]byte, error) {
		if s, ok := files[filepath.ToSlash(path)]; ok {
			return []byte(s), nil
		}
		return nil, os.ErrNotExist
	}
	in := []finding.Finding{
		{Rule: "my-rule", Severity: finding.SeverityError, File: "a.proto", Line: 2},
		{Rule: "my-rule", Severity: finding.SeverityError, File: "b.proto", Line: 1},
		{Rule: "my-rule", Severity: finding.SeverityError, File: "missing.proto", Line: 1},
		{Rule: "my-rule", Severity: finding.SeverityError},
	}

	res := ApplyFiles("root", in, read)

	if len(res.Suppressed) != 1 || res.Suppressed[0].File != "a.proto" {
		t.Fatalf("only a.proto's finding carries a directive; suppressed %+v", res.Suppressed)
	}
	if len(res.Kept) != 3 {
		t.Fatalf("b.proto, the unreadable file and the file-less finding must all be kept; kept %+v", res.Kept)
	}
}

func TestIsDirectiveToken(t *testing.T) {
	for _, tok := range []string{TokenDisableNextLine, TokenDisableFile, TokenDisable, TokenEnable} {
		if !IsDirectiveToken(tok) {
			t.Errorf("IsDirectiveToken(%q) = false", tok)
		}
	}
	for _, tok := range []string{"forge:entity", "forge:lint-disabled", "forge:"} {
		if IsDirectiveToken(tok) {
			t.Errorf("IsDirectiveToken(%q) = true", tok)
		}
	}
}

// Warnings do not gate, so silencing one is cheap by design and must
// not demand justification.
func TestSuppressingWarningWithoutReasonIsFine(t *testing.T) {
	src := "package api\n// forge:lint-disable-next-line my-rule\ncode()\n"

	res := Apply(src, []finding.Finding{warnFinding("my-rule", 3)})

	if len(res.Violations) != 0 {
		t.Fatalf("warnings need no reason, got %+v", res.Violations)
	}
}

func TestMissingReasonRuleIsItselfSuppressible(t *testing.T) {
	src := strings.Join([]string{
		"package api", // 1
		"// forge:lint-disable-file " + RuleMissingReason + ": house style, reasons tracked in review", // 2
		"// forge:lint-disable-next-line my-rule",                                                      // 3
		"code()", // 4
	}, "\n")

	res := Apply(src, []finding.Finding{errFinding("my-rule", 4)})

	if len(res.Violations) != 0 {
		t.Fatalf("missing-reason rule was suppressed; expected no violation, got %+v", res.Violations)
	}
}

// Comment syntax is not parsed, so SQL (--) and shell/YAML (#) comments
// must work for free. This is the property that lets one scanner serve
// migrationlint and the Go linters alike.
func TestNonGoCommentSyntaxesWork(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"sql", "-- forge:lint-disable-next-line my-rule: expand/contract\nALTER TABLE t DROP COLUMN c;\n"},
		{"hash", "# forge:lint-disable-next-line my-rule: intentional\nkey: value\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := Apply(tc.src, []finding.Finding{errFinding("my-rule", 2)})
			if len(res.Suppressed) != 1 {
				t.Fatalf("%s comment directive did not apply; kept %+v", tc.name, res.Kept)
			}
		})
	}
}

// A word that merely STARTS with a directive token is not a directive.
// Without the separator check, `forge:lint-disabled-by-default` would
// parse as a disable of the rule `by-default`.
func TestTokenPrefixIsNotADirective(t *testing.T) {
	src := "package api\n// forge:lint-disabledness is not a directive\ncode()\n"

	res := Apply(src, []finding.Finding{errFinding("my-rule", 3)})

	if len(res.Suppressed) != 0 {
		t.Fatalf("prefix match should not suppress, got %+v", res.Suppressed)
	}
}

// forge:lint-disable-file and forge:lint-disable-next-line both have
// forge:lint-disable as a prefix; the parser must not read the longer
// spellings as the block form.
func TestLongerTokensAreNotReadAsBlockForm(t *testing.T) {
	src := strings.Join([]string{
		"package api", // 1
		"// forge:lint-disable-next-line my-rule: one", // 2
		"a()", // 3
		"b()", // 4
	}, "\n")

	res := Apply(src, []finding.Finding{errFinding("my-rule", 3), errFinding("my-rule", 4)})

	if len(res.Kept) != 1 || res.Kept[0].Line != 4 {
		t.Fatalf("next-line form must not open a block; kept %+v", res.Kept)
	}
}

func TestNoDirectivesKeepsEverything(t *testing.T) {
	res := Apply("package api\ncode()\n", []finding.Finding{errFinding("my-rule", 2)})
	if len(res.Kept) != 1 || len(res.Suppressed) != 0 {
		t.Fatalf("clean file should keep all findings, got kept=%d suppressed=%d", len(res.Kept), len(res.Suppressed))
	}
}

func TestSuppressionRecordsDirectiveAndReason(t *testing.T) {
	src := "package api\n// forge:lint-disable-next-line my-rule: legacy import\ncode()\n"

	res := Apply(src, []finding.Finding{errFinding("my-rule", 3)})

	got := res.Suppressed[0]
	if got.Directive != TokenDisableNextLine {
		t.Errorf("directive = %q", got.Directive)
	}
	if got.Reason != "legacy import" {
		t.Errorf("reason = %q", got.Reason)
	}
	if got.Rule != "my-rule" || got.Line != 3 {
		t.Errorf("rule/line = %q/%d", got.Rule, got.Line)
	}
}

// ── lint.rules (forge.yaml) ────────────────────────────────────────────

func TestRuleSeveritiesOffDropsFinding(t *testing.T) {
	rs := RuleSeverities{"my-rule": "off"}
	out := rs.ApplyAll([]finding.Finding{errFinding("my-rule", 1), errFinding("other", 2)})
	if len(out) != 1 || out[0].Rule != "other" {
		t.Fatalf("expected only 'other' to survive, got %+v", out)
	}
}

func TestRuleSeveritiesDowngradeToWarn(t *testing.T) {
	rs := RuleSeverities{"my-rule": "warn"}
	out := rs.ApplyAll([]finding.Finding{errFinding("my-rule", 1)})
	if len(out) != 1 {
		t.Fatal("downgrade must not drop the finding")
	}
	if out[0].Severity != finding.SeverityWarning {
		t.Fatalf("severity = %q, want warning", out[0].Severity)
	}
}

func TestRuleSeveritiesUpgradeToError(t *testing.T) {
	rs := RuleSeverities{"my-rule": "error"}
	out := rs.ApplyAll([]finding.Finding{warnFinding("my-rule", 1)})
	if out[0].Severity != finding.SeverityError {
		t.Fatalf("severity = %q, want error", out[0].Severity)
	}
}

// An unknown or misspelled key must NOT silently punch a hole in the
// gate — the rule keeps its analyzer-assigned severity.
func TestUnknownRuleKeyIsInert(t *testing.T) {
	rs := RuleSeverities{"my-ruel": "off"} // typo
	out := rs.ApplyAll([]finding.Finding{errFinding("my-rule", 1)})
	if len(out) != 1 || out[0].Severity != finding.SeverityError {
		t.Fatalf("typo'd key must not disable the real rule, got %+v", out)
	}
}

func TestUnparseableSeverityKeepsAnalyzerSeverity(t *testing.T) {
	rs := RuleSeverities{"my-rule": "loud"}
	out := rs.ApplyAll([]finding.Finding{errFinding("my-rule", 1)})
	if len(out) != 1 || out[0].Severity != finding.SeverityError {
		t.Fatalf("bad severity must fail safe, got %+v", out)
	}
}

func TestExactRuleBeatsWildcard(t *testing.T) {
	rs := RuleSeverities{Wildcard: "off", "my-rule": "error"}
	out := rs.ApplyAll([]finding.Finding{errFinding("my-rule", 1), errFinding("other", 2)})
	if len(out) != 1 || out[0].Rule != "my-rule" {
		t.Fatalf("exact key should win over wildcard, got %+v", out)
	}
}

func TestValidSeverity(t *testing.T) {
	for _, ok := range []string{"off", "warn", "warning", "error", "info", "  ERROR "} {
		if !ValidSeverity(ok) {
			t.Errorf("ValidSeverity(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "loud", "fatal", "yes"} {
		if ValidSeverity(bad) {
			t.Errorf("ValidSeverity(%q) = true, want false", bad)
		}
	}
}
