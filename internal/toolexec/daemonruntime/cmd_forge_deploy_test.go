// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// NO TEST IN THIS FILE INVOKES A REAL FORGE, AND FOR THIS COMMAND THAT IS THE
// SINGLE MOST IMPORTANT PROPERTY IN THE PACKAGE.
//
// `forge env deploy <env>` without --dry-run/--explain APPLIES MANIFESTS TO A
// LIVE CLUSTER. control-plane's prod env declares
// gke_reliant-labs-475814_us-central1_prod. Unlike promote — which writes one
// file in the repo, recoverable from git — a deploy is not recoverable from
// anywhere. So every test here substitutes BOTH runner seams (runForge for the
// synchronous plan, runForgeDeploy for the detached apply) and asserts against
// canned bytes. stubNoForgeDeploy below installs a runner that FAILS the test if
// the apply seam is ever reached unintentionally.
//
// Under test: argv construction, the declared-context guard, the bound-release
// guard, that start does not block, the job lifecycle, and that a killed job
// reports UNKNOWN rather than either success or failure.

// deployPlanJSON builds a document shaped like forge's real
// `env deploy --dry-run --json` output. Nested guard/target, because that is the
// actual contract — the guard reads guard.declared_context, and a flattened
// fixture would let a broken guard pass.
func deployPlanJSON(env, mode, declaredContext, currentContext, verdict, release string) string {
	doc := map[string]any{
		"env":  env,
		"mode": mode,
		"guard": map[string]any{
			"declared_context": declaredContext,
			"current_context":  currentContext,
			"verdict":          verdict,
			"reason":           "context_declared",
		},
		"target": map[string]any{
			"kube_context":      declaredContext,
			"namespace":         "control-plane-" + env,
			"all_kube_contexts": []string{declaredContext},
		},
		"image_tag":  "v1.5.15",
		"tag_source": "release",
		"preflight":  map[string]any{"status": "ran", "findings": []any{}, "blocking": 0},
		"images": map[string]any{
			"images": []any{map[string]any{
				"reference":  "us-central1-docker.pkg.dev/p/r/admin-server@sha256:abc",
				"repository": "us-central1-docker.pkg.dev/p/r/admin-server",
				"pinning":    "digest",
			}},
			"digest_count": 1, "tag_count": 0, "no_digest_requested": false,
		},
		"resources": []any{map[string]any{
			"api_version": "apps/v1", "kind": "Deployment", "name": "admin-server",
		}},
		"rollout": map[string]any{
			"mode": "wait", "timeout_seconds": 300, "results": []any{},
			"ready": 0, "failed": 0, "timed_out": 0, "not_waited": 0,
		},
		"ok":        true,
		"exit_code": 0,
	}
	if release != "" {
		doc["release"] = release
	}
	b, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// deployAppliedJSON builds an APPLY report whose rollout results carry the
// states this layer must never reinterpret.
func deployAppliedJSON(env string, results []map[string]any, ok bool) string {
	tally := map[string]int{"ready": 0, "failed": 0, "timed_out": 0, "not_waited": 0}
	for _, r := range results {
		if state, isString := r["state"].(string); isString {
			tally[state]++
		}
	}
	generic := make([]any, len(results))
	for i, r := range results {
		generic[i] = r
	}
	doc := map[string]any{
		"env":  env,
		"mode": "apply",
		"guard": map[string]any{
			"declared_context": "gke_prod", "current_context": "k3d-control-plane",
			"verdict": "allow", "reason": "context_declared",
		},
		"target": map[string]any{"kube_context": "gke_prod", "namespace": "control-plane-" + env},
		"rollout": map[string]any{
			"mode": "wait", "timeout_seconds": 300, "results": generic,
			"ready": tally["ready"], "failed": tally["failed"],
			"timed_out": tally["timed_out"], "not_waited": tally["not_waited"],
		},
		"ok":          ok,
		"exit_code":   0,
		"duration_ms": 41234,
	}
	if !ok {
		doc["exit_code"] = 1
	}
	b, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// stubForgeDeploy installs a runner for the DETACHED apply seam.
func stubForgeDeploy(t *testing.T, res forgeCommandResult, err error) *forgeCall {
	t.Helper()
	call := &forgeCall{}
	prev := runForgeDeploy
	runForgeDeploy = func(_ context.Context, projectDir string, args []string) (forgeCommandResult, error) {
		call.Count++
		call.ProjectDir = projectDir
		call.Args = args
		return res, err
	}
	t.Cleanup(func() { runForgeDeploy = prev })
	return call
}

// stubNoForgeDeploy installs an apply runner that FAILS the test if it is ever
// invoked. Every refusal test uses it: the assertion those tests exist to make
// is that no apply was attempted.
func stubNoForgeDeploy(t *testing.T) {
	t.Helper()
	prev := runForgeDeploy
	runForgeDeploy = func(_ context.Context, _ string, args []string) (forgeCommandResult, error) {
		t.Errorf("the apply seam was invoked — a real deploy would have run: %v", args)
		return forgeCommandResult{}, nil
	}
	t.Cleanup(func() { runForgeDeploy = prev })
}

func decodeDeployStart(t *testing.T, raw []byte) forgeDeployStartResponse {
	t.Helper()
	var got forgeDeployStartResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal start response: %v (%s)", err, raw)
	}
	return got
}

func decodeDeployStatus(t *testing.T, raw []byte) forgeDeployStatusResponse {
	t.Helper()
	var got forgeDeployStatusResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal status response: %v (%s)", err, raw)
	}
	return got
}

// waitForDeployJob polls until the job leaves running, or fails the test. The
// job finishes on its own goroutine, so there is no synchronous point to hook.
func waitForDeployJob(t *testing.T, handle string) forgeDeployStatusResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := handleForgeDeployStatus(context.Background(),
			mustDeployPayload(t, forgeDeployStatusRequest{Handle: handle}))
		if err != nil {
			t.Fatalf("deploy_status: %v", err)
		}
		got := decodeDeployStatus(t, raw)
		if got.JobStatus != forgeDeployJobStatusRunning {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("deploy job %s never left running", handle)
	return forgeDeployStatusResponse{}
}

func mustDeployPayload(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

// startRequest is a fully-authorised start request against the canned plan.
func startRequest(dir, env, declaredContext, release string) forgeDeployStartRequest {
	return forgeDeployStartRequest{
		forgeDeployArgs:         forgeDeployArgs{ProjectPath: dir, Env: env},
		ExpectedDeclaredContext: declaredContext,
		ExpectedCurrentRelease:  release,
	}
}

// =============================================================================
// The plan/start split
// =============================================================================

// TestForgeDeployIsThreeDistinctCommands pins the central safety decision. A
// single command with a dry_run bool would make FALSE — the destructive value —
// what a caller gets by OMITTING the field.
func TestForgeDeployIsThreeDistinctCommands(t *testing.T) {
	registry := DefaultRegistry()
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	for _, name := range []string{"forge.deploy_plan", "forge.deploy_start", "forge.deploy_status"} {
		if registry.handlers[name] == nil {
			t.Errorf("%s is not registered", name)
		}
	}

	// Distinct request types, so a plan payload cannot become a deploy.
	if reflect.TypeOf(forgeDeployPlanRequest{}) == reflect.TypeOf(forgeDeployStartRequest{}) {
		t.Error("plan and start must be distinct request types")
	}
}

// TestForgeDeployRequestsCarryNoEscapeHatches is the reflection test the brief
// asks for, extended past dry_run to the two flags that must never be reachable
// over an RPC.
//
// --skip-preflight bypasses the check that referenced Secret keys and images
// exist on the LIVE target before anything is applied. --no-digest ships a
// mutable tag in place of an immutable digest. Both are for a human who has
// decided; a field for either would be set once as a workaround and stay set.
func TestForgeDeployRequestsCarryNoEscapeHatches(t *testing.T) {
	banned := []string{
		"dry_run", "dryrun", "plan", "apply", "write", "confirm",
		"skip_preflight", "skippreflight", "no_digest", "nodigest",
		"rollout", "prune", "force",
	}

	var walk func(typ reflect.Type, path string, seen map[reflect.Type]bool)
	walk = func(typ reflect.Type, path string, seen map[reflect.Type]bool) {
		for typ.Kind() == reflect.Ptr || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := strings.ToLower(strings.Split(field.Tag.Get("json"), ",")[0])
			if name == "" {
				name = strings.ToLower(field.Name)
			}
			for _, b := range banned {
				if name == b {
					t.Errorf("%s.%s carries %q — a deploy request must not expose it: "+
						"a boolean's zero value is the unsafe choice, and the preflight and "+
						"digest-pinning flags are deliberate human escape hatches",
						path, field.Name, name)
				}
			}
			walk(field.Type, path+"."+field.Name, seen)
		}
	}

	for _, typ := range []reflect.Type{
		reflect.TypeOf(forgeDeployPlanRequest{}),
		reflect.TypeOf(forgeDeployStartRequest{}),
		reflect.TypeOf(forgeDeployStatusRequest{}),
	} {
		walk(typ, typ.Name(), map[reflect.Type]bool{})
	}
}

// The argv for the apply carries neither escape hatch either — the request shape
// is only half the guarantee.
func TestForgeDeployArgvNeverCarriesEscapeHatches(t *testing.T) {
	args := forgeDeployArgs{ProjectPath: "/p", Env: "prod"}
	for _, argv := range [][]string{args.planArgs(), args.applyArgs()} {
		joined := strings.Join(argv, " ")
		for _, flag := range []string{"--skip-preflight", "--no-digest"} {
			if strings.Contains(joined, flag) {
				t.Errorf("argv %q carries %s", joined, flag)
			}
		}
	}
	if !strings.Contains(strings.Join(args.planArgs(), " "), "--dry-run") {
		t.Error("the plan argv must carry --dry-run")
	}
	if strings.Contains(strings.Join(args.applyArgs(), " "), "--dry-run") {
		t.Error("the apply argv must NOT carry --dry-run")
	}
}

// =============================================================================
// deploy_plan — read-only
// =============================================================================

// The plan returns forge's document verbatim, including fields this layer knows
// nothing about, and never reports mode apply.
func TestForgeDeployPlan_ReturnsDocumentVerbatim(t *testing.T) {
	dir := forgeProject(t)
	report := deployPlanJSON("prod", "dry_run", "gke_prod", "k3d-control-plane", "allow", "v1.5.15")
	// A field a newer forge might add: passthrough means it must survive.
	report = strings.TrimSuffix(report, "}") + `,"a_field_added_by_a_newer_forge":42}`

	call := stubForge(t, forgeCommandResult{Stdout: []byte(report)}, nil)
	stubNoForgeDeploy(t)

	raw, err := handleForgeDeployPlan(context.Background(), mustDeployPayload(t,
		forgeDeployPlanRequest{forgeDeployArgs{ProjectPath: dir, Env: "prod"}}))
	if err != nil {
		t.Fatalf("deploy_plan: %v", err)
	}

	var got forgeReportResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Supported || !got.IsForgeProject {
		t.Fatalf("expected a supported forge project, got %+v", got.forgeResponseMeta)
	}

	// Byte-for-byte, including the unknown field.
	var want, have any
	if err := json.Unmarshal([]byte(report), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got.Report, &have); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, have) {
		t.Errorf("report was not passed through verbatim:\n want %s\n  got %s", report, got.Report)
	}

	// The argv is a dry run, and it is the ONLY invocation.
	if call.Count != 1 {
		t.Fatalf("expected exactly one forge call, got %d", call.Count)
	}
	joined := strings.Join(call.Args, " ")
	if joined != "env deploy prod --dry-run --json" {
		t.Errorf("unexpected plan argv: %q", joined)
	}

	// And the document the caller sees never claims an apply happened.
	var facts forgeDeployPlanFacts
	if err := json.Unmarshal(got.Report, &facts); err != nil {
		t.Fatal(err)
	}
	if facts.Mode != forgeDeployModeDryRun {
		t.Errorf("plan reported mode %q, want %q", facts.Mode, forgeDeployModeDryRun)
	}
}

// A guard verdict of refuse on the read-only plan is DATA: the plan succeeds and
// carries forge's verdict, because "you cannot deploy this" is the answer the
// preview was asked for.
func TestForgeDeployPlan_GuardRefuseIsData(t *testing.T) {
	dir := forgeProject(t)
	report := deployPlanJSON("prod", "dry_run", "gke_prod", "", "refuse", "v1.5.15")
	stubForge(t, forgeCommandResult{Stdout: []byte(report), ExitCode: 1}, nil)
	stubNoForgeDeploy(t)

	raw, err := handleForgeDeployPlan(context.Background(), mustDeployPayload(t,
		forgeDeployPlanRequest{forgeDeployArgs{ProjectPath: dir, Env: "prod"}}))
	if err != nil {
		t.Fatalf("a guard refusal must not be an error: %v", err)
	}

	var got forgeReportResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.ExitCode != 1 {
		t.Errorf("forge's exit code must survive as data, got %d", got.ExitCode)
	}
	var facts forgeDeployPlanFacts
	if err := json.Unmarshal(got.Report, &facts); err != nil {
		t.Fatal(err)
	}
	if facts.Guard.Verdict != "refuse" {
		t.Errorf("guard verdict %q was not surfaced", facts.Guard.Verdict)
	}
}

// =============================================================================
// deploy_start — the guards. Each of these must start NOTHING.
// =============================================================================

// THE HEADLINE GUARD. The env's KCL now declares a different cluster than the
// one the operator saw named, so the deploy is refused. This is the check that
// does not exist on the promote path, and the one that stops a preview of
// "k3d-control-plane" turning into an apply against production.
func TestForgeDeployStart_RefusesChangedDeclaredContext(t *testing.T) {
	dir := forgeProject(t)
	// The plan comes back declaring PROD.
	plan := deployPlanJSON("prod", "dry_run", "gke_reliant-labs-475814_us-central1_prod",
		"k3d-control-plane", "allow", "v1.5.15")
	stubForge(t, forgeCommandResult{Stdout: []byte(plan)}, nil)
	stubNoForgeDeploy(t)

	// The caller authorised a deploy to the LOCAL cluster.
	raw, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t,
		startRequest(dir, "prod", "k3d-control-plane", "v1.5.15")))
	if err != nil {
		t.Fatalf("a refusal is structured data, not an error: %v", err)
	}

	got := decodeDeployStart(t, raw)
	if got.Handle != "" {
		t.Errorf("a refused deploy must return no handle, got %q", got.Handle)
	}
	if got.DeployRefused == nil {
		t.Fatal("expected a refusal")
	}
	if got.DeployRefused.Reason != forgeDeployRefusalReasonStaleDeclaredContext {
		t.Errorf("reason = %q, want %q", got.DeployRefused.Reason,
			forgeDeployRefusalReasonStaleDeclaredContext)
	}
	// The refusal must name what it FOUND, or the only recovery is a blind
	// retry against production.
	if got.DeployRefused.ActualDeclaredContext != "gke_reliant-labs-475814_us-central1_prod" {
		t.Errorf("the refusal must carry the context actually found, got %q",
			got.DeployRefused.ActualDeclaredContext)
	}
	if got.DeployRefused.ExpectedDeclaredContext != "k3d-control-plane" {
		t.Errorf("the refusal must echo the caller's claim, got %q",
			got.DeployRefused.ExpectedDeclaredContext)
	}
	// And it carries the FRESH plan so the caller can re-render.
	if len(got.Report) == 0 {
		t.Error("a refusal must carry the fresh plan")
	}
}

// The env is bound to a different release than the caller reviewed, so the
// deploy would ship digests nobody looked at.
func TestForgeDeployStart_RefusesChangedBoundRelease(t *testing.T) {
	dir := forgeProject(t)
	plan := deployPlanJSON("staging", "dry_run", "vke-staging", "k3d-control-plane", "allow", "v1.6.0")
	stubForge(t, forgeCommandResult{Stdout: []byte(plan)}, nil)
	stubNoForgeDeploy(t)

	raw, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t,
		startRequest(dir, "staging", "vke-staging", "v1.5.15")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := decodeDeployStart(t, raw)
	if got.Handle != "" {
		t.Errorf("a refused deploy must return no handle, got %q", got.Handle)
	}
	if got.DeployRefused == nil ||
		got.DeployRefused.Reason != forgeDeployRefusalReasonStaleCurrentRelease {
		t.Fatalf("expected a stale-release refusal, got %+v", got.DeployRefused)
	}
	if got.DeployRefused.ActualCurrentRelease != "v1.6.0" || !got.DeployRefused.ActualBound {
		t.Errorf("the refusal must carry the binding found: %+v", got.DeployRefused)
	}
}

// An env with no binding, claimed as bound. The two unbound spellings are
// different claims and the guard must not treat an unset field as either.
func TestForgeDeployStart_RefusesUnboundEnvClaimedAsBound(t *testing.T) {
	dir := forgeProject(t)
	// No "release" key at all — forge's unbound shape.
	plan := deployPlanJSON("dev", "dry_run", "k3d-control-plane", "k3d-control-plane", "allow", "")
	stubForge(t, forgeCommandResult{Stdout: []byte(plan)}, nil)
	stubNoForgeDeploy(t)

	raw, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t,
		startRequest(dir, "dev", "k3d-control-plane", "v1.5.15")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := decodeDeployStart(t, raw)
	if got.DeployRefused == nil || got.DeployRefused.ActualBound {
		t.Fatalf("expected an unbound refusal, got %+v", got.DeployRefused)
	}
}

// forge's OWN guard refusal is surfaced as a structured refusal rather than left
// in the report body for a caller to remember to check.
func TestForgeDeployStart_SurfacesForgeGuardRefusal(t *testing.T) {
	dir := forgeProject(t)
	plan := deployPlanJSON("prod", "dry_run", "gke_prod", "", "refuse", "v1.5.15")
	stubForge(t, forgeCommandResult{Stdout: []byte(plan), ExitCode: 1}, nil)
	stubNoForgeDeploy(t)

	raw, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t,
		startRequest(dir, "prod", "gke_prod", "v1.5.15")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := decodeDeployStart(t, raw)
	if got.DeployRefused == nil ||
		got.DeployRefused.Reason != forgeDeployRefusalReasonGuardRefused {
		t.Fatalf("expected a guard_refused refusal, got %+v", got.DeployRefused)
	}
	if got.DeployRefused.GuardVerdict != "refuse" {
		t.Errorf("the refusal must carry forge's verdict, got %q", got.DeployRefused.GuardVerdict)
	}
}

// A preview that did not run in dry_run mode means --dry-run failed to suppress
// the apply, so the guard read state that may already be mutated. An
// UNRECOGNISED mode lands here too, which is the safe direction.
func TestForgeDeployStart_RefusesPreviewThatApplied(t *testing.T) {
	for _, mode := range []string{"apply", "rollback", "unknown", "a_mode_from_a_newer_forge"} {
		t.Run(mode, func(t *testing.T) {
			dir := forgeProject(t)
			plan := deployPlanJSON("prod", mode, "gke_prod", "k3d", "allow", "v1.5.15")
			stubForge(t, forgeCommandResult{Stdout: []byte(plan)}, nil)
			stubNoForgeDeploy(t)

			_, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t,
				startRequest(dir, "prod", "gke_prod", "v1.5.15")))
			if err == nil {
				t.Fatalf("mode %q must refuse to deploy", mode)
			}
			if !strings.Contains(err.Error(), "refusing to deploy") {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// The confirmation token is mandatory on both halves, and rejected BEFORE any
// forge process starts.
func TestForgeDeployStart_RequiresConfirmationToken(t *testing.T) {
	dir := forgeProject(t)
	cases := map[string]forgeDeployStartRequest{
		"no declared context": {
			forgeDeployArgs:        forgeDeployArgs{ProjectPath: dir, Env: "prod"},
			ExpectedCurrentRelease: "v1.5.15",
		},
		"no release claim": {
			forgeDeployArgs:         forgeDeployArgs{ProjectPath: dir, Env: "prod"},
			ExpectedDeclaredContext: "gke_prod",
		},
		"contradictory release claim": {
			forgeDeployArgs:         forgeDeployArgs{ProjectPath: dir, Env: "prod"},
			ExpectedDeclaredContext: "gke_prod",
			ExpectedCurrentRelease:  "v1.5.15",
			ExpectUnbound:           true,
		},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			call := stubForge(t, forgeCommandResult{}, nil)
			stubNoForgeDeploy(t)
			if _, err := handleForgeDeployStart(context.Background(),
				mustDeployPayload(t, req)); err == nil {
				t.Fatal("expected a validation error")
			}
			if call.Count != 0 {
				t.Errorf("an unauthorised request must not spawn forge (ran %d times)", call.Count)
			}
		})
	}
}

// An env name beginning with '-' would be read by forge as a flag — which is how
// --no-digest or --skip-preflight would be reachable after all.
func TestForgeDeployStart_RejectsFlagShapedEnv(t *testing.T) {
	call := stubForge(t, forgeCommandResult{}, nil)
	stubNoForgeDeploy(t)
	_, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t,
		startRequest(forgeProject(t), "--no-digest", "gke_prod", "v1.5.15")))
	if err == nil || !strings.Contains(err.Error(), "read as a flag") {
		t.Fatalf("expected a flag-shaped-env rejection, got %v", err)
	}
	if call.Count != 0 {
		t.Errorf("forge must not be spawned, ran %d times", call.Count)
	}
}

// =============================================================================
// deploy_start — the happy path detaches
// =============================================================================

// Start returns a handle WITHOUT waiting for the deploy. The stub apply blocks
// until released, so a start that blocked would time out here.
func TestForgeDeployStart_ReturnsHandleWithoutBlocking(t *testing.T) {
	dir := forgeProject(t)
	plan := deployPlanJSON("prod", "dry_run", "gke_prod", "k3d-control-plane", "allow", "v1.5.15")
	planCall := stubForge(t, forgeCommandResult{Stdout: []byte(plan)}, nil)

	release := make(chan struct{})
	applied := deployAppliedJSON("prod",
		[]map[string]any{{"kind": "Deployment", "name": "admin-server", "state": "ready"}}, true)
	prev := runForgeDeploy
	runForgeDeploy = func(_ context.Context, _ string, args []string) (forgeCommandResult, error) {
		<-release
		if strings.Contains(strings.Join(args, " "), "--dry-run") {
			t.Error("the apply must not be a dry run")
		}
		return forgeCommandResult{Stdout: []byte(applied)}, nil
	}
	t.Cleanup(func() { runForgeDeploy = prev })

	done := make(chan forgeDeployStartResponse, 1)
	go func() {
		raw, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t,
			startRequest(dir, "prod", "gke_prod", "v1.5.15")))
		if err != nil {
			t.Errorf("deploy_start: %v", err)
			close(done)
			return
		}
		done <- decodeDeployStart(t, raw)
	}()

	var got forgeDeployStartResponse
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("deploy_start blocked on the apply — it must detach and return a handle immediately")
	}

	if got.Handle == "" {
		t.Fatal("expected a handle")
	}
	if got.JobStatus != forgeDeployJobStatusRunning {
		t.Errorf("job_status = %q, want %q", got.JobStatus, forgeDeployJobStatusRunning)
	}
	if got.DeployRefused != nil {
		t.Errorf("unexpected refusal: %+v", got.DeployRefused)
	}
	// The start reply carries the GUARD PLAN, not an apply report.
	var facts forgeDeployPlanFacts
	if err := json.Unmarshal(got.Report, &facts); err != nil {
		t.Fatal(err)
	}
	if facts.Mode != forgeDeployModeDryRun {
		t.Errorf("the start reply must carry the read-only plan, got mode %q", facts.Mode)
	}
	if planCall.Count != 1 {
		t.Errorf("expected one guard plan, got %d", planCall.Count)
	}

	// Still running while the apply is blocked.
	raw, err := handleForgeDeployStatus(context.Background(),
		mustDeployPayload(t, forgeDeployStatusRequest{Handle: got.Handle}))
	if err != nil {
		t.Fatalf("deploy_status: %v", err)
	}
	if status := decodeDeployStatus(t, raw); status.JobStatus != forgeDeployJobStatusRunning {
		t.Errorf("job_status = %q while the apply is in flight, want running", status.JobStatus)
	}

	// Then it completes, and the apply report arrives.
	close(release)
	finished := waitForDeployJob(t, got.Handle)
	if finished.JobStatus != forgeDeployJobStatusCompleted {
		t.Fatalf("job_status = %q (%s), want completed",
			finished.JobStatus, finished.JobStatusDetail)
	}
	if finished.FinishedAt == "" {
		t.Error("a finished job must report finished_at")
	}
	var appliedFacts struct {
		Mode    string `json:"mode"`
		Rollout struct {
			Results []struct {
				State string `json:"state"`
			} `json:"results"`
		} `json:"rollout"`
	}
	if err := json.Unmarshal(finished.Report, &appliedFacts); err != nil {
		t.Fatal(err)
	}
	if appliedFacts.Mode != "apply" {
		t.Errorf("the finished report must be the apply, got mode %q", appliedFacts.Mode)
	}
}

// A second deploy of the same env is refused with the in-flight handle rather
// than queued: two concurrent applies race each other's rollout.
func TestForgeDeployStart_RefusesConcurrentDeployOfSameEnv(t *testing.T) {
	dir := forgeProject(t)
	plan := deployPlanJSON("prod", "dry_run", "gke_prod", "k3d", "allow", "v1.5.15")
	stubForge(t, forgeCommandResult{Stdout: []byte(plan)}, nil)

	release := make(chan struct{})
	prev := runForgeDeploy
	runForgeDeploy = func(_ context.Context, _ string, _ []string) (forgeCommandResult, error) {
		<-release
		return forgeCommandResult{Stdout: []byte(deployAppliedJSON("prod", nil, true))}, nil
	}
	t.Cleanup(func() {
		close(release)
		runForgeDeploy = prev
	})

	first, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t,
		startRequest(dir, "prod", "gke_prod", "v1.5.15")))
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	firstHandle := decodeDeployStart(t, first).Handle

	second, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t,
		startRequest(dir, "prod", "gke_prod", "v1.5.15")))
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	got := decodeDeployStart(t, second)
	if got.DeployRefused == nil ||
		got.DeployRefused.Reason != forgeDeployRefusalReasonAlreadyRunning {
		t.Fatalf("expected an already_running refusal, got %+v", got.DeployRefused)
	}
	if got.DeployRefused.RunningHandle != firstHandle {
		t.Errorf("the refusal must name the in-flight handle: got %q want %q",
			got.DeployRefused.RunningHandle, firstHandle)
	}
}

// =============================================================================
// The rollout states must survive as THEMSELVES
// =============================================================================

// timed_out and not_waited are the ABSENCE of an answer. Nothing on this path
// may turn either into a success — that is the green-deploy-over-a-broken-
// environment failure forge's RolloutPolicy exists to prevent.
func TestForgeDeployStatus_RolloutTimedOutAndNotWaitedSurvive(t *testing.T) {
	dir := forgeProject(t)
	plan := deployPlanJSON("prod", "dry_run", "gke_prod", "k3d", "allow", "v1.5.15")
	stubForge(t, forgeCommandResult{Stdout: []byte(plan)}, nil)

	results := []map[string]any{
		{"kind": "Deployment", "name": "admin-server", "state": "ready"},
		{"kind": "Deployment", "name": "reliant-api", "state": "timed_out",
			"detail": "readiness budget expired with no verdict"},
		{"kind": "Deployment", "name": "internal-console", "state": "not_waited"},
		{"kind": "Job", "name": "migrate", "state": "failed", "detail": "condition=failed"},
	}
	// forge exits 1 for a rollout that did not converge; the report is still
	// the answer.
	stubForgeDeploy(t, forgeCommandResult{
		Stdout:   []byte(deployAppliedJSON("prod", results, false)),
		ExitCode: 1,
	}, nil)

	raw, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t,
		startRequest(dir, "prod", "gke_prod", "v1.5.15")))
	if err != nil {
		t.Fatalf("deploy_start: %v", err)
	}
	finished := waitForDeployJob(t, decodeDeployStart(t, raw).Handle)

	// The JOB completed — forge ran and produced a report. That is NOT a
	// claim the deploy succeeded.
	if finished.JobStatus != forgeDeployJobStatusCompleted {
		t.Fatalf("job_status = %q, want completed", finished.JobStatus)
	}
	if finished.ExitCode != 1 {
		t.Errorf("forge's exit code must survive, got %d", finished.ExitCode)
	}

	var doc struct {
		OK      bool `json:"ok"`
		Rollout struct {
			Results []struct {
				Name   string `json:"name"`
				State  string `json:"state"`
				Detail string `json:"detail"`
			} `json:"results"`
			Ready     int `json:"ready"`
			Failed    int `json:"failed"`
			TimedOut  int `json:"timed_out"`
			NotWaited int `json:"not_waited"`
		} `json:"rollout"`
	}
	if err := json.Unmarshal(finished.Report, &doc); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if doc.OK {
		t.Error("ok must survive as false")
	}

	states := map[string]string{}
	for _, r := range doc.Rollout.Results {
		states[r.Name] = r.State
	}
	for name, want := range map[string]string{
		"admin-server":     "ready",
		"reliant-api":      "timed_out",
		"internal-console": "not_waited",
		"migrate":          "failed",
	} {
		if states[name] != want {
			t.Errorf("%s reported state %q, want %q — this layer must not reinterpret a rollout state",
				name, states[name], want)
		}
	}
	if doc.Rollout.TimedOut != 1 || doc.Rollout.NotWaited != 1 || doc.Rollout.Failed != 1 {
		t.Errorf("the tallies must survive separately: %+v", doc.Rollout)
	}
}

// =============================================================================
// A job that did not reach a determinate outcome reports UNKNOWN
// =============================================================================

// The invocation was killed — by the ceiling, by a signal, by the process
// dying. Manifests may or may not have landed, so the only honest answer is
// UNKNOWN: not success (which would paint a half-converged cluster green) and
// not failure (which would invite a retry against a cluster mid-rollout).
func TestForgeDeployStatus_KilledJobReportsUnknown(t *testing.T) {
	cases := map[string]struct {
		res forgeCommandResult
		err error
	}{
		"killed by the ceiling": {
			res: forgeCommandResult{Stderr: []byte("signal: killed")},
			err: context.DeadlineExceeded,
		},
		"process died": {
			res: forgeCommandResult{Stderr: []byte("signal: killed")},
			err: errors.New("signal: killed"),
		},
		"exited without a report": {
			res: forgeCommandResult{ExitCode: 2, Stderr: []byte("kubectl: connection refused")},
		},
		"unparseable output": {
			res: forgeCommandResult{Stdout: []byte("Deploying to prod...\n"), ExitCode: 0},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := forgeProject(t)
			plan := deployPlanJSON("prod", "dry_run", "gke_prod", "k3d", "allow", "v1.5.15")
			stubForge(t, forgeCommandResult{Stdout: []byte(plan)}, nil)
			stubForgeDeploy(t, tc.res, tc.err)

			raw, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t,
				startRequest(dir, "prod", "gke_prod", "v1.5.15")))
			if err != nil {
				t.Fatalf("deploy_start: %v", err)
			}
			finished := waitForDeployJob(t, decodeDeployStart(t, raw).Handle)

			if finished.JobStatus != forgeDeployJobStatusUnknown {
				t.Fatalf("job_status = %q, want %q — a job that started and did not "+
					"produce a clean outcome must be UNKNOWN, never success and never failure",
					finished.JobStatus, forgeDeployJobStatusUnknown)
			}
			if finished.JobStatusDetail == "" {
				t.Error("an unknown outcome must explain itself")
			}
		})
	}
}

// A forge too old to understand `env deploy --json` never deployed anything, so
// that — and only that — is a clean FAILED.
func TestForgeDeployStatus_UnsupportedForgeReportsFailed(t *testing.T) {
	dir := forgeProject(t)
	plan := deployPlanJSON("prod", "dry_run", "gke_prod", "k3d", "allow", "v1.5.15")
	stubForge(t, forgeCommandResult{Stdout: []byte(plan)}, nil)
	stubForgeDeploy(t, forgeCommandResult{
		ExitCode: 1,
		Stderr:   []byte("unknown flag: --json\n"),
	}, nil)

	raw, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t,
		startRequest(dir, "prod", "gke_prod", "v1.5.15")))
	if err != nil {
		t.Fatalf("deploy_start: %v", err)
	}
	finished := waitForDeployJob(t, decodeDeployStart(t, raw).Handle)

	if finished.JobStatus != forgeDeployJobStatusFailed {
		t.Fatalf("job_status = %q, want failed", finished.JobStatus)
	}
	if finished.Supported {
		t.Error("supported must be false for a forge that does not know the command")
	}
	if !strings.Contains(finished.JobStatusDetail, "nothing was applied") {
		t.Errorf("the detail must state that nothing shipped: %q", finished.JobStatusDetail)
	}
}

// An unknown handle is UNKNOWN, not an error and not a failure. The registry is
// in-memory, so a daemon that restarted mid-apply has lost a handle for a deploy
// that may well have landed.
func TestForgeDeployStatus_UnknownHandleIsUnknownNotError(t *testing.T) {
	raw, err := handleForgeDeployStatus(context.Background(), mustDeployPayload(t,
		forgeDeployStatusRequest{Handle: "00000000-0000-0000-0000-000000000000"}))
	if err != nil {
		t.Fatalf("an unknown handle must not be a transport error: %v", err)
	}
	got := decodeDeployStatus(t, raw)
	if got.JobStatus != forgeDeployJobStatusUnknown {
		t.Errorf("job_status = %q, want %q", got.JobStatus, forgeDeployJobStatusUnknown)
	}
	if !strings.Contains(got.JobStatusDetail, "may or may not") {
		t.Errorf("the detail must not assert an outcome: %q", got.JobStatusDetail)
	}
}

func TestForgeDeployStatus_RequiresHandle(t *testing.T) {
	if _, err := handleForgeDeployStatus(context.Background(),
		mustDeployPayload(t, forgeDeployStatusRequest{})); err == nil {
		t.Fatal("expected a missing-handle error")
	}
}

// The four job states are distinct strings. A collision would silently merge two
// outcomes the whole design exists to keep apart.
func TestForgeDeployJobStatusesAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range []string{
		forgeDeployJobStatusRunning, forgeDeployJobStatusCompleted,
		forgeDeployJobStatusFailed, forgeDeployJobStatusUnknown,
	} {
		if seen[s] {
			t.Errorf("duplicate job status %q", s)
		}
		seen[s] = true
	}
	if len(seen) != 4 {
		t.Fatalf("expected four distinct job statuses, got %d", len(seen))
	}
}

// The detached apply must NOT inherit the two-minute read-command cap: that cap
// is what would kill a deploy mid-rollout.
func TestForgeDeployHasItsOwnInvocationCeiling(t *testing.T) {
	if forgeDeployInvocationTimeout <= forgeInvocationTimeout {
		t.Fatalf("forgeDeployInvocationTimeout (%s) must exceed the shared read cap (%s): "+
			"`forge env deploy` waits up to 5 minutes PER RESOURCE, so the read cap would "+
			"kill it mid-rollout", forgeDeployInvocationTimeout, forgeInvocationTimeout)
	}
	// And it is a separate seam, so no change here can shorten the read path.
	if fmt.Sprintf("%p", runForge) == fmt.Sprintf("%p", runForgeDeploy) {
		t.Error("the deploy runner must be a distinct seam from the read runner")
	}
}

// =============================================================================
// Hosted: the endpoint IS the declared context
// =============================================================================

// hostedPlanFixture is forge's REAL `env deploy <hosted> --dry-run --json`
// output, captured from forge's own hosted CLI flow (TestHostedCLIEndToEnd:
// real KCL render, hosted ledger and provider, only the control plane's HTTP
// faked). For a hosted env forge reports the control plane's endpoint as
// guard.declared_context with reason control_plane_declared, and no kube
// context anywhere — so the SAME declared-context guard is the staleness
// identity, with no hosted branch in this file.
func hostedPlanFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const hostedFixtureEndpoint = "http://127.0.0.1:56171"

// A hosted plan, confirmed against the endpoint the operator saw, is accepted
// unchanged and detaches the apply.
func TestForgeDeployStart_AcceptsHostedPlanByEndpoint(t *testing.T) {
	dir := forgeProject(t)
	plan := hostedPlanFixture(t, "forge_hosted_deploy_dry_run.json")
	var facts forgeDeployPlanFacts
	if err := json.Unmarshal(plan, &facts); err != nil {
		t.Fatal(err)
	}
	if facts.Guard.DeclaredContext != hostedFixtureEndpoint || facts.Guard.Reason != "control_plane_declared" ||
		facts.Target.KubeContext != "" {
		t.Fatalf("fixture is not forge's hosted plan shape: %+v", facts)
	}
	stubForge(t, forgeCommandResult{Stdout: plan}, nil)

	applied := make(chan []string, 1)
	prev := runForgeDeploy
	runForgeDeploy = func(_ context.Context, _ string, args []string) (forgeCommandResult, error) {
		applied <- args
		return forgeCommandResult{Stdout: hostedPlanFixture(t, "forge_hosted_deploy_apply.json")}, nil
	}
	t.Cleanup(func() { runForgeDeploy = prev })

	raw, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t,
		startRequest(dir, "hosted", hostedFixtureEndpoint, "v1")))
	if err != nil {
		t.Fatalf("deploy_start: %v", err)
	}
	got := decodeDeployStart(t, raw)
	if got.DeployRefused != nil {
		t.Fatalf("a hosted plan confirmed against its endpoint was refused: %+v", got.DeployRefused)
	}
	if got.Handle == "" {
		t.Fatal("expected a handle")
	}
	select {
	case args := <-applied:
		if strings.Contains(strings.Join(args, " "), "--dry-run") {
			t.Errorf("the apply must not be a dry run: %v", args)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the apply never ran")
	}
	if finished := waitForDeployJob(t, got.Handle); finished.JobStatus != forgeDeployJobStatusCompleted {
		t.Errorf("job_status = %q (%s)", finished.JobStatus, finished.JobStatusDetail)
	}
}

// The endpoint is re-checked exactly like a kube context: a plan that now
// names a DIFFERENT control plane than the one the operator confirmed is
// refused as stale, and nothing is applied.
func TestForgeDeployStart_RefusesHostedPlanWithChangedEndpoint(t *testing.T) {
	dir := forgeProject(t)
	stubForge(t, forgeCommandResult{Stdout: hostedPlanFixture(t, "forge_hosted_deploy_dry_run.json")}, nil)
	stubNoForgeDeploy(t)

	raw, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t,
		startRequest(dir, "hosted", "https://api.reliant.dev", "v1")))
	if err != nil {
		t.Fatalf("a refusal is structured data, not an error: %v", err)
	}
	got := decodeDeployStart(t, raw)
	if got.DeployRefused == nil || got.DeployRefused.Reason != forgeDeployRefusalReasonStaleDeclaredContext {
		t.Fatalf("expected a stale_declared_context refusal, got %+v", got.DeployRefused)
	}
	if got.DeployRefused.ActualDeclaredContext != hostedFixtureEndpoint {
		t.Errorf("the refusal must name the endpoint found, got %q", got.DeployRefused.ActualDeclaredContext)
	}
}
