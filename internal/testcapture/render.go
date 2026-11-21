package testcapture

import (
	"fmt"
	"io"
	"strings"
)

// Event is the part of a `go test -json` (test2json) event the capture reads.
// ImportPath is set instead of Package on cmd/go's build-output and build-fail
// events.
type Event struct {
	Action     string `json:"Action"`
	Package    string `json:"Package"`
	ImportPath string `json:"ImportPath"`
	Test       string `json:"Test"`
	Output     string `json:"Output"`
}

// maxTestBuffer bounds the output held for one running test. A passing test's
// output is dropped when it passes, so holding it at all is only for the case
// it fails; past this bound the rest is counted, not kept, and the rendered
// failure says where the whole stream is.
const maxTestBuffer = 1 << 20

// Renderer turns a `go test -json` stream back into the text `go test`
// prints, so a lane that asks for -json costs the human reading it nothing.
//
// Non-verbose (the default, what `go test` without -v prints): a passing or
// skipped test shows nothing, a failing one shows its "--- FAIL" line and its
// output with its failed subtests nested and indented beneath it, and every
// package-level line ("ok", "FAIL", a build error, a panic) is printed. A
// stream always carries -v framing (cmd/go runs the binary with
// -test.v=test2json), so the "=== RUN" lines are dropped and the result
// line is moved to the top of the test's block, where `go test` prints it.
//
// Verbose (the lane ran `go test -v`): every event's Output, in order — which
// is exactly what `go test -v` prints.
type Renderer struct {
	w       io.Writer
	verbose bool
	// open holds the tests that started and have not finished, by
	// package and name; order is their start order, for Close.
	open  map[testKey]*testBlock
	order []testKey
}

type testKey struct{ pkg, test string }

type testBlock struct {
	// result is the test's own "--- FAIL:" (or PASS/SKIP) line.
	result  string
	lines   []string
	size    int
	omitted int
}

func (b *testBlock) add(line string) {
	if b.size+len(line) > maxTestBuffer {
		b.omitted += len(line)
		return
	}
	b.size += len(line)
	b.lines = append(b.lines, line)
}

// NewRenderer renders to w. verbose is `go test -v`'s rendering.
func NewRenderer(w io.Writer, verbose bool) *Renderer {
	return &Renderer{w: w, verbose: verbose, open: map[testKey]*testBlock{}}
}

// Text prints a line that is not an event (a wrapper's output, a build error
// on stdout) verbatim.
func (r *Renderer) Text(line string) { _, _ = io.WriteString(r.w, line) }

// Event renders one event.
func (r *Renderer) Event(e Event) {
	if r.verbose {
		r.Text(e.Output)
		return
	}
	pkg := e.Package
	if pkg == "" {
		pkg = e.ImportPath
	}
	if e.Test == "" {
		r.packageEvent(pkg, e)
		return
	}
	k := testKey{pkg, e.Test}
	switch e.Action {
	case "run":
		r.block(k)
	case "output":
		b := r.block(k)
		for _, line := range splitLines(e.Output) {
			switch trimmed := strings.TrimLeft(line, " \t"); {
			case isFraming(trimmed):
				// -v's own framing, which `go test` never prints.
			case isResultLine(trimmed):
				b.result = trimmed
			default:
				b.add(line)
			}
		}
	case "fail", "bench":
		r.finish(k, true)
	case "pass", "skip":
		r.finish(k, false)
	}
}

func (r *Renderer) packageEvent(pkg string, e Event) {
	switch e.Action {
	case "output", "build-output":
		// A passing package's bare "PASS" is the binary's line, which
		// `go test` prints only under -v; its "ok" line follows.
		if e.Output != "PASS\n" {
			r.Text(e.Output)
		}
	case "fail":
		// The binary died with tests still running (a panic, a
		// timeout): their output is the evidence, so it is printed
		// before the package's verdict lines arrive.
		r.flushPackage(pkg, true)
	case "pass", "skip":
		r.flushPackage(pkg, false)
	}
}

func (r *Renderer) block(k testKey) *testBlock {
	b, ok := r.open[k]
	if !ok {
		b = &testBlock{}
		r.open[k] = b
		r.order = append(r.order, k)
	}
	return b
}

// finish ends test k. A failure's block is printed, or, for a subtest whose
// parent is still running, nested in the parent's block four spaces in — the
// shape `go test` prints a failed subtest in.
func (r *Renderer) finish(k testKey, show bool) {
	b, ok := r.open[k]
	if !ok {
		b = &testBlock{}
	}
	delete(r.open, k)
	if !show {
		return
	}
	lines := b.render()
	if i := strings.LastIndexByte(k.test, '/'); i > 0 {
		if parent, ok := r.open[testKey{k.pkg, k.test[:i]}]; ok {
			for _, line := range lines {
				parent.add("    " + line)
			}
			return
		}
	}
	for _, line := range lines {
		r.Text(line)
	}
}

func (b *testBlock) render() []string {
	var out []string
	if b.result != "" {
		out = append(out, b.result)
	}
	out = append(out, b.lines...)
	if b.omitted > 0 {
		out = append(out, fmt.Sprintf("    ... %d more bytes of output not shown here (the whole stream is in the lane's results.json)\n", b.omitted))
	}
	return out
}

// flushPackage ends every open test of pkg, printing each when show is set.
func (r *Renderer) flushPackage(pkg string, show bool) {
	for _, k := range r.order {
		if k.pkg == pkg {
			if _, open := r.open[k]; open {
				r.finishTree(k, show)
			}
		}
	}
	r.compact()
}

// finishTree ends k after any of its subtests still open, so their output
// nests under it.
func (r *Renderer) finishTree(k testKey, show bool) {
	for i := len(r.order) - 1; i >= 0; i-- {
		c := r.order[i]
		if c.pkg == k.pkg && strings.HasPrefix(c.test, k.test+"/") {
			if _, open := r.open[c]; open {
				r.finish(c, show)
			}
		}
	}
	if _, open := r.open[k]; open {
		r.finish(k, show)
	}
}

func (r *Renderer) compact() {
	kept := r.order[:0]
	for _, k := range r.order {
		if _, open := r.open[k]; open {
			kept = append(kept, k)
		}
	}
	r.order = kept
}

// Close prints what the stream left unfinished: tests that never reported a
// result are the ones a crash or a cut-off stream was in the middle of.
func (r *Renderer) Close() {
	for _, k := range r.order {
		if _, open := r.open[k]; open && !strings.Contains(k.test, "/") {
			r.finishTree(k, true)
		}
	}
	for _, k := range r.order {
		if _, open := r.open[k]; open {
			r.finish(k, true)
		}
	}
	r.compact()
}

// isFraming reports the -v lines `go test` without -v never prints.
func isFraming(trimmed string) bool {
	for _, p := range []string{"=== RUN", "=== PAUSE", "=== CONT", "=== NAME"} {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	return false
}

// isResultLine reports a test's own result line.
func isResultLine(trimmed string) bool {
	for _, p := range []string{"--- FAIL: ", "--- PASS: ", "--- SKIP: ", "--- BENCH: "} {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	return false
}

// splitLines splits s after each newline, keeping it.
func splitLines(s string) []string {
	var out []string
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}
