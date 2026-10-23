// The presentation layer shared by both lanes of `forge project upgrade`.
//
// ── Why this file exists ──────────────────────────────────────────────
//
// The report was correct and unusable. Run against a mature project
// (control-plane: two customized Next.js frontends) it emitted 14,244
// lines over 91 files, because every reported file dumped its full
// unified diff inline. Every finding in it was genuine. Nobody reads
// 14,000 lines, so in practice the check reported nothing — and the two
// findings that were cheap to act on (.github/CODEOWNERS two lines
// behind, a newly-reportable eslint.config.mjs) were invisible among the
// frontend files whose divergence is simply what a customized frontend
// LOOKS like.
//
// So the split this file implements:
//
//	--check          SUMMARIZES — what differs, and by how much
//	--check <path>   DETAILS    — that one file's diff
//	--check --all    every row, still without inline diffs
//
// ── Ranking is the product ────────────────────────────────────────────
//
// The ordering is not cosmetic; it is the only part of the report that
// does the reader's triage for them. A pristine file two lines behind is
// a one-command adopt. A file carrying forty local lines is a merge
// somebody performs by hand, and forge cannot know which of those lines
// matter. Presenting the two at equal volume is what buried the cheap
// ones. So rows are grouped by what adopting them COSTS, cheapest group
// first, and sorted within a group by the same measure.
//
// ── What is capped, and the promise that makes capping honest ─────────
//
// A group prints at most maxRowsPerGroup rows and then says how many it
// withheld and how to see them. Nothing is dropped from the report: every
// withheld row is one `--all` away, and every suppressed diff is one
// `--check <path>` away. Both escape hatches are printed next to the
// truncation that needs them, because a cap whose escape hatch is
// documented elsewhere is just a lie with a footnote.
package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// maxRowsPerGroup bounds how many files one group lists before it
// summarizes the rest. Small on purpose: the point of the group is that
// its first entries are the ones worth acting on, and a list long enough
// to scroll has already lost that argument.
const maxRowsPerGroup = 5

// driftRow is one file's difference from its template, sized but not
// shown. Both lanes reduce to this so grouping, ranking and truncation
// are written once and cannot drift apart between the two sections.
type driftRow struct {
	// Path is the project-relative path, as the user would name it
	// after --force.
	Path string
	// Missing is how many template lines this copy lacks — "how far
	// behind". Local is how many lines this copy has that no template
	// line accounts for — the evidence of customization, and the thing
	// adopting the template would discard.
	Missing int
	Local   int
	// Absent marks a file the template ships that this project does not
	// have. Adoption is a pure add: it destroys nothing, which is why
	// these are ranked apart from files that already exist.
	Absent bool
	// Note carries a lane-specific qualifier for the rare row whose
	// state the numbers do not explain by themselves (adopted this run,
	// forced, skipped). Empty for the common case.
	Note string
}

// cheap reports whether adopting this row costs the user nothing they
// wrote. `--force <path>` is a complete answer for these and only these.
func (r driftRow) cheap() bool { return !r.Absent && r.Local == 0 }

// measure renders the row's size in the fewest words that stay honest.
//
// The two numbers answer different questions and both belong on the
// line: "behind" is what you gain by adopting, "yours" is what you lose.
// A row with nothing of its own says so by omission rather than by
// printing a zero — a column of `0 yours` trains the eye to skip the
// column that matters.
func (r driftRow) measure() string {
	switch {
	case r.Absent:
		return fmt.Sprintf("%s in the template, absent here", lineCount(r.Missing))
	case r.Missing == 0 && r.Local == 0:
		// The bytes differ but no line does: reordering, whitespace, a
		// re-wrapped comment. "0 lines behind" is the phrasing the
		// advisory lane already refuses to print, for the good reason
		// that a report which states a difference of zero is how a
		// reader learns to stop reading the column.
		return "differs in formatting only"
	case r.Missing == 0:
		return fmt.Sprintf("%d yours", r.Local)
	case r.Local == 0:
		return fmt.Sprintf("%s behind", lineCount(r.Missing))
	default:
		return fmt.Sprintf("%d behind, %d yours", r.Missing, r.Local)
	}
}

// sortDriftRows orders rows by what adopting them costs, cheapest first.
//
// Within the cheap group that is simply the smaller delta: a two-line
// refresh is less to review than a two-hundred-line one. Within the
// merge group it is the count of LOCAL lines, because those are what the
// user has to reconcile by hand — the template's side of a merge is read
// once, the user's side is decided line by line.
//
// Path breaks every tie so the report is stable run to run; a report
// that reshuffles between runs cannot be diffed against itself.
func sortDriftRows(rows []driftRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Local != b.Local {
			return a.Local < b.Local
		}
		if a.Missing != b.Missing {
			return a.Missing < b.Missing
		}
		return a.Path < b.Path
	})
}

// groupDriftRows splits rows into the three kinds of work they represent
// and sorts each. The order returned is the order they should print:
// cheapest first.
func groupDriftRows(rows []driftRow) (cheap, merge, absent []driftRow) {
	for _, r := range rows {
		switch {
		case r.Absent:
			absent = append(absent, r)
		case r.Local == 0:
			cheap = append(cheap, r)
		default:
			merge = append(merge, r)
		}
	}
	sortDriftRows(cheap)
	sortDriftRows(merge)
	sortDriftRows(absent)
	return cheap, merge, absent
}

// pathColumnWidth picks the width of the path column for one group.
//
// Sized to the rows actually being printed rather than to a constant, so
// a group of short paths does not carry a gutter wide enough for the
// longest path in the project. Clamped at both ends: too narrow and the
// measures do not line up, too wide and the measure column falls off an
// 80-column terminal. A path past the clamp overflows its column rather
// than being truncated — a path you cannot copy is worse than a ragged
// column, since copying it into `--check <path>` is the next thing the
// reader does.
func pathColumnWidth(rows []driftRow) int {
	const minWidth, maxWidth = 32, 58
	w := minWidth
	for _, r := range rows {
		if n := len(r.Path); n > w {
			w = n
		}
	}
	if w > maxWidth {
		w = maxWidth
	}
	return w
}

// printDriftGroup prints one titled group, capped unless showAll.
//
// The withheld count names `--all` in place. Returns how many rows were
// withheld so the caller can decide whether the section needs a closing
// pointer at all.
func printDriftGroup(title string, rows []driftRow, showAll bool) int {
	if len(rows) == 0 {
		return 0
	}
	shown := rows
	if !showAll && len(shown) > maxRowsPerGroup {
		shown = shown[:maxRowsPerGroup]
	}
	width := pathColumnWidth(shown)

	fmt.Println()
	fmt.Printf("  %s\n", title)
	for _, r := range shown {
		line := fmt.Sprintf("    %-*s  %s", width, r.Path, r.measure())
		if r.Note != "" {
			line += "  (" + r.Note + ")"
		}
		fmt.Fprintln(os.Stdout, line)
	}
	withheld := len(rows) - len(shown)
	if withheld > 0 {
		fmt.Printf("    %-*s  %s\n", width,
			fmt.Sprintf("... %d more", withheld), "--check --all to list")
	}
	return withheld
}

// printAdoptCommands prints one `--force` line per file — never one line
// naming them all.
//
// The single-command form was technically copy-pasteable and practically
// meaningless: 34 paths in sequence, hundreds of characters, wrapping
// across the terminal, and adopting every one of them in a single
// irreversible gesture. One line per file is a menu; one line with 34
// arguments is a dare. The list is capped for the same reason the groups
// are, and says so.
func printAdoptCommands(rows []driftRow, showAll bool) {
	if len(rows) == 0 {
		return
	}
	shown := rows
	if !showAll && len(shown) > maxRowsPerGroup {
		shown = shown[:maxRowsPerGroup]
	}
	for _, r := range shown {
		fmt.Printf("    %s project upgrade --force %s\n", Name(), r.Path)
	}
	if withheld := len(rows) - len(shown); withheld > 0 {
		fmt.Printf("    ... and %d more (%s project upgrade --check --all)\n", withheld, Name())
	}
}

// printDetailPointer closes a section with the two ways to see what the
// section chose not to print.
//
// Printed whenever the section reported anything at all, not only when it
// truncated: the diffs are suppressed for EVERY row, so even an
// untruncated section is hiding something the reader may want.
func printDetailPointer() {
	fmt.Println()
	fmt.Printf("    %s project upgrade --check <path>   # one file's full diff\n", Name())
	fmt.Printf("    %s project upgrade --check --all    # every file, no diffs\n", Name())
}

// printCountsLine prints the headline tally for a section: the numbers
// somebody reads before deciding whether to read the rest.
//
// Zero-valued parts are omitted rather than printed as zeros. "20 up to
// date" is worth saying; "0 skipped" is noise that makes the line longer
// than the finding.
func printCountsLine(parts ...string) {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return
	}
	fmt.Printf("  %s\n", strings.Join(kept, ", "))
}

// countPart formats "<n> <label>" or "" when n is zero.
func countPart(n int, label string) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d %s", n, label)
}
