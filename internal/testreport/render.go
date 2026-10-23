package testreport

import (
	"fmt"
	"io"
	"strings"
)

// maxListed caps every list this report prints. A gate that dumps two hundred
// lines is a gate people scroll past, and the tail of a sorted-worst-first
// list adds nothing the head did not already say.
const maxListed = 20

// Render writes the human-readable report.
//
// The shape is deliberate: healthy packages are NEVER listed. The report's
// whole claim to being read is that its body is empty when nothing is wrong,
// so a non-empty body means something happened. A summary line always prints,
// because "how much of this suite actually ran" is the number that was
// missing in the first place.
func Render(w io.Writer, a Analysis) {
	switch a.Status() {
	case StatusUndetermined:
		fmt.Fprintln(w, "⚠️  UNDETERMINED — forge could not obtain the facts (this is NOT a pass):")
	case StatusFail:
		// Status is only Fail when there is at least one finding, so the
		// colon below always has a list under it.
		fmt.Fprintf(w, "❌ %s:\n", headline(a))
	case StatusNotApplicable:
		fmt.Fprintln(w, "⏭️  No tests in this run — nothing to judge.")
	case StatusPass:
		fmt.Fprintf(w, "✅ %s\n", summaryLine(a))
		if n := len(a.Suppressed); n > 0 {
			// Never claim "nothing skipped heavily" over a package that
			// did and was excused — the excuse is listed below, and a
			// headline that contradicts its own footnote is how a
			// report loses the reader.
			fmt.Fprintf(w, "   No package skipped more than %.0f%% of its tests, apart from the %d declared below.\n", a.Policy.MaxSkipRatio*100, n)
			break
		}
		fmt.Fprintf(w, "   No package skipped more than %.0f%% of its tests.\n", a.Policy.MaxSkipRatio*100)
	}

	if len(a.Findings) > 0 {
		fmt.Fprintln(w)
		for i, f := range a.Findings {
			if i == maxListed {
				fmt.Fprintf(w, "  ... and %d more.\n", len(a.Findings)-maxListed)
				break
			}
			renderFinding(w, f)
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %s\n", summaryLine(a))
	}

	if len(a.Undetermined) > 0 {
		if a.Status() != StatusUndetermined {
			fmt.Fprintln(w)
			fmt.Fprintln(w, "⚠️  UNDETERMINED — forge could not obtain these facts (not a pass):")
		}
		for i, u := range a.Undetermined {
			if i == maxListed {
				fmt.Fprintf(w, "  ... and %d more.\n", len(a.Undetermined)-maxListed)
				break
			}
			if u.Package == "" {
				fmt.Fprintf(w, "  %s\n", u.Reason)
				continue
			}
			fmt.Fprintf(w, "  %s: %s\n", u.Package, u.Reason)
		}
	}

	// The notes below are one line each and print only when they apply.
	if n := len(a.Suppressed); n > 0 {
		fmt.Fprintf(w, "\nℹ️  %d package(s) skipped heavily as DECLARED in forge.yaml (ci.test_skips.allow) — not reported:\n", n)
		for i, s := range a.Suppressed {
			if i == maxListed {
				fmt.Fprintf(w, "     ... and %d more.\n", n-maxListed)
				break
			}
			fmt.Fprintf(w, "     %s (%d/%d skipped) — %s\n", s.Package, s.Skipped, s.Tests, s.Exemption.Reason)
		}
	}
	if n := len(a.Stale); n > 0 {
		// An exemption that no longer suppresses anything is a rule that
		// can never fire — the same unfalsifiable shape the check exists
		// to find. Reported, never fatal: the run that healed the package
		// should not go red for having healed it.
		fmt.Fprintf(w, "\nℹ️  %d declared skip exemption(s) are no longer needed — the package(s) they cover came in clean:\n", n)
		for i, e := range a.Stale {
			if i == maxListed {
				fmt.Fprintf(w, "     ... and %d more.\n", n-maxListed)
				break
			}
			fmt.Fprintf(w, "     %s (%q) — remove it from forge.yaml (ci.test_skips.allow)\n", e.Package, e.Reason)
		}
	}
	if a.Malformed > 0 && a.Status() != StatusUndetermined {
		fmt.Fprintf(w, "\nℹ️  %d input line(s) were not `go test -json` events and were ignored.\n", a.Malformed)
	}
	if a.Totals.NoTestsRan > 0 {
		fmt.Fprintf(w, "\nℹ️  %d package(s) compiled but ran no test (a -run filter, or an empty suite).\n", a.Totals.NoTestsRan)
	}
}

func renderFinding(w io.Writer, f Finding) {
	if f.Kind == KindFailed {
		fmt.Fprintf(w, "  FAIL  %s — %s\n", f.Package, f.Detail)
		return
	}
	fmt.Fprintf(w, "  %3.0f%%  %s — %d of %d tests skipped (%s)\n",
		f.Ratio*100, f.Package, f.Skipped, f.Tests, describe(f.Kind))
	for _, r := range f.Reasons {
		fmt.Fprintf(w, "        %d× %q\n", r.Count, r.Reason)
	}
	if len(f.Examples) > 0 {
		fmt.Fprintf(w, "        e.g. %s\n", strings.Join(f.Examples, ", "))
	}
}

func describe(k Kind) string {
	switch k {
	case KindZeroEvidence:
		return "zero-evidence: nothing in this package ran, so its pass proves nothing"
	case KindMassSkip:
		return "mass-skip"
	default:
		return string(k)
	}
}

func headline(a Analysis) string {
	skips, fails := 0, 0
	for _, f := range a.Findings {
		if f.Kind == KindFailed {
			fails++
			continue
		}
		skips++
	}
	switch {
	case skips > 0 && fails > 0:
		return fmt.Sprintf("%d package(s) skipped most of their tests, and %d failed", skips, fails)
	case fails > 0:
		return fmt.Sprintf("%d package(s) failed", fails)
	default:
		return fmt.Sprintf("%d package(s) skipped so much that their pass proves nothing", skips)
	}
}

func summaryLine(a Analysis) string {
	t := a.Totals
	s := fmt.Sprintf("%d package(s), %d test(s): %d passed, %d skipped (%.1f%%)",
		t.Packages, t.Tests, t.Passed, t.Skipped, t.SkipRatio*100)
	if t.Failed > 0 {
		s += fmt.Sprintf(", %d failed", t.Failed)
	}
	if t.NoTestFiles > 0 {
		s += fmt.Sprintf("; %d package(s) have no test files", t.NoTestFiles)
	}
	return s + "."
}
