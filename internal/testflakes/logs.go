package testflakes

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/testreport"
)

// Reading a red run's failing Go tests out of its logs: the port of
// flake-report.sh's attribution (control-plane's version, the superset of
// the three repos'). Attribution never depends on output order, because a
// sharded run can print every package summary BEFORE the failed tests'
// output. The rules, strongest first:
//
//  1. a `go test -json` stream names each failure's package and test outright;
//  2. the `FAIL <pkg> <Test>` annotation a CI shard prints (`::error::` /
//     `##[error]`), and the `(package <pkg>)` suffix a merge gate prints,
//     name both;
//  3. otherwise a `--- FAIL: <Test>` is attributed to the `FAIL <pkg>` line
//     that follows it IN THE SAME SOURCE (a log file, or one job's step in
//     `gh run view --log-failed`); one still unattributed when its source
//     ends is dropped, never carried into a later step's unrelated package.
//
// A source is the unit within which rule 3 may attribute: one step of the
// --log-failed output (the job and step names it prefixes each line with), or
// one *.log file of a log directory. *.json files of a log directory are read
// as `go test -json` streams.

var (
	ansiRe        = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	logTimeRe     = regexp.MustCompile(`^[0-9TZ:.-]+ ?`)
	annotationRe  = regexp.MustCompile(`^(::error::|##\[error\])FAIL [^ ]+ Test[A-Za-z0-9_]+$`)
	dashFailRe    = regexp.MustCompile(`^--- FAIL: Test[A-Za-z0-9_]+`)
	packageNoteRe = regexp.MustCompile(`\(package [^)]+\)`)
	failPkgRe     = regexp.MustCompile(`^FAIL\t[^ \t]+`)
	foreignJobRe  = regexp.MustCompile(`(?i)windows|macos|darwin`)
	buildTagRe    = regexp.MustCompile(`^//go:build ([A-Za-z0-9_]+)$`)
)

// logRecord is one log line and the source it came from.
type logRecord struct {
	src  string
	line string
}

// logEvent is the part of a `go test -json` event attribution reads.
type logEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
	Output  string `json:"Output"`
}

// runLogs is a red run's logs: the --log-failed output and a directory of
// step logs and artifacts.
type runLogs struct {
	records []logRecord
	events  []logEvent
}

// readRunLogs reads failedLog (`gh run view --log-failed` output, lines
// "<job>\t<step>\t<timestamp> <line>"; nil for none) and every *.log file
// and `go test -json` stream (*.json or *.jsonl) under dir ("" for none).
// Jobs on Windows or macOS are dropped: a failure there, rerun here, would
// "pass" and be misreported as a flake.
func readRunLogs(failedLog []byte, dir string) runLogs {
	var rl runLogs
	for _, raw := range splitLines(failedLog) {
		f := strings.SplitN(raw, "\t", 3)
		if len(f) < 3 {
			continue
		}
		if foreignJobRe.MatchString(f[0]) {
			continue
		}
		line := strings.TrimPrefix(f[2], "\ufeff")
		line = logTimeRe.ReplaceAllString(line, "")
		rl.records = append(rl.records, logRecord{src: f[0] + "\t" + f[1], line: ansiRe.ReplaceAllString(line, "")})
	}
	if dir == "" {
		return rl
	}
	var logs, streams []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		switch filepath.Ext(p) {
		case ".log":
			logs = append(logs, p)
		case ".json", ".jsonl":
			streams = append(streams, p)
		}
		return nil
	})
	sort.Strings(logs)
	sort.Strings(streams)
	for _, p := range logs {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, line := range splitLines(data) {
			rl.records = append(rl.records, logRecord{src: p, line: ansiRe.ReplaceAllString(line, "")})
		}
	}
	for _, p := range streams {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, line := range splitLines(data) {
			var ev logEvent
			if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &ev) == nil && ev.Action != "" {
				rl.events = append(rl.events, ev)
			}
		}
	}
	return rl
}

func splitLines(data []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		out = append(out, strings.TrimRight(sc.Text(), "\r"))
	}
	return out
}

// failing names every failing top-level test as (package, test), sorted and
// unique, by the three attribution rules. module qualifies a package printed
// relative to the module (`./internal/x` style names some gates print).
func (rl runLogs) failing(module string) []testreport.TestID {
	seen := map[testreport.TestID]bool{}
	add := func(pkg, test string) {
		id := testreport.TestID{Lang: testreport.LangGo, Package: pkg, Name: test}
		seen[id] = true
	}
	for _, ev := range rl.events {
		if ev.Action == "fail" && ev.Test != "" && !strings.Contains(ev.Test, "/") {
			add(ev.Package, ev.Test)
		}
	}
	qualify := func(p string) string {
		if module != "" && p != module && !strings.HasPrefix(p, module+"/") && !strings.Contains(strings.SplitN(p, "/", 2)[0], ".") {
			return module + "/" + p
		}
		return p
	}
	cur := ""
	pending := map[string]bool{}
	for _, r := range rl.records {
		if r.src != cur {
			pending, cur = map[string]bool{}, r.src
		}
		line := r.line
		switch {
		case annotationRe.MatchString(line):
			w := strings.Fields(line)
			add(qualify(w[1]), w[2])
			delete(pending, w[2])
		case dashFailRe.MatchString(line):
			w := strings.Fields(line)
			if m := packageNoteRe.FindString(line); m != "" {
				add(qualify(m[len("(package "):len(m)-1]), w[2])
			} else {
				pending[w[2]] = true
			}
		case failPkgRe.MatchString(line):
			f := strings.Split(line, "\t")
			for n := range pending {
				add(f[1], n)
			}
			pending = map[string]bool{}
		case strings.HasPrefix(line, "ok  \t"):
			pending = map[string]bool{}
		}
	}
	out := make([]testreport.TestID, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sortIDs(out)
	return out
}

var (
	blockEndCollectRe = regexp.MustCompile(`^(--- (PASS|SKIP): |ok  |PASS$)`)
	blockEndRe        = regexp.MustCompile(`^(--- |=== |FAIL|ok  |PASS)`)
)

// block is everything the run printed for test's failure: from its `=== RUN`
// (go test prints a race report and the "race detected" line BEFORE `---
// FAIL`), through `--- FAIL`, to the next test or summary; then the JSON
// streams' output for it and its subtests.
func (rl runLogs) block(test string) string {
	var b strings.Builder
	var buf strings.Builder
	collecting, on := false, false
	for _, r := range rl.records {
		line := r.line
		if line == "=== RUN   "+test {
			buf.Reset()
			collecting = true
		}
		if collecting {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
		if strings.HasPrefix(line, "--- FAIL: "+test+" ") || line == "--- FAIL: "+test {
			if collecting {
				b.WriteString(buf.String())
			} else {
				b.WriteString(line)
				b.WriteByte('\n')
			}
			collecting, on = false, true
			continue
		}
		if collecting && blockEndCollectRe.MatchString(line) {
			collecting = false
		}
		if on && blockEndRe.MatchString(line) {
			on = false
		}
		if on {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	for _, ev := range rl.events {
		if ev.Action == "output" && (ev.Test == test || strings.HasPrefix(ev.Test, test+"/")) {
			b.WriteString(ev.Output)
		}
	}
	return b.String()
}

// Excerpt is the first lines of a failure, for an issue body: 25 lines of at
// most 300 bytes each.
func Excerpt(output string) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) > 25 {
		lines = lines[:25]
	}
	for i, l := range lines {
		if len(l) > 300 {
			lines[i] = l[:300]
		}
	}
	return strings.Join(lines, "\n")
}

// maxStoredOutput bounds a failure's output in the record, as
// testreport.Limits bounds a case's: its first and last halves.
const maxStoredOutput = 8 << 10

func boundOutput(s string) (string, bool) {
	if len(s) <= maxStoredOutput {
		return s, false
	}
	half := maxStoredOutput / 2
	return s[:half] + "\n… [output truncated] …\n" + s[len(s)-half:], true
}

// logFailures are a red run's failing tests as cases, each carrying the full
// failure output (classification reads all of it) and its signature.
func logFailures(rl runLogs, module string) []testreport.Case {
	ids := rl.failing(module)
	out := make([]testreport.Case, 0, len(ids))
	for _, id := range ids {
		output := rl.block(id.Name)
		out = append(out, testreport.Case{
			ID: id, Status: testreport.StatusFail, Attempts: 1, FailedAttempts: 1,
			Output: output, Signature: testreport.FailureSignature(output),
		})
	}
	return out
}

// resultsFailures are a results document's failing top-level tests, one case
// each (a top-level test's failing subtests' output joined), plus each
// package that failed with no failing case to show for it.
func resultsFailures(r *testreport.Results) []testreport.Case {
	byTop := map[testreport.TestID]*testreport.Case{}
	var order []testreport.TestID
	for _, c := range r.Leaves() {
		if c.Status != testreport.StatusFail {
			continue
		}
		top := c.ID.TopLevel()
		f := byTop[top]
		if f == nil {
			f = &testreport.Case{ID: top, Status: testreport.StatusFail, Attempts: c.Attempts, FailedAttempts: c.FailedAttempts, File: c.File, Line: c.Line}
			byTop[top] = f
			order = append(order, top)
		}
		f.Output += c.Output
	}
	out := make([]testreport.Case, 0, len(order))
	for _, id := range order {
		f := byTop[id]
		f.Signature = testreport.FailureSignature(f.Output)
		out = append(out, *f)
	}
	for _, p := range r.Packages {
		if (p.Status == testreport.StatusFail || p.BuildFailed) && p.Failed == 0 {
			out = append(out, testreport.Case{ID: p.ID(), Status: testreport.StatusFail, Attempts: 1, FailedAttempts: 1, Output: p.Output, Signature: p.Signature})
		}
	}
	return out
}
