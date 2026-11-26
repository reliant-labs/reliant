//go:build e2e

package cli

import (
	"testing"
	"time"
)

// alpineEOLMargin is how long before a branch's end of life the scaffold
// must have moved off it, so no newly scaffolded project starts on a base a
// scanner is about to call end-of-life.
const alpineEOLMargin = 120 * 24 * time.Hour

// TestE2ENightlyScaffoldedRuntimeBaseIsSupported pins NS FG-28: the
// Dockerfile forge scaffolds shipped alpine:3.21 after Trivy reported it
// end-of-life ("no longer supported by the distribution … security updates
// are not provided"). Every runtime stage must name an Alpine branch with at
// least alpineEOLMargin of support left.
//
// NIGHTLY (the TestE2ENightly prefix; internal/e2eshard), because its verdict
// changes with the calendar on purpose: it is the reminder to bump the base
// (one line in Dockerfile.tmpl plus a row in alpineReleases), and a reminder
// must not turn a merge or a push to main red for a change that did not
// cause it. nightly.yml runs it every day; a red one opens the "Nightly CI is
// red" issue. The clock-free half, TestScaffoldedRuntimeBaseHasAKnownRelease,
// runs on every merge.
func TestE2ENightlyScaffoldedRuntimeBaseIsSupported(t *testing.T) {
	for _, base := range scaffoldedAlpineBases(t) {
		released, ok := alpineReleases[base]
		if !ok {
			t.Errorf("alpine:%s has no release date in alpineReleases: add it", base)
			continue
		}
		eol := released.AddDate(2, 0, 0)
		if time.Until(eol) < alpineEOLMargin {
			t.Errorf("Dockerfile.tmpl's runtime base alpine:%s reaches end of life on %s: move it to the newest Alpine branch (and add that branch to alpineReleases)",
				base, eol.Format("2006-01-02"))
		}
	}
}
