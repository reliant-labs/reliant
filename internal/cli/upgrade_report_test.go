package cli

import (
	"fmt"
	"strings"
	"testing"
)

// The presentation contract of `forge project upgrade --check`.
//
// These exist because the report was CORRECT and unusable: against a real
// project (two customized Next.js frontends) it emitted 14,244 lines over
// 91 files, every finding genuine, and nobody reads 14,000 lines. What
// follows pins the three properties that made it readable — no inline
// diffs, ranked by adopt cost, capped with an escape hatch — because each
// one is the kind of thing a later well-meaning change quietly undoes.

// rowsFixture is a spread of the shapes a real project produces: pristine
// files a little behind, heavily customized files, and a pure add.
func rowsFixture() []driftRow {
	return []driftRow{
		{Path: "frontends/web/src/lib/auth/oidc.ts", Missing: 12, Local: 40},
		{Path: ".github/CODEOWNERS", Missing: 2},
		{Path: "frontends/web/src/lib/connect.ts", Missing: 48, Local: 44},
		{Path: "frontends/web/eslint.config.mjs", Missing: 6},
		{Path: "frontends/web/src/lib/admin-url.ts", Missing: 33, Absent: true},
		{Path: "frontends/web/src/app/layout.tsx", Missing: 48, Local: 19},
	}
}

// TestGroupDriftRows_SortsByAdoptCost is the ranking, which IS the value of
// the report. A pristine file two lines behind is a one-command adopt; a
// file with forty local lines is a merge done by hand. Presenting them in
// arrival order is what buried the cheap ones.
func TestGroupDriftRows_SortsByAdoptCost(t *testing.T) {
	t.Parallel()
	cheap, merge, absent := groupDriftRows(rowsFixture())

	wantCheap := []string{".github/CODEOWNERS", "frontends/web/eslint.config.mjs"}
	if got := pathsOf(cheap); !equalStrings(got, wantCheap) {
		t.Errorf("cheap adopts should lead, smallest delta first\n got: %v\nwant: %v", got, wantCheap)
	}

	// Within the merge group the ordering key is LOCAL lines: those are
	// what the reader has to reconcile by hand, so fewest-first is
	// cheapest-first.
	wantMerge := []string{
		"frontends/web/src/app/layout.tsx",   // 19 local
		"frontends/web/src/lib/auth/oidc.ts", // 40 local
		"frontends/web/src/lib/connect.ts",   // 44 local
	}
	if got := pathsOf(merge); !equalStrings(got, wantMerge) {
		t.Errorf("merges should be ordered by the local lines you must reconcile\n got: %v\nwant: %v", got, wantMerge)
	}

	// A file the project does not have is neither a refresh nor a merge:
	// adopting it destroys nothing, so it is ranked apart.
	if got := pathsOf(absent); !equalStrings(got, []string{"frontends/web/src/lib/admin-url.ts"}) {
		t.Errorf("an absent file belongs in its own group, got %v", got)
	}
}

// TestDriftRow_CheapIsExactlyNoLocalEdits pins the predicate the whole
// ranking rests on: `--force <path>` is a complete answer for a file with
// no lines of its own, and a destructive one for a file that has them.
func TestDriftRow_CheapIsExactlyNoLocalEdits(t *testing.T) {
	t.Parallel()
	cases := []struct {
		row  driftRow
		want bool
	}{
		{driftRow{Missing: 2}, true},
		{driftRow{Missing: 500}, true},               // big but still a pure refresh
		{driftRow{Missing: 2, Local: 1}, false},      // one local line is enough to lose
		{driftRow{Missing: 33, Absent: true}, false}, // an add is cheap, but not this kind
	}
	for _, tc := range cases {
		if got := tc.row.cheap(); got != tc.want {
			t.Errorf("cheap(%+v) = %v, want %v", tc.row, got, tc.want)
		}
	}
}

// TestDriftRow_MeasureNeverPrintsAZeroDelta: "behind by 0 lines" is the
// phrasing the advisory engine deliberately refuses to emit, on the
// reasoning that a report stating a difference of zero teaches readers to
// skip the column that matters. The summary must honour the same rule.
func TestDriftRow_MeasureNeverPrintsAZeroDelta(t *testing.T) {
	t.Parallel()
	for _, row := range []driftRow{
		{Path: "a", Missing: 0, Local: 0},
		{Path: "b", Missing: 0, Local: 3},
	} {
		got := row.measure()
		if strings.Contains(got, "0 line") || strings.HasPrefix(got, "0 ") {
			t.Errorf("measure(%+v) = %q — a zero delta must not be stated as a count", row, got)
		}
		if got == "" {
			t.Errorf("measure(%+v) said nothing at all", row)
		}
	}
}

// TestPrintDriftGroup_TruncationBoundary walks the cap exactly: at the
// limit everything prints and nothing is withheld; one past it, the last
// row gives way to a count AND the command that reveals it. A cap whose
// escape hatch is documented elsewhere is a lie with a footnote.
func TestPrintDriftGroup_TruncationBoundary(t *testing.T) {
	for _, n := range []int{maxRowsPerGroup - 1, maxRowsPerGroup, maxRowsPerGroup + 1, 40} {
		t.Run(fmt.Sprintf("%d_rows", n), func(t *testing.T) {
			rows := make([]driftRow, 0, n)
			for i := 0; i < n; i++ {
				rows = append(rows, driftRow{Path: fmt.Sprintf("file-%02d.ts", i), Missing: i + 1})
			}
			out := captureStdout(t, func() { printDriftGroup("Group:", rows, false) })

			wantShown := n
			if wantShown > maxRowsPerGroup {
				wantShown = maxRowsPerGroup
			}
			for i := 0; i < wantShown; i++ {
				if !strings.Contains(out, fmt.Sprintf("file-%02d.ts", i)) {
					t.Errorf("row %d should be listed:\n%s", i, out)
				}
			}
			if n <= maxRowsPerGroup {
				if strings.Contains(out, "more") {
					t.Errorf("%d rows fit under the cap and must not be truncated:\n%s", n, out)
				}
				return
			}
			if !strings.Contains(out, fmt.Sprintf("... %d more", n-maxRowsPerGroup)) {
				t.Errorf("expected the withheld count:\n%s", out)
			}
			if !strings.Contains(out, "--all") {
				t.Errorf("a truncated group must name --all in place:\n%s", out)
			}
		})
	}
}

// TestPrintDriftGroup_ShowAllUncaps: --all is the promise that makes
// capping honest, so it must actually list everything.
func TestPrintDriftGroup_ShowAllUncaps(t *testing.T) {
	rows := make([]driftRow, 0, 30)
	for i := 0; i < 30; i++ {
		rows = append(rows, driftRow{Path: fmt.Sprintf("file-%02d.ts", i), Missing: i + 1})
	}
	out := captureStdout(t, func() { printDriftGroup("Group:", rows, true) })
	for i := 0; i < 30; i++ {
		if !strings.Contains(out, fmt.Sprintf("file-%02d.ts", i)) {
			t.Errorf("--all must list row %d:\n%s", i, out)
		}
	}
	if strings.Contains(out, "more") {
		t.Errorf("--all must not truncate:\n%s", out)
	}
}

// TestPrintAdoptCommands_OnePerLine is defect #2, stated as a test.
//
// The old output emitted ONE --force naming 34 paths in sequence: hundreds
// of characters, wrapping across the terminal, and adopting all 34 in a
// single irreversible gesture. One command per line is a menu; one command
// with 34 arguments is a dare.
func TestPrintAdoptCommands_OnePerLine(t *testing.T) {
	rows := []driftRow{
		{Path: "a/one.ts", Missing: 1},
		{Path: "b/two.ts", Missing: 2},
		{Path: "c/three.ts", Missing: 3},
	}
	out := captureStdout(t, func() { printAdoptCommands(rows, true) })

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.Contains(line, "--force") {
			continue
		}
		if n := strings.Count(line, ".ts"); n != 1 {
			t.Errorf("each --force line must name exactly one path, got %d:\n  %s", n, line)
		}
	}
	for _, r := range rows {
		if !strings.Contains(out, "--force "+r.Path) {
			t.Errorf("%s should have its own adopt command:\n%s", r.Path, out)
		}
	}
}

// ── helpers ──────────────────────────────────────────────────────────────

func pathsOf(rows []driftRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Path)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
