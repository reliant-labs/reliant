package testreport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/forge/pkg/testreport"
)

func TestNewReport(t *testing.T) {
	res := parseResults(t, "testdata/results/go-test.jsonl", testreport.ResultsOptions{Lane: testreport.LaneUnit})
	unit := parseCov(t, "unit.profile", testreport.LaneUnit)
	server := parseCov(t, "server.profile", testreport.LaneE2E)
	cov := merge(t, testreport.MergeOptions{ModulePath: "example.com/demo"}, unit, server)
	rep := testreport.NewReport(testreport.ReportInput{
		Project: "demo", Commit: "c1", Tree: "tree1",
		Runs:     []testreport.RunRef{{ID: "01J", Lane: testreport.LaneUnit, Source: "local", ExitCode: 1}},
		Results:  []*testreport.Results{res},
		Coverage: cov,
		BaseSHA:  "c0",
		Changed:  []testreport.ChangedFile{{Path: "a/a.go", Ranges: []testreport.LineRange{{Start: 5, End: 8}}}},
	})
	if rep.Schema != testreport.SchemaReport || rep.OK || rep.ExitCode != 1 {
		t.Errorf("head %+v", rep.Head)
	}
	if rep.Totals.Failed != 1 || rep.Totals.FlakyRetryPass != 1 || rep.Totals.PackagesFailed != 1 {
		t.Errorf("totals %+v", rep.Totals)
	}
	if len(rep.Failures) != 2 || rep.Failures[0].Test.String() != "go:example.com/res/broken:" || rep.Failures[1].Test.Name != "TestFail" {
		t.Errorf("failures %+v", rep.Failures)
	}
	if len(rep.Flakes) != 1 || rep.Flakes[0].Signal != testreport.SignalRetryPass {
		t.Errorf("flakes %+v", rep.Flakes)
	}
	c := rep.Coverage
	if c == nil || c.Combined != 69.2 || c.Lanes[testreport.LaneUnit] != 25 || c.Granularity != testreport.GranularityStatement {
		t.Fatalf("coverage %+v", c)
	}
	if c.Delta != nil || c.DeltaReason != "no report for base" || rep.Base == nil || rep.Base.Report {
		t.Errorf("delta %v %q base %+v", c.Delta, c.DeltaReason, rep.Base)
	}
	if c.Diff == nil || c.Diff.Lines != 4 || c.Diff.Covered != 3 {
		t.Errorf("diff %+v", c.Diff)
	}
	if len(rep.Packages) != 3 || rep.Packages[0].Path != "a" {
		t.Errorf("packages, worst first: %+v", rep.Packages)
	}
	var doc map[string]any
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"schema", "ok", "exit_code", "error"} {
		if _, ok := doc[k]; !ok && k != "error" {
			t.Errorf("head key %q missing", k)
		}
	}
	if !strings.Contains(string(b), `"test":"go:example.com/res/ok:TestFail"`) {
		t.Errorf("test ids serialise as strings: %s", b)
	}

	empty := testreport.NewReport(testreport.ReportInput{})
	if empty.ExitCode != 2 || empty.OK || len(empty.Runs) != 0 {
		t.Errorf("no results is undetermined: %+v", empty.Head)
	}
}

func TestDocumentsRoundTrip(t *testing.T) {
	cov := merge(t, testreport.MergeOptions{}, parseCov(t, "unit.profile", testreport.LaneUnit))
	b, err := json.Marshal(cov)
	if err != nil {
		t.Fatal(err)
	}
	var back testreport.Coverage
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	b2, _ := json.Marshal(&back)
	if !bytes.Equal(b, b2) {
		t.Errorf("coverage document does not round-trip")
	}
	if back.Schema != testreport.SchemaCoverage {
		t.Errorf("schema %q", back.Schema)
	}
}

// ConvertCovdata runs `go tool covdata textfmt`, so it is slow. The directory
// mixes two builds (cov1, cov2); each converts on its own, and cov1's is
// exactly the textfmt output the fixture recorded.
func TestConvertCovdata(t *testing.T) {
	testkit.Slow(t, "execs `go tool covdata textfmt`")
	dir := t.TempDir()
	for _, sub := range []string{"cov1", "cov2"} {
		entries, err := os.ReadDir(filepath.Join("testdata/cover", sub))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join("testdata/cover", sub, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	builds, err := testreport.ConvertCovdata(context.Background(), "", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(builds) != 2 {
		t.Fatalf("%d builds, want 2", len(builds))
	}
	want, err := os.ReadFile("testdata/cover/server.profile")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range builds {
		if b.MetaHash == "edd7ab081ad6e8c176868bc8cb47e814" {
			found = true
			got, err := testreport.ParseCoverage(bytes.NewReader(b.Profile), testreport.CoverageOptions{Lane: "e2e"})
			if err != nil {
				t.Fatal(err)
			}
			exp, _ := testreport.ParseCoverage(bytes.NewReader(want), testreport.CoverageOptions{Lane: "e2e"})
			if g, e := blocks(got), blocks(exp); len(g) != len(e) {
				t.Errorf("cov1 converted to %d blocks, want %d", len(g), len(e))
			}
		}
	}
	if !found {
		t.Errorf("cov1's build is missing: %+v", builds)
	}
}
