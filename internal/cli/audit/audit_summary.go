// File: internal/cli/audit/audit_summary.go
//
// The `summary` object of `forge project audit --json`: how bad, in one
// jq path. Before it the report carried only per-category objects and
// overall_status, so "how many findings, in which categories" meant
// knowing each category's detail keys.

package audit

import (
	"reflect"
	"sort"

	"github.com/reliant-labs/forge/internal/cli/audittype"
)

// ReportSummary rolls the categories up into counts. Additive to the
// report: every key the report already had keeps its shape.
type ReportSummary struct {
	// Categories is the number of categories this run emitted.
	Categories int `json:"categories"`
	// ByStatus counts categories per status; every status is present,
	// zero included, so `.summary.by_status.error` is never null.
	ByStatus map[audittype.Status]int `json:"by_status"`
	// Findings is the sum of every counted category's findings.
	Findings int `json:"findings"`
	// Warn and Error name the categories at that status, sorted.
	Warn  []string `json:"warn"`
	Error []string `json:"error"`
	// PerCategory is each category's status and, where the category
	// reports findings, how many.
	PerCategory map[string]CategoryCount `json:"per_category"`
}

// CategoryCount is one category's line in the summary. Findings is absent
// for a category that reports a state rather than a list of findings
// (version, shape, features, …), and for one whose collector failed before
// it could count.
type CategoryCount struct {
	Status   audittype.Status `json:"status"`
	Findings *int             `json:"findings,omitempty"`
}

// findingCounter counts one category's findings from its details. ok is
// false when the details carry none of the keys the count is read from.
type findingCounter func(details map[string]any) (n int, ok bool)

// findingCounters names, per category, the detail keys that ARE its
// findings — not its inventory (unscoped_auth's acknowledged_rpcs and
// owner_scoped_tables are not findings; its unscoped and raw-SQL lists
// are). Every category in auditCategoryOrder is either here or in
// statusOnlyCategories; TestEveryAuditCategoryDeclaresHowItCounts holds
// that, so a new category chooses.
var findingCounters = map[string]findingCounter{
	"unscoped_auth":       lenOf("unscoped_rpcs", "raw_sql_unscoped_rpcs", "raw_sql_unverifiable_rpcs"),
	"conventions":         sumOfIntMap("counts"),
	"codegen":             lenOf("user_edited_gen_files", "orphan_gen_files"),
	"migration_safety":    lenOf("allow_destructive_files"),
	"optional_deps_guard": intAt("finding_count"),
	"config_deps":         intAt("finding_count"),
	"scaffold_markers":    intAt("total_markers"),
	"crud_stubs":          intAt("total_stubs"),
	"file_sizes":          lenOf("oversized_files", "god_object_types"),
	"orphan_stubs":        lenOf("orphan_services"),
	"unhandled_rpcs":      lenOf("unhandled_rpcs"),
	// Count non-primary locks once, not again for mixed directories.
	"lockfiles": lenOf("stragglers"),
}

// statusOnlyCategories report a state, not a list of findings.
var statusOnlyCategories = map[string]bool{
	"version": true, "shape": true, "features": true, "ingress": true,
	"environments": true, "resources": true, "external_builds": true,
	"prerequisites": true, "deps": true,
}

// summarize builds the report's summary from its categories.
func summarize(cats map[string]audittype.Category) ReportSummary {
	s := ReportSummary{
		Categories: len(cats),
		ByStatus: map[audittype.Status]int{
			audittype.StatusOK: 0, audittype.StatusWarn: 0, audittype.StatusError: 0,
		},
		Warn:        []string{},
		Error:       []string{},
		PerCategory: make(map[string]CategoryCount, len(cats)),
	}
	for name, c := range cats {
		s.ByStatus[c.Status]++
		switch c.Status {
		case audittype.StatusWarn:
			s.Warn = append(s.Warn, name)
		case audittype.StatusError:
			s.Error = append(s.Error, name)
		}
		line := CategoryCount{Status: c.Status}
		if count, counted := findingCounters[name]; counted {
			n, ok := count(c.Details)
			// A clean category that skipped its details (a "n/a" early
			// return) has zero findings; a failed one has an unknown
			// number, which is not zero.
			if ok || c.Status == audittype.StatusOK {
				line.Findings = &n
				s.Findings += n
			}
		}
		s.PerCategory[name] = line
	}
	sort.Strings(s.Warn)
	sort.Strings(s.Error)
	return s
}

// lenOf sums the lengths of the list-valued detail keys present.
func lenOf(keys ...string) findingCounter {
	return func(details map[string]any) (int, bool) {
		n, found := 0, false
		for _, k := range keys {
			v, ok := details[k]
			if !ok {
				continue
			}
			found = true
			if rv := reflect.ValueOf(v); rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array || rv.Kind() == reflect.Map {
				n += rv.Len()
			}
		}
		return n, found
	}
}

// intAt reads an integer detail.
func intAt(key string) findingCounter {
	return func(details map[string]any) (int, bool) {
		n, ok := details[key].(int)
		return n, ok
	}
}

// sumOfIntMap sums a map[string]int detail (conventions' per-severity counts).
func sumOfIntMap(key string) findingCounter {
	return func(details map[string]any) (int, bool) {
		m, ok := details[key].(map[string]int)
		if !ok {
			return 0, false
		}
		n := 0
		for _, v := range m {
			n += v
		}
		return n, true
	}
}
