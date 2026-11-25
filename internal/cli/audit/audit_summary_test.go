package audit

import (
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cli/audittype"
)

// Every category says how it counts: a list of findings (findingCounters)
// or a state (statusOnlyCategories). A new category that chose neither would
// silently drop out of .summary.findings.
func TestEveryAuditCategoryDeclaresHowItCounts(t *testing.T) {
	for _, name := range CategoryNames() {
		_, counted := findingCounters[name]
		if counted == statusOnlyCategories[name] {
			t.Errorf("category %q must be in exactly one of findingCounters and statusOnlyCategories", name)
		}
	}
	known := map[string]bool{}
	for _, name := range CategoryNames() {
		known[name] = true
	}
	for name := range findingCounters {
		if !known[name] {
			t.Errorf("findingCounters names %q, which the audit does not emit", name)
		}
	}
	for name := range statusOnlyCategories {
		if !known[name] {
			t.Errorf("statusOnlyCategories names %q, which the audit does not emit", name)
		}
	}
}

func TestSummarize_CountsPerCategoryAndOverall(t *testing.T) {
	cats := map[string]audittype.Category{
		"unscoped_auth": {Status: audittype.StatusWarn, Details: map[string]any{
			"unscoped_rpcs":             []unscopedRPC{{Method: "A"}, {Method: "B"}},
			"raw_sql_unverifiable_rpcs": []rawSQLRPC{{Method: "C"}},
			"acknowledged_rpcs":         []acknowledgedRPC{{Method: "D"}}, // inventory, not a finding
		}},
		"conventions":      {Status: audittype.StatusError, Details: map[string]any{"counts": map[string]int{"conventions/error": 2, "conventions/warning": 1}}},
		"scaffold_markers": {Status: audittype.StatusOK, Details: map[string]any{"total_markers": 0}},
		// A clean "n/a" early return carries no details: zero findings.
		"orphan_stubs": {Status: audittype.StatusOK},
		// A collector that failed before counting: unknown, not zero.
		"config_deps": {Status: audittype.StatusWarn, Summary: "config-deps scan failed: boom"},
		"version":     {Status: audittype.StatusWarn},
	}
	s := summarize(cats)

	if s.Categories != 6 {
		t.Errorf("categories = %d, want 6", s.Categories)
	}
	if want := map[audittype.Status]int{"ok": 2, "warn": 3, "error": 1}; !maps.Equal(s.ByStatus, want) {
		t.Errorf("by_status = %v, want %v", s.ByStatus, want)
	}
	if !slices.Equal(s.Warn, []string{"config_deps", "unscoped_auth", "version"}) || !slices.Equal(s.Error, []string{"conventions"}) {
		t.Errorf("warn = %v, error = %v", s.Warn, s.Error)
	}
	wantFindings := map[string]*int{
		"unscoped_auth": ptr(3), "conventions": ptr(3), "scaffold_markers": ptr(0),
		"orphan_stubs": ptr(0), "config_deps": nil, "version": nil,
	}
	for name, want := range wantFindings {
		got := s.PerCategory[name].Findings
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Errorf("per_category[%s].findings = %v, want %v", name, deref(got), deref(want))
		}
		if s.PerCategory[name].Status != cats[name].Status {
			t.Errorf("per_category[%s].status = %q, want %q", name, s.PerCategory[name].Status, cats[name].Status)
		}
	}
	if s.Findings != 6 {
		t.Errorf("findings = %d, want 6", s.Findings)
	}
}

// The JSON contract: summary is added beside the existing top-level keys,
// which keep their names, and its lists and status counts are never null.
func TestReportJSON_HasSummaryBesideTheExistingKeys(t *testing.T) {
	cats := map[string]audittype.Category{
		"version":       {Status: audittype.StatusOK},
		"unscoped_auth": {Status: audittype.StatusOK, Details: map[string]any{"unscoped_rpcs": []unscopedRPC{}}},
	}
	report := Report{
		ProjectName: "p", ProjectKind: "service", BinaryVersion: "dev", GeneratedAt: time.Now().UTC(),
		Categories: cats, OverallStatus: rollupStatus(cats), Summary: summarize(cats),
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"binary_version", "categories", "generated_at", "overall_status", "project_kind", "project_name", "summary"}
	if !slices.Equal(keys, want) {
		t.Fatalf("top-level keys = %v, want %v", keys, want)
	}

	summary := doc["summary"].(map[string]any)
	for _, k := range []string{"categories", "by_status", "findings", "warn", "error", "per_category"} {
		if summary[k] == nil {
			t.Errorf("summary.%s is null or missing: %s", k, data)
		}
	}
	byStatus := summary["by_status"].(map[string]any)
	if byStatus["ok"] != 2.0 || byStatus["warn"] != 0.0 || byStatus["error"] != 0.0 {
		t.Errorf("summary.by_status = %v, want ok=2 warn=0 error=0", byStatus)
	}
	per := summary["per_category"].(map[string]any)
	if _, has := per["version"].(map[string]any)["findings"]; has {
		t.Errorf("a status-only category must carry no findings count: %v", per["version"])
	}
	if got := per["unscoped_auth"].(map[string]any)["findings"]; got != 0.0 {
		t.Errorf("per_category.unscoped_auth.findings = %v, want 0", got)
	}
}

func TestSummarize_Lockfiles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		want  int
	}{
		{name: "no locks"},
		{name: "single manager", files: []string{"pnpm-lock.yaml"}},
		{name: "mixed managers", files: []string{"pnpm-lock.yaml", "package-lock.json", "web/yarn.lock"}, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cat := auditLockfiles(writeLockfileTree(t, tc.files...))
			s := summarize(map[string]audittype.Category{"lockfiles": cat})
			got := s.PerCategory["lockfiles"].Findings
			if got == nil || *got != tc.want || s.Findings != tc.want {
				t.Fatalf("findings = %v, total = %d, want %d", deref(got), s.Findings, tc.want)
			}
		})
	}
	t.Run("incomplete scan", func(t *testing.T) {
		cat := auditLockfiles(filepath.Join(t.TempDir(), "missing"))
		s := summarize(map[string]audittype.Category{"lockfiles": cat})
		if s.PerCategory["lockfiles"].Findings != nil || cat.Status != audittype.StatusWarn {
			t.Fatalf("incomplete scan must warn with unknown findings: %+v", s)
		}
	})
}

func ptr(n int) *int { return &n }

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
