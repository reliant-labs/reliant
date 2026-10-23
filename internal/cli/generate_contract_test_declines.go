// Reporting for contract_test.go scaffolds forge declined to write.
//
// contract_test.go.tmpl emits exactly one call — `_, err := pkg.New(pkg.Deps{})`
// — which compiles only for the canonical single-`Service`, two-result-`New`
// package. Three package shapes are therefore declined rather than scaffolded,
// because writing the file anyway hands back a package that does not build.
//
// Each decline used to print its own line, unconditionally. In control-plane
// that was ~25 lines on every run, describing 25 packages whose shape is a
// deliberate choice and is not going to change. They split into two kinds,
// and the two deserve opposite treatment:
//
//   - A multi-interface package, or one whose interface is not named Service,
//     is simply a package forge does not scaffold tests for. There is no
//     advice in it — "write tests manually" is what the author is already
//     doing. ROUTINE: behind -v.
//
//   - A single-result `New` is different: `func New(Deps) (Service, error)`
//     IS the shape forge's own conventions ask for, and polishing to it
//     turns the auto-scaffold back on. That is real advice, so it survives
//     at default verbosity — but as ONE line with a count, not one line per
//     package. The per-package list is what -v is for.
//
// The distinction that keeps this honest: a line stays at default verbosity
// when acting on it CHANGES something. "This package has two interfaces" does
// not; "these four packages would get scaffolded tests if you polished their
// constructor" does.
package cli

import (
	"fmt"
	"sort"
)

// contractTestDeclines accumulates the packages whose contract_test.go
// scaffold was declined during one walk of internal/, so the run can report
// them as a summary instead of a running commentary.
//
// The zero value is ready to use. It is not safe for concurrent use; the
// walk that fills it is sequential.
type contractTestDeclines struct {
	// routine holds packages declined for a shape forge simply does not
	// scaffold for, each with its reason.
	routine []string
	// polish holds packages whose only obstacle is a single-result New —
	// the actionable set.
	polish []string
}

// note records a decline that carries no advice.
func (d *contractTestDeclines) note(rel, reason string) {
	if d == nil {
		return
	}
	d.routine = append(d.routine, fmt.Sprintf("%s/ (%s)", rel, reason))
}

// notePolish records a package that would be auto-scaffolded if its
// constructor were polished to the canonical two-result shape.
func (d *contractTestDeclines) notePolish(rel string) {
	if d == nil {
		return
	}
	d.polish = append(d.polish, rel)
}

// report prints the accumulated declines: the routine ones only under -v,
// and the actionable ones as a single counted line naming where the detail
// lives. Silent when nothing was declined.
func (d *contractTestDeclines) report() {
	if d == nil {
		return
	}
	sort.Strings(d.routine)
	for _, r := range d.routine {
		routinef("  ℹ️  Skipped contract_test.go scaffold for %s\n", r)
	}
	if len(d.polish) == 0 {
		return
	}
	sort.Strings(d.polish)
	if generateVerbose {
		for _, p := range d.polish {
			fmt.Printf("  ℹ️  Skipped contract_test.go scaffold for %s/ (New is single-result)\n", p)
		}
	}
	// One line rather than one per package — but still behind -v, because
	// it is a standing fact, not an event.
	//
	// This was the closest call in the whole change. `func New(Deps)
	// (Service, error)` IS forge's canonical shape and polishing to it
	// re-enables the auto-scaffold, so the advice is genuinely real, which
	// argues for default verbosity. What settles it is that the line is
	// IDENTICAL on every run: these packages have the shape their authors
	// chose, and control-plane would print the same count until someone
	// changed seven constructors. Advice that never changes is
	// documentation, and documentation printed on every build is read
	// once and skipped forever — taking the block it sits in with it.
	//
	// So it keeps the count and the example (so one -v is enough to act),
	// and `forge lint` remains the surface that judges package shape.
	routinef("  ℹ️  %d package(s) would get a scaffolded contract_test.go if New returned (Service, error) — e.g. %s/.\n",
		len(d.polish), d.polish[0])
}

// polishSummary renders what report() would print for the actionable set,
// for tests that assert the collapse without capturing stdout.
func (d *contractTestDeclines) polishSummary() string {
	if d == nil || len(d.polish) == 0 {
		return ""
	}
	sorted := append([]string(nil), d.polish...)
	sort.Strings(sorted)
	return fmt.Sprintf("%d package(s) would get a scaffolded contract_test.go if New returned (Service, error) — e.g. %s/",
		len(sorted), sorted[0])
}

// routineLines renders the suppressed-by-default set, for tests.
func (d *contractTestDeclines) routineLines() []string {
	if d == nil {
		return nil
	}
	out := append([]string(nil), d.routine...)
	sort.Strings(out)
	return out
}
