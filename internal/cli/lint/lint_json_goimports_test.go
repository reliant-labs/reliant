package lint

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// One goimports finding, as golangci-lint v2 prints it: the issue, the source
// line, a caret, and the run's summary. forge project upgrade turned each line
// into a needs_human item of its own (reliant#2327). The lane keeps the raw
// lines — they are the issue's output — and the anchored finding now carries
// the remedy.
const goimportsRunOutput = "cmd/demo/cmd/db.go:53:1: File is not properly formatted (goimports)\n" +
	"\tforgedb \"example.com/demo/db\"\n" +
	"^\n" +
	"1 issues:\n" +
	"* goimports: 1\n"

func TestGolangciFormatterFindingCarriesTheFormatFix(t *testing.T) {
	t.Parallel()
	fs := externalLinesToFindings(goimportsRunOutput, "golangci-lint", lintSevError)
	if len(fs) == 0 {
		t.Fatal("no findings")
	}
	got := fs[0]
	if got.File != "cmd/demo/cmd/db.go" || got.Line != 53 || got.Rule != "goimports" || got.FixHint != formatFixHint {
		t.Errorf("anchored finding = %+v, want cmd/demo/cmd/db.go:53 goimports with fix %q", got, formatFixHint)
	}
	for _, f := range fs[1:] {
		if f.File != "" || f.Rule != "external" {
			t.Errorf("a line under the issue became an issue of its own: %+v", f)
		}
	}
	// A linter that is not a formatter gets no formatting remedy.
	other := externalLinesToFindings("a.go:3:2: ineffectual assignment to x (ineffassign)\n", "golangci-lint", lintSevError)
	if len(other) != 1 || other[0].FixHint != "" {
		t.Errorf("ineffassign finding = %+v, want no fix hint", other)
	}
}

// The same shape from the real golangci-lint the project pins, over a fixture
// module with one misgrouped import.
func TestGolangciFormatterFindingShapeFromTheRealTool(t *testing.T) {
	if testing.Short() {
		t.Skip("runs golangci-lint over a fixture module; runs in task test")
	}
	bin, err := exec.LookPath("golangci-lint")
	if err != nil {
		t.Skip("golangci-lint not on PATH")
	}
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"go.mod": "module example.com/demo\n\ngo 1.24\n",
		".golangci.yml": "version: \"2\"\nlinters:\n  default: none\nformatters:\n  enable:\n    - goimports\n" +
			"  settings:\n    goimports:\n      local-prefixes:\n        - example.com/demo\n",
		"a/a.go": "package a\n\nimport (\n\t\"fmt\"\n\t\"example.com/demo/b\"\n\t\"os\"\n)\n\nvar _ = fmt.Sprint(os.Args, b.X)\n",
		"b/b.go": "package b\n\nvar X = 1\n",
	} {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(bin, "run", "--allow-serial-runners", "./...")
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()
	fs := externalLinesToFindings(string(out), "golangci-lint", lintSevError)
	var anchored []lintJSONFinding
	for _, f := range fs {
		if f.File != "" {
			anchored = append(anchored, f)
		}
	}
	if len(anchored) != 1 || anchored[0].Rule != "goimports" || anchored[0].File != "a/a.go" || anchored[0].FixHint != formatFixHint {
		t.Fatalf("anchored findings = %+v, want one goimports finding on a/a.go with the format fix\n%s", anchored, out)
	}
	if fs[0].File == "" {
		t.Errorf("golangci-lint printed something before the issue, so the raw lines no longer follow it:\n%s", out)
	}
}

// Each diff the format lane reports follows its own file's finding.
func TestFormatFindingsPairEachDiffWithItsFile(t *testing.T) {
	t.Parallel()
	fs := formatFindings([]formatPending{
		{File: "a.go", Tool: "gofmt", Diff: "--- a/a.go\n+++ b/a.go\n"},
		{File: "b.go", Tool: "gofmt", Diff: "--- a/b.go\n+++ b/b.go\n"},
	})
	if len(fs) != 4 {
		t.Fatalf("findings = %+v, want a finding and a diff per file", fs)
	}
	for i, file := range []string{"a.go", "b.go"} {
		if fs[2*i].File != file || !strings.Contains(fs[2*i+1].Message, "+++ b/"+file) {
			t.Errorf("file %s: finding %+v then %+v, want its own diff under it", file, fs[2*i], fs[2*i+1])
		}
	}
}

// The JSON report names each finding's lane, which is the boundary a raw line
// belongs inside.
func TestFlattenLintOutcomesNamesTheLane(t *testing.T) {
	t.Parallel()
	fs, _ := flattenLintOutcomes([]lintStepOutcome{
		{name: "golangci-lint", ran: true, findings: []lintJSONFinding{{File: "a.go", Rule: "goimports"}, {Rule: "external", Message: "^"}}},
		{name: "format", ran: true, findings: []lintJSONFinding{{File: "a.go", Rule: "format"}}},
	})
	want := []string{"golangci-lint", "golangci-lint", "format"}
	for i, f := range fs {
		if f.Lane != want[i] {
			t.Errorf("finding %d lane = %q, want %q", i, f.Lane, want[i])
		}
	}
}
