package testreport

import (
	"bufio"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// caseAcc accumulates one case's attempts in formats that report each
// attempt as its own record (JUnit reruns).
type caseAcc struct {
	c        Case
	failOut  string
	failTrnc bool
}

func (a *caseAcc) attempt(st Status, ms int64, out string, trunc bool) {
	a.c.Attempts++
	a.c.DurationMS += ms
	switch st {
	case StatusFail:
		a.c.FailedAttempts++
		a.failOut, a.failTrnc = out, trunc
	case StatusPass:
		if a.c.FailedAttempts > 0 {
			a.c.RetryPassed = true
		}
	}
	a.c.Status = st
}

// caseSet is the cases of a format that is read record by record, in
// first-seen order, bounded by MaxCases.
type caseSet struct {
	lim    Limits
	byID   map[TestID]*caseAcc
	order  []TestID
	budget outputBudget
	pkgs   map[TestID]*PackageResult
	porder []TestID
}

func newCaseSet(lim Limits) *caseSet {
	return &caseSet{lim: lim, byID: map[TestID]*caseAcc{}, budget: outputBudget{left: lim.MaxTotalOutputBytes}, pkgs: map[TestID]*PackageResult{}}
}

func (s *caseSet) get(id TestID) (*caseAcc, error) {
	if a, ok := s.byID[id]; ok {
		return a, nil
	}
	if len(s.byID) >= s.lim.MaxCases {
		return nil, &LimitError{Limit: "MaxCases", Max: int64(s.lim.MaxCases)}
	}
	a := &caseAcc{c: Case{ID: id}}
	s.byID[id] = a
	s.order = append(s.order, id)
	return a, nil
}

func (s *caseSet) pkg(lang Lang, path string) (*PackageResult, error) {
	k := TestID{Lang: lang, Package: path}
	if p, ok := s.pkgs[k]; ok {
		return p, nil
	}
	if len(s.pkgs) >= s.lim.MaxPackages {
		return nil, &LimitError{Limit: "MaxPackages", Max: int64(s.lim.MaxPackages)}
	}
	p := &PackageResult{Lang: lang, Path: path}
	s.pkgs[k] = p
	s.porder = append(s.porder, k)
	return p, nil
}

// results finalises the cases: package statuses roll up from their cases
// unless already set.
func (s *caseSet) results(r *Results) {
	r.Cases = make([]Case, 0, len(s.order))
	pkgStatus := map[TestID]Status{}
	for _, id := range s.order {
		a := s.byID[id]
		c := a.c
		if c.FailedAttempts > 0 {
			c.Signature = FailureSignature(a.failOut)
			c.File, c.Line = locate(a.failOut)
			c.Output, c.OutputTruncated = s.budget.keep(a.failOut, a.failTrnc)
		}
		r.Cases = append(r.Cases, c)
		k := TestID{Lang: id.Lang, Package: id.Package}
		switch {
		case c.Status == StatusFail:
			pkgStatus[k] = StatusFail
		case c.Status == StatusPass && pkgStatus[k] != StatusFail:
			pkgStatus[k] = StatusPass
		case pkgStatus[k] == "":
			pkgStatus[k] = StatusSkip
		}
	}
	r.Packages = make([]PackageResult, 0, len(s.porder))
	for _, k := range s.porder {
		p := *s.pkgs[k]
		if p.Status == "" {
			p.Status = pkgStatus[k]
		}
		if p.Status == "" {
			p.Status = StatusSkip
		}
		r.Packages = append(r.Packages, p)
	}
}

func secondsToMS(s string) int64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(s, ",", "")), 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) || v > 1e9 {
		return 0
	}
	return int64(math.Round(v * 1000))
}

// parseJUnit reads JUnit XML: <testsuites>, or a bare <testsuite>, nested
// suites included. Repeated testcases with one id are attempts of one case
// (a rerunning runner's report). Surefire's flakyFailure / rerunFailure
// elements are attempts too.
func parseJUnit(br *bufio.Reader, opts ResultsOptions, lim Limits) (*Results, error) {
	d := xml.NewDecoder(br)
	d.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	set := newCaseSet(lim)
	r := &Results{}
	var suites []string
	sawRoot := false
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var le *LimitError
			if errors.As(err, &le) {
				return nil, le
			}
			return nil, fmt.Errorf("testreport: junit: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "testsuites":
				sawRoot = true
			case "testsuite":
				sawRoot = true
				suites = append(suites, attr(t, "name"))
			case "testcase":
				suite := ""
				if len(suites) > 0 {
					suite = suites[len(suites)-1]
				}
				if err := readJUnitCase(d, t, suite, opts, lim, set, r); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			if t.Name.Local == "testsuite" && len(suites) > 0 {
				suites = suites[:len(suites)-1]
			}
		}
	}
	if !sawRoot {
		return nil, ErrNoInput
	}
	set.results(r)
	return r, nil
}

func attr(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func junitLang(opts ResultsOptions, file, class string) Lang {
	if opts.Lang != "" {
		return opts.Lang
	}
	if file != "" {
		return LangFromPath(file)
	}
	if l := LangFromPath(class); l == LangGo || l == LangTS {
		return l
	}
	return LangOther
}

// readJUnitCase reads one <testcase> through its end element.
func readJUnitCase(d *xml.Decoder, start xml.StartElement, suite string, opts ResultsOptions, lim Limits, set *caseSet, r *Results) error {
	class, name, file := attr(start, "classname"), attr(start, "name"), attr(start, "file")
	pkgName := class
	if pkgName == "" {
		pkgName = suite
	}
	lang := junitLang(opts, file, class)
	pkgName = relativize(pkgName, opts.Root) // a JS runner's classname is often an absolute file path
	ms := secondsToMS(attr(start, "time"))
	status := StatusPass
	out := newBoundedText(lim.MaxOutputBytes)
	var sysOut []string
	extraFails, flakyPass := 0, false
	depth := 0
	var textInto *boundedText
	for {
		tok, err := d.Token()
		if err != nil {
			var le *LimitError
			if errors.As(err, &le) {
				return le
			}
			return fmt.Errorf("testreport: junit: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch t.Name.Local {
			case "failure", "error":
				status = StatusFail
				if m := attr(t, "message"); m != "" {
					out.write(m + "\n")
				}
				textInto = out
			case "flakyFailure", "flakyError":
				extraFails++
				flakyPass = true
			case "rerunFailure", "rerunError":
				extraFails++
			case "skipped":
				if status != StatusFail {
					status = StatusSkip
				}
			case "system-out", "system-err":
				textInto = newBoundedText(lim.MaxOutputBytes / 2)
				sysOut = append(sysOut, "")
			}
		case xml.CharData:
			if textInto != nil {
				textInto.write(string(t))
				if textInto != out && len(sysOut) > 0 {
					sysOut[len(sysOut)-1] = textInto.String()
				}
			}
		case xml.EndElement:
			if depth == 0 && t.Name.Local == "testcase" {
				return recordJUnitCase(set, r, lang, pkgName, name, file, status, ms, out, sysOut, extraFails, flakyPass)
			}
			depth--
			textInto = nil
		}
	}
}

func recordJUnitCase(set *caseSet, r *Results, lang Lang, pkgName, name, file string, status Status, ms int64, out *boundedText, sysOut []string, extraFails int, flakyPass bool) error {
	r.Stats.Events++
	if _, err := set.pkg(lang, pkgName); err != nil {
		return err
	}
	a, err := set.get(TestID{Lang: lang, Package: pkgName, Name: name})
	if err != nil {
		return err
	}
	for i := 0; i < extraFails; i++ {
		a.attempt(StatusFail, 0, out.String(), out.truncated())
	}
	if status == StatusFail {
		for _, s := range sysOut {
			if s != "" {
				out.write("\n" + s)
			}
		}
	}
	if flakyPass && status == StatusPass {
		a.c.RetryPassed = true
	}
	a.attempt(status, ms, out.String(), out.truncated())
	if file != "" && a.c.File == "" {
		a.c.File = file
	}
	return nil
}

// vitestDoc is the subset of the Vitest (and Jest) JSON reporter's document
// this parser reads.
type vitestDoc struct {
	NumTotalTests *int `json:"numTotalTests"`
	TestResults   []struct {
		Name             string `json:"name"`
		Status           string `json:"status"`
		Message          string `json:"message"`
		AssertionResults []struct {
			AncestorTitles  []string `json:"ancestorTitles"`
			Title           string   `json:"title"`
			Status          string   `json:"status"`
			Duration        *float64 `json:"duration"`
			FailureMessages []string `json:"failureMessages"`
		} `json:"assertionResults"`
	} `json:"testResults"`
}

func vitestStatus(s string) Status {
	switch s {
	case "passed":
		return StatusPass
	case "failed":
		return StatusFail
	}
	return StatusSkip // skipped, pending, todo, disabled
}

// parseVitest reads the Vitest JSON reporter's document. A case's name is its
// describe titles and its own title joined with " > "; its package is its
// test file, relative to opts.Root. Two cases with one name in one file are
// told apart with " #2", " #3": a JSON report lists each case once, so a
// repeat is a different case, not a retry.
func parseVitest(br *bufio.Reader, opts ResultsOptions, lim Limits) (*Results, error) {
	var doc vitestDoc
	dec := json.NewDecoder(br)
	if err := dec.Decode(&doc); err != nil {
		var le *LimitError
		if errors.As(err, &le) {
			return nil, le
		}
		if errors.Is(err, io.EOF) {
			return nil, ErrNoInput
		}
		return nil, fmt.Errorf("testreport: vitest: %w", err)
	}
	if doc.NumTotalTests == nil && doc.TestResults == nil {
		return nil, fmt.Errorf("testreport: vitest: no testResults: not a Vitest JSON report")
	}
	set := newCaseSet(lim)
	r := &Results{}
	for _, f := range doc.TestResults {
		file := relativize(f.Name, opts.Root)
		lang := opts.Lang
		if lang == "" {
			lang = LangFromPath(file)
		}
		p, err := set.pkg(lang, file)
		if err != nil {
			return nil, err
		}
		if f.Status == "failed" && len(f.AssertionResults) == 0 {
			// The file failed before any test ran (an import error).
			p.Status = StatusFail
			b := newBoundedText(lim.MaxOutputBytes)
			b.write(f.Message)
			p.Output = scrub(b.String())
			p.Signature = FailureSignature(f.Message)
		}
		seen := map[string]int{}
		for _, a := range f.AssertionResults {
			r.Stats.Events++
			name := strings.Join(append(append([]string(nil), a.AncestorTitles...), a.Title), " > ")
			seen[name]++
			if n := seen[name]; n > 1 {
				name = fmt.Sprintf("%s #%d", name, n)
			}
			acc, err := set.get(TestID{Lang: lang, Package: file, Name: name})
			if err != nil {
				return nil, err
			}
			out := newBoundedText(lim.MaxOutputBytes)
			for _, m := range a.FailureMessages {
				out.write(m + "\n")
			}
			ms := int64(0)
			if a.Duration != nil && *a.Duration > 0 && *a.Duration < 1e12 {
				ms = int64(math.Round(*a.Duration))
			}
			acc.attempt(vitestStatus(a.Status), ms, out.String(), out.truncated())
		}
	}
	set.results(r)
	return r, nil
}
