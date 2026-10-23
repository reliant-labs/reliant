package testreport

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
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

// event is the subset of the `go test -json` event shape this package reads.
// The full shape (Time, Elapsed, Output) carries nothing a skip verdict needs.
//
// ImportPath is present because a BUILD failure is reported differently from a
// test failure: cmd/go emits `{"Action":"build-fail","ImportPath":"..."}` with
// no Package field. A parser that only knows Package silently drops those, and
// a package that did not compile then looks identical to a package that was
// never asked to run — zero tests, no complaint. See [Run.Failed].
type event struct {
	Action     string `json:"Action"`
	Package    string `json:"Package"`
	ImportPath string `json:"ImportPath"`
	Test       string `json:"Test"`
	Output     string `json:"Output"`
}

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

// parseState is the running tally Parse builds as it walks the stream. It
// exists so each event action is handled by a short method with one level of
// nesting: the switch used to live inside the read loop, four blocks deep,
// where the shape of the loop obscured the shape of the rules.
type parseState struct {
	// outcomes[pkg][test] = terminal outcome, last write wins (`-count=2`
	// reruns a test; the final verdict is the one that counts).
	outcomes map[string]map[string]Outcome
	// pending holds the last couple of output lines for each test that has
	// not finished, so a skip can be attributed to its message. Entries are
	// dropped the moment the test terminates: the alternative is holding
	// every line of a multi-hundred-megabyte stream in memory.
	pending map[string][]string
	// skipWhy[pkg][test] is the t.Skip message. Kept per TEST rather than
	// pre-aggregated per package so the counts can exclude container
	// parents at the end, exactly as the test tallies do — a reason count
	// that disagreed with the skip count beside it would undermine both.
	skipWhy map[string]map[string]string
	// parents[pkg] = set of test names that have at least one subtest.
	parents     map[string]map[string]bool
	pkgResult   map[string]Outcome
	buildFailed map[string]bool
	// seen preserves every package the stream mentioned, including ones
	// with no test events at all.
	seen map[string]bool
}

func newParseState() *parseState {
	return &parseState{
		outcomes:    map[string]map[string]Outcome{},
		pending:     map[string][]string{},
		skipWhy:     map[string]map[string]string{},
		parents:     map[string]map[string]bool{},
		pkgResult:   map[string]Outcome{},
		buildFailed: map[string]bool{},
		seen:        map[string]bool{},
	}
}

// consume folds one non-blank input line into the tally. A line that is not a
// JSON object, or carries no action, is counted as malformed rather than
// being fatal.
func (s *parseState) consume(trimmed string, run *Run) {
	var ev event
	if err := json.Unmarshal([]byte(trimmed), &ev); err != nil || ev.Action == "" {
		run.Malformed++
		return
	}
	run.Events++

	pkg := ev.Package
	if pkg == "" {
		pkg = ev.ImportPath
	}
	if pkg != "" {
		s.seen[pkg] = true
	}

	switch ev.Action {
	case "output":
		s.recordOutput(pkg, ev)
	case "build-fail":
		if pkg != "" {
			s.buildFailed[pkg] = true
		}
	case string(OutcomePass), string(OutcomeFail), string(OutcomeSkip):
		s.recordOutcome(pkg, ev, run)
	}
}

// recordOutput keeps only the last two output lines of an unfinished test —
// enough to attribute a skip to its message, bounded so a huge stream does
// not accumulate in memory.
func (s *parseState) recordOutput(pkg string, ev event) {
	if pkg == "" || ev.Test == "" {
		return
	}
	key := pkg + "\x00" + ev.Test
	buf := s.pending[key]
	buf = append(buf, ev.Output)
	if len(buf) > 2 {
		buf = buf[len(buf)-2:]
	}
	s.pending[key] = buf
}

// recordOutcome files a terminal pass/fail/skip against its package or test.
func (s *parseState) recordOutcome(pkg string, ev event, run *Run) {
	switch {
	case pkg == "":
		// An event with neither Package nor ImportPath cannot be
		// attributed. Do not fold it into some other package.
		run.Malformed++
	case ev.Test == "":
		s.pkgResult[pkg] = Outcome(ev.Action)
	default:
		if s.outcomes[pkg] == nil {
			s.outcomes[pkg] = map[string]Outcome{}
			s.parents[pkg] = map[string]bool{}
		}
		s.outcomes[pkg][ev.Test] = Outcome(ev.Action)
		if idx := strings.LastIndex(ev.Test, "/"); idx > 0 {
			s.parents[pkg][ev.Test[:idx]] = true
		}
		key := pkg + "\x00" + ev.Test
		if ev.Action == string(OutcomeSkip) {
			if why := skipReason(s.pending[key]); why != "" {
				if s.skipWhy[pkg] == nil {
					s.skipWhy[pkg] = map[string]string{}
				}
				s.skipWhy[pkg][ev.Test] = why
			}
		}
		delete(s.pending, key)
	}
}

// Parse reads a `go test -json` stream and tallies it per package.
//
// It is deliberately forgiving about lines it does not recognise and strict
// about the totals it reports. Unknown actions ("run", "output", "pause") are
// ignored; non-JSON lines are counted, not fatal. What it never does is
// invent a total: a package whose stream is truncated is marked Incomplete
// rather than reported with the partial counts it happens to hold.
//
// Reading is line-oriented via bufio.Reader (not Scanner) because a single
// `output` event can carry a very long line — a scanner's token limit would
// turn one fat log line into a parse failure for the whole run.
func Parse(r io.Reader) (*Run, error) {
	br := bufio.NewReader(r)
	run := &Run{}
	st := newParseState()

	sawAnyLine := false
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			sawAnyLine = true
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				st.consume(trimmed, run)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
	}

	if !sawAnyLine {
		return nil, ErrNoInput
	}

	outcomes := st.outcomes
	skipWhy := st.skipWhy
	parents := st.parents
	pkgResult := st.pkgResult
	buildFailed := st.buildFailed
	seen := st.seen

	for pkg := range seen {
		p := Package{Path: pkg, Result: pkgResult[pkg], BuildFailed: buildFailed[pkg]}
		for name, outcome := range outcomes[pkg] {
			if parents[pkg][name] {
				// A container, not a test. Its own verdict is a
				// summary of its children, who are counted below.
				continue
			}
			p.Tests++
			switch outcome {
			case OutcomePass:
				p.Passed++
			case OutcomeFail:
				p.Failed++
			case OutcomeSkip:
				p.Skipped++
				p.Skips = append(p.Skips, name)
				if why := skipWhy[pkg][name]; why != "" {
					if p.SkipReasons == nil {
						p.SkipReasons = map[string]int{}
					}
					// Bounded: a message interpolating a unique
					// value would otherwise grow one entry per
					// test, and the report shows only the top few.
					if len(p.SkipReasons) < maxDistinctReasons || p.SkipReasons[why] > 0 {
						p.SkipReasons[why]++
					}
				}
			}
		}
		sort.Strings(p.Skips)
		switch {
		case p.Tests == 0 && p.Result == OutcomeSkip:
			// cmd/go's "[no test files]".
			p.NoTestFiles = true
		case p.Result == "" && !p.BuildFailed:
			// No terminal package event. With test events that means a
			// truncated stream; with none it means the package was
			// announced ("start") and the stream stopped there. Both are
			// "forge does not know", not "nothing skipped".
			p.Incomplete = true
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

// skipReason extracts the t.Skip message from the output lines that preceded a
// skip event.
//
// cmd/go emits the message as a plain output line, indented and prefixed with
// the caller's file:line, immediately before `--- SKIP:`. Both markers around
// it are recognisable, so the message is whatever is left.
func skipReason(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || isTestMarker(line) {
			continue
		}
		// Strip a leading "file_test.go:42: " so the same message from
		// two call sites counts as one reason.
		if idx := strings.Index(line, ": "); idx > 0 && strings.Contains(line[:idx], ".go:") {
			line = strings.TrimSpace(line[idx+2:])
		}
		const maxLen = 100
		if len(line) > maxLen {
			line = line[:maxLen] + "..."
		}
		return line
	}
	return ""
}

// isTestMarker reports whether a line is one of cmd/go's own status lines
// rather than something the test printed.
func isTestMarker(line string) bool {
	for _, p := range []string{"=== RUN", "=== PAUSE", "=== CONT", "=== NAME", "--- SKIP", "--- PASS", "--- FAIL"} {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}
