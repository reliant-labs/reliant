package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/generator"
)

// End-to-end cover for the summarize/detail split, driven through the real
// command against a real scaffolded project.
//
// The unit tests in upgrade_report_test.go pin the formatting primitives.
// These pin the thing a user actually experiences: that `--check` stopped
// printing 14,000 lines, that the diff it stopped printing is genuinely one
// command away, and that a fresh project still says nothing at all.

// checkOutput runs `upgrade --check` (optionally --all, optionally naming
// paths) against dir and returns everything it printed.
func checkOutput(t *testing.T, dir string, showAll bool, paths ...string) string {
	t.Helper()
	var out string
	withCwd(t, dir, func() {
		out = captureStdout(t, func() {
			if err := runUpgradeWithView(true /* check */, false, showAll, paths, ""); err != nil {
				t.Fatalf("runUpgrade --check %v: %v", paths, err)
			}
		})
	})
	return out
}

// TestUpgradeCheck_SummaryPrintsNoInlineDiffs is defect #1, stated as a
// test: unbounded inline diffs were the bulk of the 14,244 lines. A drift
// REPORT says a file differs and by how much; the diff is what you ask for
// on one file.
func TestUpgradeCheck_SummaryPrintsNoInlineDiffs(t *testing.T) {
	dir := scaffoldAdvisoryProject(t)
	rel := filepath.Join("frontends", "web", "src", "lib", "query-client.ts")
	staleFrontendFile(t, filepath.Join(dir, rel), 8)

	out := checkOutput(t, dir, false)

	if !strings.Contains(out, rel) {
		t.Fatalf("the stale file must still be reported:\n%s", out)
	}
	for _, marker := range []string{"--- a/", "+++ b/", "@@ -"} {
		if strings.Contains(out, marker) {
			t.Errorf("the summary must not contain diff hunks (found %q):\n%s", marker, out)
		}
	}
	// Suppressing the diff is only honest if the report says where to
	// get it.
	if !strings.Contains(out, "--check <path>") {
		t.Errorf("the summary must point at the per-file detail view:\n%s", out)
	}
}

// TestUpgradeCheck_NamedPathPrintsItsDiff is the other half of the split:
// what --check declines to print, `--check <path>` produces in full.
func TestUpgradeCheck_NamedPathPrintsItsDiff(t *testing.T) {
	dir := scaffoldAdvisoryProject(t)
	rel := filepath.Join("frontends", "web", "src", "lib", "query-client.ts")
	staleFrontendFile(t, filepath.Join(dir, rel), 8)

	out := checkOutput(t, dir, false, rel)

	if !strings.Contains(out, "--- a/") || !strings.Contains(out, "@@ -") {
		t.Errorf("--check <path> must print the file's full diff:\n%s", out)
	}
	if !strings.Contains(out, rel) {
		t.Errorf("the detail view must name the file:\n%s", out)
	}
	// The detail view is one file's view: it must not drag the whole
	// report along behind it.
	if strings.Contains(out, "Files you own, whose forge templates have moved on") {
		t.Errorf("--check <path> should replace the summary, not append to it:\n%s", out)
	}
}

// TestUpgradeCheck_DetailViewIsReadOnly: every truncated list in the report
// points at this view, so it must be incapable of writing. --force remains
// the only gesture that adopts.
func TestUpgradeCheck_DetailViewIsReadOnly(t *testing.T) {
	dir := scaffoldAdvisoryProject(t)
	rel := filepath.Join("frontends", "web", "src", "lib", "query-client.ts")
	full := filepath.Join(dir, rel)
	staleFrontendFile(t, full, 8)
	before := mustRead(t, full)

	_ = checkOutput(t, dir, false, rel)

	if mustRead(t, full) != before {
		t.Errorf("--check %s modified the file it was asked to describe", rel)
	}
}

// TestUpgradeCheck_UnknownPathIsRefused: silence on a typo'd path would be
// read as "no differences", the opposite of the truth.
func TestUpgradeCheck_UnknownPathIsRefused(t *testing.T) {
	dir := scaffoldAdvisoryProject(t)

	var err error
	withCwd(t, dir, func() {
		_ = captureStdout(t, func() {
			err = runUpgradeWithView(true, false, false, []string{"no/such/file.ts"}, "")
		})
	})
	if err == nil {
		t.Fatal("an unreported path must be refused, not silently ignored")
	}
	if !strings.Contains(err.Error(), "no/such/file.ts") {
		t.Errorf("the refusal must name the offending path: %v", err)
	}
}

// TestUpgradeCheck_CheapAdoptsOutrankMerges is the ordering principle
// end-to-end: a trivially adoptable file must appear ABOVE the customized
// ones, because being buried under them is exactly how the real finding
// went unseen.
func TestUpgradeCheck_CheapAdoptsOutrankMerges(t *testing.T) {
	dir := scaffoldAdvisoryProject(t)
	cheapRel := filepath.Join(".github", "pull_request_template.md")
	mergeRel := filepath.Join("frontends", "web", "src", "lib", "query-client.ts")

	// A pristine file a little behind, and a heavily customized one.
	staleFrontendFile(t, filepath.Join(dir, cheapRel), 3)
	mergeFull := filepath.Join(dir, mergeRel)
	staleFrontendFile(t, mergeFull, 6)
	appendToFile(t, mergeFull, "\n"+strings.Repeat("const LOCAL_ONLY = 1;\n", 30))

	out := checkOutput(t, dir, true)

	cheapAt := strings.Index(out, cheapRel)
	mergeAt := strings.Index(out, mergeRel)
	if cheapAt < 0 || mergeAt < 0 {
		t.Fatalf("both files should be reported (cheap=%d merge=%d):\n%s", cheapAt, mergeAt, out)
	}
	if cheapAt > mergeAt {
		t.Errorf("the cheap adopt must be listed before the merge — surfacing it IS the value:\n%s", out)
	}
	if !strings.Contains(out, "Cheap adopts") {
		t.Errorf("expected the cheap-adopts group heading:\n%s", out)
	}
	if !strings.Contains(out, "Merges") {
		t.Errorf("expected the merges group heading:\n%s", out)
	}
}

// TestUpgradeCheck_AllRevealsEveryTruncatedPath is the promise that makes
// capping honest: nothing is dropped from the report, it is only withheld
// from the default view.
func TestUpgradeCheck_AllRevealsEveryTruncatedPath(t *testing.T) {
	dir := scaffoldAdvisoryProject(t)
	cfg := loadAdvisoryConfig(t, dir)

	// Make enough advisory rows stale to force truncation.
	files, err := generator.AdvisoryFilesFor(dir, cfg)
	if err != nil {
		t.Fatalf("AdvisoryFilesFor: %v", err)
	}
	var staled []string
	for _, f := range files {
		full := filepath.Join(dir, f.Path)
		if !strings.HasSuffix(f.Path, ".ts") && !strings.HasSuffix(f.Path, ".tsx") {
			continue
		}
		lines := strings.Split(mustRead(t, full), "\n")
		if len(lines) < 30 {
			continue
		}
		staleFrontendFile(t, full, 4)
		staled = append(staled, f.Path)
		if len(staled) > maxRowsPerGroup+3 {
			break
		}
	}
	if len(staled) <= maxRowsPerGroup {
		t.Skipf("project has too few advisory rows (%d) to exercise truncation", len(staled))
	}

	capped := checkOutput(t, dir, false)
	if !strings.Contains(capped, "more") {
		t.Fatalf("expected the default view to truncate %d rows:\n%s", len(staled), capped)
	}

	all := checkOutput(t, dir, true)
	for _, p := range staled {
		if !strings.Contains(all, p) {
			t.Errorf("--all must reveal %s — nothing may be dropped from the report:\n%s", p, all)
		}
	}
}

// TestUpgradeCheck_FreshScaffoldReportsClean is the false-positive gate for
// the whole command. A project the scaffolder just wrote has nothing to
// adopt, and a report that invents work on a clean tree is worse than no
// report at all.
func TestUpgradeCheck_FreshScaffoldReportsClean(t *testing.T) {
	dir := scaffoldAdvisoryProject(t)

	out := checkOutput(t, dir, false)

	for _, forbidden := range []string{
		"Cheap adopts",
		"Merges (your lines",
		"with local edits",
		"Files you own, whose forge templates have moved on",
		"--- a/",
	} {
		if strings.Contains(out, forbidden) {
			t.Errorf("a fresh scaffold must report clean, found %q:\n%s", forbidden, out)
		}
	}
}
