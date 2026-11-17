package forgeconv

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLintHandlerFileSize_TableDriven exercises the analyzer end-to-end
// across the three canonical states: a file below the threshold (no
// warning), a file above the threshold (warning fires with both the
// `<actual> > <threshold> lines` phrasing and the forge scaffold handler-file
// remediation pointer), and a comment-heavy file whose RAW line count
// dwarfs the threshold but whose SOURCE LOC is tiny (counter must
// strip comments and blanks).
//
// Threshold is fixed at 10 so the over-threshold fixture can be
// meaningfully larger without producing thousands of lines of fixture
// noise. The production default (config.DefaultHandlerFileMaxLOC) is
// not exercised here — its value is asserted in config tests.
func TestLintHandlerFileSize_TableDriven(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		fixture     string
		threshold   int
		wantCount   int
		wantMsgPart string
	}{
		{
			name:        "under threshold — no warning",
			fixture:     "under_threshold",
			threshold:   10,
			wantCount:   0,
			wantMsgPart: "",
		},
		{
			name:        "over threshold — single warning",
			fixture:     "over_threshold",
			threshold:   10,
			wantCount:   1,
			wantMsgPart: "consider splitting it with 'forge scaffold handler-file'",
		},
		{
			name:        "mostly comments — no warning (comments stripped)",
			fixture:     "mostly_comments",
			threshold:   10,
			wantCount:   0,
			wantMsgPart: "",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := filepath.Join("testdata", "handler_file_size", tc.fixture)
			res, err := LintHandlerFileSize(root, tc.threshold)
			if err != nil {
				t.Fatalf("LintHandlerFileSize: %v", err)
			}
			got := findingsForRule(res.Findings, "forgeconv-handler-file-size")
			if len(got) != tc.wantCount {
				t.Fatalf("rule findings = %d, want %d:\n%s", len(got), tc.wantCount, res.FormatText())
			}
			if tc.wantCount > 0 {
				if got[0].Severity != SeverityWarning {
					t.Errorf("severity = %s, want warning", got[0].Severity)
				}
				if !strings.Contains(got[0].Message, tc.wantMsgPart) {
					t.Errorf("message %q does not contain %q", got[0].Message, tc.wantMsgPart)
				}
				// Message must include actual > threshold form so the
				// user sees both numbers at the violation site.
				if !strings.Contains(got[0].Message, "> 10 lines") {
					t.Errorf("message should include the threshold; got %q", got[0].Message)
				}
				// Warnings must not gate the build.
				if res.HasErrors() {
					t.Errorf("handler-file-size is a warning rule; HasErrors() should be false")
				}
			}
		})
	}
}

// TestLintHandlerFileSize_DisabledThreshold verifies that a zero (or
// negative) threshold short-circuits the walk — the caller can opt out
// entirely without running into the directory-walk overhead.
func TestLintHandlerFileSize_DisabledThreshold(t *testing.T) {
	t.Parallel()
	root := filepath.Join("testdata", "handler_file_size", "over_threshold")
	res, err := LintHandlerFileSize(root, 0)
	if err != nil {
		t.Fatalf("LintHandlerFileSize: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("expected 0 findings with threshold=0, got %d:\n%s", len(res.Findings), res.FormatText())
	}
}

// TestLintHandlerFileSize_NoHandlersDir verifies projects without a
// handlers/ tree (CLI / library) get an empty result rather than an
// error — consistent with LintHandlerTests.
func TestLintHandlerFileSize_NoHandlersDir(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	res, err := LintHandlerFileSize(tmp, 100)
	if err != nil {
		t.Fatalf("LintHandlerFileSize on empty project: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("empty project should produce 0 findings, got %d", len(res.Findings))
	}
}

// TestCountGoSourceLOC_StripsCommentsAndBlanks verifies the line-counter
// strips line comments, block comments (including multi-line), and
// blank lines. The numbers come from the mostly_comments fixture: it
// has exactly one source line (`return nil`) plus the `package`,
// `func`, and the function-body close brace — four total non-blank,
// non-comment lines.
func TestCountGoSourceLOC_StripsCommentsAndBlanks(t *testing.T) {
	t.Parallel()
	path := filepath.Join("testdata", "handler_file_size", "mostly_comments", "internal", "handlers", "billing", "handlers.go")
	loc, err := countGoSourceLOC(path)
	if err != nil {
		t.Fatalf("countGoSourceLOC: %v", err)
	}
	if loc > 10 {
		t.Errorf("mostly-comments fixture should produce <= 10 source LOC, got %d", loc)
	}
	if loc < 1 {
		t.Errorf("counter swallowed all lines — got %d, want >= 1", loc)
	}
}
