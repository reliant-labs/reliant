// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// STAGE ONE: the approvable plan.
//
// `--plan-only` builds, pushes and cuts, computes the §8.6 plan with its
// digest, and writes NO promotion. It is slow — minutes — so it runs detached
// through the same job machinery as the apply, and the plan arrives from
// deploy_status.
//
// These tests are the whole reason the plan-only path is separate from the
// instant `--dry-run` preview. The two documents look similar and describe
// different things: the preview describes the env's CURRENT binding, the
// plan-only document describes what a deploy would actually ship.

// planOnlyJSON is a canned `env deploy --plan-only --json` document, in the
// shape fbb71dee emits: target.release carries the AUTO version this stage cut,
// next_step is the exact --approve command, and deploy_plan is the §8.6 plan.
func planOnlyJSON(env, version, digest string, findings ...map[string]string) string {
	plan := map[string]any{
		"digest":           digest,
		"environment_id":   env,
		"bundle_id":        "bundle-" + version,
		"release_version":  version,
		"live_basis":       map[string]any{"drift_observed": false},
		"findings":         findings,
		"config_identical": false,
	}
	doc := map[string]any{
		"env":         env,
		"ok":          true,
		"exit_code":   0,
		"confirmed":   false,
		"applied":     false,
		"target":      map[string]any{"release": version},
		"next_step":   "forge env deploy " + env + " " + version + " --approve " + digest,
		"deploy_plan": plan,
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// The plan-only invocation carries --plan-only and nothing that writes.
func TestForgeDeployPlanStart_InvokesPlanOnly(t *testing.T) {
	dir := forgeProject(t)
	run := stubForgeDeploy(t, forgeCommandResult{
		Stdout: []byte(planOnlyJSON("prod", "20261003.114500-abcdef123456", "sha256:aaa")),
	}, nil)

	raw, err := handleForgeDeployPlanStart(context.Background(), mustDeployPayload(t,
		forgeDeployPlanStartRequest{forgeDeployArgs: forgeDeployArgs{ProjectPath: dir, Env: "prod"}}))
	if err != nil {
		t.Fatalf("deploy_plan_start: %v", err)
	}
	got := decodeDeployStart(t, raw)
	if got.Handle == "" {
		t.Fatal("plan-only must return a handle: it takes minutes and cannot be awaited inline")
	}
	if got.JobStatus != forgeDeployJobStatusRunning {
		t.Errorf("job status = %q, want running", got.JobStatus)
	}
	// NO plan document on the start reply — it does not exist yet. A reply
	// that carried one would invite a UI to approve before the build ran.
	if len(got.Report) != 0 {
		t.Errorf("the start reply must carry no plan: none has been computed yet; got %s", got.Report)
	}

	waitForDeployJob(t, got.Handle)

	if !slices.Contains(run.Args, "--plan-only") {
		t.Errorf("the stage-one invocation %q must carry --plan-only", run.Args)
	}
	for _, flag := range []string{"--yes", "--approve", "--dry-run"} {
		if slices.Contains(run.Args, flag) {
			t.Errorf("the stage-one invocation %q must not carry %s: it PRODUCES the plan "+
				"to approve and approves nothing itself", run.Args, flag)
		}
	}
}

// The plan document reaches the caller VERBATIM, and is marked as a plan.
func TestForgeDeployPlanStart_ReturnsThePlanVerbatim(t *testing.T) {
	dir := forgeProject(t)
	doc := planOnlyJSON("prod", "20261003.114500-abcdef123456", "sha256:deadbeef",
		map[string]string{"code": "image_changed", "class": "info", "section": "images",
			"subject": "api"})
	stubForgeDeploy(t, forgeCommandResult{Stdout: []byte(doc)}, nil)

	raw, err := handleForgeDeployPlanStart(context.Background(), mustDeployPayload(t,
		forgeDeployPlanStartRequest{forgeDeployArgs: forgeDeployArgs{ProjectPath: dir, Env: "prod"}}))
	if err != nil {
		t.Fatalf("deploy_plan_start: %v", err)
	}
	status := waitForDeployJob(t, decodeDeployStart(t, raw).Handle)

	if status.JobStatus != forgeDeployJobStatusCompleted {
		t.Fatalf("job status = %q (%s), want completed", status.JobStatus, status.JobStatusDetail)
	}
	// PlanOnly is STATED, not inferred. A caller must never have to guess
	// whether a report describes a plan or a deploy.
	if !status.PlanOnly {
		t.Error("a stage-one job's status must mark the report as a plan")
	}

	var got, want any
	if err := json.Unmarshal(status.Report, &got); err != nil {
		t.Fatalf("report is not forge's document: %v", err)
	}
	if err := json.Unmarshal([]byte(doc), &want); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if !jsonEqual(got, want) {
		t.Errorf("the plan was altered in transit:\n got %v\nwant %v", got, want)
	}
}

// A plan-only run writes nothing, so when it dies the environment is
// UNCHANGED. Reporting "manifests may have reached the cluster" there would
// send an operator to inspect production over a failed build.
func TestForgeDeployPlanStart_FailureIsNotIndeterminate(t *testing.T) {
	dir := forgeProject(t)

	for name, res := range map[string]struct {
		result forgeCommandResult
		err    error
	}{
		"could not run":       {forgeCommandResult{}, context.DeadlineExceeded},
		"no parseable output": {forgeCommandResult{ExitCode: 1, Stderr: []byte("boom")}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			stubForgeDeploy(t, res.result, res.err)

			raw, err := handleForgeDeployPlanStart(context.Background(), mustDeployPayload(t,
				forgeDeployPlanStartRequest{
					forgeDeployArgs: forgeDeployArgs{ProjectPath: dir, Env: "prod"},
				}))
			if err != nil {
				t.Fatalf("deploy_plan_start: %v", err)
			}
			status := waitForDeployJob(t, decodeDeployStart(t, raw).Handle)

			if status.JobStatus != forgeDeployJobStatusFailed {
				t.Errorf("a failed plan is FAILED, not %q: --plan-only writes no promotion and "+
					"applies nothing, so the environment is unchanged", status.JobStatus)
			}
			if strings.Contains(status.JobStatusDetail, "may or may not") {
				t.Errorf("a failed plan must not hedge about the cluster; it touched none: %q",
					status.JobStatusDetail)
			}
		})
	}
}

// One env at a time, across BOTH stages. A plan-only run builds and cuts, so
// it must not run underneath an in-flight deploy of the same env.
func TestForgeDeployPlanStart_RefusesWhileADeployIsInFlight(t *testing.T) {
	dir := forgeProject(t)
	plan := deployPlanJSON("prod", "dry_run", "gke_prod", "gke_prod", "allow", "v1.5.15")
	stubForge(t, forgeCommandResult{Stdout: []byte(plan)}, nil)

	// A deploy that does not finish until this test lets it, so the env
	// stays claimed while the plan-only start is attempted.
	//
	// IT IS DRAINED BEFORE THE TEST RETURNS, deliberately. The job runs on
	// its own goroutine and cleanups run LIFO, so a job still blocked when
	// the runner stub is torn down would wake up and call whatever stub the
	// NEXT test installed — which is a real apply seam invocation in a test
	// that asserts none happens. Measured: that is exactly what it did.
	block := make(chan struct{})
	prev := runForgeDeploy
	runForgeDeploy = func(ctx context.Context, _ string, _ []string) (forgeCommandResult, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return forgeCommandResult{Stdout: []byte(`{"ok":true}`)}, nil
	}
	t.Cleanup(func() { runForgeDeploy = prev })

	raw, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t,
		startRequest(dir, "prod", "gke_prod", "v1.5.15")))
	if err != nil {
		t.Fatalf("deploy_start: %v", err)
	}
	inFlight := decodeDeployStart(t, raw).Handle
	defer func() {
		close(block)
		waitForDeployJob(t, inFlight)
	}()

	planRaw, err := handleForgeDeployPlanStart(context.Background(), mustDeployPayload(t,
		forgeDeployPlanStartRequest{forgeDeployArgs: forgeDeployArgs{ProjectPath: dir, Env: "prod"}}))
	if err != nil {
		t.Fatalf("a refusal is structured data, not a transport error: %v", err)
	}
	got := decodeDeployStart(t, planRaw)
	if got.DeployRefused == nil ||
		got.DeployRefused.Reason != forgeDeployRefusalReasonAlreadyRunning {
		t.Fatalf("planning must be refused while a deploy of the same env is in flight; got %+v", got)
	}
	if got.Handle != "" {
		t.Error("a refused plan must return no handle")
	}
}

// The checkout is authorised before anything is built or claimed.
func TestForgeDeployPlanStart_RefusesAnUnlistedCheckout(t *testing.T) {
	dir := forgeProject(t)
	stubForge(t, forgeCommandResult{Stdout: []byte(checkoutsJSON(t.TempDir()))}, nil)
	stubNoForgeDeploy(t)

	if _, err := handleForgeDeployPlanStart(context.Background(), mustDeployPayload(t,
		forgeDeployPlanStartRequest{forgeDeployArgs: forgeDeployArgs{
			ProjectPath: dir, Env: "prod", CheckoutPath: t.TempDir(),
		}})); err == nil {
		t.Error("plan-only built from an unlisted checkout")
	}
}

func TestForgeDeployPlanStartIsRegistered(t *testing.T) {
	stubForge(t, forgeCommandResult{}, nil)
	// A non-forge dir short-circuits before forge would run.
	if _, err := handle(t, "forge.deploy_plan_start", map[string]any{
		"project_path": t.TempDir(), "env": "prod",
	}); err != nil {
		t.Fatalf("forge.deploy_plan_start not dispatchable: %v", err)
	}
}

// jsonEqual compares two decoded JSON values.
func jsonEqual(a, b any) bool {
	left, errA := json.Marshal(a)
	right, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(left) == string(right)
}
