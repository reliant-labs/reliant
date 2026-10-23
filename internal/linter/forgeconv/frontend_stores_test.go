package forgeconv

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLintFrontendStores_TableDriven exercises the analyzer across the
// three canonical states (both signals → warning, Zustand only → no
// warning, gen only → no warning), plus the historic web/src/store/
// layout to make sure pre-workspaces projects are still covered.
func TestLintFrontendStores_TableDriven(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		fixture   string
		wantCount int
		wantFile  string // expected to appear in finding.File
	}{
		{
			name:      "both Zustand and gen import — warning",
			fixture:   "both",
			wantCount: 1,
			wantFile:  "frontends/web/src/stores/billing.ts",
		},
		{
			name:      "Zustand only — no warning",
			fixture:   "zustand_only",
			wantCount: 0,
		},
		{
			name:      "gen import only — no warning",
			fixture:   "gen_only",
			wantCount: 0,
		},
		{
			name:      "historic web/src/store/ layout — warning",
			fixture:   "web_layout",
			wantCount: 1,
			wantFile:  "web/src/store/orders.ts",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := filepath.Join("testdata", "frontend_stores", tc.fixture)
			res, err := LintFrontendStores(root)
			if err != nil {
				t.Fatalf("LintFrontendStores: %v", err)
			}
			got := findingsForRule(res.Findings, "forgeconv-frontend-stores-no-server-data")
			if len(got) != tc.wantCount {
				t.Fatalf("rule findings = %d, want %d:\n%s", len(got), tc.wantCount, res.FormatText())
			}
			if tc.wantCount > 0 {
				if got[0].Severity != SeverityWarning {
					t.Errorf("severity = %s, want warning", got[0].Severity)
				}
				if !strings.Contains(got[0].Message, "React Query") {
					t.Errorf("message should point to React Query; got %q", got[0].Message)
				}
				// File paths use OS-native separators; compare via
				// filepath.ToSlash so the assertion works on Windows
				// too (the fixture stores TS files but the file paths
				// are still OS-flavored).
				if !strings.Contains(filepath.ToSlash(got[0].File), tc.wantFile) {
					t.Errorf("finding file %q should contain %q", got[0].File, tc.wantFile)
				}
				if got[0].Line <= 0 {
					t.Errorf("finding line should point at the create<...> call; got %d", got[0].Line)
				}
				if res.HasErrors() {
					t.Errorf("frontend-stores is a warning rule; HasErrors() should be false")
				}
			}
		})
	}
}

// TestLintFrontendStores_NoFrontends verifies projects without
// frontends/ or web/ trees (Go-only services) get an empty result
// rather than an error.
func TestLintFrontendStores_NoFrontends(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	res, err := LintFrontendStores(tmp)
	if err != nil {
		t.Fatalf("LintFrontendStores on empty project: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("empty project should produce 0 findings, got %d", len(res.Findings))
	}
}
