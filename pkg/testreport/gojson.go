package testreport

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
)

// goEvent is the subset of the `go test -json` event this parser reads.
//
// ImportPath is read because a BUILD failure is reported differently from a
// test failure: cmd/go emits {"Action":"build-fail","ImportPath":"..."} with no
// Package field, and a parser that knows only Package drops it, so a package
// that did not compile looks like one that was never asked to run.
//
// Elapsed and Time stay raw: a malformed value in a field the verdict does
// not need must not turn a readable event into a malformed one.
type goEvent struct {
	Action     string          `json:"Action"`
	Package    string          `json:"Package"`
	ImportPath string          `json:"ImportPath"`
	Test       string          `json:"Test"`
	Output     string          `json:"Output"`
	Elapsed    json.RawMessage `json:"Elapsed"`
}

func (e goEvent) elapsedMS() int64 {
	if len(e.Elapsed) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(string(e.Elapsed), 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) || v > 1e9 {
		return 0
	}
	return int64(math.Round(v * 1000))
}

// goCase is one test's running state.
type goCase struct {
	id        TestID
	attempts  int
	failed    int
	last      Status
	retryPass bool
	durMS     int64
	// out is the current attempt's output (markers dropped).
	out *boundedText
	// failOut is the last failed attempt's output, unscrubbed.
	failOut      string
	failTrunc    bool
	failCounted  int // bytes of failOut charged to the budget
	recentOutput []string
	skipWhy      string
}

type goPackage struct {
	res PackageResult
	out *boundedText
	// seenOrder keeps first-seen order irrelevant; results are sorted.
}

type goParser struct {
	lim   Limits
	res   *Results
	cases map[TestID]*goCase
	pkgs  map[string]*goPackage
	// buffered is the output bytes held in live buffers plus kept
	// failure output: bounded by MaxTotalOutputBytes.
	buffered int64
}

// parseGoJSON reads a `go test -json` stream.
//
// It is forgiving about lines it does not recognise and strict about the
// totals it reports: non-JSON lines are counted, not fatal, and a package
// whose stream is truncated is marked Incomplete rather than reported with
// the partial counts it happens to hold.
func parseGoJSON(br *bufio.Reader, opts ResultsOptions, lim Limits) (*Results, error) {
	p := &goParser{lim: lim, res: &Results{}, cases: map[TestID]*goCase{}, pkgs: map[string]*goPackage{}}
	sawAny := false
	for {
		line, tooLong, err := readLine(br, lim.MaxLineBytes)
		if line != "" || tooLong {
			sawAny = true
		}
		if tooLong {
			p.res.Stats.Lines++
			p.res.Stats.Malformed++
		} else if trimmed := strings.TrimSpace(line); trimmed != "" {
			p.res.Stats.Lines++
			if err := p.consume(trimmed); err != nil {
				return nil, err
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
	}
	if !sawAny {
		return nil, ErrNoInput
	}
	return p.finish(), nil
}

// readLine reads one line, without its delimiter's significance: the line
// is returned as read. A line longer than max is consumed and reported
// tooLong, never buffered whole.
func readLine(br *bufio.Reader, max int) (line string, tooLong bool, err error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if !tooLong {
			if len(buf)+len(chunk) > max {
				tooLong = true
				buf = nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if tooLong {
			return "", true, err
		}
		return string(buf), false, err
	}
}

func (p *goParser) pkg(path string) (*goPackage, error) {
	if gp, ok := p.pkgs[path]; ok {
		return gp, nil
	}
	if len(p.pkgs) >= p.lim.MaxPackages {
		return nil, &LimitError{Limit: "MaxPackages", Max: int64(p.lim.MaxPackages)}
	}
	gp := &goPackage{res: PackageResult{Lang: LangGo, Path: path}}
	p.pkgs[path] = gp
	return gp, nil
}

func (p *goParser) test(pkg, name string) (*goCase, error) {
	id := TestID{Lang: LangGo, Package: pkg, Name: name}
	if c, ok := p.cases[id]; ok {
		return c, nil
	}
	if len(p.cases) >= p.lim.MaxCases {
		return nil, &LimitError{Limit: "MaxCases", Max: int64(p.lim.MaxCases)}
	}
	c := &goCase{id: id}
	p.cases[id] = c
	return c, nil
}

// consume folds one non-blank line in. A line that is not a JSON object, or
// carries no action, is malformed rather than fatal.
func (p *goParser) consume(line string) error {
	var ev goEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Action == "" {
		p.res.Stats.Malformed++
		return nil
	}
	p.res.Stats.Events++
	pkg := ev.Package
	if pkg == "" {
		pkg = ev.ImportPath
	}
	// cmd/go reports a test build under its variant's name, "p [p.test]".
	// It is package p's build: fold it into p, and remember the name.
	variant := ""
	if i := strings.Index(pkg, " ["); i > 0 {
		variant, pkg = pkg, pkg[:i]
	}
	var gp *goPackage
	if pkg != "" {
		var err error
		if gp, err = p.pkg(pkg); err != nil {
			return err
		}
		if variant != "" && gp.res.BuildID == "" {
			gp.res.BuildID = variant
		}
	}
	switch ev.Action {
	case "output", "build-output":
		switch {
		case gp == nil:
		case ev.Test == "":
			p.write(&gp.out, ev.Output)
		default:
			c, err := p.test(pkg, ev.Test)
			if err != nil {
				return err
			}
			c.recentOutput = append(c.recentOutput, ev.Output)
			if len(c.recentOutput) > 2 {
				c.recentOutput = c.recentOutput[len(c.recentOutput)-2:]
			}
			if !isRunMarker(ev.Output) {
				p.write(&c.out, ev.Output)
			}
		}
	case "build-fail":
		if gp != nil {
			gp.res.BuildFailed = true
		}
	case "run":
		if gp != nil && ev.Test != "" {
			c, err := p.test(pkg, ev.Test)
			if err != nil {
				return err
			}
			p.release(&c.out)
		}
	case string(StatusPass), string(StatusFail), string(StatusSkip):
		switch {
		case gp == nil:
			// Neither Package nor ImportPath: it cannot be attributed, and
			// folding it into some other package would be a lie.
			p.res.Stats.Malformed++
		case ev.Test == "":
			gp.res.Status = Status(ev.Action)
			gp.res.DurationMS = ev.elapsedMS()
		default:
			c, err := p.test(pkg, ev.Test)
			if err != nil {
				return err
			}
			p.terminate(c, Status(ev.Action), ev.elapsedMS())
		}
	}
	return nil
}

// terminate records one attempt's result.
func (p *goParser) terminate(c *goCase, st Status, ms int64) {
	c.attempts++
	c.durMS += ms
	switch st {
	case StatusFail:
		c.failed++
		p.dropFailOut(c)
		if c.out != nil {
			c.failOut, c.failTrunc = c.out.String(), c.out.truncated()
			c.failCounted = c.out.size()
			c.out = nil // its bytes stay charged, now as failOut
		}
	case StatusPass:
		if c.failed > 0 {
			c.retryPass = true
		}
	case StatusSkip:
		if why := skipReason(c.recentOutput); why != "" {
			c.skipWhy = why
		}
	}
	c.last = st
	c.recentOutput = nil
	p.release(&c.out)
}

// write appends output to a buffer while the run-wide budget lasts.
func (p *goParser) write(b **boundedText, s string) {
	if *b == nil {
		*b = newBoundedText(p.lim.MaxOutputBytes)
	}
	before := (*b).size()
	room := p.lim.MaxTotalOutputBytes - p.buffered
	if room <= 0 {
		(*b).total += int64(len(s)) // remember it was cut
		return
	}
	if int64(len(s)) > room {
		(*b).total += int64(len(s)) - room
		s = s[:room]
	}
	(*b).write(s)
	p.buffered += int64((*b).size() - before)
}

func (p *goParser) release(b **boundedText) {
	if *b != nil {
		p.buffered -= int64((*b).size())
		*b = nil
	}
}

func (p *goParser) dropFailOut(c *goCase) {
	p.buffered -= int64(c.failCounted)
	c.failOut, c.failTrunc, c.failCounted = "", false, 0
}

func (p *goParser) finish() *Results {
	r := p.res
	for _, c := range p.cases {
		if c.attempts == 0 {
			continue // announced, never finished: its package is Incomplete
		}
		cs := Case{
			ID: c.id, Status: c.last, Attempts: c.attempts, FailedAttempts: c.failed,
			RetryPassed: c.retryPass, DurationMS: c.durMS, SkipReason: c.skipWhy,
		}
		if c.failed > 0 {
			cs.Output = scrub(c.failOut)
			cs.OutputTruncated = c.failTrunc
			cs.Signature = FailureSignature(c.failOut)
			cs.File, cs.Line = locate(c.failOut)
		}
		r.Cases = append(r.Cases, cs)
	}
	hasCase := map[string]bool{}
	for _, c := range r.Cases {
		hasCase[c.ID.Package] = true
	}
	for _, gp := range p.pkgs {
		pr := gp.res
		switch {
		case !hasCase[pr.Path] && pr.Status == StatusSkip:
			pr.NoTestFiles = true // cmd/go's "[no test files]"
		case pr.Status == "" && !pr.BuildFailed:
			// No terminal package event: with test events a truncated
			// stream, without any an announced package the stream stopped
			// at. Both are "forge does not know", not "nothing skipped".
			pr.Incomplete = true
		}
		if (pr.Status == StatusFail || pr.BuildFailed) && gp.out != nil {
			raw := gp.out.String()
			pr.Output = scrub(raw)
			pr.Signature = FailureSignature(raw)
		}
		r.Packages = append(r.Packages, pr)
	}
	if r.Cases == nil {
		r.Cases = []Case{}
	}
	if r.Packages == nil {
		r.Packages = []PackageResult{}
	}
	return r
}

// isRunMarker is cmd/go's per-test bookkeeping line, which says nothing about
// a failure.
func isRunMarker(s string) bool {
	t := strings.TrimSpace(s)
	return strings.HasPrefix(t, "=== RUN") || strings.HasPrefix(t, "=== PAUSE") ||
		strings.HasPrefix(t, "=== CONT") || strings.HasPrefix(t, "=== NAME")
}

// skipReason extracts the t.Skip message from the output lines that preceded
// a skip event. cmd/go prints it as a plain output line, indented and
// prefixed with the caller's file:line, immediately before `--- SKIP:`, so the
// message is what is left between those markers.
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
