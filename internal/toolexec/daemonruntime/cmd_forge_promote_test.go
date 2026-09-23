// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// NO TEST IN THIS FILE INVOKES FORGE, AND THAT IS A SAFETY PROPERTY, NOT A
// CONVENIENCE.
//
// forge.promote_apply WRITES a release binding. Every bound environment in the
// reference project is a CLOUD environment and one of them is a live GKE prod
// cluster; a test that shelled out to a real forge in a real project would move
// a real pointer. That has already happened once during this work — a test
// binary built from temporarily-mutated source wrote control-plane's release
// ledger and moved staging from v1.3.0 to v1.5.15. It was caught and restored,
// and it is the reason every test here substitutes the forgeCommandRunner seam
// (stubForge, cmd_forge_test.go) and asserts against canned bytes.
//
// Under test: argv construction, the concurrency guard, and the states a caller
// must tell apart. The plan documents below are trimmed from real
// `forge env promote --plan --json` output.

// promotePlanJSON builds a plan document shaped like forge's real one. Nested
// current/target, because that is the actual contract — the guard reads
// current.release, and a flattened fixture would let a broken guard pass.
func promotePlanJSON(env, target, current string, bound, dryRun, applied bool, direction string) string {
	doc := map[string]any{
		"env":              env,
		"ledger":           ".forge/env-releases.json",
		"generated_at":     "2026-07-01T00:25:15Z",
		"dry_run":          dryRun,
		"applied":          applied,
		"direction":        direction,
		"direction_detail": "FORWARD — " + target + " is 20 release(s) NEWER than " + current,
		"releases_between": 20,
		"current": map[string]any{
			"bound":         bound,
			"release":       current,
			"promoted_at":   "2026-07-01T00:25:15Z",
			"release_known": true,
		},
		"target": map[string]any{"release": target, "images": 4},
		"images": []any{
			map[string]any{"image": "control-plane", "change": "changed",
				"current_digest": "sha256:c3b0074", "target_digest": "sha256:1ea5668"},
			map[string]any{"image": "internal-console", "change": "added",
				"target_digest": "sha256:1ef891e"},
		},
		"tally":         map[string]any{"unchanged": 0, "changed": 3, "added": 1, "removed": 0},
		"commits":       map[string]any{"state": "dirty_release", "detail": "commit range is MEANINGLESS"},
		"changed":       true,
		"ships_nothing": true,
		"next_step":     "forge env deploy " + env,
		"ok":            true,
	}
	b, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func decodeForgePromoteApply(t *testing.T, raw []byte) forgePromoteApplyResponse {
	t.Helper()
	var got forgePromoteApplyResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal apply response: %v (%s)", err, raw)
	}
	return got
}

// stubForgeSequence installs a runner that returns a different canned result per
// call and records every argv. The apply path runs forge twice (guard plan, then
// the write), so a single-result stub cannot express it — and cannot catch a
// guard that read the wrong document.
func stubForgeSequence(t *testing.T, results ...forgeCommandResult) *forgeCallLog {
	t.Helper()
	log := &forgeCallLog{}
	prev := runForge
	runForge = func(_ context.Context, projectDir string, args []string) (forgeCommandResult, error) {
		log.ProjectDirs = append(log.ProjectDirs, projectDir)
		log.Args = append(log.Args, args)
		idx := len(log.Args) - 1
		if idx >= len(results) {
			t.Errorf("forge invoked %d time(s), only %d canned result(s): unexpected extra call %v",
				len(log.Args), len(results), args)
			return forgeCommandResult{}, nil
		}
		return results[idx], nil
	}
	t.Cleanup(func() { runForge = prev })
	return log
}

type forgeCallLog struct {
	ProjectDirs []string
	Args        [][]string
}

// wrote reports whether any recorded invocation was a real promote — a
// `env promote` argv without --plan. This is the assertion that matters most in
// this file: several tests exist only to prove no write was attempted.
func (l *forgeCallLog) wrote() bool {
	for _, args := range l.Args {
		if len(args) >= 2 && args[0] == "env" && args[1] == "promote" {
			plan := false
			for _, a := range args {
				if a == "--plan" {
					plan = true
				}
			}
			if !plan {
				return true
			}
		}
	}
	return false
}

// --- registration ---

func TestForgePromoteCommandsAreRegistered(t *testing.T) {
	// A handler that is written but not registered is invisible to the RPC
	// layer above.
	for _, name := range []string{"forge.promote_plan", "forge.promote_apply"} {
		t.Run(name, func(t *testing.T) {
			stubForge(t, forgeCommandResult{}, nil)
			// A non-forge dir short-circuits before forge would run, so
			// this asserts dispatch only — and dispatches nothing that
			// could write.
			if _, err := handle(t, name, map[string]any{
				"project_path": t.TempDir(), "env": "staging", "release": "v1.5.15",
				"expected_current_release": "v1.3.0",
			}); err != nil {
				t.Fatalf("%s not dispatchable: %v", name, err)
			}
		})
	}
}

// TestForgePromoteCommandsAreTwoDistinctNames pins the central safety decision:
// the write is reached by NAMING it, never by defaulting a boolean. A dry_run
// field that a caller forgets defaults to false, and false is the destructive
// value — so the safe call would become the destructive one by omission.
func TestForgePromoteCommandsAreTwoDistinctNames(t *testing.T) {
	if _, err := DefaultRegistry().Handle(context.Background(), "forge.promote_plan", []byte(`{}`)); err == nil {
		t.Error("expected validation error, but the command must at least exist")
	}
	if _, err := DefaultRegistry().Handle(context.Background(), "forge.promote_apply", []byte(`{}`)); err == nil {
		t.Error("expected validation error, but the command must at least exist")
	}

	// Neither request struct may carry a dry-run-shaped switch. If one did,
	// the two commands would be one command with a defaulting flag, and the
	// name-based guarantee above would be decoration.
	for _, typ := range []reflect.Type{
		reflect.TypeOf(forgePromotePlanRequest{}),
		reflect.TypeOf(forgePromoteApplyRequest{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			tag, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
			for _, banned := range []string{"dry_run", "dryrun", "plan", "apply", "write", "commit"} {
				if strings.EqualFold(tag, banned) {
					t.Errorf("%s carries %q — the plan/apply split must be two command NAMES, "+
						"not a boolean whose zero value is the destructive choice", typ.Name(), tag)
				}
			}
		}
	}
}

// --- argv ---

func TestForgePromoteArgs(t *testing.T) {
	t.Run("plan passes --plan", func(t *testing.T) {
		dir := forgeProject(t)
		call := stubForge(t, forgeCommandResult{
			Stdout: []byte(promotePlanJSON("staging", "v1.5.15", "v1.3.0", true, true, false, "ahead")),
		}, nil)

		if _, err := handle(t, "forge.promote_plan", map[string]any{
			"project_path": dir, "env": "staging", "release": "v1.5.15",
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		want := []string{"env", "promote", "v1.5.15", "--to", "staging", "--plan", "--json"}
		if !reflect.DeepEqual(call.Args, want) {
			t.Errorf("args:\n got %v\nwant %v", call.Args, want)
		}
		if call.ProjectDir != dir {
			t.Errorf("project dir: got %q want %q", call.ProjectDir, dir)
		}
	})

	t.Run("apply plans first, then promotes without --plan", func(t *testing.T) {
		dir := forgeProject(t)
		log := stubForgeSequence(t,
			forgeCommandResult{Stdout: []byte(promotePlanJSON("staging", "v1.5.15", "v1.3.0", true, true, false, "ahead"))},
			forgeCommandResult{Stdout: []byte(promotePlanJSON("staging", "v1.5.15", "v1.3.0", true, false, true, "ahead"))},
		)

		if _, err := handle(t, "forge.promote_apply", map[string]any{
			"project_path": dir, "env": "staging", "release": "v1.5.15",
			"expected_current_release": "v1.3.0",
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(log.Args) != 2 {
			t.Fatalf("apply must run the read-only guard plan and then the write, got %d call(s): %v",
				len(log.Args), log.Args)
		}
		wantPlan := []string{"env", "promote", "v1.5.15", "--to", "staging", "--plan", "--json"}
		wantApply := []string{"env", "promote", "v1.5.15", "--to", "staging", "--json"}
		if !reflect.DeepEqual(log.Args[0], wantPlan) {
			t.Errorf("guard call must be the dry run:\n got %v\nwant %v", log.Args[0], wantPlan)
		}
		if !reflect.DeepEqual(log.Args[1], wantApply) {
			t.Errorf("write call:\n got %v\nwant %v", log.Args[1], wantApply)
		}
	})
}

// The plan command must never be able to write, and the response it produces
// must never claim it did.
func TestForgePromotePlanNeverProducesApplied(t *testing.T) {
	dir := forgeProject(t)
	log := stubForgeSequence(t, forgeCommandResult{
		Stdout: []byte(promotePlanJSON("staging", "v1.5.15", "v1.3.0", true, true, false, "ahead")),
	})

	raw, err := handle(t, "forge.promote_plan", map[string]any{
		"project_path": dir, "env": "staging", "release": "v1.5.15",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if log.wrote() {
		t.Fatal("forge.promote_plan attempted a real promote — it must only ever pass --plan")
	}

	var facts forgePromotePlanFacts
	if err := json.Unmarshal(decodeForgeResponse(t, raw).Report, &facts); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if facts.Applied {
		t.Error("applied must be false on the plan path")
	}
	if !facts.DryRun {
		t.Error("dry_run must be true on the plan path")
	}
}

// The plan document crosses this layer byte-for-byte, including fields this
// package knows nothing about. That is the transport rule, and it is what lets a
// newer forge add a field without a change here.
func TestForgePromotePlanPassesReportThroughVerbatim(t *testing.T) {
	dir := forgeProject(t)
	report := `{"env":"staging","release":"v1.5.15","dry_run":true,"applied":false,` +
		`"direction":"ahead","ships_nothing":true,"next_step":"forge env deploy staging",` +
		`"current":{"bound":true,"release":"v1.3.0"},"target":{"release":"v1.5.15"},` +
		`"a_field_added_by_a_newer_forge":42}`
	stubForge(t, forgeCommandResult{Stdout: []byte(report)}, nil)

	raw, err := handle(t, "forge.promote_plan", map[string]any{
		"project_path": dir, "env": "staging", "release": "v1.5.15",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := decodeForgeResponse(t, raw)

	var want, have map[string]any
	if err := json.Unmarshal([]byte(report), &want); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if err := json.Unmarshal(got.Report, &have); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if !reflect.DeepEqual(want, have) {
		t.Errorf("report must pass through unmodified:\n got %v\nwant %v", have, want)
	}

	// ships_nothing and next_step are the two fields a UI must not lose:
	// promote moves a pointer, and nothing reaches a cluster until deploy.
	if have["ships_nothing"] != true {
		t.Error("ships_nothing must survive verbatim — promote ships nothing")
	}
	if have["next_step"] != "forge env deploy staging" {
		t.Errorf("next_step must survive verbatim, got %v", have["next_step"])
	}
}

// --- the concurrency guard ---

// THE central test. EnvBinding keeps no history, so an unintended overwrite is
// unrecoverable except from git. Applying against a binding the caller never saw
// is exactly that overwrite.
func TestForgePromoteApplyRefusesStaleExpectedRelease(t *testing.T) {
	dir := forgeProject(t)
	// The caller last saw v1.3.0. Someone else promoted v1.4.0 in between.
	log := stubForgeSequence(t, forgeCommandResult{
		Stdout: []byte(promotePlanJSON("staging", "v1.5.15", "v1.4.0", true, true, false, "ahead")),
	})

	raw, err := handle(t, "forge.promote_apply", map[string]any{
		"project_path": dir, "env": "staging", "release": "v1.5.15",
		"expected_current_release": "v1.3.0",
	})
	if err != nil {
		t.Fatalf("a refusal is structured data, not a transport error: %v", err)
	}
	if log.wrote() {
		t.Fatal("NOTHING may be written when the caller's view of the binding is stale")
	}
	if len(log.Args) != 1 {
		t.Errorf("only the read-only guard plan should have run, got %v", log.Args)
	}

	got := decodeForgePromoteApply(t, raw)
	if got.PromoteRefused == nil {
		t.Fatal("the refusal must be reported, not silently skipped")
	}
	if got.PromoteRefused.Reason != forgePromoteRefusalReasonStaleBinding {
		t.Errorf("reason must be the stable token %q, got %q",
			forgePromoteRefusalReasonStaleBinding, got.PromoteRefused.Reason)
	}
	// The binding that was ACTUALLY found is what makes this actionable: a
	// refusal without it leaves only a blind retry.
	if got.PromoteRefused.ActualCurrentRelease != "v1.4.0" {
		t.Errorf("the found binding must be carried, got %q", got.PromoteRefused.ActualCurrentRelease)
	}
	if !got.PromoteRefused.ActualBound {
		t.Error("actual_bound must be true — the env IS bound, just not to what the caller expected")
	}
	if got.PromoteRefused.ExpectedCurrentRelease != "v1.3.0" {
		t.Errorf("the caller's claim must be echoed, got %q", got.PromoteRefused.ExpectedCurrentRelease)
	}
	if got.PromoteRefused.ActualPromotedAt == "" {
		t.Error("actual_promoted_at must be carried so an operator can see how stale their view was")
	}
	if !strings.Contains(got.PromoteRefused.Detail, "v1.4.0") ||
		!strings.Contains(got.PromoteRefused.Detail, "v1.3.0") {
		t.Errorf("detail must name both releases, got %q", got.PromoteRefused.Detail)
	}
	// The FRESH plan comes back, so the caller can re-render the real diff
	// without another round trip.
	if len(got.Report) == 0 {
		t.Error("the fresh plan must accompany a refusal")
	}
}

func TestForgePromoteApplySucceedsWhenExpectedReleaseMatches(t *testing.T) {
	dir := forgeProject(t)
	log := stubForgeSequence(t,
		forgeCommandResult{Stdout: []byte(promotePlanJSON("staging", "v1.5.15", "v1.3.0", true, true, false, "ahead"))},
		forgeCommandResult{Stdout: []byte(promotePlanJSON("staging", "v1.5.15", "v1.3.0", true, false, true, "ahead"))},
	)

	raw, err := handle(t, "forge.promote_apply", map[string]any{
		"project_path": dir, "env": "staging", "release": "v1.5.15",
		"expected_current_release": "v1.3.0",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !log.wrote() {
		t.Fatal("a matching expectation must proceed to the real promote")
	}

	got := decodeForgePromoteApply(t, raw)
	if got.PromoteRefused != nil {
		t.Fatalf("must not refuse a matching expectation: %+v", got.PromoteRefused)
	}

	// The APPLIED document is returned, not the preview. The caller shows
	// what happened rather than assuming its preview was still accurate.
	var facts forgePromotePlanFacts
	if err := json.Unmarshal(got.Report, &facts); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if !facts.Applied {
		t.Error("the returned plan must be the APPLIED one (applied:true), not the preview")
	}
	if facts.DryRun {
		t.Error("the returned plan must not be a dry run")
	}
}

func TestForgePromoteApplyGuardHandlesUnboundEnvs(t *testing.T) {
	dir := forgeProject(t)

	t.Run("expect_unbound matches a first promote", func(t *testing.T) {
		log := stubForgeSequence(t,
			forgeCommandResult{Stdout: []byte(promotePlanJSON("dev", "v1.5.15", "", false, true, false, "initial"))},
			forgeCommandResult{Stdout: []byte(promotePlanJSON("dev", "v1.5.15", "", false, false, true, "initial"))},
		)
		raw, err := handle(t, "forge.promote_apply", map[string]any{
			"project_path": dir, "env": "dev", "release": "v1.5.15", "expect_unbound": true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if decodeForgePromoteApply(t, raw).PromoteRefused != nil {
			t.Error("a first promote against an unbound env must be allowed")
		}
		if !log.wrote() {
			t.Error("expected the write to proceed")
		}
	})

	t.Run("expect_unbound refuses an env that IS bound", func(t *testing.T) {
		// The dangerous direction: the caller believes this env has never
		// been promoted, but it is live on v1.4.0. Writing would silently
		// overwrite it.
		log := stubForgeSequence(t,
			forgeCommandResult{Stdout: []byte(promotePlanJSON("prod", "v1.5.15", "v1.4.0", true, true, false, "ahead"))},
		)
		raw, err := handle(t, "forge.promote_apply", map[string]any{
			"project_path": dir, "env": "prod", "release": "v1.5.15", "expect_unbound": true,
		})
		if err != nil {
			t.Fatalf("a refusal is data: %v", err)
		}
		if log.wrote() {
			t.Fatal("must not overwrite a bound env the caller thought was unbound")
		}
		refusal := decodeForgePromoteApply(t, raw).PromoteRefused
		if refusal == nil {
			t.Fatal("expected a refusal")
		}
		if !refusal.ExpectedUnbound || !refusal.ActualBound {
			t.Errorf("the mismatch must be legible: %+v", refusal)
		}
	})

	t.Run("a named expectation refuses an env with no binding", func(t *testing.T) {
		log := stubForgeSequence(t,
			forgeCommandResult{Stdout: []byte(promotePlanJSON("dev", "v1.5.15", "", false, true, false, "initial"))},
		)
		raw, err := handle(t, "forge.promote_apply", map[string]any{
			"project_path": dir, "env": "dev", "release": "v1.5.15",
			"expected_current_release": "v1.3.0",
		})
		if err != nil {
			t.Fatalf("a refusal is data: %v", err)
		}
		if log.wrote() {
			t.Fatal("a disagreement about state must not write")
		}
		if decodeForgePromoteApply(t, raw).PromoteRefused == nil {
			t.Error("expected a refusal when the expected release does not exist")
		}
	})
}

// A confirmation token is MANDATORY. An unset one must not be readable as "I saw
// nothing", because that would let an empty payload authorise a blind overwrite.
func TestForgePromoteApplyRequiresAConfirmationToken(t *testing.T) {
	dir := forgeProject(t)

	for _, tc := range []struct {
		name    string
		payload map[string]any
	}{
		{
			name: "neither expectation stated",
			payload: map[string]any{
				"project_path": dir, "env": "staging", "release": "v1.5.15",
			},
		},
		{
			name: "contradictory expectations",
			payload: map[string]any{
				"project_path": dir, "env": "staging", "release": "v1.5.15",
				"expected_current_release": "v1.3.0", "expect_unbound": true,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := stubForgeSequence(t)
			if _, err := handle(t, "forge.promote_apply", tc.payload); err == nil {
				t.Error("expected an error when the confirmation token is not a clear claim")
			}
			if len(log.Args) != 0 {
				t.Errorf("forge must not run at all for an unauthorised request, ran %v", log.Args)
			}
		})
	}
}

// --- rollback visibility ---

// A rollback is LEGITIMATE and must not be suppressed — but it must be visible.
// A "removed" image means a workload would be DELETED, which is the single most
// consequential fact this path can carry, and the daemon must neither block it
// nor quietly launder it into a forward move.
func TestForgePromoteRollbackIsReportedNotSuppressed(t *testing.T) {
	dir := forgeProject(t)
	rollback := `{"env":"prod","dry_run":true,"applied":false,` +
		`"direction":"behind",` +
		`"direction_detail":"ROLLBACK — v1.3.0 moves the environment BACKWARDS",` +
		`"current":{"bound":true,"release":"v1.5.15","promoted_at":"2026-07-01T00:25:15Z"},` +
		`"target":{"release":"v1.3.0"},` +
		`"images":[{"image":"internal-console","change":"removed","current_digest":"sha256:1ef891e"}],` +
		`"tally":{"unchanged":0,"changed":3,"added":0,"removed":1},` +
		`"commits":{"state":"dirty_release","reverts":true},` +
		`"ships_nothing":true,"next_step":"forge env deploy prod","ok":true}`

	t.Run("on the plan path", func(t *testing.T) {
		stubForge(t, forgeCommandResult{Stdout: []byte(rollback)}, nil)
		raw, err := handle(t, "forge.promote_plan", map[string]any{
			"project_path": dir, "env": "prod", "release": "v1.3.0",
		})
		if err != nil {
			t.Fatalf("a rollback is a legitimate plan, not an error: %v", err)
		}

		var parsed struct {
			Direction       string `json:"direction"`
			DirectionDetail string `json:"direction_detail"`
			Images          []struct {
				Image  string `json:"image"`
				Change string `json:"change"`
			} `json:"images"`
			Tally struct {
				Removed int `json:"removed"`
			} `json:"tally"`
			Commits struct {
				Reverts bool `json:"reverts"`
			} `json:"commits"`
		}
		if err := json.Unmarshal(decodeForgeResponse(t, raw).Report, &parsed); err != nil {
			t.Fatalf("unmarshal report: %v", err)
		}
		if parsed.Direction != "behind" {
			t.Errorf(`direction must stay "behind", got %q`, parsed.Direction)
		}
		if !strings.Contains(parsed.DirectionDetail, "ROLLBACK") {
			t.Errorf("direction_detail must still say ROLLBACK, got %q", parsed.DirectionDetail)
		}
		if len(parsed.Images) != 1 || parsed.Images[0].Change != "removed" {
			t.Errorf(`a removed workload must survive verbatim, got %+v`, parsed.Images)
		}
		if parsed.Tally.Removed != 1 {
			t.Errorf("tally.removed must stay 1 — a rollback would DELETE a workload, got %d", parsed.Tally.Removed)
		}
		if !parsed.Commits.Reverts {
			t.Error("commits.reverts must survive: the listed commits are being taken AWAY")
		}
	})

	t.Run("on the apply path, with a matching expectation", func(t *testing.T) {
		// A rollback is not blocked here. The guard's job is agreement
		// about CURRENT state, not a policy opinion about direction —
		// forge owns the verdict and the operator owns the decision.
		applied := strings.Replace(
			strings.Replace(rollback, `"dry_run":true`, `"dry_run":false`, 1),
			`"applied":false`, `"applied":true`, 1)
		log := stubForgeSequence(t,
			forgeCommandResult{Stdout: []byte(rollback)},
			forgeCommandResult{Stdout: []byte(applied)},
		)

		raw, err := handle(t, "forge.promote_apply", map[string]any{
			"project_path": dir, "env": "prod", "release": "v1.3.0",
			"expected_current_release": "v1.5.15",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !log.wrote() {
			t.Fatal("a rollback with a matching expectation must be allowed to proceed")
		}
		got := decodeForgePromoteApply(t, raw)
		if got.PromoteRefused != nil {
			t.Fatalf("a rollback is legitimate; only a stale binding refuses: %+v", got.PromoteRefused)
		}
		var parsed struct {
			Direction string `json:"direction"`
			Applied   bool   `json:"applied"`
		}
		if err := json.Unmarshal(got.Report, &parsed); err != nil {
			t.Fatalf("unmarshal report: %v", err)
		}
		if parsed.Direction != "behind" || !parsed.Applied {
			t.Errorf("the applied rollback must still report itself as a rollback, got %+v", parsed)
		}
	})
}

// --- forge too old, on BOTH commands ---

func TestForgePromoteUnsupportedForgeOnBothCommands(t *testing.T) {
	// The pinned forge's phrasing for a flag it does not have. `--plan` and
	// `--json` on `env promote` are exactly the kind of flag an older forge
	// lacks, and the result must be structured and version-stamped — not a
	// crash, and emphatically not a silent apparent success.
	stderr := []byte("unknown flag: --plan\n")

	t.Run("promote_plan", func(t *testing.T) {
		dir := forgeProject(t)
		stubForge(t, forgeCommandResult{Stderr: stderr, ExitCode: 1}, nil)

		raw, err := handle(t, "forge.promote_plan", map[string]any{
			"project_path": dir, "env": "staging", "release": "v1.5.15",
		})
		if err != nil {
			t.Fatalf("an old forge must not be an error: %v", err)
		}
		got := decodeForgeResponse(t, raw)
		if got.Supported {
			t.Error("supported must be false")
		}
		if !got.IsForgeProject {
			t.Error("is_forge_project must still be true")
		}
		if got.ForgeVersion == "" {
			t.Error("forge_version must be stamped so the UI can explain WHY")
		}
		if !strings.Contains(got.UnsupportedReason, "unknown flag") {
			t.Errorf("unsupported_reason should carry forge's complaint, got %q", got.UnsupportedReason)
		}
	})

	t.Run("promote_apply", func(t *testing.T) {
		dir := forgeProject(t)
		log := stubForgeSequence(t, forgeCommandResult{Stderr: stderr, ExitCode: 1})

		raw, err := handle(t, "forge.promote_apply", map[string]any{
			"project_path": dir, "env": "staging", "release": "v1.5.15",
			"expected_current_release": "v1.3.0",
		})
		if err != nil {
			t.Fatalf("an old forge must not be an error: %v", err)
		}
		// This is the important half: an unsupported forge is detected on
		// the READ-ONLY guard call, so no write is ever attempted. A version
		// miss must not become a half-executed promote.
		if log.wrote() {
			t.Fatal("an unsupported forge must be detected before any write is attempted")
		}
		if len(log.Args) != 1 {
			t.Errorf("only the guard plan should have run, got %v", log.Args)
		}

		got := decodeForgePromoteApply(t, raw)
		if got.Supported {
			t.Error("supported must be false")
		}
		if got.ForgeVersion == "" {
			t.Error("forge_version must be stamped")
		}
		if got.PromoteRefused != nil {
			t.Error("an old forge is a capability miss, not a stale-binding refusal — they are different conditions")
		}
	})
}

// --- not a forge project, on BOTH commands ---

func TestForgePromoteNotAForgeProjectOnBothCommands(t *testing.T) {
	for _, tc := range []struct {
		command string
		payload map[string]any
	}{
		{"forge.promote_plan", map[string]any{"env": "staging", "release": "v1.5.15"}},
		{"forge.promote_apply", map[string]any{"env": "staging", "release": "v1.5.15",
			"expected_current_release": "v1.3.0"}},
	} {
		t.Run(tc.command, func(t *testing.T) {
			dir := t.TempDir() // exists, no forge.yaml
			log := stubForgeSequence(t)

			payload := map[string]any{"project_path": dir}
			for k, v := range tc.payload {
				payload[k] = v
			}
			raw, err := handle(t, tc.command, payload)
			if err != nil {
				t.Fatalf("a non-forge project is a normal answer, not an error: %v", err)
			}
			if len(log.Args) != 0 {
				t.Errorf("forge must not be invoked for a non-forge project, ran %v", log.Args)
			}

			got := decodeForgePromoteApply(t, raw)
			if got.IsForgeProject {
				t.Error("is_forge_project must be false without forge.yaml")
			}
			if got.Supported {
				t.Error("supported must be false when no forge command ran")
			}
			if got.ForgeVersion == "" {
				t.Error("forge_version must be stamped even for a non-forge project")
			}
			if len(got.Report) != 0 {
				t.Errorf("no report expected, got %s", got.Report)
			}
			if got.PromoteRefused != nil {
				t.Error("a non-forge project is not a stale-binding refusal")
			}
		})
	}
}

// --- payload validation ---

func TestForgePromoteRequiredFieldsAreValidated(t *testing.T) {
	dir := forgeProject(t)

	for _, tc := range []struct {
		name    string
		command string
		payload map[string]any
	}{
		{"plan without project_path", "forge.promote_plan",
			map[string]any{"env": "staging", "release": "v1.5.15"}},
		{"plan without env", "forge.promote_plan",
			map[string]any{"project_path": dir, "release": "v1.5.15"}},
		{"plan without release", "forge.promote_plan",
			map[string]any{"project_path": dir, "env": "staging"}},
		{"apply without env", "forge.promote_apply",
			map[string]any{"project_path": dir, "release": "v1.5.15", "expected_current_release": "v1.3.0"}},
		{"apply without release", "forge.promote_apply",
			map[string]any{"project_path": dir, "env": "staging", "expected_current_release": "v1.3.0"}},
		// An env or release beginning with '-' would be consumed by forge as
		// a FLAG, which on the apply path means an argument deciding whether
		// a write happens.
		{"flag-shaped release", "forge.promote_apply",
			map[string]any{"project_path": dir, "env": "staging", "release": "--to",
				"expected_current_release": "v1.3.0"}},
		{"flag-shaped env", "forge.promote_apply",
			map[string]any{"project_path": dir, "env": "--plan", "release": "v1.5.15",
				"expected_current_release": "v1.3.0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := stubForgeSequence(t)
			if _, err := handle(t, tc.command, tc.payload); err == nil {
				t.Error("expected a validation error")
			}
			if len(log.Args) != 0 {
				t.Errorf("forge must not run for an invalid request, ran %v", log.Args)
			}
		})
	}
}

func TestForgePromoteInvalidPayloadIsRejected(t *testing.T) {
	for _, command := range []string{"forge.promote_plan", "forge.promote_apply"} {
		t.Run(command, func(t *testing.T) {
			stubForgeSequence(t)
			if _, err := DefaultRegistry().Handle(context.Background(), command, []byte("{not json")); err == nil {
				t.Fatal("expected an error for a malformed payload")
			}
		})
	}
}

func TestForgePromoteMissingPathUsesStableErrorPrefix(t *testing.T) {
	for _, command := range []string{"forge.promote_plan", "forge.promote_apply"} {
		t.Run(command, func(t *testing.T) {
			stubForgeSequence(t)
			_, err := handle(t, command, map[string]any{
				"project_path": t.TempDir() + "/definitely-absent",
				"env":          "staging", "release": "v1.5.15",
				"expected_current_release": "v1.3.0",
			})
			if err == nil {
				t.Fatal("expected an error for a missing project path")
			}
			if !strings.HasPrefix(err.Error(), forgeProjectDirNotExistPrefix) {
				t.Errorf("error must start with %q, got %q", forgeProjectDirNotExistPrefix, err)
			}
		})
	}
}

// --- the plan must really be a plan ---

// If the "read-only" preview comes back claiming it was applied, --plan did not
// suppress the write and the guard read state that had ALREADY been mutated.
// Writing again on top of that is the worst available outcome, so it refuses.
func TestForgePromoteApplyRefusesWhenThePreviewClaimsItWrote(t *testing.T) {
	dir := forgeProject(t)
	for _, tc := range []struct {
		name string
		doc  string
	}{
		{"applied true", promotePlanJSON("staging", "v1.5.15", "v1.3.0", true, true, true, "ahead")},
		{"dry_run false", promotePlanJSON("staging", "v1.5.15", "v1.3.0", true, false, false, "ahead")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := stubForgeSequence(t, forgeCommandResult{Stdout: []byte(tc.doc)})
			_, err := handle(t, "forge.promote_apply", map[string]any{
				"project_path": dir, "env": "staging", "release": "v1.5.15",
				"expected_current_release": "v1.3.0",
			})
			if err == nil {
				t.Fatal("a preview that claims it wrote must halt the promote")
			}
			if !strings.HasPrefix(err.Error(), forgeCommandFailedPrefix) {
				t.Errorf("error must be prefix-matched, got %q", err)
			}
			if log.wrote() {
				t.Fatal("must not write on top of a preview that already wrote")
			}
		})
	}
}

// A plan about a different environment than the one requested means something is
// wrong with the argv or with forge; either way it must not authorise a write.
func TestForgePromoteApplyRefusesAPlanForADifferentEnv(t *testing.T) {
	dir := forgeProject(t)
	log := stubForgeSequence(t, forgeCommandResult{
		Stdout: []byte(promotePlanJSON("prod", "v1.5.15", "v1.3.0", true, true, false, "ahead")),
	})

	_, err := handle(t, "forge.promote_apply", map[string]any{
		"project_path": dir, "env": "staging", "release": "v1.5.15",
		"expected_current_release": "v1.3.0",
	})
	if err == nil {
		t.Fatal("a plan for the wrong env must not authorise a write")
	}
	if log.wrote() {
		t.Fatal("nothing may be written when the guard plan is about another env")
	}
}

// --- failure mapping ---

func TestForgePromoteApplyNonZeroWithNoReportIsAnError(t *testing.T) {
	dir := forgeProject(t)
	log := stubForgeSequence(t, forgeCommandResult{
		Stderr:   []byte("release ledger v9.9.9 not found\n"),
		ExitCode: 1,
	})

	_, err := handle(t, "forge.promote_apply", map[string]any{
		"project_path": dir, "env": "staging", "release": "v9.9.9",
		"expected_current_release": "v1.3.0",
	})
	if err == nil {
		t.Fatal("expected an error for a non-zero exit with no report")
	}
	if !strings.HasPrefix(err.Error(), forgeCommandFailedPrefix) {
		t.Errorf("error must start with %q, got %q", forgeCommandFailedPrefix, err)
	}
	if log.wrote() {
		t.Fatal("a failed guard plan must not be followed by a write")
	}
}

// --- no deploy command ---

// Deploy mutates a LIVE CLUSTER rather than a file in the repo — a different
// risk class, and a separate decision. This asserts the daemon surface did not
// quietly grow one alongside promote.
func TestNoForgeDeployCommandIsRegistered(t *testing.T) {
	for _, name := range []string{"forge.deploy", "forge.env_deploy", "forge.promote_deploy"} {
		if _, err := DefaultRegistry().Handle(context.Background(), name, []byte(`{}`)); err == nil {
			t.Errorf("%s is registered — deploying to a live cluster is a separate risk class "+
				"and was deliberately not added with the promote path", name)
		}
	}
}
