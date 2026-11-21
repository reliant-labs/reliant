package audit

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/audittype"
)

func writeLockfileTree(t *testing.T, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestAuditLockfiles(t *testing.T) {
	cases := []struct {
		name           string
		files          []string
		wantStatus     audittype.Status
		wantManagers   []string
		wantPrimary    string
		wantStragglers []string
		wantMixedDirs  []lockfileDir
	}{
		{
			name:         "no JS at all",
			files:        []string{"go.mod"},
			wantStatus:   audittype.StatusOK,
			wantManagers: []string{},
		},
		{
			name:         "one manager across several packages",
			files:        []string{"pnpm-lock.yaml", "frontends/web/pnpm-lock.yaml", "tools/cli/pnpm-lock.yaml"},
			wantStatus:   audittype.StatusOK,
			wantManagers: []string{"pnpm"},
			wantPrimary:  "pnpm",
		},
		{
			name:         "npm's two lock names are one manager",
			files:        []string{"package-lock.json", "pkg/npm-shrinkwrap.json"},
			wantStatus:   audittype.StatusOK,
			wantManagers: []string{"npm"},
			wantPrimary:  "npm",
		},
		{
			name:           "two managers in different directories; the root lock is primary",
			files:          []string{"pnpm-lock.yaml", "frontends/web/pnpm-lock.yaml", "desktop/package-lock.json", "desktop/shell/package-lock.json", "docs/package-lock.json"},
			wantStatus:     audittype.StatusWarn,
			wantManagers:   []string{"npm", "pnpm"},
			wantPrimary:    "pnpm",
			wantStragglers: []string{"desktop/package-lock.json", "desktop/shell/package-lock.json", "docs/package-lock.json"},
		},
		{
			name:           "no root lock: the most-used manager is primary",
			files:          []string{"a/yarn.lock", "b/yarn.lock", "c/bun.lockb"},
			wantStatus:     audittype.StatusWarn,
			wantManagers:   []string{"bun", "yarn"},
			wantPrimary:    "yarn",
			wantStragglers: []string{"c/bun.lockb"},
		},
		{
			name:           "two managers' locks in one directory",
			files:          []string{"web/package-lock.json", "web/pnpm-lock.yaml"},
			wantStatus:     audittype.StatusWarn,
			wantManagers:   []string{"npm", "pnpm"},
			wantPrimary:    "npm", // a tie, broken by name
			wantStragglers: []string{"web/pnpm-lock.yaml"},
			wantMixedDirs:  []lockfileDir{{Dir: "web", Lockfiles: []string{"web/package-lock.json", "web/pnpm-lock.yaml"}}},
		},
		{
			name:         "lockfiles of dependencies and build output are not the project's",
			files:        []string{"pnpm-lock.yaml", "node_modules/dep/package-lock.json", "web/node_modules/x/yarn.lock", "web/dist/yarn.lock", "testdata/fixture/bun.lock"},
			wantStatus:   audittype.StatusOK,
			wantManagers: []string{"pnpm"},
			wantPrimary:  "pnpm",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat := auditLockfiles(writeLockfileTree(t, tc.files...))
			if cat.Status != tc.wantStatus {
				t.Errorf("status = %s, want %s (%s)", cat.Status, tc.wantStatus, cat.Summary)
			}
			if got := cat.Details["managers"]; !reflect.DeepEqual(got, tc.wantManagers) {
				t.Errorf("managers = %v, want %v", got, tc.wantManagers)
			}
			if got, _ := cat.Details["primary_manager"].(string); got != tc.wantPrimary {
				t.Errorf("primary_manager = %q, want %q", got, tc.wantPrimary)
			}
			gotStragglers, _ := cat.Details["stragglers"].([]string)
			if !reflect.DeepEqual(gotStragglers, tc.wantStragglers) {
				t.Errorf("stragglers = %v, want %v", gotStragglers, tc.wantStragglers)
			}
			gotMixed, _ := cat.Details["mixed_dirs"].([]lockfileDir)
			if !reflect.DeepEqual(gotMixed, tc.wantMixedDirs) {
				t.Errorf("mixed_dirs = %+v, want %+v", gotMixed, tc.wantMixedDirs)
			}
			if tc.wantStatus == audittype.StatusWarn {
				hint, _ := cat.Details["hint"].(string)
				if !strings.Contains(hint, tc.wantPrimary+" install") || !strings.Contains(hint, "frontend-workspaces") {
					t.Errorf("hint does not name the primary manager's install and the workspace skill: %q", hint)
				}
			}
		})
	}
}

func TestAuditLockfiles_IncompleteScanWarns(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "missing-project")
	cat := auditLockfiles(projectDir)
	if cat.Status != audittype.StatusWarn || !strings.Contains(cat.Summary, "incomplete") {
		t.Fatalf("got %s (%s), want an incomplete-scan warning", cat.Status, cat.Summary)
	}
	if scanErr, ok := cat.Details["scan_error"].(string); !ok || !strings.Contains(scanErr, "missing-project") {
		t.Errorf("scan_error = %q, want the failed path", scanErr)
	}
	if _, ok := cat.Details["hint"]; ok {
		t.Errorf("incomplete scan must not recommend a package manager: %+v", cat.Details)
	}
}

// The category is advisory: a mixed-lockfile project must never make
// `forge project audit` exit non-zero (audit_gate.go gates on error only).
func TestAuditLockfiles_NeverGates(t *testing.T) {
	cat := auditLockfiles(writeLockfileTree(t, "package-lock.json", "pnpm-lock.yaml", "a/yarn.lock", "b/bun.lock"))
	if cat.Status == audittype.StatusError {
		t.Fatalf("lockfiles reported error; it must stay advisory: %s", cat.Summary)
	}
	r := &Report{Categories: map[string]audittype.Category{"lockfiles": cat}}
	r.OverallStatus = rollupStatus(r.Categories)
	if err := gateOnReport(r); err != nil {
		t.Errorf("mixed lockfiles gated the audit: %v", err)
	}
}
