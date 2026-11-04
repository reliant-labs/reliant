// Copyright (c) 2025 Reliant Labs
package main

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

// goTestJSON is `go test -json` output for one package: a fast test, a slow
// one the diff edits, a slow one it does not, a failing one with log output,
// and a slow subtest (subtests are not budgeted: their parent is).
const goTestJSON = `{"Action":"start","Package":"example.com/m/p"}
{"Action":"run","Package":"example.com/m/p","Test":"TestFast"}
{"Action":"output","Package":"example.com/m/p","Test":"TestFast","Output":"=== RUN   TestFast\n"}
{"Action":"output","Package":"example.com/m/p","Test":"TestFast","Output":"    fast_test.go:9: a passing test's log\n"}
{"Action":"pass","Package":"example.com/m/p","Test":"TestFast","Elapsed":0.01}
{"Action":"run","Package":"example.com/m/p","Test":"TestMineSlow"}
{"Action":"pass","Package":"example.com/m/p","Test":"TestMineSlow","Elapsed":12.5}
{"Action":"run","Package":"example.com/m/p","Test":"TestTheirsSlow"}
{"Action":"pass","Package":"example.com/m/p","Test":"TestTheirsSlow","Elapsed":30}
{"Action":"run","Package":"example.com/m/p","Test":"TestParent/sub"}
{"Action":"pass","Package":"example.com/m/p","Test":"TestParent/sub","Elapsed":20}
{"Action":"pass","Package":"example.com/m/p","Test":"TestParent","Elapsed":0.2}
{"Action":"run","Package":"example.com/m/p","Test":"TestBroken"}
{"Action":"output","Package":"example.com/m/p","Test":"TestBroken","Output":"=== RUN   TestBroken\n"}
{"Action":"output","Package":"example.com/m/p","Test":"TestBroken","Output":"--- FAIL: TestBroken (0.00s)\n"}
{"Action":"output","Package":"example.com/m/p","Test":"TestBroken","Output":"    broken_test.go:5: boom\n"}
{"Action":"fail","Package":"example.com/m/p","Test":"TestBroken","Elapsed":0}
{"Action":"output","Package":"example.com/m/p","Output":"FAIL\n"}
{"Action":"output","Package":"example.com/m/p","Output":"FAIL\texample.com/m/p\t43.1s\n"}
{"Action":"fail","Package":"example.com/m/p","Elapsed":43.1}
go: some message that is not an event
`

func runFilter(t *testing.T, owned map[string]bool) (string, string, bool) {
	t.Helper()
	var out, report strings.Builder
	f := newBudgetFilter(10*time.Second, owned, &out)
	// Feed it in odd-sized chunks: a write need not end on a line.
	for b := []byte(goTestJSON); len(b) > 0; {
		n := min(37, len(b))
		if _, err := f.Write(b[:n]); err != nil {
			t.Fatal(err)
		}
		b = b[n:]
	}
	_ = f.Close()
	failed := f.verdict(&report)
	return out.String(), report.String(), failed
}

func TestBudgetFilterPrintsWhatPlainGoTestWould(t *testing.T) {
	out, _, _ := runFilter(t, nil)
	for _, want := range []string{"--- FAIL: TestBroken", "broken_test.go:5: boom", "FAIL\texample.com/m/p\t43.1s", "go: some message that is not an event"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, not := range []string{"=== RUN", "a passing test's log"} {
		if strings.Contains(out, not) {
			t.Errorf("output has %q, which plain go test does not print:\n%s", not, out)
		}
	}
}

// A package that dies with a test running (a panic, a timeout) reports no
// result for it: its output must still be printed.
func TestBudgetFilterPrintsAnUnfinishedTestsOutput(t *testing.T) {
	var out strings.Builder
	f := newBudgetFilter(time.Second, nil, &out)
	_, _ = io.WriteString(f, `{"Action":"run","Package":"m/p","Test":"TestHang"}
{"Action":"output","Package":"m/p","Test":"TestHang","Output":"panic: test timed out after 5m0s\n"}
{"Action":"output","Package":"m/p","Output":"FAIL\tm/p\t300.1s\n"}
{"Action":"fail","Package":"m/p","Elapsed":300.1}
{"Action":"run","Package":"m/q","Test":"TestCut"}
{"Action":"output","Package":"m/q","Test":"TestCut","Output":"last words\n"}
`)
	_ = f.Close()
	for _, want := range []string{"panic: test timed out", "FAIL\tm/p", "last words"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestBudgetFailsOnlyTheTestsTheChangeWrote(t *testing.T) {
	owned := ownedTests("example.com/m", []changedPkg{{Dir: "./p", Tests: []string{"TestMineSlow", "TestFast"}}})
	_, report, failed := runFilter(t, owned)
	if !failed {
		t.Fatalf("an edited test over budget must fail the step:\n%s", report)
	}
	if !strings.Contains(report, "TestMineSlow") || !strings.Contains(report, "testkit.Slow") {
		t.Errorf("the failure must name the test and the fix:\n%s", report)
	}
	if !strings.Contains(report, "over-budget example.com/m/p TestTheirsSlow") {
		t.Errorf("a test the change did not write is reported:\n%s", report)
	}
	if strings.Contains(report, "TestParent") {
		t.Errorf("subtests are budgeted through their parent, which was fast:\n%s", report)
	}

	_, report, failed = runFilter(t, nil)
	if failed {
		t.Fatalf("over-budget tests the change did not write are reported, not failed:\n%s", report)
	}
}

func TestLoadFactor(t *testing.T) {
	for _, c := range []struct {
		load float64
		cpus int
		want int
	}{{0, 32, 1}, {32, 32, 1}, {33, 32, 2}, {150, 32, 5}, {1000, 32, 6}, {1, 0, 1}} {
		if got := loadFactor(c.load, c.cpus); got != c.want {
			t.Errorf("loadFactor(%v, %d) = %d, want %d", c.load, c.cpus, got, c.want)
		}
	}
}

func TestRunnerGoStepUsesJSONOnlyUnderABudget(t *testing.T) {
	pl := &Plan{Go: GoPlan{Packages: []string{"./internal/leaf"}}}
	r := &runner{root: "/repo", out: io.Discard, errOut: io.Discard}
	s := r.goSteps(pl)
	if slices.Contains(s[0].argv, "-json") || s[0].budget != nil {
		t.Fatalf("no budget, plain go test: %v", s[0].argv)
	}
	r.budget = time.Second
	s = r.goSteps(pl)
	if s[0].argv[2] != "-json" || s[0].budget == nil || !slices.Contains(s[0].argv, "-short") {
		t.Fatalf("a budget runs go test -json -short through the filter: %v", s[0].argv)
	}
	if err := r.setBudget(context.Background(), "bogus", ""); err == nil {
		t.Fatal("-test-budget bogus must be refused")
	}
}
