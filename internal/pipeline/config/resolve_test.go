package config

import (
	"reflect"
	"testing"
	"time"
)

func mustLoad(t *testing.T, doc string) Resolved {
	t.Helper()
	r, err := Load([]byte(doc))
	if err != nil {
		t.Fatalf("Load:\n%s\nerror: %v", doc, err)
	}
	return r
}

func TestDefaults(t *testing.T) {
	d := Defaults()
	want := Resolved{
		Version: 1,
		Gate: ResolvedGate{Runner: RunnerMachine, Pool: "gate", Timeout: 60 * time.Minute, Stages: []ResolvedStage{
			{Name: StageLint, State: StageUnavailable, Source: StageSourceDefault, Reason: undetected + ": set gate.stages.lint.run, or turn the stage off"},
			{Name: StageTest, State: StageUnavailable, Source: StageSourceDefault, Reason: "no test command was detected: the file names it (gate, gate.run or gate.stages.test.run)"},
			{Name: StageBuild, State: StageUnavailable, Source: StageSourceDefault, Reason: undetected + ": set gate.stages.build.run, or turn the stage off"},
			{Name: StageSBOMScan, State: StageOn, Source: StageSourceDefault, Kind: StageBuiltinScan, Scan: &BuiltinScan{Policy: ScanPolicyDefault},
				Reason: "a syft SBOM of the build output scanned by grype, judged by forge's default vuln_gate (block KEV and fixable criticals, warn on high)"},
		}},
		Queue:  ResolvedQueue{BatchMax: 4, Speculate: 0, RerunOnRed: 1, Preempt: false},
		Review: ResolvedReview{MaxRounds: 3},
		Fix:    ResolvedFix{AttemptsPerSignature: 3},
		Flake:  ResolvedFlake{Policy: FlakeQuarantine, QuarantineDays: 7},
		Sweep: ResolvedSweep{
			Schedule: Schedule{Cron: "0 6 * * *"},
			Checks: []SweepCheck{
				{Name: "stale_needs_person"}, {Name: "stale_pr"}, {Name: "flake_quarantine_expiring"},
				{Name: "orphan_queue_branches"}, {Name: "pin_drift"}, {Name: "unproven_rate"}, {Name: "escapes"},
			},
		},
	}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("Defaults() =\n%+v\nwant\n%+v", d, want)
	}
	rel := DefaultRelease()
	if rel.Schedule != (Schedule{Every: 2 * time.Hour, IfMainMoved: true}) || rel.OnFail != OnFailRollback || rel.Envs != nil || rel.OnLand {
		t.Errorf("DefaultRelease() = %+v", rel)
	}
}

func TestMinimalFileResolvesToDefaults(t *testing.T) {
	got := mustLoad(t, "version: 1\ngate: \"npm test\"\n")
	want := Defaults()
	want.Gate.Run = "npm test"
	want.Gate.Stages[1] = ResolvedStage{Name: StageTest, State: StageOn, Source: StageSourceOverride, Kind: StageCommand, Command: "npm test", Reason: "set in pipeline.yaml"}
	// Resolve allocates the (empty) list fields; compare them as empty.
	want.OwnerApproval, want.Pins, want.Consumers, want.Recurring = []OwnerApproval{}, []Pin{}, []Consumer{}, []ResolvedRecurringJob{}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("minimal file resolved to\n%+v\nwant\n%+v", got, want)
	}
	if got.Release != nil || got.Check != nil {
		t.Errorf("release and check must be off by default: %+v %+v", got.Release, got.Check)
	}
}

func TestResolve(t *testing.T) {
	cases := []struct {
		name  string
		doc   string
		check func(t *testing.T, r Resolved)
	}{
		{"gate string shorthand", base, func(t *testing.T, r Resolved) {
			if r.Gate.Run != "npm test" || r.Gate.Runner != RunnerMachine || r.Gate.Pool != "gate" {
				t.Errorf("gate = %+v", r.Gate)
			}
		}},
		{"gate with only checks infers runner checks", "version: 1\ngate: {checks: [CI]}\n", func(t *testing.T, r Resolved) {
			if r.Gate.Runner != RunnerChecks || r.Gate.Pool != "" || !reflect.DeepEqual(r.Gate.Checks, []string{"CI"}) {
				t.Errorf("gate = %+v", r.Gate)
			}
		}},
		{"explicit runner checks", "version: 1\ngate: {runner: checks, checks: [CI], timeout: 2h}\n", func(t *testing.T, r Resolved) {
			if r.Gate.Runner != RunnerChecks || r.Gate.Timeout != 2*time.Hour {
				t.Errorf("gate = %+v", r.Gate)
			}
		}},
		{"machine run combined with checks", "version: 1\ngate: {run: \"task gate\", checks: [Windows], pool: big}\n", func(t *testing.T, r Resolved) {
			if r.Gate.Runner != RunnerMachine || r.Gate.Pool != "big" || r.Gate.Run != "task gate" || len(r.Gate.Checks) != 1 {
				t.Errorf("gate = %+v", r.Gate)
			}
		}},
		{"gate env scalars become strings", "version: 1\ngate: {run: x, env: {N: 8, B: true, S: s}}\n", func(t *testing.T, r Resolved) {
			if !reflect.DeepEqual(r.Gate.Env, map[string]string{"N": "8", "B": "true", "S": "s"}) {
				t.Errorf("env = %v", r.Gate.Env)
			}
		}},
		{"infra patterns compiled", "version: 1\ngate: {run: x, infra_patterns: [\"no space left\", \"^E[0-9]+\"]}\n", func(t *testing.T, r Resolved) {
			if len(r.Gate.InfraPatterns) != 2 || !r.Gate.InfraPatterns[1].MatchString("E42 boom") {
				t.Errorf("infra patterns = %v", r.Gate.InfraPatterns)
			}
		}},
		{"check string shorthand", base + "check: \"task ci\"\n", func(t *testing.T, r Resolved) {
			if r.Check == nil || r.Check.Run != "task ci" || r.Check.Checks != nil {
				t.Errorf("check = %+v", r.Check)
			}
		}},
		{"check object with checks only", base + "check: {checks: [\"CI gate\"]}\n", func(t *testing.T, r Resolved) {
			if r.Check == nil || r.Check.Run != "" || r.Check.Checks[0] != "CI gate" {
				t.Errorf("check = %+v", r.Check)
			}
		}},
		// check: null is an error (it is a string-or-mapping shorthand,
		// and null is neither); the sections that are only mappings read
		// null as absent.
		{"null sections are absent", base + "queue:\nsweep:\n", func(t *testing.T, r Resolved) {
			if r.Check != nil || r.Queue != Defaults().Queue || r.Sweep.Schedule.Cron != DefaultSweepSchedule {
				t.Errorf("got check %+v queue %+v sweep %+v", r.Check, r.Queue, r.Sweep)
			}
		}},
		{"queue overrides keep unset defaults", base + "queue: {batch_max: 6, preempt: true}\n", func(t *testing.T, r Resolved) {
			if r.Queue != (ResolvedQueue{BatchMax: 6, Speculate: 0, RerunOnRed: 1, Preempt: true}) {
				t.Errorf("queue = %+v", r.Queue)
			}
		}},
		{"gate allows an unisolated landing only when it says so", "version: 1\ngate: {run: make test, allow_unisolated: true}\n", func(t *testing.T, r Resolved) {
			if !r.Gate.AllowUnisolated || Defaults().Gate.AllowUnisolated {
				t.Errorf("allow_unisolated = %v (default %v)", r.Gate.AllowUnisolated, Defaults().Gate.AllowUnisolated)
			}
		}},
		{"explicit zero rerun", base + "queue: {rerun_on_red: 0}\n", func(t *testing.T, r Resolved) {
			if r.Queue.RerunOnRed != 0 {
				t.Errorf("rerun_on_red = %d, want the written 0", r.Queue.RerunOnRed)
			}
		}},
		{"review fix flake", base + "review: {max_rounds: 5}\nfix: {attempts_per_signature: 2}\nflake: {policy: delete}\n", func(t *testing.T, r Resolved) {
			if r.Review.MaxRounds != 5 || r.Fix.AttemptsPerSignature != 2 || r.Flake != (ResolvedFlake{Policy: FlakeDelete, QuarantineDays: 7}) {
				t.Errorf("review %+v fix %+v flake %+v", r.Review, r.Fix, r.Flake)
			}
		}},
		{"release {} turns releases on with defaults", base + "release: {}\n", func(t *testing.T, r Resolved) {
			if r.Release == nil || !reflect.DeepEqual(*r.Release, DefaultRelease()) {
				t.Errorf("release = %+v", r.Release)
			}
		}},
		{"release env defaults", base + "release: {on_land: true, pool: rel, envs: [{name: staging}, {name: prod, soak: 30m, early_pass: 8m, approval: required}]}\n", func(t *testing.T, r Resolved) {
			want := []ResolvedReleaseEnv{
				{Name: "staging", Approval: EnvApprovalProjectSetting, Inconclusive: InconclusiveProbes, Probes: []Probe{{Name: "health"}, {Name: "synthetic"}}, Strategy: StrategyAllAtOnce},
				{Name: "prod", Soak: 30 * time.Minute, EarlyPass: 8 * time.Minute, Approval: EnvApprovalRequired, Inconclusive: InconclusiveProbes, Probes: []Probe{{Name: "health"}, {Name: "synthetic"}}, Strategy: StrategyAllAtOnce},
			}
			if r.Release == nil || !r.Release.OnLand || r.Release.Pool != "rel" || !reflect.DeepEqual(r.Release.Envs, want) {
				t.Errorf("release = %+v", r.Release)
			}
		}},
		{"release cron schedule", base + "release: {schedule: \"0 */4 * * 1-5 if main moved\", on_fail: hold}\n", func(t *testing.T, r Resolved) {
			if r.Release.Schedule != (Schedule{Cron: "0 */4 * * 1-5", IfMainMoved: true}) || r.Release.OnFail != OnFailHold {
				t.Errorf("release = %+v", r.Release)
			}
		}},
		{"release env probes and strategy", base + "release: {envs: [{name: dev, inconclusive: probes, probes: [synthetic, {name: smoke, run: task smoke}], approval: none, strategy: {kind: all_at_once}}]}\n", func(t *testing.T, r Resolved) {
			e := r.Release.Envs[0]
			wantProbes := []Probe{{Name: "synthetic"}, {Name: "smoke", Run: "task smoke"}}
			if e.Strategy != StrategyAllAtOnce || e.Inconclusive != InconclusiveProbes || e.Approval != EnvApprovalNone || !reflect.DeepEqual(e.Probes, wantProbes) {
				t.Errorf("env = %+v", e)
			}
		}},
		{"recurring defaults", base + "recurring: [{name: a, schedule: \"0 3 * * 1\", prompt: p.md}, {name: b, schedule: every 6h, run: x, output: pr}]\n", func(t *testing.T, r Resolved) {
			a, b := r.Recurring[0], r.Recurring[1]
			if a.Output != OutputReport || a.MaxPRs != 0 || a.Timeout != time.Hour || a.Schedule.Cron != "0 3 * * 1" {
				t.Errorf("a = %+v", a)
			}
			if b.Output != OutputPR || b.MaxPRs != 1 || b.Schedule.Every != 6*time.Hour {
				t.Errorf("b = %+v", b)
			}
		}},
		{"sweep checks replace the defaults", base + "sweep: {schedule: every 12h, checks: [pin_drift, {name: licenses, run: task licenses}]}\n", func(t *testing.T, r Resolved) {
			want := []SweepCheck{{Name: "pin_drift"}, {Name: "licenses", Run: "task licenses"}}
			if !reflect.DeepEqual(r.Sweep.Checks, want) || r.Sweep.Schedule.Every != 12*time.Hour {
				t.Errorf("sweep = %+v", r.Sweep)
			}
			if !r.Sweep.Checks[0].Builtin() || r.Sweep.Checks[1].Builtin() {
				t.Errorf("Builtin() wrong for %+v", r.Sweep.Checks)
			}
		}},
		{"explicit empty sweep checks keep only the digest", base + "sweep: {checks: []}\n", func(t *testing.T, r Resolved) {
			if r.Sweep.Checks == nil || len(r.Sweep.Checks) != 0 {
				t.Errorf("sweep checks = %#v, want an empty non-nil list", r.Sweep.Checks)
			}
		}},
		{"consumer mode defaults to advisory", base + "consumers: [{repo: a/b, command: x}]\n", func(t *testing.T, r Resolved) {
			if r.Consumers[0].Mode != ConsumerAdvisory {
				t.Errorf("consumer = %+v", r.Consumers[0])
			}
		}},
		{"playbooks", base + "playbooks: {main: .reliant/playbooks/main.md}\n", func(t *testing.T, r Resolved) {
			if r.Playbooks[SourceMain] != ".reliant/playbooks/main.md" || len(r.Playbooks) != 1 {
				t.Errorf("playbooks = %v", r.Playbooks)
			}
		}},
		{"anchors and aliases", "version: 1\ngate: &g {run: x, checks: [A]}\ncheck: {checks: [A]}\nrelease: {envs: [{name: a, soak: &s 10m}, {name: b, soak: *s}]}\n", func(t *testing.T, r Resolved) {
			if r.Release.Envs[1].Soak != 10*time.Minute {
				t.Errorf("aliased soak = %v", r.Release.Envs[1].Soak)
			}
		}},
		{"owner approvals and pins", base + "owner_approval: [{kind: approve_breaking_proto, paths: [\"proto/**\"]}]\npins: [{repo: a/b, file: go.mod, lockstep_with: [c/d]}]\n", func(t *testing.T, r Resolved) {
			if r.OwnerApproval[0].Kind != ApproveBreakingProto || r.Pins[0].LockstepWith[0] != "c/d" {
				t.Errorf("approvals %+v pins %+v", r.OwnerApproval, r.Pins)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.check(t, mustLoad(t, tc.doc))
		})
	}
}

func TestResolveCopies(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\ngate: {run: x, checks: [A], env: {K: v}}\ngenerated: [g]\nregen: r\n"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := cfg.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	r.Gate.Checks[0], r.Gate.Env["K"], r.Generated[0] = "changed", "changed", "changed"
	if cfg.Gate.Checks[0] != "A" || cfg.Gate.Env["K"] != "v" || cfg.Generated[0] != "g" {
		t.Fatalf("mutating Resolved changed the Config: %+v %+v", cfg.Gate, cfg.Generated)
	}
}
