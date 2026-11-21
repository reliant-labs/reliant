package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/exitcode"
	"github.com/reliant-labs/forge/internal/schemagen"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// These tests run the record verbs over run directories written into a temp
// dir, with git answered by a fake: nothing execs git or go.

const trCommit = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"

// trProject writes a checkout with one recorded run whose unit lane holds
// results (and coverage of one file), and points the verbs at it.
func trProject(t *testing.T, results string, laneExit int) string {
	t.Helper()
	root := t.TempDir()
	src := "package a\n\nfunc F() int {\n\treturn 1\n}\n"
	trWrite(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.24\n")
	trWrite(t, filepath.Join(root, "a/a.go"), src)
	run := filepath.Join(testreport.RunsRoot(root), "01RUN")
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	trJSON(t, filepath.Join(run, testreport.RunManifestFile), testreport.RunManifest{
		Schema: testreport.RunManifestSchema, ID: "01RUN", CreatedAt: now, Commit: trCommit, TreeHash: "tree1",
		Branch: "feat", Project: "shop", Lanes: []string{"unit"},
	})
	trWrite(t, filepath.Join(run, "unit", "results.json"), results)
	trWrite(t, filepath.Join(run, "unit", "cover.out"), "mode: atomic\nexample.com/m/a/a.go:3.14,5.2 1 1\n")
	trJSON(t, filepath.Join(run, "unit", testreport.LaneManifestFile), testreport.LaneManifest{
		Schema: testreport.LaneManifestSchema, Lane: "unit", ExitCode: laneExit, StartedAt: now, FinishedAt: now,
		Format: testreport.LaneFormatGoJSON, Results: testreport.LaneResultsParsed, ResultsFile: "results.json",
		Coverage: []testreport.LaneCoverageInput{{Path: "cover.out", Format: testreport.CoverageGoProfile}},
	})
	blob := testreport.GitBlobID([]byte(src))
	prev := testGit
	testGit = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch {
		case args[0] == "rev-parse" && args[1] == "--show-toplevel":
			return []byte(root + "\n"), nil
		case args[0] == "ls-tree":
			return []byte("100644 blob " + blob + "\ta/a.go\x00"), nil
		}
		return nil, fmt.Errorf("fake git: no %v", args)
	}
	t.Cleanup(func() { testGit = prev })
	t.Chdir(root)
	return root
}

func trWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func trJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	trWrite(t, path, string(b))
}

func trStream(status string) string {
	s := `{"Action":"run","Package":"example.com/m/a","Test":"TestF"}` + "\n"
	if status == "skip" {
		s += `{"Action":"output","Package":"example.com/m/a","Test":"TestF","Output":"    a_test.go:3: no db\n"}` + "\n"
	}
	if status == "fail" {
		s += `{"Action":"output","Package":"example.com/m/a","Test":"TestF","Output":"    a_test.go:5: boom\n"}` + "\n"
	}
	pkg := "pass"
	if status == "fail" {
		pkg = "fail"
	}
	return s + fmt.Sprintf(`{"Action":%q,"Package":"example.com/m/a","Test":"TestF","Elapsed":0}`+"\n", status) +
		fmt.Sprintf(`{"Action":%q,"Package":"example.com/m/a","Elapsed":0.1}`+"\n", pkg)
}

func trRun(t *testing.T, cmd *cobra.Command, args ...string) (string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), exitcode.Of(err)
}

func TestTestReportJSONExitCodes(t *testing.T) {
	cases := []struct {
		name     string
		stream   string
		laneExit int
		args     []string
		want     int
	}{
		{"passing run", trStream("pass"), 0, nil, exitOK},
		{"failing test", trStream("fail"), 1, nil, exitWrong},
		{"every test skipped (zero evidence)", trStream("skip"), 0, nil, exitUndetermined},
		{"upload required with no control plane", trStream("pass"), 0, []string{"--upload=required"}, exitRefused},
		{"plain upload keeps the run's code", trStream("pass"), 0, []string{"--upload"}, exitOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			trProject(t, tc.stream, tc.laneExit)
			out, code := trRun(t, newTestReportCmd(), append([]string{"--json"}, tc.args...)...)
			if code != tc.want {
				t.Fatalf("exit %d, want %d\n%s", code, tc.want, out)
			}
			var rep testreport.Report
			if err := json.Unmarshal([]byte(out), &rep); err != nil {
				t.Fatalf("not one JSON document: %v\n%s", err, out)
			}
			if rep.Schema != testreport.SchemaReport || rep.ExitCode != tc.want || rep.OK != (tc.want == 0) {
				t.Fatalf("head %+v disagrees with exit %d", rep.Head, tc.want)
			}
			if rep.Coverage == nil || rep.Coverage.Combined != 100 || len(rep.Suggested) == 0 {
				t.Fatalf("coverage/suggested missing: %+v", rep)
			}
		})
	}
}

func TestTestReportGateJSONIsVerifyTestRunsShape(t *testing.T) {
	trProject(t, trStream("skip"), 0)
	gate := filepath.Join(t.TempDir(), "test.json")
	_, code := trRun(t, newTestReportCmd(), "--gate-json", gate)
	if code != exitUndetermined {
		t.Fatalf("exit %d", code)
	}
	b, err := os.ReadFile(gate)
	if err != nil {
		t.Fatal(err)
	}
	var g release.Gate
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	if g.Name != testGateName || g.Status != release.GateStatusFailed || !strings.Contains(g.Summary, "skipped so much") {
		t.Fatalf("gate: %+v", g)
	}
}

func TestTestReportNoRunsIsUndetermined(t *testing.T) {
	root := trProject(t, trStream("pass"), 0)
	if err := os.RemoveAll(testreport.RunsRoot(root)); err != nil {
		t.Fatal(err)
	}
	out, code := trRun(t, newTestReportCmd(), "--json")
	var rep testreport.Report
	if code != exitUndetermined || json.Unmarshal([]byte(out), &rep) != nil || rep.ExitCode != exitUndetermined || rep.Error == "" {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestTestReportExports(t *testing.T) {
	trProject(t, trStream("pass"), 0)
	out, code := trRun(t, newTestReportCmd(), "--format", "lcov")
	if code != 0 || !strings.Contains(out, "SF:a/a.go\nDA:3,1\nDA:4,1\nDA:5,1\n") {
		t.Fatalf("exit %d lcov:\n%s", code, out)
	}
	out, _ = trRun(t, newTestReportCmd(), "--format", "html")
	if !strings.Contains(out, "<!doctype html>") || !strings.Contains(out, "return 1") {
		t.Fatalf("html:\n%s", out)
	}
	if _, code := trRun(t, newTestReportCmd(), "--format", "json", "--json"); code != 0 {
		t.Fatal("--format json --json refused")
	}
	if _, code := trRun(t, newTestReportCmd(), "--format", "lcov", "--json"); code == 0 {
		t.Fatal("--format lcov --json accepted")
	}
	if _, code := trRun(t, newTestReportCmd(), "--upload=sometimes"); code == 0 {
		t.Fatal("--upload=sometimes accepted")
	}
}

func TestTestCoverageAndRuns(t *testing.T) {
	trProject(t, trStream("pass"), 0)
	out, code := trRun(t, newTestCoverageCmd(), "a", "--by", "file", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	var doc testCoverageDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != schemaTestCoverageView || doc.Coverage == nil || doc.Coverage.Schema != testreport.SchemaCoverage ||
		len(doc.Coverage.Files) != 1 || doc.Coverage.Files[0].BlobID == "" || doc.Rollup[0].Path != "a/a.go" {
		t.Fatalf("coverage doc: %s", out)
	}
	if _, code := trRun(t, newTestCoverageCmd(), "nowhere"); code != exitUndetermined {
		t.Fatalf("an empty path filter exited %d", code)
	}

	out, code = trRun(t, newTestRunsCmd(), "--json")
	var runs testRunsDoc
	if code != 0 || json.Unmarshal([]byte(out), &runs) != nil {
		t.Fatalf("runs: exit %d %s", code, out)
	}
	if runs.Schema != schemaTestRuns || len(runs.Runs) != 1 || runs.Runs[0].Lanes[0].Lane != "unit" || !runs.Runs[0].Lanes[0].Coverage {
		t.Fatalf("runs doc: %s", out)
	}
	out, _ = trRun(t, newTestRunsCmd(), "--branch", "main")
	if !strings.Contains(out, "No test runs") {
		t.Fatalf("branch filter: %s", out)
	}
}

// What the verbs print validates against the schema `forge project schema`
// publishes for it.
func TestTestRecordVerbsMatchTheirPublishedSchemas(t *testing.T) {
	trProject(t, trStream("fail"), 1)
	for _, tc := range []struct {
		schema string
		cmd    *cobra.Command
		args   []string
	}{
		{"test-report", newTestReportCmd(), []string{"--json"}},
		{"test-coverage-view", newTestCoverageCmd(), []string{"--json", "--by", "file"}},
		{"test-coverage-view", newTestCoverageCmd(), []string{"--json", "nowhere"}},
		{"test-runs", newTestRunsCmd(), []string{"--json"}},
	} {
		out, _ := trRun(t, tc.cmd, tc.args...)
		var schema bytes.Buffer
		if err := runProjectSchema(&schema, tc.schema); err != nil {
			t.Fatal(err)
		}
		compiled, err := schemagen.Compile(schema.Bytes())
		if err != nil {
			t.Fatalf("%s: %v", tc.schema, err)
		}
		v, err := schemagen.DecodeJSON([]byte(out))
		if err != nil {
			t.Fatalf("%s %v: not JSON: %v\n%s", tc.schema, tc.args, err, out)
		}
		if errs := compiled.Validate(v); len(errs) > 0 {
			t.Errorf("%s %v: does not validate: %v", tc.schema, tc.args, errs)
		}
	}
}

func TestTestIngestIntoNamedRun(t *testing.T) {
	root := trProject(t, trStream("pass"), 0)
	junit := filepath.Join(t.TempDir(), "r.xml")
	trWrite(t, junit, `<testsuites><testsuite name="s"><testcase classname="t" name="bad"><failure message="x"/></testcase></testsuite></testsuites>`)
	run := filepath.Join(testreport.RunsRoot(root), "01RUN")
	out, code := trRun(t, newTestIngestCmd(), "--lane", "py", "--format", "junit", "--run", run, junit)
	if code != 0 || strings.TrimSpace(out) != run {
		t.Fatalf("ingest: exit %d %q", code, out)
	}
	_, code = trRun(t, newTestReportCmd())
	if code != exitWrong {
		t.Fatalf("the ingested failure did not fail the report: exit %d", code)
	}
	if _, code := trRun(t, newTestIngestCmd(), "--lane", "py", "--format", "pytest", junit); code == 0 {
		t.Fatal("an unknown format was accepted")
	}
}
