package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testreport"
)

const covACoverSrc = "package a\n\nfunc F(x int) int {\n\tif x > 0 {\n\t\treturn 1\n\t}\n\treturn 2\n}\n"

// covACoverProject is a recorded run of trCommit whose unit lane covers a/a.go's
// first branch (lines 3-5) and misses the fallthrough (lines 6-7). Git is
// answered by a fake that also knows one base commit, whose diff against the
// run's commit changed lines 4 and 7 of a/a.go.
func covACoverProject(t *testing.T, baseRev string) {
	t.Helper()
	root := trProject(t, trStream("pass"), 0)
	trWrite(t, filepath.Join(root, "a/a.go"), covACoverSrc)
	run := filepath.Join(testreport.RunsRoot(root), "01RUN")
	trWrite(t, filepath.Join(run, "unit", "cover.out"),
		"mode: atomic\nexample.com/m/a/a.go:3.22,5.3 2 1\nexample.com/m/a/a.go:6.3,7.14 2 0\n")
	blob := testreport.GitBlobID([]byte(covACoverSrc))
	const baseSHA = "1111111111111111111111111111111111111111"
	prev := testGit
	testGit = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch {
		case args[0] == "rev-parse" && args[1] == "--show-toplevel":
			return []byte(root + "\n"), nil
		case args[0] == "ls-tree":
			return []byte("100644 blob " + blob + "\ta/a.go\x00"), nil
		case args[0] == "rev-parse" && strings.HasPrefix(args[len(args)-1], baseRev+"^"):
			return []byte(baseSHA + "\n"), nil
		case args[0] == "merge-base":
			return []byte(baseSHA + "\n"), nil
		case args[0] == "diff" && strings.Contains(strings.Join(args, " "), baseSHA):
			return []byte("diff --git a/a/a.go b/a/a.go\n--- a/a/a.go\n+++ b/a/a.go\n@@ -4,0 +4,1 @@\n+\tif x > 0 {\n@@ -7,0 +7,1 @@\n+\treturn 2\n"), nil
		}
		return nil, fmt.Errorf("fake git: no %v", args)
	}
	t.Cleanup(func() { testGit = prev })
}

func covADecodeCoverage(t *testing.T, out string) testCoverageDoc {
	t.Helper()
	var doc testCoverageDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not a coverage document: %v\n%s", err, out)
	}
	return doc
}

func TestTestCoverageTextReport(t *testing.T) {
	covACoverProject(t, "origin/main")
	out, code := trRun(t, newTestCoverageCmd())
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, want := range []string{
		"forge test coverage @ abcdefabcdef",
		"Runs: the newest run's commit",
		"Combined: 50.0% of 4 statements · unit 50.0%",
		"cover  statements  package",
		"50.0%           4  a\n",
		"next: reliant forge test coverage a --by file",
		"next: reliant forge test coverage --diff origin/main",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "(dirty tree)") || strings.Contains(out, "Diff vs") {
		t.Errorf("a clean run with no --diff must show neither:\n%s", out)
	}
}

func TestTestCoverageByFileNarrowedToAPathAndLane(t *testing.T) {
	covACoverProject(t, "origin/main")
	out, code := trRun(t, newTestCoverageCmd(), "a/a.go", "--by", "file", "--lane", "unit")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, want := range []string{"forge test coverage — a/a.go", ", lane unit", "statements  file", "a/a.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "--by file") {
		t.Errorf("--by file must not suggest itself:\n%s", out)
	}
}

func TestTestCoverageDiffShowsTheChangedLinesNoTestReached(t *testing.T) {
	covACoverProject(t, "origin/main")
	out, code := trRun(t, newTestCoverageCmd(), "--diff", "origin/main")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "Diff vs 111111111111: 1 of 2 changed lines covered (50.0%)") ||
		!strings.Contains(out, "a/a.go: 1/2, not covered: 7") {
		t.Errorf("diff section wrong:\n%s", out)
	}
	if strings.Contains(out, "next: reliant forge test coverage --diff") {
		t.Errorf("with --diff already given, it must not be suggested:\n%s", out)
	}

	jsonOut, code := trRun(t, newTestCoverageCmd(), "--diff", "origin/main", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	doc := covADecodeCoverage(t, jsonOut)
	if doc.Diff == nil || doc.Diff.Lines != 2 || doc.Diff.Covered != 1 || doc.BaseSHA == "" {
		t.Fatalf("diff = %+v base %q", doc.Diff, doc.BaseSHA)
	}
	if len(doc.Diff.Files) != 1 || len(doc.Diff.Files[0].Uncovered) != 1 || doc.Diff.Files[0].Uncovered[0].Start != 7 {
		t.Errorf("uncovered = %+v", doc.Diff.Files)
	}
}

func TestTestCoverageRefusals(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
		code int
	}{
		{"unknown --by", []string{"--by", "directory"}, `--by "directory": use package or file`, exitWrong},
		{"--commit with --run", []string{"--commit", "abc", "--run", "01RUN"}, "pass --commit or --run, not both", exitWrong},
		{"a commit with no clean run", []string{"--commit", "deadbeef"}, "no clean-tree run of commit deadbeef", exitUndetermined},
		{"an unknown run", []string{"--run", "nope"}, "nope", exitUndetermined},
		{"a lane nothing ran", []string{"--lane", "e2e"}, "", exitUndetermined},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			covACoverProject(t, "origin/main")
			out, code := trRun(t, newTestCoverageCmd(), tc.args...)
			if code != tc.code {
				t.Fatalf("exit %d, want %d\n%s", code, tc.code, out)
			}
		})
		if tc.code != exitUndetermined {
			continue // a wrong request is just an error, not a failure document
		}
		t.Run(tc.name+" as a JSON failure document", func(t *testing.T) {
			covACoverProject(t, "origin/main")
			out, code := trRun(t, newTestCoverageCmd(), append([]string{"--json"}, tc.args...)...)
			if code != tc.code {
				t.Fatalf("exit %d\n%s", code, out)
			}
			doc := covADecodeCoverage(t, out)
			if doc.OK || doc.ExitCode != exitUndetermined || !strings.Contains(doc.Error, tc.want) || doc.Rollup == nil {
				t.Errorf("failure doc = %+v", doc.Head)
			}
		})
	}
}

func TestTestCoverageWithNoRecordedRuns(t *testing.T) {
	root := trProject(t, trStream("pass"), 0)
	if err := os.RemoveAll(testreport.RunsRoot(root)); err != nil {
		t.Fatal(err)
	}
	out, code := trRun(t, newTestCoverageCmd(), "--json")
	if code != exitUndetermined {
		t.Fatalf("exit %d\n%s", code, out)
	}
	doc := covADecodeCoverage(t, out)
	if !strings.Contains(doc.Error, "no test runs are recorded under") || len(doc.Suggested) != 1 || doc.Suggested[0] != "task test" {
		t.Errorf("doc = %+v suggested %v", doc.Head, doc.Suggested)
	}
}

func TestTestCoverageRunsThatCarryNone(t *testing.T) {
	root := trProject(t, trStream("pass"), 0)
	laneDir := filepath.Join(testreport.RunsRoot(root), "01RUN", "unit")
	var lm testreport.LaneManifest
	b, err := os.ReadFile(filepath.Join(laneDir, testreport.LaneManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &lm); err != nil {
		t.Fatal(err)
	}
	lm.Coverage = nil
	trJSON(t, filepath.Join(laneDir, testreport.LaneManifestFile), lm)
	if err := os.Remove(filepath.Join(laneDir, "cover.out")); err != nil {
		t.Fatal(err)
	}

	out, code := trRun(t, newTestCoverageCmd(), "--json")
	if code != exitUndetermined {
		t.Fatalf("exit %d\n%s", code, out)
	}
	doc := covADecodeCoverage(t, out)
	if doc.Error != "the runs carry no coverage" || len(doc.Suggested) != 1 || !strings.Contains(doc.Suggested[0], "-coverprofile=") {
		t.Errorf("doc = %+v suggested %v", doc.Head, doc.Suggested)
	}
}

func TestNarrowCoverage(t *testing.T) {
	c := &testreport.Coverage{Files: []testreport.CoverageFile{
		{Path: "internal/billing/a.go", Package: "example.com/m/internal/billing"},
		{Path: "internal/billing/sub/b.go", Package: "example.com/m/internal/billing/sub"},
		{Path: "internal/billingx/c.go", Package: "example.com/m/internal/billingx"},
		{Path: "cmd/main.go", Package: "example.com/m/cmd"},
	}}
	tests := []struct {
		name, filter string
		want         []string
	}{
		{"empty keeps everything", "", []string{"internal/billing/a.go", "internal/billing/sub/b.go", "internal/billingx/c.go", "cmd/main.go"}},
		{"dot keeps everything", ".", []string{"internal/billing/a.go", "internal/billing/sub/b.go", "internal/billingx/c.go", "cmd/main.go"}},
		{"a directory matches on a path boundary, not a prefix", "internal/billing", []string{"internal/billing/a.go", "internal/billing/sub/b.go"}},
		{"trailing and leading slashes and backslashes are forgiven", `\internal\billing\sub/`, []string{"internal/billing/sub/b.go"}},
		{"one file", "cmd/main.go", []string{"cmd/main.go"}},
		{"an import path suffix", "billingx", []string{"internal/billingx/c.go"}},
		{"nothing", "nowhere", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, f := range narrowCoverage(c, tc.filter).Files {
				got = append(got, f.Path)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("files = %v, want %v", got, tc.want)
			}
		})
	}
	if len(c.Files) != 4 {
		t.Error("narrowing must not modify the original")
	}
}

func TestWorstRollup(t *testing.T) {
	roll := func(path string, stmts int, pct float64) testreport.Rollup {
		r := testreport.Rollup{Path: path}
		r.Combined.Statements, r.Combined.Pct = stmts, pct
		return r
	}
	if got := worstRollup(nil); got != "" {
		t.Errorf("empty rollup: %q", got)
	}
	got := worstRollup([]testreport.Rollup{roll("a", 10, 90), roll("empty", 0, 0), roll("worst", 4, 12.5), roll("b", 8, 60)})
	if got != "worst" {
		t.Errorf("worst = %q: a path with no statements must not win with 0%%", got)
	}
}

func TestFormatRangesTruncatesAfterTwelve(t *testing.T) {
	var rs []testreport.LineRange
	for i := 1; i <= 14; i++ {
		rs = append(rs, testreport.LineRange{Start: i * 10, End: i * 10})
	}
	rs[1].End = 25
	got := formatRanges(rs)
	want := "10, 20-25, 30, 40, 50, 60, 70, 80, 90, 100, 110, 120, … (2 more)"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if got := formatRanges(rs[:2]); got != "10, 20-25" {
		t.Errorf("short list: %q", got)
	}
	if got := formatRanges(nil); got != "" {
		t.Errorf("nil: %q", got)
	}
}

func TestSortedTallyLanes(t *testing.T) {
	m := map[testreport.Lane]testreport.Tally{"unit": {Pct: 1}, "e2e": {Pct: 2}, "integration": {Pct: 3}}
	if got := strings.Join(sortedTallyLanes(m), ","); got != "e2e,integration,unit" {
		t.Errorf("lanes = %s", got)
	}
}
