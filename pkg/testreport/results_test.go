package testreport_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testreport"
)

func TestTestIDRoundTrip(t *testing.T) {
	cases := []struct {
		id   testreport.TestID
		want string
	}{
		{testreport.TestID{Lang: "go", Package: "github.com/acme/shop/internal/billing", Name: "TestRefund/partial"}, "go:github.com/acme/shop/internal/billing:TestRefund/partial"},
		{testreport.TestID{Lang: "ts", Package: "src/a:b%c.test.ts", Name: "suite > it: works"}, "ts:src/a%3Ab%25c.test.ts:suite > it: works"},
		{testreport.TestID{Lang: "go", Package: "example.com/x"}, "go:example.com/x:"},
	}
	for _, c := range cases {
		if got := c.id.String(); got != c.want {
			t.Errorf("String() = %q, want %q", got, c.want)
		}
		back, err := testreport.ParseTestID(c.want)
		if err != nil || back != c.id {
			t.Errorf("ParseTestID(%q) = %+v, %v; want %+v", c.want, back, err, c.id)
		}
		b, _ := json.Marshal(c.id)
		var dec testreport.TestID
		if err := json.Unmarshal(b, &dec); err != nil || dec != c.id {
			t.Errorf("JSON round trip of %q: %+v, %v", b, dec, err)
		}
	}
	for _, bad := range []string{"", "go", ":pkg:name", "go:pkgonly"} {
		if _, err := testreport.ParseTestID(bad); err == nil {
			t.Errorf("ParseTestID(%q) succeeded", bad)
		}
	}
	sub := testreport.TestID{Lang: "go", Package: "p", Name: "TestA/b/c"}
	if top := sub.TopLevel(); top.Name != "TestA" || !sub.IsSubtest() || top.IsSubtest() {
		t.Errorf("TopLevel(%v) = %v", sub, top)
	}
	ts := testreport.TestID{Lang: "ts", Package: "a.test.ts", Name: "x/y"}
	if ts.TopLevel() != ts || ts.IsSubtest() {
		t.Errorf("a TS name has no subtest path: %v", ts.TopLevel())
	}
}

func TestLangFromPath(t *testing.T) {
	for in, want := range map[string]testreport.Lang{
		"a/b.go": "go", "src/x.tsx": "ts", "src/y.mjs": "ts", "app.py": "py", "Makefile": "other", `src\win.ts`: "ts",
	} {
		if got := testreport.LangFromPath(in); got != want {
			t.Errorf("LangFromPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetectFormat(t *testing.T) {
	cases := map[string]testreport.Format{
		`{"Time":"x","Action":"start","Package":"p"}`:                 testreport.FormatGoJSON,
		"go: downloading x\n{\"Action\":\"run\",\"Package\":\"p\"}\n": testreport.FormatGoJSON,
		`<?xml version="1.0"?><testsuites><testsuite name="a">`:       testreport.FormatJUnit,
		`<testsuite name="a">`:                                        testreport.FormatJUnit,
		`<?xml version="1.0"?><coverage line-rate="0.5">`:             testreport.FormatCobertura,
		`{"numTotalTests":3,"testResults":[]}`:                        testreport.FormatVitest,
		`{"/a/b.ts":{"path":"/a/b.ts","statementMap":{}}}`:            testreport.FormatIstanbul,
		"mode: atomic\na/b.go:1.1,2.2 1 0\n":                          testreport.FormatGoCover,
		"TN:\nSF:/a/b.ts\nDA:1,1\n":                                   testreport.FormatLCOV,
		"\ufeffSF:/a/b.ts\n":                                          testreport.FormatLCOV,
		"hello world":                                                 testreport.FormatUnknown,
	}
	for in, want := range cases {
		if got := testreport.DetectFormat([]byte(in)); got != want {
			t.Errorf("DetectFormat(%q) = %q, want %q", in, got, want)
		}
	}
}

func parseResults(t *testing.T, path string, opts testreport.ResultsOptions) *testreport.Results {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := testreport.ParseResults(f, opts)
	if err != nil {
		t.Fatalf("ParseResults(%s): %v", path, err)
	}
	return r
}

func findCase(t *testing.T, r *testreport.Results, id string) testreport.Case {
	t.Helper()
	for _, c := range r.Cases {
		if c.ID.String() == id {
			return c
		}
	}
	t.Fatalf("no case %s", id)
	return testreport.Case{}
}

func findPkg(t *testing.T, r *testreport.Results, path string) testreport.PackageResult {
	t.Helper()
	for _, p := range r.Packages {
		if p.Path == path {
			return p
		}
	}
	t.Fatalf("no package %s", path)
	return testreport.PackageResult{}
}

// testdata/results/go-test.jsonl is a real `go test -json -count=2 ./...` of a
// module with a passing, a skipping, a failing and a flaky test, a table test
// with subtests, a package that does not build, and one with no tests.
func TestParseGoJSON_RealStream(t *testing.T) {
	r := parseResults(t, "testdata/results/go-test.jsonl", testreport.ResultsOptions{Lane: testreport.LaneUnit})
	if r.Schema != testreport.SchemaResults || r.Format != testreport.FormatGoJSON || r.Lane != "unit" || r.OK || r.ExitCode != 1 {
		t.Errorf("head = %+v format %s lane %s", r.Head, r.Format, r.Lane)
	}
	const ok = "go:example.com/res/ok:"
	flaky := findCase(t, r, ok+"TestFlaky")
	if flaky.Status != testreport.StatusPass || flaky.Attempts != 2 || flaky.FailedAttempts != 1 || !flaky.RetryPassed {
		t.Errorf("TestFlaky = %+v; want pass after one failed attempt", flaky)
	}
	if !strings.Contains(flaky.Output, "first attempt fails") || flaky.Signature == "" {
		t.Errorf("TestFlaky keeps its failure output: %q sig %q", flaky.Output, flaky.Signature)
	}
	fail := findCase(t, r, ok+"TestFail")
	if fail.Status != testreport.StatusFail || fail.Attempts != 2 || fail.FailedAttempts != 2 || fail.RetryPassed {
		t.Errorf("TestFail = %+v", fail)
	}
	if fail.File != "ok_test.go" || fail.Line != 22 || !strings.Contains(fail.Output, "got 3, want 4") {
		t.Errorf("TestFail location %s:%d output %q", fail.File, fail.Line, fail.Output)
	}
	if strings.Contains(fail.Output, "=== RUN") {
		t.Errorf("run markers are not failure output: %q", fail.Output)
	}
	if skip := findCase(t, r, ok+"TestSkip"); skip.Status != testreport.StatusSkip || skip.SkipReason != "needs DATABASE_URL" {
		t.Errorf("TestSkip = %+v", skip)
	}
	if b := findCase(t, r, ok+"TestTable/b"); b.Status != testreport.StatusSkip || b.SkipReason != "b is not ready" {
		t.Errorf("TestTable/b = %+v", b)
	}
	okPkg := findPkg(t, r, "example.com/res/ok")
	// Leaves: Pass, Skip, Fail, Flaky, Table/a, Table/b. TestTable is a container.
	if okPkg.Tests != 6 || okPkg.Passed != 3 || okPkg.Failed != 1 || okPkg.Skipped != 2 || okPkg.Status != testreport.StatusFail {
		t.Errorf("ok package = %+v", okPkg)
	}
	broken := findPkg(t, r, "example.com/res/broken")
	if !broken.BuildFailed || broken.BuildID != "example.com/res/broken [example.com/res/broken.test]" || !strings.Contains(broken.Output, "cannot use") || broken.Incomplete {
		t.Errorf("broken package = %+v", broken)
	}
	if nt := findPkg(t, r, "example.com/res/notests"); !nt.NoTestFiles {
		t.Errorf("notests package = %+v", nt)
	}
	for _, p := range r.Packages {
		if strings.Contains(p.Path, " [") {
			t.Errorf("a test variant's name is folded into its package, not listed: %q", p.Path)
		}
	}
	want := testreport.Totals{Passed: 3, Failed: 1, Skipped: 2, FlakyRetryPass: 1, PackagesFailed: 1}
	if r.Totals != want {
		t.Errorf("totals = %+v, want %+v", r.Totals, want)
	}
	outs := r.Outcomes("c1", "run1")
	var flakyRows []testreport.Status
	for _, o := range outs {
		if o.Test.Name == "TestFlaky" {
			flakyRows = append(flakyRows, o.Status)
		}
	}
	if len(flakyRows) != 2 || flakyRows[0] != testreport.StatusFail || flakyRows[1] != testreport.StatusPass {
		t.Errorf("TestFlaky outcomes = %v", flakyRows)
	}
}

func TestParseGoJSON_TruncatedStreamIsIncomplete(t *testing.T) {
	in := `{"Action":"start","Package":"p"}
{"Action":"run","Package":"p","Test":"TestA"}
{"Action":"pass","Package":"p","Test":"TestA","Elapsed":0.5}
{"Action":"run","Package":"p","Test":"TestB"}
`
	r, err := testreport.ParseResults(strings.NewReader(in), testreport.ResultsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p := r.Packages[0]
	if !p.Incomplete || p.Tests != 1 || len(r.Cases) != 1 || r.Cases[0].DurationMS != 500 {
		t.Errorf("truncated: pkg %+v cases %+v", p, r.Cases)
	}
}

func TestParseResults_Errors(t *testing.T) {
	if _, err := testreport.ParseResults(strings.NewReader(""), testreport.ResultsOptions{}); !errors.Is(err, testreport.ErrNoInput) {
		t.Errorf("empty input: %v", err)
	}
	if _, err := testreport.ParseResults(strings.NewReader(""), testreport.ResultsOptions{Format: testreport.FormatGoJSON}); !errors.Is(err, testreport.ErrNoInput) {
		t.Errorf("empty go-json input: %v", err)
	}
	if _, err := testreport.ParseResults(strings.NewReader("just text"), testreport.ResultsOptions{}); err == nil {
		t.Error("unrecognised input parsed")
	}
	if _, err := testreport.ParseResults(strings.NewReader("mode: set\n"), testreport.ResultsOptions{}); err == nil {
		t.Error("a coverage profile parsed as results")
	}
}

func TestParseResults_Limits(t *testing.T) {
	var b strings.Builder
	for i := range 50 {
		b.WriteString(`{"Action":"pass","Package":"p","Test":"T` + string(rune('A'+i%26)) + strings.Repeat("x", i) + `"}` + "\n")
	}
	stream := b.String()
	var le *testreport.LimitError
	_, err := testreport.ParseResults(strings.NewReader(stream), testreport.ResultsOptions{Limits: testreport.Limits{MaxCases: 10}})
	if !errors.As(err, &le) || le.Limit != "MaxCases" {
		t.Errorf("MaxCases: %v", err)
	}
	_, err = testreport.ParseResults(strings.NewReader(stream), testreport.ResultsOptions{Limits: testreport.Limits{MaxInputBytes: 100}})
	if !errors.As(err, &le) || le.Limit != "MaxInputBytes" {
		t.Errorf("MaxInputBytes: %v", err)
	}
	exact := `{"Action":"pass","Package":"p"}` + "\n"
	if _, err := testreport.ParseResults(strings.NewReader(exact), testreport.ResultsOptions{Limits: testreport.Limits{MaxInputBytes: int64(len(exact))}}); err != nil {
		t.Errorf("input of exactly MaxInputBytes: %v", err)
	}
	long := `{"Action":"pass","Package":"p","Test":"TestA","Output":"` + strings.Repeat("y", 5000) + `"}` + "\n" + `{"Action":"pass","Package":"p"}` + "\n"
	r, err := testreport.ParseResults(strings.NewReader(long), testreport.ResultsOptions{Format: testreport.FormatGoJSON, Limits: testreport.Limits{MaxLineBytes: 1000}})
	if err != nil || r.Stats.Malformed != 1 || r.Stats.Events != 1 {
		t.Errorf("an over-long line is malformed, not fatal: %+v %v", r, err)
	}
}

func TestParseGoJSON_OutputIsBoundedHeadAndTail(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"Action":"run","Package":"p","Test":"TestBig"}` + "\n")
	b.WriteString(`{"Action":"output","Package":"p","Test":"TestBig","Output":"ASSERTION at the start\n"}` + "\n")
	for range 2000 {
		b.WriteString(`{"Action":"output","Package":"p","Test":"TestBig","Output":"noise noise noise noise\n"}` + "\n")
	}
	b.WriteString(`{"Action":"output","Package":"p","Test":"TestBig","Output":"PANIC at the end token=ghp_abcdefghijklmnopqrstuvwxyz0123\n"}` + "\n")
	b.WriteString(`{"Action":"fail","Package":"p","Test":"TestBig"}` + "\n")
	b.WriteString(`{"Action":"fail","Package":"p"}` + "\n")
	r, err := testreport.ParseResults(strings.NewReader(b.String()), testreport.ResultsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := r.Cases[0]
	if !c.OutputTruncated || len(c.Output) > 8<<10+64 {
		t.Errorf("output not bounded: %d bytes, truncated %v", len(c.Output), c.OutputTruncated)
	}
	if !strings.Contains(c.Output, "ASSERTION at the start") || !strings.Contains(c.Output, "PANIC at the end") || !strings.Contains(c.Output, "[output truncated]") {
		t.Errorf("head and tail kept: %q…", c.Output[:80])
	}
	if strings.Contains(c.Output, "ghp_abcdefghij") {
		t.Error("a credential shape survived scrubbing")
	}
}

func TestParseJUnit(t *testing.T) {
	r := parseResults(t, "testdata/results/junit.xml", testreport.ResultsOptions{Lane: "py", Root: "/work/repo"})
	if r.Format != testreport.FormatJUnit {
		t.Fatalf("format %s", r.Format)
	}
	flaky := findCase(t, r, "other:tests.test_api.TestAPI:test_flaky")
	if flaky.Status != testreport.StatusPass || flaky.Attempts != 2 || !flaky.RetryPassed {
		t.Errorf("surefire flakyFailure = %+v", flaky)
	}
	rerun := findCase(t, r, "other:tests.test_api.TestAPI:test_rerun")
	if rerun.Status != testreport.StatusPass || rerun.Attempts != 2 || rerun.FailedAttempts != 1 || !rerun.RetryPassed || !strings.Contains(rerun.Output, "boom") {
		t.Errorf("repeated testcase = %+v", rerun)
	}
	fail := findCase(t, r, "other:tests.test_api.TestAPI:test_fail")
	if fail.Status != testreport.StatusFail || !strings.Contains(fail.Output, "AssertionError: 1 != 2") || !strings.Contains(fail.Output, "captured stdout") || fail.DurationMS != 1500 {
		t.Errorf("failure = %+v", fail)
	}
	if s := findCase(t, r, "other:tests.test_api.TestAPI:test_skip"); s.Status != testreport.StatusSkip {
		t.Errorf("skip = %+v", s)
	}
	ts := findCase(t, r, "ts:src/math.test.ts:adds")
	if ts.Status != testreport.StatusPass || ts.File != "/work/repo/src/math.test.ts" {
		t.Errorf("a suite-named case with a file: %+v", ts)
	}
	if r.Totals.Failed != 1 || r.Totals.Passed != 3 || r.Totals.Skipped != 1 || r.Totals.FlakyRetryPass != 2 {
		t.Errorf("totals %+v", r.Totals)
	}
	if p := findPkg(t, r, "tests.test_api.TestAPI"); p.Status != testreport.StatusFail || p.Tests != 4 {
		t.Errorf("package %+v", p)
	}
}

func TestParseVitest(t *testing.T) {
	r := parseResults(t, "testdata/results/vitest.json", testreport.ResultsOptions{Lane: "frontend", Root: "/work/repo/frontends/web"})
	if r.Format != testreport.FormatVitest {
		t.Fatalf("format %s", r.Format)
	}
	c := findCase(t, r, "ts:src/lib/money.test.ts:money > formats > cents")
	if c.Status != testreport.StatusPass || c.DurationMS != 3 {
		t.Errorf("pass case %+v", c)
	}
	f := findCase(t, r, "ts:src/lib/money.test.ts:money > rounds")
	if f.Status != testreport.StatusFail || !strings.Contains(f.Output, "expected 2 to be 3") {
		t.Errorf("fail case %+v", f)
	}
	if d := findCase(t, r, "ts:src/lib/money.test.ts:money > rounds #2"); d.Status != testreport.StatusPass {
		t.Errorf("a repeated name is a second case, not a retry: %+v", d)
	}
	if s := findCase(t, r, "ts:src/lib/money.test.ts:todo later"); s.Status != testreport.StatusSkip {
		t.Errorf("todo is a skip: %+v", s)
	}
	broken := findPkg(t, r, "src/broken.test.ts")
	if broken.Status != testreport.StatusFail || !strings.Contains(broken.Output, "Cannot find module") {
		t.Errorf("a file that failed before its tests ran: %+v", broken)
	}
	if r.Totals.PackagesFailed != 1 || r.ExitCode != 1 {
		t.Errorf("totals %+v head %+v", r.Totals, r.Head)
	}
}
