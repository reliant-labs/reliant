// Copyright (c) 2025 Reliant Labs
package gatehost

import (
	"archive/tar"
	"bytes"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun"
	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

type entry struct {
	name, body, link string
	typ              byte
}

func tarOf(t *testing.T, entries ...entry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644, Typeflag: e.typ, Linkname: e.link}
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

var reportPlan = []gaterun.PlanStage{
	offStage(config.StageLint), cmdStage(config.StageTest, "go test ./..."), offStage(config.StageBuild), offStage(config.StageSBOMScan),
}

const passDoc = `{"version":1,"exit_code":0,"stages":[` +
	`{"name":"lint","state":"off","exit_code":-1,"seconds":0,"note":""},` +
	`{"name":"test","state":"pass","exit_code":0,"seconds":3,"note":""},` +
	`{"name":"build","state":"off","exit_code":-1,"seconds":0,"note":""},` +
	`{"name":"sbom_scan","state":"off","exit_code":-1,"seconds":0,"note":""}]}`

func assembleTar(t *testing.T, res Result, entries ...entry) gaterun.GateResult {
	t.Helper()
	o, err := readOutput(tarOf(t, entries...))
	if err != nil {
		return gaterun.ResultFromError("g", err)
	}
	return assemble("g", gaterun.IsolationSysbox, reportPlan, res, o)
}

var exited0 = Result{Class: gaterun.ClassPass, ExitCode: 0}

func TestReportReadsAWellFormedOutput(t *testing.T) {
	g := assembleTar(t, exited0,
		entry{name: "out/", typ: tar.TypeDir},
		entry{name: "out/" + resultFile, body: passDoc},
		entry{name: "out/logs/test.log", body: "ok  pkg 0.1s"},
		entry{name: "out/" + repoResultFile, body: `{"failing_tests":[]}`},
	)
	if g.Class != gaterun.ClassPass || g.Stages[config.StageTest].LogTail != "ok  pkg 0.1s" || string(g.RepoResult) != `{"failing_tests":[]}` {
		t.Fatalf("%+v", g)
	}
}

func TestReportRefusesLinksAndNeverFollowsThem(t *testing.T) {
	for _, typ := range []byte{tar.TypeSymlink, tar.TypeLink} {
		g := assembleTar(t, exited0,
			entry{name: "out/" + resultFile, typ: typ, link: "/etc/passwd"},
			entry{name: "out/logs/test.log", typ: tar.TypeSymlink, link: "../../../../root/.ssh/id_rsa"},
		)
		if g.Class != gaterun.ClassUnproven || g.ReasonCode != gaterun.ReasonResultInvalid || !strings.Contains(g.Reason, "not a regular file") {
			t.Fatalf("link type %c: %s %s %q", typ, g.Class, g.ReasonCode, g.Reason)
		}
	}
	// A linked log is dropped, never read; the gate still reports.
	g := assembleTar(t, exited0,
		entry{name: "out/" + resultFile, body: passDoc},
		entry{name: "out/logs/test.log", typ: tar.TypeSymlink, link: "/etc/shadow"},
	)
	if g.Class != gaterun.ClassPass || g.Stages[config.StageTest].LogTail != "" {
		t.Fatalf("linked log: %+v", g)
	}
}

func TestReportRefusesPathsOutsideTheOutputDirectory(t *testing.T) {
	g := assembleTar(t, exited0,
		entry{name: "out/../" + resultFile, body: passDoc},
		entry{name: "/out/" + resultFile, body: passDoc},
		entry{name: "elsewhere/" + resultFile, body: passDoc},
	)
	if g.Class != gaterun.ClassUnproven || !strings.Contains(g.Reason, "wrote no") {
		t.Fatalf("%s %q", g.Class, g.Reason)
	}
}

func TestReportSizeCaps(t *testing.T) {
	// A result file over its cap is refused, not read.
	big := passDoc + strings.Repeat(" ", resultMaxBytes)
	g := assembleTar(t, exited0, entry{name: "out/" + resultFile, body: big})
	if g.Class != gaterun.ClassUnproven || !strings.Contains(g.Reason, "over the") {
		t.Fatalf("oversize result: %s %q", g.Class, g.Reason)
	}
	// A stream over the whole cap fails the read.
	huge := tarOf(t, entry{name: "out/junk", body: strings.Repeat("x", reportMaxBytes+1024)})
	if _, err := readOutput(huge); err == nil {
		t.Fatal("a stream over the report cap was read")
	}
}

func TestReportRejectsAResultThatDoesNotMatchThePlan(t *testing.T) {
	for name, doc := range map[string]string{
		"an off stage that ran":        strings.Replace(passDoc, `"name":"lint","state":"off"`, `"name":"lint","state":"pass"`, 1),
		"a state that is not its exit": strings.Replace(passDoc, `"state":"pass","exit_code":0`, `"state":"pass","exit_code":1`, 1),
		"stages out of order":          strings.Replace(strings.Replace(passDoc, `"name":"lint"`, `"name":"tmp"`, 1), `"name":"test"`, `"name":"lint"`, 1),
		"an unknown field":             strings.Replace(passDoc, `"version":1`, `"version":1,"trusted":true`, 1),
		"not json":                     "{",
	} {
		g := assembleTar(t, exited0, entry{name: "out/" + resultFile, body: doc})
		if g.Class != gaterun.ClassUnproven || g.ReasonCode != gaterun.ReasonResultInvalid {
			t.Errorf("%s: %s %s %q", name, g.Class, g.ReasonCode, g.Reason)
		}
	}
	// A stage that ran after a failure.
	fail := strings.Replace(passDoc, `"state":"pass","exit_code":0`, `"state":"fail","exit_code":1`, 1)
	fail = strings.Replace(fail, `"exit_code":0,"stages"`, `"exit_code":1,"stages"`, 1)
	plan := []gaterun.PlanStage{offStage(config.StageLint), cmdStage(config.StageTest, "t"), cmdStage(config.StageBuild, "b"), offStage(config.StageSBOMScan)}
	ran := strings.Replace(fail, `{"name":"build","state":"off","exit_code":-1`, `{"name":"build","state":"pass","exit_code":0`, 1)
	o, err := readOutput(tarOf(t, entry{name: "out/" + resultFile, body: ran}))
	if err != nil {
		t.Fatal(err)
	}
	if g := assemble("g", gaterun.IsolationSysbox, plan, Result{Class: gaterun.ClassFail, ExitCode: 1}, o); g.Class != gaterun.ClassUnproven {
		t.Fatalf("a stage after a failure ran: %s %q", g.Class, g.Reason)
	}
}
