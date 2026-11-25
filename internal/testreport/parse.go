package testreport

import (
	"errors"
	"io"
	"sort"

	pkgtestreport "github.com/reliant-labs/forge/pkg/testreport"
)

// Outcome is a test's terminal result. It mirrors the `Action` field of the
// `go test -json` events that end a test: "pass", "fail", "skip".
type Outcome string

// The three terminal outcomes.
const (
	OutcomePass Outcome = "pass"
	OutcomeFail Outcome = "fail"
	OutcomeSkip Outcome = "skip"
)

// Package holds one package's tally from a run.
//
// Tests/Passed/Failed/Skipped count LEAF tests only — a test that ran no
// subtests of its own. A parent is a container, not a test, and counting it
// alongside its children lets a package inflate its pass count with
// containers while every leaf beneath them skipped. Concretely: a table test
// whose 20 cases all skip is 20 skipped leaves (ratio 1.00), not "1 pass and
// 20 skips" (ratio 0.95). The stricter reading is the honest one.
type Package struct {
	Path    string
	Tests   int
	Passed  int
	Failed  int
	Skipped int

	// Skips names the skipped leaf tests, sorted, so a finding can show
	// what actually went missing instead of only a number.
	Skips []string

	// SkipReasons counts the t.Skip messages behind those skips.
	//
	// A list of test NAMES says what went missing; the reason says what to
	// DO about it. "124 tests skipped" is a number to argue with;
	// `118x "DATABASE_URL not set"` is an instruction. cmd/go prints the
	// message as an ordinary output line just before `--- SKIP:`, so it
	// costs nothing to collect — and if the format ever changes the map
	// comes back empty and the report falls back to names, because a
	// verdict must never depend on scraping free text.
	SkipReasons map[string]int

	// Result is the package-level terminal action ("pass"/"fail"/"skip"),
	// or "" when the stream ended before the package finished.
	Result Outcome

	// NoTestFiles is a package cmd/go skipped outright: it has no test
	// files. Not a finding — nothing was lost, there was nothing there.
	NoTestFiles bool

	// BuildFailed is a package that never produced a test binary.
	BuildFailed bool

	// Incomplete is a package with test events but no terminal package
	// event: the stream was truncated (killed run, `head`-ed log, crash).
	// Its tallies are partial, so it is UNDETERMINED rather than counted.
	Incomplete bool
}

// SkipRatio is the share of this package's leaf tests that skipped, in [0,1].
// A package with no tests has ratio 0 — there is no share of nothing.
func (p Package) SkipRatio() float64 {
	if p.Tests == 0 {
		return 0
	}
	return float64(p.Skipped) / float64(p.Tests)
}

// Run is a whole `go test -json` stream, tallied.
type Run struct {
	// Packages, sorted by import path.
	Packages []Package

	// Events is the number of parsed JSON events. Zero means forge learned
	// nothing from this input, which [Analyze] reports as UNDETERMINED.
	Events int

	// Malformed is the number of input lines that were not JSON objects.
	// A few are tolerable (a toolchain banner ahead of the stream); all of
	// them, with zero events, means the input was not `go test -json`.
	Malformed int
}

// ErrNoInput is returned by [Parse] when the reader held nothing at all.
// Callers must not treat it as an empty-but-valid run: a zero-byte stream is
// the absence of facts, not the fact that nothing skipped.
var ErrNoInput = errors.New("no input: expected a `go test -json` stream")

// Parse reads a `go test -json` stream and tallies it per package.
//
// The stream is read by pkg/testreport, the one `go test -json` parser forge
// has (the record `forge test` keeps and the control plane ingests use it
// too); Parse tallies that parser's cases into the skip-gate's shape.
//
// It is deliberately forgiving about lines it does not recognise and strict
// about the totals it reports. Unknown actions ("run", "output", "pause") are
// ignored; non-JSON lines are counted, not fatal. What it never does is
// invent a total: a package whose stream is truncated is marked Incomplete
// rather than reported with the partial counts it happens to hold. The input
// is the project's own suite output, so it is read without size limits.
func Parse(r io.Reader) (*Run, error) {
	res, err := pkgtestreport.ParseResults(r, pkgtestreport.ResultsOptions{
		Format: pkgtestreport.FormatGoJSON,
		Limits: pkgtestreport.Limits{MaxInputBytes: -1, MaxLineBytes: -1, MaxCases: -1, MaxPackages: -1},
	})
	if errors.Is(err, pkgtestreport.ErrNoInput) {
		return nil, ErrNoInput
	}
	if err != nil {
		return nil, err
	}
	run := &Run{Events: res.Stats.Events, Malformed: res.Stats.Malformed}
	leaves := map[string][]pkgtestreport.Case{}
	for _, c := range res.Leaves() {
		leaves[c.ID.Package] = append(leaves[c.ID.Package], c)
	}
	for _, pr := range res.Packages {
		buildFailed := pr.BuildFailed
		if pr.BuildID != "" {
			// cmd/go reported the build under its test variant's name
			// ("p [p.test]"). The skip gate has always listed that name as
			// its own package beside p, so it still does.
			run.Packages = append(run.Packages, Package{Path: pr.BuildID, BuildFailed: pr.BuildFailed, Incomplete: !pr.BuildFailed})
			buildFailed = false
		}
		p := Package{
			Path: pr.Path, Result: Outcome(pr.Status), BuildFailed: buildFailed,
			NoTestFiles: pr.NoTestFiles, Incomplete: pr.Incomplete,
			Tests: pr.Tests, Passed: pr.Passed, Failed: pr.Failed, Skipped: pr.Skipped,
		}
		// Leaves arrive sorted by name, so Skips is sorted.
		for _, c := range leaves[pr.Path] {
			if c.Status != pkgtestreport.StatusSkip {
				continue
			}
			p.Skips = append(p.Skips, c.ID.Name)
			if why := c.SkipReason; why != "" {
				if p.SkipReasons == nil {
					p.SkipReasons = map[string]int{}
				}
				// Bounded: a message interpolating a unique value would
				// otherwise grow one entry per test, and the report shows
				// only the top few.
				if len(p.SkipReasons) < maxDistinctReasons || p.SkipReasons[why] > 0 {
					p.SkipReasons[why]++
				}
			}
		}
		if p.BuildFailed {
			p.Incomplete = false
		} else if p.Result == "" {
			p.Incomplete = !p.NoTestFiles
		}
		run.Packages = append(run.Packages, p)
	}
	sort.Slice(run.Packages, func(i, j int) bool { return run.Packages[i].Path < run.Packages[j].Path })
	return run, nil
}

// maxDistinctReasons bounds the per-package reason map. A skip message that
// interpolates a unique value ("skipping shard 37") would otherwise grow one
// entry per test, and the report only ever shows the top few anyway.
const maxDistinctReasons = 64
