package config

import (
	"errors"
	"strings"
	"testing"
)

// base is the smallest valid file; most cases append to it.
const base = "version: 1\ngate: \"npm test\"\n"

func parseErrors(t *testing.T, doc string) Errors {
	t.Helper()
	cfg, err := Parse([]byte(doc))
	if err == nil {
		t.Fatalf("Parse accepted an invalid file:\n%s\ngot %+v", doc, cfg)
	}
	var errs Errors
	if !errors.As(err, &errs) {
		t.Fatalf("Parse returned %T (%v), want Errors", err, err)
	}
	return errs
}

func findError(errs Errors, path string) *FieldError {
	for _, e := range errs {
		if e.Path == path {
			return e
		}
	}
	return nil
}

func TestParseRejects(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		path string
		want string // substring of the message
		line int    // 0: not checked
	}{
		// version
		{"version missing", "gate: x\n", "version", "is required", 0},
		{"version 2", "version: 2\ngate: x\n", "version", "version 2 is not supported: this build of Reliant reads pipeline.yaml version 1", 1},
		{"version as a string", "version: \"1\"\ngate: x\n", "version", `must be the integer 1, got "1"`, 1},
		{"version as a list", "version: [1]\ngate: x\n", "version", "must be the integer 1, got a list", 1},
		{"version null", "version:\ngate: x\n", "version", "is required", 1},
		{"empty document", "", "version", "the file is empty", 0},
		{"comment-only document", "# nothing yet\n", "version", "the file is empty", 0},
		{"two documents", base + "---\nversion: 1\n", "", "more than one YAML document", 3},
		{"top-level list", "- version: 1\n", "", "must be a mapping", 1},
		{"syntax error", "version: 1\ngate: [\n", "", "line", 0},

		// strict keys
		{"unknown top-level key", base + "gatee: x\n", "gatee", `unknown key "gatee": the top level takes version, check, gate`, 3},
		{"unknown queue key", base + "queue: {batchmax: 3}\n", "queue.batchmax", "queue takes batch_max, speculate, rerun_on_red, preempt", 3},
		{"unknown nested list key", base + "release:\n  envs:\n    - {name: prod, soaks: 1m}\n", "release.envs[0].soaks", "unknown key", 5},
		{"unknown gate key", "version: 1\ngate: {run: x, command: y}\n", "gate.command", "unknown key", 2},

		// types
		{"int as word", base + "queue: {batch_max: four}\n", "queue.batch_max", `must be an integer, got "four"`, 3},
		{"int as float", base + "queue: {batch_max: 4.5}\n", "queue.batch_max", "must be an integer", 3},
		{"bool as string", base + "queue: {preempt: \"yes\"}\n", "queue.preempt", "must be true or false", 3},
		{"list as string", base + "generated: \"gen/**\"\n", "generated", "must be a list", 3},
		{"mapping as list", base + "queue: [1]\n", "queue", "must be a mapping, got a list", 3},
		{"string as mapping", base + "regen: {run: x}\n", "regen", "must be a string, got a mapping", 3},
		{"gate as list", "version: 1\ngate: [a]\n", "gate", "must be a mapping, got a list", 2},
		{"duration without unit", "version: 1\ngate: {run: x, timeout: 60}\n", "gate.timeout", "duration with a unit", 2},
		{"duration garbage", "version: 1\ngate: {run: x, timeout: soon}\n", "gate.timeout", "duration with a unit", 2},
		{"sweep check as list", base + "sweep: {checks: [[a]]}\n", "sweep.checks[0]", "must be a mapping", 3},

		// gate
		{"gate missing", "version: 1\ncheck: x\n", "gate", "is required", 0},
		{"gate null", "version: 1\ngate:\n", "gate", "is empty", 2},
		{"gate empty string", "version: 1\ngate: \"\"\n", "gate", "needs a command for runner machine", 2},
		{"gate blank run", "version: 1\ngate: {run: \"  \"}\n", "gate.run", "needs a command", 2},
		{"gate empty mapping", "version: 1\ngate: {}\n", "gate", "needs a command", 2},
		{"machine runner without run", "version: 1\ngate: {runner: machine, checks: [A]}\n", "gate", "needs a command", 2},
		{"checks runner without checks", "version: 1\ngate: {runner: checks}\n", "gate.checks", "runner checks needs the GitHub check names", 0},
		{"checks runner with run", "version: 1\ngate: {runner: checks, run: x, checks: [A]}\n", "gate.run", "runs no command", 2},
		{"checks runner with pool", "version: 1\ngate: {runner: checks, checks: [A], pool: gate}\n", "gate.pool", "only to runner machine", 2},
		{"checks runner with env", "version: 1\ngate: {runner: checks, checks: [A], env: {A: b}}\n", "gate.env", "only to runner machine", 2},
		{"unknown runner", "version: 1\ngate: {runner: vm, run: x}\n", "gate.runner", `must be machine or checks, got "vm"`, 2},
		{"bad pool", "version: 1\ngate: {run: x, pool: Big-Pool}\n", "gate.pool", "lowercase", 2},
		{"timeout too short", "version: 1\ngate: {run: x, timeout: 5s}\n", "gate.timeout", "between 1m and 6h, got 5s", 2},
		{"timeout too long", "version: 1\ngate: {run: x, timeout: 7h}\n", "gate.timeout", "between 1m and 6h", 2},
		{"duplicate check", "version: 1\ngate: {run: x, checks: [A, A]}\n", "gate.checks[1]", `check "A" is listed twice`, 2},
		{"empty check name", "version: 1\ngate: {run: x, checks: [\"\"]}\n", "gate.checks[0]", "is empty", 2},
		{"reserved env", "version: 1\ngate: {run: x, env: {RELIANT_QUARANTINE: a}}\n", "gate.env.RELIANT_QUARANTINE", "reserved", 2},
		{"bad env name", "version: 1\ngate: {run: x, env: {1BAD: a}}\n", "gate.env.1BAD", "not an environment variable name", 2},
		{"env value mapping", "version: 1\ngate: {run: x, env: {A: {b: c}}}\n", "gate.env.A", "must be a string", 2},
		{"bad infra pattern", "version: 1\ngate:\n  run: x\n  infra_patterns: [\"ok\", \"(\"]\n", "gate.infra_patterns[1]", "not a valid regular expression", 4},
		{"absolute infra file", "version: 1\ngate: {run: x, infra_patterns_file: /etc/infra.txt}\n", "gate.infra_patterns_file", "repository-relative", 2},
		{"escaping infra file", "version: 1\ngate: {run: x, infra_patterns_file: ../infra.txt}\n", "gate.infra_patterns_file", "repository-relative", 2},

		// check
		{"check empty mapping", base + "check: {}\n", "check", "needs run, checks, or both", 3},
		{"check blank", base + "check: \"  \"\n", "check", "is empty", 3},
		{"check duplicate", base + "check: {checks: [A, A]}\n", "check.checks[1]", "listed twice", 3},

		// generated and regen
		{"regen without generated", base + "regen: \"task generate\"\n", "regen", "needs `generated`", 3},
		{"blank regen", base + "generated: [a]\nregen: \" \"\n", "regen", "is empty", 4},
		{"absolute glob", base + "generated: [\"/gen/**\"]\n", "generated[0]", "no leading /", 3},
		{"escaping glob", base + "generated: [\"../gen/**\"]\n", "generated[0]", "must not leave the repository", 3},
		{"invalid glob", base + "generated: [\"gen/[a\"]\n", "generated[0]", "not a valid glob", 3},
		{"empty glob", base + "generated: [\"\"]\n", "generated[0]", "is empty", 3},

		// owner_approval
		{"approval kind missing", base + "owner_approval: [{paths: [a]}]\n", "owner_approval[0].kind", "is required", 3},
		{"approval kind decided in the app", base + "owner_approval: [{kind: emergency_land}]\n", "owner_approval[0].kind", "decided in the app", 3},
		{"approval kind unknown", base + "owner_approval: [{kind: approve_everything}]\n", "owner_approval[0].kind", "unknown kind", 3},
		{"approval bad path glob", base + "owner_approval: [{kind: approve_breaking_proto, paths: [\"/proto\"]}]\n", "owner_approval[0].paths[0]", "no leading /", 3},
		{"approval empty when_cmd", base + "owner_approval: [{kind: approve_breaking_proto, when_cmd: \"\"}]\n", "owner_approval[0].when_cmd", "is empty", 3},
		{"approval empty when", base + "owner_approval: [{kind: approve_prod_fix, when: \"\"}]\n", "owner_approval[0].when", "is empty", 3},
		{"approval bad CEL", base + "owner_approval: [{kind: approve_prod_fix, when: \"incident.source in [\"}]\n", "owner_approval[0].when", "not a valid CEL expression", 3},
		{"approval long when", base + "owner_approval: [{kind: approve_prod_fix, when: \"" + strings.Repeat("a", 201) + "\"}]\n", "owner_approval[0].when", "the limit is 200", 3},

		// pins
		{"pin repo missing", base + "pins: [{file: go.mod}]\n", "pins[0].repo", "is required", 3},
		{"pin repo not owner/name", base + "pins: [{repo: forge}]\n", "pins[0].repo", "not a GitHub repository", 3},
		{"pin repo dot name", base + "pins: [{repo: a/..}]\n", "pins[0].repo", "not a GitHub repository", 3},
		{"pin twice", base + "pins: [{repo: a/b}, {repo: A/B}]\n", "pins[1].repo", "pinned twice", 3},
		{"lockstep self", base + "pins: [{repo: a/b, lockstep_with: [a/b]}]\n", "pins[0].lockstep_with[0]", "own repo", 3},
		{"lockstep twice", base + "pins: [{repo: a/b, lockstep_with: [c/d, c/d]}]\n", "pins[0].lockstep_with[1]", "listed twice", 3},
		{"lockstep bad repo", base + "pins: [{repo: a/b, lockstep_with: [c]}]\n", "pins[0].lockstep_with[0]", "not a GitHub repository", 3},
		{"pin absolute file", base + "pins: [{repo: a/b, file: /go.mod}]\n", "pins[0].file", "repository-relative", 3},
		{"bump unknown repo placeholder", base + "pins: [{repo: reliant-labs/reliant, lockstep_with: [reliant-labs/forge], bump: \"x {reliant-labs/forgee}\"}]\n", "pins[0].bump", "placeholder {reliant-labs/forgee} names no repository this pin moves", 3},
		{"bump ambiguous short name", base + "pins: [{repo: a/x, lockstep_with: [b/x], bump: \"x {x}\"}]\n", "pins[0].bump", "ambiguous", 3},
		{"bump blank", base + "pins: [{repo: a/b, bump: \"\"}]\n", "pins[0].bump", "is empty", 3},

		// consumers
		{"consumer command missing", base + "consumers: [{repo: a/b}]\n", "consumers[0].command", "is required", 3},
		{"consumer repo missing", base + "consumers: [{command: x}]\n", "consumers[0].repo", "is required", 3},
		{"consumer mode unknown", base + "consumers: [{repo: a/b, command: x, mode: blocking}]\n", "consumers[0].mode", "must be advisory or required", 3},
		{"consumer twice", base + "consumers: [{repo: a/b, command: x}, {repo: a/b, command: y}]\n", "consumers[1].repo", "listed twice", 3},

		// queue, review, fix, flake
		{"batch_max zero", base + "queue: {batch_max: 0}\n", "queue.batch_max", "between 1 and 16, got 0", 3},
		{"batch_max too large", base + "queue: {batch_max: 17}\n", "queue.batch_max", "between 1 and 16", 3},
		{"speculate too large", base + "queue: {speculate: 5}\n", "queue.speculate", "between 0 and 4", 3},
		{"rerun negative", base + "queue: {rerun_on_red: -1}\n", "queue.rerun_on_red", "between 0 and 3", 3},
		{"max_rounds zero", base + "review: {max_rounds: 0}\n", "review.max_rounds", "between 1 and 10", 3},
		{"fix attempts too many", base + "fix: {attempts_per_signature: 11}\n", "fix.attempts_per_signature", "between 1 and 10", 3},
		{"flake policy unknown", base + "flake: {policy: ignore}\n", "flake.policy", "must be quarantine or delete", 3},
		{"quarantine too long", base + "flake: {quarantine_days: 8}\n", "flake.quarantine_days", "between 1 and 7", 3},

		// release
		{"release null", base + "release:\n", "release", "write `release: {}`", 3},
		{"release schedule too frequent", base + "release: {schedule: every 30s}\n", "release.schedule", "shortest interval is 1m", 3},
		{"release schedule 4 fields", base + "release: {schedule: \"0 6 * *\"}\n", "release.schedule", "got 4 fields", 3},
		{"release schedule descriptor", base + "release: {schedule: \"@daily\"}\n", "release.schedule", "5-field cron", 3},
		{"release schedule bad cron", base + "release: {schedule: \"61 * * * *\"}\n", "release.schedule", "invalid cron line", 3},
		{"release schedule empty", base + "release: {schedule: \"\"}\n", "release.schedule", "is empty", 3},
		{"release pool bad", base + "release: {pool: Release}\n", "release.pool", "lowercase", 3},
		{"release on_fail unknown", base + "release: {on_fail: retry}\n", "release.on_fail", "must be rollback, hold or fix_forward", 3},
		{"release envs empty", base + "release: {envs: []}\n", "release.envs", "lists no env", 3},
		{"env name missing", base + "release: {envs: [{soak: 1m}]}\n", "release.envs[0].name", "is required", 3},
		{"env name bad", base + "release: {envs: [{name: Prod}]}\n", "release.envs[0].name", "lowercase", 3},
		{"env twice", base + "release: {envs: [{name: prod}, {name: prod}]}\n", "release.envs[1].name", "appears twice", 3},
		{"soak too long", base + "release: {envs: [{name: prod, soak: 7h}]}\n", "release.envs[0].soak", "between 0s and 6h", 3},
		{"early_pass too short", base + "release: {envs: [{name: prod, early_pass: 30s}]}\n", "release.envs[0].early_pass", "between 1m and 6h", 3},
		{"early_pass over soak", base + "release: {envs: [{name: prod, soak: 5m, early_pass: 8m}]}\n", "release.envs[0].early_pass", "longer than soak (5m)", 3},
		{"approval prod_only", base + "release: {envs: [{name: prod, approval: prod_only}]}\n", "release.envs[0].approval", "is a project setting", 3},
		{"approval unknown", base + "release: {envs: [{name: prod, approval: maybe}]}\n", "release.envs[0].approval", "must be project_setting, none or required", 3},
		{"inconclusive unknown", base + "release: {envs: [{name: prod, inconclusive: fail}]}\n", "release.envs[0].inconclusive", "must be probes", 3},
		{"inconclusive pass refused", base + "release: {envs: [{name: prod, inconclusive: pass}]}\n", "release.envs[0].inconclusive", "neither a pass nor a block", 3},
		{"inconclusive hold refused", base + "release: {envs: [{name: prod, inconclusive: hold}]}\n", "release.envs[0].inconclusive", "the env's probes judge it", 3},
		{"probes empty", base + "release: {envs: [{name: prod, probes: []}]}\n", "release.envs[0].probes", "lists no probe", 3},
		{"probe unknown builtin", base + "release: {envs: [{name: prod, probes: [ping]}]}\n", "release.envs[0].probes[0]", `unknown built-in probe "ping"`, 3},
		{"probe command shadows builtin", base + "release: {envs: [{name: prod, probes: [{name: health, run: x}]}]}\n", "release.envs[0].probes[0].name", "is a built-in probe", 3},
		{"probe command without run", base + "release: {envs: [{name: prod, probes: [{name: smoke}]}]}\n", "release.envs[0].probes[0].run", "needs run", 3},
		{"probe command blank run", base + "release: {envs: [{name: prod, probes: [{name: smoke, run: \" \"}]}]}\n", "release.envs[0].probes[0].run", "is empty", 3},
		{"probe twice", base + "release: {envs: [{name: prod, probes: [health, health]}]}\n", "release.envs[0].probes[1]", "listed twice", 3},
		{"canary refused", base + "release: {envs: [{name: prod, strategy: {kind: canary, steps: [5, 25, 100]}}]}\n", "release.envs[0].strategy.kind", "canary is not available yet: no runtime implements traffic splitting", 3},
		{"strategy kind missing", base + "release: {envs: [{name: prod, strategy: {}}]}\n", "release.envs[0].strategy.kind", "is required", 3},
		{"strategy unknown", base + "release: {envs: [{name: prod, strategy: {kind: blue_green}}]}\n", "release.envs[0].strategy.kind", "unknown strategy", 3},
		{"steps without canary", base + "release: {envs: [{name: prod, strategy: {kind: all_at_once, steps: [5]}}]}\n", "release.envs[0].strategy.steps", "only to a canary", 3},

		// recurring
		{"prompt and run", base + "recurring: [{name: a, schedule: \"0 3 * * 1\", prompt: p.md, run: x}]\n", "recurring[0]", "sets both prompt and run", 3},
		{"neither prompt nor run", base + "recurring: [{name: a, schedule: \"0 3 * * 1\"}]\n", "recurring[0]", "needs prompt", 3},
		{"recurring name missing", base + "recurring: [{schedule: \"0 3 * * 1\", run: x}]\n", "recurring[0].name", "is required", 3},
		{"recurring name bad", base + "recurring: [{name: Pentest, schedule: \"0 3 * * 1\", run: x}]\n", "recurring[0].name", "lowercase", 3},
		{"recurring twice", base + "recurring: [{name: a, schedule: \"0 3 * * 1\", run: x}, {name: a, schedule: \"0 4 * * 1\", run: y}]\n", "recurring[1].name", "defined twice", 3},
		{"recurring schedule missing", base + "recurring: [{name: a, run: x}]\n", "recurring[0].schedule", "is required", 3},
		{"recurring if main moved", base + "recurring: [{name: a, schedule: \"every 2h if main moved\", run: x}]\n", "recurring[0].schedule", "applies only to release.schedule", 3},
		{"recurring prompt absolute", base + "recurring: [{name: a, schedule: \"0 3 * * 1\", prompt: /p.md}]\n", "recurring[0].prompt", "repository-relative", 3},
		{"recurring output unknown", base + "recurring: [{name: a, schedule: \"0 3 * * 1\", run: x, output: email}]\n", "recurring[0].output", "must be pr, incident, report or none", 3},
		{"max_prs without pr", base + "recurring: [{name: a, schedule: \"0 3 * * 1\", run: x, output: incident, max_prs: 2}]\n", "recurring[0].max_prs", "only to output pr", 3},
		{"max_prs too many", base + "recurring: [{name: a, schedule: \"0 3 * * 1\", run: x, output: pr, max_prs: 11}]\n", "recurring[0].max_prs", "between 1 and 10", 3},
		{"tools on a run job", base + "recurring: [{name: a, schedule: \"0 3 * * 1\", run: x, tools: [shell]}]\n", "recurring[0].tools", "only to a prompt job", 3},
		{"tools twice", base + "recurring: [{name: a, schedule: \"0 3 * * 1\", prompt: p.md, tools: [shell, shell]}]\n", "recurring[0].tools[1]", "listed twice", 3},
		{"tool empty", base + "recurring: [{name: a, schedule: \"0 3 * * 1\", prompt: p.md, tools: [\"\"]}]\n", "recurring[0].tools[0]", "is empty", 3},
		{"recurring timeout", base + "recurring: [{name: a, schedule: \"0 3 * * 1\", run: x, timeout: 13h}]\n", "recurring[0].timeout", "between 1m and 12h", 3},

		// sweep
		{"sweep unknown builtin", base + "sweep: {checks: [stale]}\n", "sweep.checks[0]", `unknown built-in check "stale"`, 3},
		{"sweep command named as builtin", base + "sweep: {checks: [{name: pin_drift, run: x}]}\n", "sweep.checks[0].name", "is a built-in check", 3},
		{"sweep command without run", base + "sweep: {checks: [{name: licenses}]}\n", "sweep.checks[0].run", "needs run", 3},
		{"sweep command without name", base + "sweep: {checks: [{run: x}]}\n", "sweep.checks[0].name", "is required", 3},
		{"sweep command bad name", base + "sweep: {checks: [{name: Licenses, run: x}]}\n", "sweep.checks[0].name", "lowercase", 3},
		{"sweep twice", base + "sweep: {checks: [pin_drift, pin_drift]}\n", "sweep.checks[1]", "listed twice", 3},
		{"sweep schedule bad", base + "sweep: {schedule: daily}\n", "sweep.schedule", "5-field cron", 3},
		{"sweep if main moved", base + "sweep: {schedule: \"0 6 * * * if main moved\"}\n", "sweep.schedule", "applies only to release.schedule", 3},

		// playbooks
		{"playbook unknown source", base + "playbooks: {pagerduty: p.md}\n", "playbooks.pagerduty", "unknown incident source", 3},
		{"playbook absolute path", base + "playbooks: {main: /p.md}\n", "playbooks.main", "repository-relative", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := parseErrors(t, tc.doc)
			e := findError(errs, tc.path)
			if e == nil {
				t.Fatalf("no error at path %q; got:\n%v", tc.path, errs)
			}
			if !strings.Contains(e.Msg, tc.want) {
				t.Errorf("error at %q = %q, want it to contain %q", tc.path, e.Msg, tc.want)
			}
			if tc.line != 0 && e.Line != tc.line {
				t.Errorf("error at %q on line %d, want line %d", tc.path, e.Line, tc.line)
			}
		})
	}
}

func TestParseVersionErrorStandsAlone(t *testing.T) {
	// A version-2 file is written against a schema this build does not know:
	// its other keys must not be reported as unknown.
	errs := parseErrors(t, "version: 2\ngate: x\nmerge_strategy: rebase\n")
	if len(errs) != 1 || errs[0].Path != "version" {
		t.Fatalf("got %v, want only the version error", errs)
	}
}

func TestParseReportsEveryProblemInLineOrder(t *testing.T) {
	doc := "version: 1\n" +
		"gate: {run: x, timeout: 1s}\n" +
		"queue: {batch_max: 0, typo: 1}\n" +
		"recurring: [{name: a, schedule: nope, run: x}]\n"
	errs := parseErrors(t, doc)
	// Decode errors (the unknown key) are reported before validation runs,
	// so this file reports only typo; fixing it surfaces the rest.
	if len(errs) != 1 || errs[0].Path != "queue.typo" {
		t.Fatalf("got %v, want only the unknown key", errs)
	}
	errs = parseErrors(t, strings.Replace(doc, ", typo: 1", "", 1))
	var paths []string
	for _, e := range errs {
		paths = append(paths, e.Path)
	}
	want := []string{"gate.timeout", "queue.batch_max", "recurring[0].schedule"}
	if strings.Join(paths, " ") != strings.Join(want, " ") {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	if got := errs.Error(); !strings.Contains(got, "gate.timeout (line 2): must be between 1m and 6h") || strings.Count(got, "\n") != 2 {
		t.Errorf("Error() = %q, want one line per problem", got)
	}
}

func TestFieldErrorFormat(t *testing.T) {
	cases := []struct {
		e    FieldError
		want string
	}{
		{FieldError{Path: "gate.run", Line: 3, Msg: "is empty"}, "gate.run (line 3): is empty"},
		{FieldError{Path: "gate.run", Msg: "is empty"}, "gate.run: is empty"},
		{FieldError{Line: 4, Msg: "bad"}, "line 4: bad"},
		{FieldError{Msg: "bad"}, "bad"},
	}
	for _, tc := range cases {
		if got := tc.e.Error(); got != tc.want {
			t.Errorf("Error() = %q, want %q", got, tc.want)
		}
	}
}

func TestValidateConfigBuiltInCode(t *testing.T) {
	// A Config built in code has no positions: errors carry paths only.
	err := (&Config{}).Validate()
	var errs Errors
	if !errors.As(err, &errs) {
		t.Fatalf("Validate() = %v, want Errors", err)
	}
	for _, path := range []string{"version", "gate"} {
		e := findError(errs, path)
		if e == nil || e.Line != 0 {
			t.Errorf("error at %q = %+v, want one with no line", path, e)
		}
	}
	if err := (&Config{Version: 3, Gate: &Gate{Run: "x"}}).Validate(); err == nil || !strings.Contains(err.Error(), "version 3 is not supported") {
		t.Errorf("Validate(version 3) = %v", err)
	}
	r, err := (&Config{Version: 1, Gate: &Gate{Run: "go test ./..."}}).Resolve()
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}
	if r.Gate.Run != "go test ./..." || r.Gate.Pool != DefaultGatePool {
		t.Errorf("Resolve() gate = %+v", r.Gate)
	}
}
