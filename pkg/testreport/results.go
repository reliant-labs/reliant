package testreport

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Results is the forge.test.results/v1 document: one lane run's test cases,
// normalised from whichever format the lane wrote.
//
// Every case is kept, passes included. A store that keeps only the cases that
// did not pass can still imply the passes, because Packages lists every
// package the run tested and how many leaf tests each ran.
type Results struct {
	Head
	// Lane is the lane the run came from.
	Lane Lane `json:"lane,omitempty"`
	// Format is the input format the cases were read from.
	Format Format `json:"format"`
	// Packages is every package (Go) or test file (Vitest) or class (JUnit)
	// the run reported, sorted by language then path.
	Packages []PackageResult `json:"packages"`
	// Cases is every test case, parents included, sorted by id.
	Cases []Case `json:"cases"`
	// Totals counts leaf cases ([Totals]).
	Totals Totals `json:"totals"`
	// Stats describes how the input read.
	Stats ParseStats `json:"stats"`
}

// PackageResult is one package's outcome in a run.
type PackageResult struct {
	Lang Lang `json:"lang"`
	// Path is the Go import path, the Vitest test file, or the JUnit class.
	Path string `json:"path"`
	// Status is the package-level result, or "" when the input ended before
	// the package finished.
	Status Status `json:"status,omitempty"`
	// DurationMS is the package's elapsed time, when the format records it.
	DurationMS int64 `json:"duration_ms,omitempty"`
	// Tests, Passed, Failed and Skipped count LEAF cases: a test with
	// subtests is a container, and counting it beside its children would let
	// containers inflate a pass count while every leaf beneath them skipped.
	Tests   int `json:"tests"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
	// NoTestFiles is a package cmd/go skipped because it has no test files.
	NoTestFiles bool `json:"no_test_files,omitempty"`
	// BuildFailed is a package that never produced a test binary.
	BuildFailed bool `json:"build_failed,omitempty"`
	// BuildID is the name cmd/go reported the package's test build under,
	// "p [p.test]", when it used one. Its build events are folded into this
	// package.
	BuildID string `json:"build_id,omitempty"`
	// Incomplete is a package with no terminal event: the stream was
	// truncated. Its tallies are partial.
	Incomplete bool `json:"incomplete,omitempty"`
	// Output is the package's own output when it failed (a build error, a
	// panic outside any test, a timeout), bounded like a case's.
	Output string `json:"output,omitempty"`
	// Signature is [FailureSignature] of Output, when there is one.
	Signature string `json:"signature,omitempty"`
}

// ID is the package-level [TestID] (an empty Name): what a failure no test
// case owns is attributed to.
func (p PackageResult) ID() TestID { return TestID{Lang: p.Lang, Package: p.Path} }

// Case is one test case's result in a run.
type Case struct {
	ID TestID `json:"id"`
	// Status is the result of the case's LAST attempt: under `go test
	// -count=N`, or a rerun-on-failure runner, the final verdict is the one
	// that counts. RetryPassed says when an earlier attempt failed.
	Status Status `json:"status"`
	// Attempts is how many times the case reached a result in this run (1
	// unless `-count=N` or a retrying runner ran it again).
	Attempts int `json:"attempts"`
	// FailedAttempts is how many of those attempts failed.
	FailedAttempts int `json:"failed_attempts,omitempty"`
	// RetryPassed is a case that failed and then passed within the run: a
	// confirmed flake signal.
	RetryPassed bool `json:"retry_passed,omitempty"`
	// DurationMS is the case's elapsed time summed over its attempts.
	DurationMS int64 `json:"duration_ms,omitempty"`
	// Output is the failure output of the last failed attempt, scrubbed of
	// credential shapes and bounded by [Limits.MaxOutputBytes] (its first and
	// last halves). Empty for a case that never failed.
	Output string `json:"output,omitempty"`
	// OutputTruncated says Output was cut.
	OutputTruncated bool `json:"output_truncated,omitempty"`
	// Signature is [FailureSignature] of the failure output: equal for two
	// failures that differ only in addresses, durations, ids and temp paths.
	Signature string `json:"signature,omitempty"`
	// SkipReason is the t.Skip message of a skipped case, when recoverable.
	SkipReason string `json:"skip_reason,omitempty"`
	// File and Line locate the failure (`refund_test.go:88`) when the output
	// names one. File is as printed, usually a base name.
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
}

// Totals counts the leaf cases of one or more runs.
type Totals struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
	// FlakyRetryPass counts the leaf cases that failed and then passed
	// within their run.
	FlakyRetryPass int `json:"flaky_retry_pass"`
	// PackagesFailed counts packages that failed with no failing case to
	// show for it (a build failure, a panic in TestMain, a timeout).
	PackagesFailed int `json:"packages_failed,omitempty"`
}

// Add sums two Totals.
func (t Totals) Add(o Totals) Totals {
	return Totals{
		Passed: t.Passed + o.Passed, Failed: t.Failed + o.Failed, Skipped: t.Skipped + o.Skipped,
		FlakyRetryPass: t.FlakyRetryPass + o.FlakyRetryPass, PackagesFailed: t.PackagesFailed + o.PackagesFailed,
	}
}

// ParseStats describes how an input read: evidence that the parser learned
// something, or did not.
type ParseStats struct {
	// Lines is the number of non-blank input lines (line-oriented formats).
	Lines int `json:"lines,omitempty"`
	// Events is the number of records the parser understood (`go test
	// -json` events, JUnit testcases, Vitest assertions). Zero means the
	// input carried no facts.
	Events int `json:"events"`
	// Malformed is the number of records that could not be read: a line that
	// is not JSON, an event naming no package, a line over MaxLineBytes.
	Malformed int `json:"malformed,omitempty"`
}

// ResultsOptions configures [ParseResults].
type ResultsOptions struct {
	// Format is the input's format; empty detects it ([DetectFormat]).
	Format Format
	// Lane is stamped on the document.
	Lane Lane
	// Lang is the language of JUnit cases whose testcase names no file.
	// Default: the file attribute's extension, else [LangOther].
	Lang Lang
	// Root is the directory Vitest's absolute test-file paths are made
	// relative to (the project root). Empty keeps them as written.
	Root string
	// Limits bounds the parse.
	Limits Limits
}

// ParseResults reads one lane run's results. The format is detected unless
// opts names it. A reader that holds nothing returns [ErrNoInput]; input no
// parser recognises is an error naming [FormatUnknown].
//
// The returned document's Head is set from the results: exit code 0 when no
// leaf case and no package failed, 1 otherwise. A caller that knows the
// lane's real exit code stamps it over.
func ParseResults(r io.Reader, opts ResultsOptions) (*Results, error) {
	lim := opts.Limits.resolved()
	br := bufio.NewReaderSize(newLimitedReader(r, lim.MaxInputBytes), 64<<10)
	format := opts.Format
	if format == "" {
		head, err := br.Peek(detectPeekBytes)
		if len(head) == 0 {
			if err != nil && err != io.EOF {
				return nil, err
			}
			return nil, ErrNoInput
		}
		format = DetectFormat(head)
		if !format.IsResults() {
			return nil, fmt.Errorf("testreport: input is not a results format (detected %s)", format)
		}
	}
	var (
		res *Results
		err error
	)
	switch format {
	case FormatGoJSON:
		res, err = parseGoJSON(br, opts, lim)
	case FormatJUnit:
		res, err = parseJUnit(br, opts, lim)
	case FormatVitest:
		res, err = parseVitest(br, opts, lim)
	default:
		return nil, fmt.Errorf("testreport: %q is not a results format", format)
	}
	if err != nil {
		return nil, err
	}
	res.Format = format
	res.Lane = opts.Lane
	res.finish()
	return res, nil
}

// finish sorts the document, tallies leaf cases per package and in total, and
// sets the head.
func (r *Results) finish() {
	sort.Slice(r.Cases, func(i, j int) bool { return lessID(r.Cases[i].ID, r.Cases[j].ID) })
	sort.Slice(r.Packages, func(i, j int) bool {
		a, b := r.Packages[i], r.Packages[j]
		if a.Lang != b.Lang {
			return a.Lang < b.Lang
		}
		return a.Path < b.Path
	})
	parents := parentSet(r.Cases)
	idx := make(map[TestID]int, len(r.Packages))
	for i, p := range r.Packages {
		idx[p.ID()] = i
		r.Packages[i].Tests, r.Packages[i].Passed, r.Packages[i].Failed, r.Packages[i].Skipped = 0, 0, 0, 0
	}
	var t Totals
	for _, c := range r.Cases {
		if parents[c.ID] {
			continue
		}
		i, ok := idx[TestID{Lang: c.ID.Lang, Package: c.ID.Package}]
		var p *PackageResult
		if ok {
			p = &r.Packages[i]
			p.Tests++
		}
		switch c.Status {
		case StatusPass:
			t.Passed++
			if p != nil {
				p.Passed++
			}
		case StatusFail:
			t.Failed++
			if p != nil {
				p.Failed++
			}
		case StatusSkip:
			t.Skipped++
			if p != nil {
				p.Skipped++
			}
		}
		if c.RetryPassed {
			t.FlakyRetryPass++
		}
	}
	for _, p := range r.Packages {
		if (p.Status == StatusFail || p.BuildFailed) && p.Failed == 0 {
			t.PackagesFailed++
		}
	}
	r.Totals = t
	r.Schema = SchemaResults
	r.ExitCode = 0
	if t.Failed > 0 || t.PackagesFailed > 0 {
		r.ExitCode = 1
	}
	r.OK = r.ExitCode == 0
}

// Leaves returns the cases that ran no subtests of their own.
func (r *Results) Leaves() []Case {
	parents := parentSet(r.Cases)
	out := make([]Case, 0, len(r.Cases))
	for _, c := range r.Cases {
		if !parents[c.ID] {
			out = append(out, c)
		}
	}
	return out
}

// parentSet marks every Go case another case is a direct subtest of. Only
// Go names carry a subtest path.
func parentSet(cases []Case) map[TestID]bool {
	parents := map[TestID]bool{}
	for _, c := range cases {
		if c.ID.Lang != LangGo {
			continue
		}
		if i := strings.LastIndex(c.ID.Name, "/"); i > 0 {
			p := c.ID
			p.Name = c.ID.Name[:i]
			parents[p] = true
		}
	}
	return parents
}

func lessID(a, b TestID) bool {
	if a.Lang != b.Lang {
		return a.Lang < b.Lang
	}
	if a.Package != b.Package {
		return a.Package < b.Package
	}
	return a.Name < b.Name
}

// Outcome is one attempt of one test in one run: the row flake history is
// computed from (test id, lane, commit, run id, attempt, status).
type Outcome struct {
	Test    TestID `json:"test"`
	Lane    Lane   `json:"lane"`
	Commit  string `json:"commit"`
	Run     string `json:"run"`
	Attempt int    `json:"attempt"`
	Status  Status `json:"status"`
}

// Outcomes flattens the run's leaf cases into per-attempt rows for the given
// commit and run id, attempts numbered from 1. The record keeps counts, not
// the order of attempts, so the order is normalised: a case that ended
// passing lists its failures first; one that ended failing lists its passes
// first. Either way the last attempt carries the case's Status.
func (r *Results) Outcomes(commit, run string) []Outcome {
	var out []Outcome
	for _, c := range r.Leaves() {
		n := max(c.Attempts, 1)
		for a := 1; a <= n; a++ {
			st := c.Status
			switch c.Status {
			case StatusPass:
				if a <= c.FailedAttempts {
					st = StatusFail
				}
			case StatusFail:
				if a <= n-max(c.FailedAttempts, 1) {
					st = StatusPass
				}
			}
			out = append(out, Outcome{Test: c.ID, Lane: r.Lane, Commit: commit, Run: run, Attempt: a, Status: st})
		}
	}
	return out
}

// detectPeekBytes is how much of an input DetectFormat looks at.
const detectPeekBytes = 8 << 10

// DetectFormat names the format of an input from its first bytes (8 KiB is
// plenty): a `go test -json` event, JUnit or Cobertura XML, a Vitest or
// istanbul JSON document, a Go coverage profile or an LCOV tracefile.
// [FormatUnknown] when none matches.
func DetectFormat(head []byte) Format {
	s := bytes.TrimLeft(head, " \t\r\n\ufeff")
	s = bytes.TrimPrefix(s, []byte("\xef\xbb\xbf"))
	switch {
	case bytes.HasPrefix(s, []byte("mode:")):
		return FormatGoCover
	case bytes.HasPrefix(s, []byte("TN:")), bytes.HasPrefix(s, []byte("SF:")):
		return FormatLCOV
	case bytes.HasPrefix(s, []byte("<")):
		switch {
		case bytes.Contains(s, []byte("<testsuites")), bytes.Contains(s, []byte("<testsuite")):
			return FormatJUnit
		case bytes.Contains(s, []byte("<coverage")):
			return FormatCobertura
		}
	case bytes.HasPrefix(s, []byte("{")):
		switch {
		case bytes.Contains(s, []byte(`"testResults"`)) || bytes.Contains(s, []byte(`"numTotalTests"`)):
			return FormatVitest
		case bytes.Contains(s, []byte(`"statementMap"`)):
			return FormatIstanbul
		case bytes.Contains(s, []byte(`"Action"`)):
			return FormatGoJSON
		}
	}
	// A go -json stream may open with a toolchain banner line before its
	// first event.
	for _, line := range bytes.SplitN(s, []byte("\n"), 8) {
		l := bytes.TrimSpace(line)
		if bytes.HasPrefix(l, []byte("{")) && bytes.Contains(l, []byte(`"Action"`)) {
			return FormatGoJSON
		}
	}
	return FormatUnknown
}
