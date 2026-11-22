package cigate

import (
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// go test's failures, from its plain output or its -json event stream.
//
// A test log line names only a file's base name (`orders_test.go:9: msg`), so
// it waits in its package's testState until the package is known to have
// failed: in plain output that is the `FAIL <pkg>` summary line that ends the
// package's block; in -json it is the package's own terminal `fail` event.
//
// In -json, every event carries its Package, and an event's Test is the only
// thing that attributes output to a test. Output with no Test (TestMain, an
// init panic, a leak checker, `FAIL`, `exit status`) is the package's: it goes
// to the package's own buffer and is never assigned to a test, whichever test
// happens to be running or printed last.

var (
	reTestRun     = regexp.MustCompile(`^=== (?:RUN|CONT|NAME|PAUSE)\s+(\S+)`)
	reTestFail    = regexp.MustCompile(`^\s*--- FAIL: (\S+)`)
	reTestLog     = regexp.MustCompile(`^(\s+)([^\s/:]+\.go):(\d+): ?(.*)$`)
	rePkgFail     = regexp.MustCompile(`^FAIL\s+(\S+)(?:\s|$)`)
	rePkgOK       = regexp.MustCompile(`^ok\s+(\S+)\s`)
	reBuildHeader = regexp.MustCompile(`^# (\S+)`)
	reExitStatus  = regexp.MustCompile(`^exit status \d+$`)
)

// packageLogTail is how many of a package's own output lines a package-level
// finding carries.
const packageLogTail = 10

// testState is one package's go test output read so far.
type testState struct {
	// plain is true for plain output, where nothing attributes a panic to a
	// test or to the package, so the first panic stands for the package.
	plain bool

	test      string   // the test whose output is being read
	failed    []string // tests that failed, in order
	failedSet map[string]bool
	panics    map[string]string // each test's first panic line
	pending   []pendingLog
	// last indexes the pending log line continuation lines extend; -1 none.
	last   int
	indent int

	// The package's own output (-json events with no Test): the first panic,
	// its located diagnostics, and the tail of its log.
	panic    string
	diags    []Finding
	log      []string
	building bool // a `# <pkg>` build header opened the package's output
}

// pendingLog is a test's located line, waiting for its package to fail. A
// test log line names a base name in the package's directory (file); a
// located line a test printed has its project-relative path (rel).
type pendingLog struct {
	test, file, rel, msg string
	line, column         int
}

func newTestState() *testState { return &testState{last: -1} }

func newPlainState() *testState { return &testState{last: -1, plain: true} }

func (s *testState) setTest(name string) {
	if name != s.test {
		s.test, s.last = name, -1
	}
}

func (s *testState) fail(test string) {
	if s.failedSet == nil {
		s.failedSet = map[string]bool{}
	}
	if !s.failedSet[test] {
		s.failedSet[test] = true
		s.failed = append(s.failed, test)
	}
}

func (s *testState) recordPanic(line string) {
	if s.panics == nil {
		s.panics = map[string]string{}
	}
	if _, ok := s.panics[s.test]; !ok {
		s.panics[s.test] = line
	}
	if s.plain && s.panic == "" {
		s.panic = line
	}
	s.last = -1
}

// consume reads one line of a test's output, reporting whether it was go
// test's own (a test header, a test log line or its continuation, a panic).
func (s *testState) consume(line string) bool {
	if m := reTestRun.FindStringSubmatch(line); m != nil {
		s.setTest(m[1])
		return true
	}
	if m := reTestFail.FindStringSubmatch(line); m != nil {
		s.setTest(m[1])
		s.fail(m[1])
		return true
	}
	if strings.HasPrefix(line, "panic: ") {
		s.recordPanic(strings.TrimSpace(line))
		return true
	}
	if s.test == "" {
		return false
	}
	if m := reTestLog.FindStringSubmatch(line); m != nil {
		n, _ := strconv.Atoi(m[3])
		s.pending = append(s.pending, pendingLog{test: s.test, file: m[2], line: n, msg: strings.TrimSpace(m[4])})
		s.last, s.indent = len(s.pending)-1, len(m[1])
		return true
	}
	// A continuation of a multi-line t.Error message: indented deeper than
	// the log line it belongs to.
	if s.last >= 0 && strings.TrimSpace(line) != "" && leadingSpace(line) > s.indent {
		s.pending[s.last].msg += "\n" + strings.TrimSpace(line)
		return true
	}
	if strings.TrimSpace(line) != "" && leadingSpace(line) == 0 {
		s.last = -1
	}
	return false
}

// testLocated keeps a located line a test printed: it is a finding only if
// that test fails.
func (s *testState) testLocated(f Finding) {
	s.pending = append(s.pending, pendingLog{test: s.test, rel: f.Path, line: f.Line, column: f.Column, msg: f.Message})
	s.last = -1
}

// plainSummary ends the plain-output package block a `FAIL <pkg>` or
// `ok <pkg>` line closes, placing a failed package's findings.
func (p *findingParser) plainSummary(line string) bool {
	if m := rePkgFail.FindStringSubmatch(line); m != nil {
		p.flushPackage(m[1], p.plain, true)
	} else if !rePkgOK.MatchString(line) {
		return false
	}
	p.plain, p.header = newPlainState(), ""
	return true
}

// testEvent is the part of a `go test -json` event the parser reads.
type testEvent struct {
	Action      string `json:"Action"`
	Package     string `json:"Package"`
	Test        string `json:"Test"`
	Output      string `json:"Output"`
	ImportPath  string `json:"ImportPath"`  // build-output, build-fail
	FailedBuild string `json:"FailedBuild"` // a package's fail: the package whose build failed
}

// jsonLine reads one `go test -json` event. A line that is not one is plain
// output.
func (p *findingParser) jsonLine(line string) {
	var ev testEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Action == "" {
		p.line(line)
		return
	}
	switch ev.Action {
	case "build-output":
		for _, l := range eventLines(ev.Output) {
			p.addLocated(l, buildPackage(ev.ImportPath))
		}
	case "output":
		p.jsonOutput(ev)
	case "fail":
		p.jsonFail(ev)
	case "pass", "skip":
		if ev.Test == "" {
			delete(p.pkgs, ev.Package)
		}
	}
}

// jsonOutput reads an output event's lines: a test's when the event names its
// Test, the package's own otherwise.
func (p *findingParser) jsonOutput(ev testEvent) {
	st := p.state(ev.Package)
	for _, l := range eventLines(ev.Output) {
		if ev.Test == "" {
			p.packageLine(ev.Package, st, l)
			continue
		}
		st.setTest(ev.Test)
		if st.consume(l) {
			continue
		}
		if f, ok := p.located(l); ok {
			st.testLocated(f)
		}
	}
}

// packageLine keeps one line of the package's own output. A located line under
// a `# <pkg>` header is a build error (Go before 1.24 printed those as package
// output): a finding at once, and the package's build failed. Any other
// located line is a package diagnostic, placed when the package fails. The
// first panic and the tail of the log, minus go test's own summary lines,
// describe a failure nothing located.
func (p *findingParser) packageLine(pkg string, st *testState, line string) {
	trim := strings.TrimSpace(line)
	switch {
	case trim == "":
		return
	case reBuildHeader.MatchString(line):
		st.building = true
		return
	}
	if f, ok := p.located(line); ok {
		if st.building {
			p.add(f)
			p.buildErr[pkg] = true
		} else {
			st.diags = append(st.diags, f)
		}
		return
	}
	if strings.HasPrefix(trim, "panic: ") && st.panic == "" {
		st.panic = trim
		return
	}
	if trim == "PASS" || trim == "FAIL" || rePkgFail.MatchString(trim) || rePkgOK.MatchString(trim) || reExitStatus.MatchString(trim) {
		return
	}
	st.log = append(st.log, trim)
	if len(st.log) > packageLogTail {
		st.log = st.log[len(st.log)-packageLogTail:]
	}
}

// jsonFail records a failed test, or ends a failed package: its terminal fail
// event is what places its findings, whatever other packages printed between
// its events and whether or not a `FAIL <pkg>` line was printed.
func (p *findingParser) jsonFail(ev testEvent) {
	if ev.Package == "" {
		return
	}
	st := p.state(ev.Package)
	if ev.Test != "" {
		st.fail(ev.Test)
		return
	}
	if ev.FailedBuild != "" && p.buildErr[buildPackage(ev.FailedBuild)] {
		p.buildErr[ev.Package] = true
	}
	p.flushPackage(ev.Package, st, true)
	delete(p.pkgs, ev.Package)
}

func (p *findingParser) state(pkg string) *testState {
	st, ok := p.pkgs[pkg]
	if !ok {
		st = newTestState()
		p.pkgs[pkg] = st
	}
	return st
}

// flushUnfinished places what a -json stream cut short (a killed run, a
// truncated log) left: the failed tests of packages with no terminal event.
// Without that event nothing says the package itself failed, so neither its
// diagnostics nor a package-level finding are added. Plain output's pending
// block has no package line, so it cannot be placed.
func (p *findingParser) flushUnfinished() {
	pkgs := make([]string, 0, len(p.pkgs))
	for pkg := range p.pkgs {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)
	for _, pkg := range pkgs {
		p.flushPackage(pkg, p.pkgs[pkg], false)
	}
}

// flushPackage places a package's findings. Each failed test's located lines
// are findings; a failed test with none (nor a subtest with one) is a finding
// on the package directory, carrying its panic when there was one. When the
// package itself failed (pkgFailed), its own located diagnostics are findings
// too, and a package with no failed test, no diagnostic and no build error
// (a TestMain exit, a timeout, an init panic) is a finding on its directory
// with its panic and the tail of its log.
func (p *findingParser) flushPackage(pkg string, st *testState, pkgFailed bool) {
	dir, ok := p.pkgDir(pkg)
	if !ok {
		return
	}
	located := p.flushTests(dir, st)
	if !pkgFailed {
		return
	}
	for _, f := range st.diags {
		p.add(f)
	}
	switch {
	case len(st.failed) == 0 && len(st.diags) == 0 && !p.buildErr[pkg]:
		msg := pkg + " failed"
		if st.panic != "" {
			msg += ": " + st.panic
		}
		if len(st.log) > 0 {
			msg += "\n" + strings.Join(st.log, "\n")
		}
		p.add(Finding{Path: dir, Level: LevelError, Message: msg})
	case !st.plain && st.panic != "" && len(located) > 0:
		// Tests failed, and the package's own output also panicked (in
		// plain output that panic was the failing test's, already placed).
		p.add(Finding{Path: dir, Level: LevelError, Message: pkg + ": " + st.panic})
	}
}

// flushTests places the failed tests' findings in dir and returns the tests
// placed.
func (p *findingParser) flushTests(dir string, st *testState) map[string]bool {
	located := map[string]bool{}
	for _, l := range st.pending {
		// Under -v (and -json) a passing test's log is printed too.
		if !st.failedSet[l.test] {
			continue
		}
		located[l.test] = true
		f := Finding{Path: l.rel, Line: l.line, Column: l.column, Level: LevelError, Message: l.test + ": " + l.msg}
		if f.Path == "" {
			f.Path = path.Join(dir, l.file)
		}
		p.add(f)
	}
	for _, t := range st.failed {
		if located[t] || hasLocatedSubtest(located, t) {
			continue
		}
		msg := t + " failed"
		if pm := st.panics[t]; pm != "" {
			msg = t + ": " + pm
		}
		located[t] = true
		p.add(Finding{Path: dir, Level: LevelError, Message: msg})
	}
	return located
}

func hasLocatedSubtest(located map[string]bool, test string) bool {
	for t := range located {
		if strings.HasPrefix(t, test+"/") {
			return true
		}
	}
	return false
}

// pkgDir is the project-relative directory of an import path in the module.
func (p *findingParser) pkgDir(pkg string) (string, bool) {
	mod := p.in.Module
	switch {
	case mod == "":
		return "", false
	case pkg == mod:
		return ".", true
	case strings.HasPrefix(pkg, mod+"/"):
		return strings.TrimPrefix(pkg, mod+"/"), true
	}
	return "", false
}

// buildPackage is the package a build names: `pkg [pkg.test]` is pkg's test
// build.
func buildPackage(importPath string) string {
	if i := strings.IndexByte(importPath, ' '); i >= 0 {
		return importPath[:i]
	}
	return importPath
}

func eventLines(output string) []string {
	return strings.Split(strings.TrimRight(output, "\n"), "\n")
}
