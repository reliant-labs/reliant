package lint

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

// TestLintJSONReportShape pins the exact serialized shape of the
// `forge lint --json` contract — field names, omission rules, key
// order. A failure here means a breaking change for every sub-agent /
// CI consumer parsing the report; extensions must be additive.
func TestLintJSONReportShape(t *testing.T) {
	report := buildLintJSONReport([]lintJSONFinding{
		{
			File:     "handlers/api/handlers.go",
			Line:     42,
			Col:      7,
			Severity: lintSevError,
			Rule:     "forgeconv-contract-names",
			Message:  "contract.go declares 'Sender', expected 'Service'",
			FixHint:  "rename the interface to Service",
		},
		{
			// File-less finding: file/line/col/fix_hint must be omitted.
			Severity: lintSevWarning,
			Rule:     "external",
			Message:  "some raw sub-tool line",
		},
		{
			Severity: lintSevInfo,
			Rule:     "skipped",
			Message:  "contracts feature disabled — skipping contract linter",
		},
	}, true)

	got, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	want := `{
  "findings": [
    {
      "file": "handlers/api/handlers.go",
      "line": 42,
      "col": 7,
      "severity": "error",
      "rule": "forgeconv-contract-names",
      "message": "contract.go declares 'Sender', expected 'Service'",
      "fix_hint": "rename the interface to Service"
    },
    {
      "severity": "warning",
      "rule": "external",
      "message": "some raw sub-tool line"
    },
    {
      "severity": "info",
      "rule": "skipped",
      "message": "contracts feature disabled — skipping contract linter"
    }
  ],
  "summary": {
    "errors": 1,
    "warnings": 1,
    "infos": 1,
    "total": 3
  },
  "ok": false
}`
	if string(got) != want {
		t.Errorf("JSON shape drifted.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestLintJSONReportEmptyFindings pins the empty-report shape: findings
// must serialize as [] (not null) so `jq '.findings[]'` never chokes,
// and ok must be true when nothing gated.
func TestLintJSONReportEmptyFindings(t *testing.T) {
	report := buildLintJSONReport(nil, false)
	got, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"findings":[],"summary":{"errors":0,"warnings":0,"infos":0,"total":0},"ok":true}`
	if string(got) != want {
		t.Errorf("empty report drifted.\ngot:  %s\nwant: %s", got, want)
	}
}

// TestLintJSONOKIndependentOfWarnings pins the gating contract: ok
// reflects the text-mode exit code, NOT the presence of warnings. A
// report full of warnings from advisory linters stays ok=true.
func TestLintJSONOKIndependentOfWarnings(t *testing.T) {
	report := buildLintJSONReport([]lintJSONFinding{
		{Severity: lintSevWarning, Rule: "forge-config-deps", Message: "w"},
		{Severity: lintSevWarning, Rule: "forge-test-conventions", Message: "w"},
	}, false)
	if !report.OK {
		t.Error("warnings-only report must keep ok=true (advisory linters never gate)")
	}
	if report.Summary.Warnings != 2 || report.Summary.Errors != 0 {
		t.Errorf("unexpected summary: %+v", report.Summary)
	}
}

// TestExternalLinesToFindings is the table for sub-tool output
// normalization: parseable `file:line[:col]: message` lines become
// file-scoped findings; everything else is preserved verbatim with
// rule "external"; blank lines are dropped (but nothing else is).
func TestExternalLinesToFindings(t *testing.T) {
	cases := []struct {
		name     string
		output   string
		rule     string
		severity string
		want     []lintJSONFinding
	}{
		{
			name:     "golangci line with trailing linter attribution",
			output:   "internal/cli/lint.go:12:5: ineffectual assignment to err (ineffassign)",
			rule:     "golangci-lint",
			severity: lintSevError,
			want: []lintJSONFinding{{
				File: "internal/cli/lint.go", Line: 12, Col: 5,
				Severity: lintSevError, Rule: "ineffassign",
				Message: "ineffectual assignment to err (ineffassign)",
			}},
		},
		{
			name:     "buf line without column keeps the buf rule",
			output:   "proto/api/v1/api.proto:3:1:Files with package \"api.v1\" must be within a directory \"api/v1\".",
			rule:     "buf",
			severity: lintSevError,
			want: []lintJSONFinding{{
				File: "proto/api/v1/api.proto", Line: 3, Col: 1,
				Severity: lintSevError, Rule: "buf",
				Message: "Files with package \"api.v1\" must be within a directory \"api/v1\".",
			}},
		},
		{
			name:     "unparseable line preserved verbatim as external",
			output:   "npm ERR! Lifecycle script `lint` failed with error:",
			rule:     "golangci-lint",
			severity: lintSevError,
			want: []lintJSONFinding{{
				Severity: lintSevError, Rule: "external",
				Message: "npm ERR! Lifecycle script `lint` failed with error:",
			}},
		},
		{
			name:     "blank lines dropped, mixed parse",
			output:   "\n\nmain.go:1:1: missing package comment\n\nsome banner\n",
			rule:     "golangci-lint",
			severity: lintSevError,
			want: []lintJSONFinding{
				{File: "main.go", Line: 1, Col: 1, Severity: lintSevError, Rule: "golangci-lint", Message: "missing package comment"},
				{Severity: lintSevError, Rule: "external", Message: "some banner"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := externalLinesToFindings(tc.output, tc.rule, tc.severity)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d findings, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("finding[%d]:\ngot:  %+v\nwant: %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestParseSeverity covers the canonical severity vocabulary that
// replaced the old normalizeLintSeverity shim. The internal linters
// emit finding.Severity directly (single spelling), so the only
// normalization left is parsing free-form forge.yaml rule levels.
// Input is lenient — "warn" is the spelling forge.yaml itself uses for
// the warning level, so it must parse to SeverityWarning; the
// single-spelling guarantee applies to the parsed OUTPUT. Values
// outside the vocabulary, including the "off" dial, return ("", false)
// — the disabled-rule sentinel — rather than the old shim's
// "degrade to warning" fallback (which has been deleted).
func TestParseSeverity(t *testing.T) {
	okCases := []struct {
		in   string
		want finding.Severity
	}{
		{"error", finding.SeverityError},
		{"warn", finding.SeverityWarning},
		{"warning", finding.SeverityWarning},
		{"info", finding.SeverityInfo},
	}
	for _, c := range okCases {
		got, ok := finding.ParseSeverity(c.in)
		if !ok || got != c.want {
			t.Errorf("ParseSeverity(%q) = (%q, %v), want (%q, true)", c.in, got, ok, c.want)
		}
	}
	for _, bad := range []string{"off", "WEIRD", ""} {
		if got, ok := finding.ParseSeverity(bad); ok {
			t.Errorf("ParseSeverity(%q) = (%q, true), want (\"\", false)", bad, got)
		}
	}
}

// TestRunLintJSONRejectsSuggestionModes pins the refusal: --json with
// the suggestion / mutation flags must error out instead of emitting a
// report that silently ignored the flag.
func TestRunLintJSONRejectsSuggestionModes(t *testing.T) {
	for _, f := range []lintFlags{
		{fix: true, jsonOut: true},
		{suggestExcludes: true, jsonOut: true},
		{suggestBufExcepts: true, jsonOut: true},
	} {
		if err := runLintJSON(context.Background(), f, []string{"./..."}); err == nil {
			t.Errorf("expected error for flags %+v with --json", f)
		}
	}
}
