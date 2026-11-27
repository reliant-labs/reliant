// Copyright (c) 2025 Reliant Labs
package gatehost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun"
	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// record appends the stage's name to a file in the work volume, so a test
// reads back which stages ran and in what order.
func record(name string, exit int) string {
	return `echo ` + name + ` >>"$RELIANT_GATE_ROOT/order"; echo "log of ` + name + `"; exit ` + strconv.Itoa(exit)
}

func states(g gaterun.GateResult) string {
	var b []string
	for _, s := range g.StageList() {
		b = append(b, string(s.Name)+"="+string(s.Verdict))
	}
	return strings.Join(b, " ")
}

// runGate runs the gate and returns its result and the order in which the
// stages appended to the order file (read before teardown removes it).
func runGate(t *testing.T, f *fakeBackend, r *Runner, req gaterun.StartRequest) (gaterun.GateResult, string) {
	t.Helper()
	order := ""
	tamper := f.tamper
	f.tamper = func(res *Result) {
		b, _ := os.ReadFile(filepath.Join(f.dir(req.GateID), "order"))
		order = strings.Join(strings.Fields(string(b)), ",")
		if tamper != nil {
			tamper(res)
		}
	}
	g := r.Run(context.Background(), f, req)
	return g, order
}

func TestStagesRunInOrderAndAFailureStopsTheRest(t *testing.T) {
	f := newFake(t, gaterun.IsolationSysbox)
	r := newRunner(t, f, map[string]string{"README": "x"})
	g, order := runGate(t, f, r, startReq(
		cmdStage(config.StageLint, record("lint", 0)),
		cmdStage(config.StageTest, record("test", 1)),
		cmdStage(config.StageBuild, record("build", 0)),
		scanStage(""),
	))
	if order != "lint,test" {
		t.Fatalf("stages ran %q, want lint,test", order)
	}
	if got, want := states(g), "lint=pass test=fail build=skipped sbom_scan=skipped"; got != want {
		t.Fatalf("stages %q, want %q", got, want)
	}
	if g.Class != gaterun.ClassFail || g.ExitCode() != 1 || g.ReasonCode != gaterun.ReasonStage {
		t.Fatalf("class %s exit %d reason %s, want fail 1 stage", g.Class, g.ExitCode(), g.ReasonCode)
	}
	if !strings.Contains(g.Stages[config.StageTest].LogTail, "log of test") {
		t.Fatalf("test stage log tail %q", g.Stages[config.StageTest].LogTail)
	}
	if g.Landable() {
		t.Fatal("a failed gate is landable")
	}
	if len(f.teardowns) != 1 || f.teardowns[0] != "gate-b1-1" {
		t.Fatalf("teardowns %v", f.teardowns)
	}
}

func TestEveryStagePassingOnSysboxIsLandable(t *testing.T) {
	f := newFake(t, gaterun.IsolationSysbox)
	f.binDir = fakeForge(t, `{"gate":{"verdict":"pass"}}`, 0)
	r := newRunner(t, f, nil)
	g, _ := runGate(t, f, r, startReq(
		cmdStage(config.StageLint, "true"), cmdStage(config.StageTest, "true"),
		cmdStage(config.StageBuild, `echo bin >"$RELIANT_GATE_ARTIFACTS/app"`), scanStage("prod"),
	))
	if got, want := states(g), "lint=pass test=pass build=pass sbom_scan=pass"; got != want {
		t.Fatalf("stages %q, want %q (%s)", got, want, g.Reason)
	}
	if g.Class != gaterun.ClassPass || !g.Landable() || g.Isolation != gaterun.IsolationSysbox {
		t.Fatalf("class %s isolation %s landable %v", g.Class, g.Isolation, g.Landable())
	}
	if g.Scan == nil || g.Scan.Verdict != "pass" {
		t.Fatalf("scan %+v", g.Scan)
	}
}

func TestSBOMScanInvocation(t *testing.T) {
	f := newFake(t, gaterun.IsolationSysbox)
	f.binDir = fakeForge(t, `{"gate":{"verdict":"pass"}}`, 0)
	var args string
	f.tamper = func(*Result) {
		b, _ := os.ReadFile(filepath.Join(f.dir("gate-b1-1"), "forge-args"))
		args = strings.TrimSpace(string(b))
	}
	r := newRunner(t, f, nil)
	r.Run(context.Background(), f, startReq(
		offStage(config.StageLint), cmdStage(config.StageTest, "true"),
		cmdStage(config.StageBuild, `echo bin >"$RELIANT_GATE_ARTIFACTS/app"`), scanStage("prod"),
	))
	root := f.dir("gate-b1-1")
	want := "vuln gate " + root + "/artifacts --db " + root + "/vulndb --out " + root + "/scan --json --env prod"
	if args != want {
		t.Fatalf("forge ran with\n%q, want\n%q", args, want)
	}
}

func TestTheRuncBackendIsNeverLandable(t *testing.T) {
	f := newFake(t, gaterun.IsolationRunc)
	r := newRunner(t, f, nil)
	g, _ := runGate(t, f, r, startReq(cmdStage(config.StageLint, "true"), cmdStage(config.StageTest, "true"),
		cmdStage(config.StageBuild, "true"), offStage(config.StageSBOMScan)))
	if g.Class != gaterun.ClassPass || g.Isolation != gaterun.IsolationRunc {
		t.Fatalf("class %s isolation %s", g.Class, g.Isolation)
	}
	if g.Landable() {
		t.Fatal("a pass on runc is landable; it must never be")
	}
	for _, iso := range []gaterun.Isolation{gaterun.IsolationRunc, ""} {
		if (gaterun.GateResult{Class: gaterun.ClassPass, Isolation: iso}).Landable() {
			t.Fatalf("a pass in %q is landable", iso)
		}
	}
	for _, iso := range []gaterun.Isolation{gaterun.IsolationSysbox, gaterun.IsolationVM} {
		if !(gaterun.GateResult{Class: gaterun.ClassPass, Isolation: iso}).Landable() {
			t.Fatalf("a pass in %q is not landable", iso)
		}
	}
}

func TestOffAndUnavailableStagesAreReportedAsSuchNeverAsPassed(t *testing.T) {
	f := newFake(t, gaterun.IsolationSysbox)
	r := newRunner(t, f, nil)
	g, order := runGate(t, f, r, startReq(
		offStage(config.StageLint), cmdStage(config.StageTest, record("test", 0)), gaterun.PlanStage{Name: config.StageBuild, State: config.StageUnavailable, Reason: "no build script"}, offStage(config.StageSBOMScan),
	))
	if order != "test" {
		t.Fatalf("ran %q", order)
	}
	if got, want := states(g), "lint=off test=pass build=unavailable sbom_scan=off"; got != want {
		t.Fatalf("stages %q, want %q", got, want)
	}
	if g.Stages[config.StageLint].Exit != -1 || g.Stages[config.StageBuild].Summary != "no build script" {
		t.Fatalf("off/unavailable rendering: %+v", g.Stages)
	}
	if g.Class != gaterun.ClassPass {
		t.Fatalf("class %s", g.Class)
	}
}

func TestStageExitMapping(t *testing.T) {
	for _, tc := range []struct {
		exit  string
		state gaterun.StageState
		class gaterun.Class
	}{
		{"0", gaterun.StagePass, gaterun.ClassPass},
		{"1", gaterun.StageFail, gaterun.ClassFail},
		{"2", gaterun.StageUnproven, gaterun.ClassUnproven},
		{"3", gaterun.StageFail, gaterun.ClassFail},
		{"127", gaterun.StageFail, gaterun.ClassFail},
	} {
		t.Run(tc.exit, func(t *testing.T) {
			f := newFake(t, gaterun.IsolationSysbox)
			r := newRunner(t, f, nil)
			g := r.Run(context.Background(), f, startReq(
				offStage(config.StageLint), cmdStage(config.StageTest, "exit "+tc.exit),
				cmdStage(config.StageBuild, "true"), offStage(config.StageSBOMScan)))
			if g.Stages[config.StageTest].Verdict != tc.state || g.Class != tc.class || g.ExitCode() != tc.class.ExitCode() {
				t.Fatalf("exit %s: stage %s class %s (%s), want %s %s", tc.exit, g.Stages[config.StageTest].Verdict, g.Class, g.Reason, tc.state, tc.class)
			}
			if tc.state != gaterun.StagePass && g.Stages[config.StageBuild].Verdict != gaterun.StageSkipped {
				t.Fatalf("build after a %s test is %s", tc.state, g.Stages[config.StageBuild].Verdict)
			}
		})
	}
}

func TestScanVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		doc   string
		exit  int
		state gaterun.StageState
		class gaterun.Class
	}{
		{"pass", `{"gate":{"verdict":"pass"}}`, 0, gaterun.StagePass, gaterun.ClassPass},
		{"warn", `{"gate":{"verdict":"warn"}}`, 0, gaterun.StageWarn, gaterun.ClassPass},
		{"forge's gate off", `{"gate":{"verdict":"off"}}`, 0, gaterun.StageOff, gaterun.ClassPass},
		{"block", `{"gate":{"verdict":"block","reason":"vuln_gate_blocked"}}`, 1, gaterun.StageFail, gaterun.ClassFail},
		{"stale", `{"gate":{"verdict":"stale","reason":"vuln_gate_unscanned"}}`, 2, gaterun.StageUnproven, gaterun.ClassUnproven},
		{"exit 0 with a block verdict", `{"gate":{"verdict":"block"}}`, 0, gaterun.StageUnproven, gaterun.ClassUnproven},
		{"exit 0 with no document", `not json`, 0, gaterun.StageUnproven, gaterun.ClassUnproven},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, gaterun.IsolationSysbox)
			f.binDir = fakeForge(t, tc.doc, tc.exit)
			r := newRunner(t, f, nil)
			g := r.Run(context.Background(), f, startReq(offStage(config.StageLint), cmdStage(config.StageTest, "true"),
				cmdStage(config.StageBuild, "true"), scanStage("")))
			if g.Stages[config.StageSBOMScan].Verdict != tc.state || g.Class != tc.class {
				t.Fatalf("scan %s class %s (%s / %s), want %s %s", g.Stages[config.StageSBOMScan].Verdict, g.Class, g.Stages[config.StageSBOMScan].Summary, g.Reason, tc.state, tc.class)
			}
		})
	}
}

func TestAScanWithoutForgeVulnGateIsUnproven(t *testing.T) {
	f := newFake(t, gaterun.IsolationSysbox)
	f.binDir = t.TempDir()
	// A forge that predates `vuln gate`: its --help fails.
	if err := os.WriteFile(filepath.Join(f.binDir, "forge"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := newRunner(t, f, nil)
	g := r.Run(context.Background(), f, startReq(offStage(config.StageLint), cmdStage(config.StageTest, "true"),
		cmdStage(config.StageBuild, "true"), scanStage("")))
	if g.Stages[config.StageSBOMScan].Verdict != gaterun.StageUnproven || g.Class != gaterun.ClassUnproven || !strings.Contains(g.Stages[config.StageSBOMScan].Summary, "forge vuln gate") {
		t.Fatalf("scan %+v class %s", g.Stages[config.StageSBOMScan], g.Class)
	}
}

func TestABackendThatDidNotSeeTheEndIsUnproven(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tamper func(*Result)
		code   string
	}{
		{"timeout", func(r *Result) {
			*r = Result{Class: gaterun.ClassUnproven, ExitCode: -1, TimedOut: true, Reason: "wall"}
		}, gaterun.ReasonLimitExceeded},
		{"oom", func(r *Result) { *r = Result{Class: gaterun.ClassUnproven, ExitCode: 137, OOMKilled: true} }, gaterun.ReasonLimitExceeded},
		{"lost attach", func(r *Result) { *r = Result{Class: gaterun.ClassUnproven, ExitCode: -1, Reason: "attach lost"} }, gaterun.ReasonBackendFailed},
		// The script said fail; an exit of 0 disagrees, so neither is believed.
		{"exit disagrees with the result file", func(r *Result) { *r = Result{Class: gaterun.ClassPass, ExitCode: 0} }, gaterun.ReasonResultInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, gaterun.IsolationSysbox)
			f.tamper = tc.tamper
			r := newRunner(t, f, nil)
			g := r.Run(context.Background(), f, startReq(offStage(config.StageLint), cmdStage(config.StageTest, "exit 1"),
				cmdStage(config.StageBuild, "true"), offStage(config.StageSBOMScan)))
			if g.Class != gaterun.ClassUnproven || g.ReasonCode != tc.code {
				t.Fatalf("class %s code %s (%s), want unproven %s", g.Class, g.ReasonCode, g.Reason, tc.code)
			}
		})
	}
}

func TestFetchAndBackendFailuresAreUnprovenAndTornDown(t *testing.T) {
	f := newFake(t, gaterun.IsolationSysbox)
	r := newRunner(t, f, nil)
	r.Fetcher = dirFetcher{err: ErrCandidateMoved}
	g := r.Run(context.Background(), f, startReq(offStage(config.StageLint), cmdStage(config.StageTest, "true"),
		offStage(config.StageBuild), offStage(config.StageSBOMScan)))
	if g.Class != gaterun.ClassUnproven || g.ReasonCode != gaterun.ReasonCandidateMoved || len(f.specs) != 0 {
		t.Fatalf("moved candidate: %+v, ran %d", g, len(f.specs))
	}

	f = newFake(t, gaterun.IsolationSysbox)
	f.runErr = errors.New("engine did not start")
	r = newRunner(t, f, nil)
	g = r.Run(context.Background(), f, startReq(offStage(config.StageLint), cmdStage(config.StageTest, "true"),
		offStage(config.StageBuild), offStage(config.StageSBOMScan)))
	if g.Class != gaterun.ClassUnproven || g.ReasonCode != gaterun.ReasonBackendFailed || len(f.teardowns) != 1 {
		t.Fatalf("backend failure: %+v, teardowns %v", g, f.teardowns)
	}
}

func TestTheGateGetsOnlyItsOwnEnvironment(t *testing.T) {
	t.Setenv("RELIANT_DAEMON_SECRET_FOR_TEST", "leak")
	f := newFake(t, gaterun.IsolationSysbox)
	r := newRunner(t, f, nil)
	req := startReq(offStage(config.StageLint), cmdStage(config.StageTest, "true"), offStage(config.StageBuild), offStage(config.StageSBOMScan))
	req.Env = map[string]string{"GOFLAGS": "-mod=mod"}
	r.Run(context.Background(), f, req)
	env := strings.Join(f.specs[0].Env, "\n")
	if strings.Contains(env, "RELIANT_DAEMON_SECRET_FOR_TEST") || !strings.Contains(env, "GOFLAGS=-mod=mod") || !strings.Contains(env, "RELIANT_RUN_ID=gate-b1-1") {
		t.Fatalf("gate env:\n%s", env)
	}
	if f.specs[0].Limits.Wall != time.Minute {
		t.Fatalf("wall %s, want the request's 1m", f.specs[0].Limits.Wall)
	}
	req.Env = map[string]string{"HOME": "/root"}
	if err := req.Validate(); !errors.Is(err, gaterun.ErrInvalidRequest) {
		t.Fatalf("gate.env setting HOME: %v", err)
	}
}

func TestSelectFailsClosed(t *testing.T) {
	ctx := context.Background()
	sysbox := &fakeBackend{iso: gaterun.IsolationSysbox}
	runc := &fakeBackend{iso: gaterun.IsolationRunc}
	r := &Runner{Isolated: []Backend{sysbox}, NotIsolated: runc, Image: "gate:test"}

	if b, err := r.Select(ctx, false); err != nil || b != sysbox {
		t.Fatalf("ready sysbox: %v %v", b, err)
	}
	if b, err := r.Select(ctx, true); err != nil || b != sysbox {
		t.Fatalf("sysbox is preferred over runc even when runc is allowed: %v %v", b, err)
	}
	sysbox.preflightErr = errors.New("egress rules missing")
	if _, err := r.Select(ctx, false); !errors.Is(err, gaterun.ErrNoIsolatedGateMachine) || !strings.Contains(err.Error(), "egress rules missing") {
		t.Fatalf("failed preflight, runc not allowed: %v", err)
	}
	if b, err := r.Select(ctx, true); err != nil || b != runc {
		t.Fatalf("failed preflight, runc allowed: %v %v", b, err)
	}
	sysbox.preflightErr, sysbox.unavailable = nil, "no sysbox-runc"
	if _, err := r.Select(ctx, false); !errors.Is(err, gaterun.ErrNoIsolatedGateMachine) {
		t.Fatalf("undetected sysbox: %v", err)
	}
	if _, err := (&Runner{NotIsolated: runc, Image: "gate:test"}).Select(ctx, false); !errors.Is(err, gaterun.ErrNoIsolatedGateMachine) {
		t.Fatalf("no isolated backend at all: %v", err)
	}
	if _, err := (&Runner{Isolated: []Backend{&fakeBackend{iso: gaterun.IsolationSysbox}}}).Select(ctx, false); !errors.Is(err, gaterun.ErrNoIsolatedGateMachine) {
		t.Fatalf("no gate image: %v", err)
	}
}

func TestJobs(t *testing.T) {
	ctx := context.Background()
	f := newFake(t, gaterun.IsolationSysbox)
	f.block = make(chan struct{})
	r := newRunner(t, f, nil)
	j := &Jobs{Runner: r}
	req := startReq(offStage(config.StageLint), cmdStage(config.StageTest, "true"), offStage(config.StageBuild), offStage(config.StageSBOMScan))

	if got := j.Start(ctx, req); got.State != gaterun.JobRunning || got.Isolation != gaterun.IsolationSysbox {
		t.Fatalf("start: %+v", got)
	}
	if got := j.Start(ctx, req); got.State != gaterun.JobRunning {
		t.Fatalf("starting the same gate again: %+v", got)
	}
	other := req
	other.GateID = "gate-b2-1"
	if got := j.Start(ctx, other); got.State != gaterun.JobBusy {
		t.Fatalf("a second gate on a one-gate member: %+v", got)
	}
	if got := j.Status("gate-nope-1"); got.State != gaterun.JobUnknown {
		t.Fatalf("unknown gate: %+v", got)
	}
	close(f.block)
	var st gaterun.StatusReply
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if st = j.Status(req.GateID); st.State == gaterun.JobDone {
			break
		}
	}
	if st.State != gaterun.JobDone || st.Result == nil || st.Result.Class != gaterun.ClassPass {
		t.Fatalf("status: %+v", st)
	}
	if got := j.Start(ctx, req); got.State != gaterun.JobDone {
		t.Fatalf("start of a finished gate: %+v", got)
	}

	// A finished result is kept for Retain, then forgotten.
	now := time.Now()
	j.now = func() time.Time { return now.Add(3 * time.Hour) }
	if got := j.Status(req.GateID); got.State != gaterun.JobUnknown {
		t.Fatalf("after retention: %+v", got)
	}

	// A machine with no isolated backend refuses, and keeps no job.
	j2 := &Jobs{Runner: &Runner{Isolated: []Backend{&fakeBackend{iso: gaterun.IsolationSysbox, unavailable: "no sysbox"}}, Image: "x"}}
	if got := j2.Start(ctx, req); got.State != gaterun.JobRefused || !strings.Contains(got.Reason, "no isolated gate machine") {
		t.Fatalf("refusal: %+v", got)
	}
	if got := j2.Status(req.GateID); got.State != gaterun.JobUnknown {
		t.Fatalf("a refused gate left a job: %+v", got)
	}
}

func TestCancelStopsARunningGate(t *testing.T) {
	f := newFake(t, gaterun.IsolationSysbox)
	f.block = make(chan struct{})
	j := &Jobs{Runner: newRunner(t, f, nil)}
	req := startReq(offStage(config.StageLint), cmdStage(config.StageTest, "true"), offStage(config.StageBuild), offStage(config.StageSBOMScan))
	j.Start(context.Background(), req)
	j.Cancel(req.GateID)
	var st gaterun.StatusReply
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if st = j.Status(req.GateID); st.State == gaterun.JobDone {
			break
		}
	}
	if st.Result == nil || st.Result.Class != gaterun.ClassUnproven || st.Result.ReasonCode != gaterun.ReasonCanceled {
		t.Fatalf("canceled gate: %+v", st.Result)
	}
}
