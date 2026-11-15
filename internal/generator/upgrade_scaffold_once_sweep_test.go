// File: internal/generator/upgrade_scaffold_once_sweep_test.go
//
// The data-loss guard for tier FLIPS.
//
// `forge generate`'s stale-artifact sweep (internal/cli/generate_cleanup.go)
// is marker-driven: a file carrying forge's certification marker that no run
// wrote is a stale candidate, and `--force-cleanup` DELETES a pristine
// candidate. That is correct for a renamed service's leftover handler.
//
// It is data loss for a file that changed TIER. When a command-tree template
// moves from Tier-1 (regenerated every run) to scaffold-once (written once,
// then the user's), every project scaffolded before the flip still has a copy
// on disk carrying the old markers — and after the flip nothing writes it, so
// it never enters WrittenThisRun. The sweep reads "certified, not written this
// run" and flags it. Pristine is the common case (the user never edited it),
// and pristine is exactly the condition under which the sweep deletes rather
// than reports. So the user loses their serve pipeline, their ServeSpec, their
// version stamp, or their migration policy.
//
// generator.UpgradeManagedPaths() carries an explicit exclusion for each such
// path. This test derives the REQUIRED set from the templates' own banners
// rather than from a hand-maintained list, so the next tier flip cannot ship
// the same data loss by forgetting an entry.
package generator

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// cmdTreeTemplates maps every command-tree template that lands in
// cmd/<bin>/cmd/ to the file it is written as.
//
// It is the one thing this test states by hand, and it is deliberately a
// mapping rather than a tier list: the TIER of each entry is read from the
// template itself below, so this table says only "these templates produce
// these command-tree files" — a fact that changes when a file is added or
// renamed, not when its ownership changes.
var cmdTreeTemplates = map[string]string{
	"cmd-tree-root.go.tmpl":     "root.go",
	"cmd-tree-serve.go.tmpl":    "serve.go",
	"cmd-tree-server.go.tmpl":   "server.go",
	"cmd-tree-version.go.tmpl":  "version.go",
	"cmd-tree-db.go.tmpl":       "db.go",
	"cmd-tree-commands.go.tmpl": "commands.go",
	"cmd-tree-metrics.go.tmpl":  "metrics.go",
}

// scaffoldOnceBanner is the canonical Tier-2 marker the banner linter
// enforces (internal/linter/scaffolds/banners.go). A template carrying it has
// declared itself scaffold-once and user-owned — which is precisely the
// property that makes the stale sweep dangerous for it.
const scaffoldOnceBanner = "yours: scaffolded once"

// TestScaffoldOnceCmdTreeFilesAreExcludedFromStaleSweep is the guard.
//
// For every command-tree template whose OWN BANNER says it is scaffold-once,
// the corresponding cmd/cmd/<file>.go path must appear in
// UpgradeManagedPaths(). The required set is computed from the banners, so
// flipping a template's tier automatically extends what this test demands —
// there is no second list to remember to update.
func TestScaffoldOnceCmdTreeFilesAreExcludedFromStaleSweep(t *testing.T) {
	excluded := UpgradeManagedPaths()

	var required []string
	for tmpl, file := range cmdTreeTemplates {
		body, err := templates.ProjectTemplates().Get(tmpl)
		if err != nil {
			t.Fatalf("read template %s: %v", tmpl, err)
		}
		if !strings.Contains(string(body), scaffoldOnceBanner) {
			continue // Tier-1: written every run, so the sweep sees it.
		}
		// Slash form: UpgradeManagedPaths is keyed like the ownership keys it
		// is compared against, while cmdTreePath joins natively.
		required = append(required, filepath.ToSlash(cmdTreePath("", file)))
	}

	// Fail loudly on an empty derived set. If the banner text is ever
	// reworded, every Contains check above goes false, the loop finds
	// nothing, and a test that silently asserts nothing would go on
	// passing while the protection it describes is gone.
	if len(required) == 0 {
		t.Fatalf("derived no scaffold-once command-tree files from %d templates — "+
			"the %q banner probably moved or was reworded, and this guard is now asserting nothing",
			len(cmdTreeTemplates), scaffoldOnceBanner)
	}

	var missing []string
	for _, path := range required {
		if !excluded[path] {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("scaffold-once command-tree files missing from UpgradeManagedPaths(): %v\n\n"+
			"Each of these carries the %q banner, so forge writes it once and never again. "+
			"Projects scaffolded before the flip still have a marker-bearing copy on disk that "+
			"no run rewrites, so `forge generate`'s stale sweep will flag it and --force-cleanup "+
			"will DELETE it. Add an entry for each to the exclusion list in UpgradeManagedPaths.",
			missing, scaffoldOnceBanner)
	}
}

// TestCmdTreeTemplateTableMatchesShippedTemplates keeps the hand-written table
// above honest.
//
// Every name in it must resolve to a real shipped template, so a renamed or
// deleted template surfaces here instead of silently dropping out of the
// banner-derived requirement — a stale entry would make the guard above skip a
// file that still needs protecting.
func TestCmdTreeTemplateTableMatchesShippedTemplates(t *testing.T) {
	if len(cmdTreeTemplates) == 0 {
		t.Fatal("cmdTreeTemplates is empty")
	}
	for tmpl := range cmdTreeTemplates {
		if _, err := templates.ProjectTemplates().Get(tmpl); err != nil {
			t.Errorf("cmdTreeTemplates names %q, which is not a shipped project template: %v", tmpl, err)
		}
	}
}
