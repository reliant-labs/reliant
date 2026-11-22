package testcapture

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// testdata/go-test.jsonl is a real `go test -json ./...` stream (Go 1.27) of
// two packages: one with a passing, a skipped, a failing (with a failing and
// a passing subtest) and a failing parallel test, one with a passing test
// that prints to stdout. Times are zeroed.
func readFixture(t *testing.T) []Event {
	t.Helper()
	f, err := os.Open("testdata/go-test.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var evs []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		evs = append(evs, e)
	}
	return evs
}

func renderAll(evs []Event, verbose bool) string {
	var b strings.Builder
	r := NewRenderer(&b, verbose)
	for _, e := range evs {
		r.Event(e)
	}
	r.Close()
	return b.String()
}

// What `go test ./...` printed for the same packages, package by package
// (its last line, the overall FAIL, is cmd/go's own and is in no stream).
func TestRendererPrintsWhatGoTestPrints(t *testing.T) {
	t.Parallel()
	want := "ok  \texample.com/x/b\t(cached)\n" +
		"--- FAIL: TestFail (0.00s)\n" +
		"    --- FAIL: TestFail/sub (0.00s)\n" +
		"        a_test.go:5: boom\n" +
		"    a_test.go:5: parent log\n" +
		"--- FAIL: TestPar (0.00s)\n" +
		"    a_test.go:6: par\n" +
		"FAIL\n" +
		"FAIL\texample.com/x/a\t0.00s\n"
	if got := renderAll(readFixture(t), false); got != want {
		t.Fatalf("rendered:\n%s\nwant:\n%s", got, want)
	}
}

func TestRendererVerbosePrintsEveryOutput(t *testing.T) {
	t.Parallel()
	evs := readFixture(t)
	var want strings.Builder
	for _, e := range evs {
		want.WriteString(e.Output)
	}
	if got := renderAll(evs, true); got != want.String() {
		t.Fatalf("verbose:\n%s\nwant:\n%s", got, want.String())
	}
}

// A binary that dies mid-test (a panic, a timeout) leaves tests with no
// result: their output is what explains it, so it is printed.
func TestRendererPrintsTestsACrashLeftRunning(t *testing.T) {
	t.Parallel()
	pkg := "example.com/x/c"
	evs := []Event{
		{Action: "run", Package: pkg, Test: "TestHang"},
		{Action: "output", Package: pkg, Test: "TestHang", Output: "=== RUN   TestHang\n"},
		{Action: "output", Package: pkg, Test: "TestHang", Output: "    c_test.go:9: waiting\n"},
		{Action: "output", Package: pkg, Output: "panic: test timed out after 1s\n"},
		{Action: "fail", Package: pkg},
		{Action: "run", Package: pkg, Test: "TestCut"},
		{Action: "output", Package: pkg, Test: "TestCut", Output: "    c_test.go:12: the stream ended here\n"},
	}
	want := "panic: test timed out after 1s\n" +
		"    c_test.go:9: waiting\n" +
		"    c_test.go:12: the stream ended here\n"
	if got := renderAll(evs, false); got != want {
		t.Fatalf("rendered:\n%q\nwant:\n%q", got, want)
	}
}

func TestRendererBoundsAPassingTestsBuffer(t *testing.T) {
	t.Parallel()
	pkg := "example.com/x/d"
	line := strings.Repeat("x", 1023) + "\n"
	evs := []Event{{Action: "run", Package: pkg, Test: "TestBig"}}
	for range 2*maxTestBuffer/len(line) + 1 {
		evs = append(evs, Event{Action: "output", Package: pkg, Test: "TestBig", Output: line})
	}
	evs = append(evs, Event{Action: "output", Package: pkg, Test: "TestBig", Output: "--- FAIL: TestBig (0.00s)\n"},
		Event{Action: "fail", Package: pkg, Test: "TestBig"})
	got := renderAll(evs, false)
	if !strings.HasPrefix(got, "--- FAIL: TestBig (0.00s)\n") {
		t.Fatalf("the result line is not first: %.80q", got)
	}
	if len(got) > maxTestBuffer+1024 || !strings.Contains(got, "more bytes of output not shown here") {
		t.Fatalf("rendered %d bytes without saying what was left out", len(got))
	}
}
