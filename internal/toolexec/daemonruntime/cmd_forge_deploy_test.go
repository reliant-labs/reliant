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
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
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

// testApprovedDigest is the plan digest the canned start requests approve. A
// fixed value so a test can assert the argv carries THIS digest rather than
// merely some digest.
const testApprovedDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

// testApprovedVersion is the auto version a plan-only stage would have cut —
// `<YYYYMMDD>.<HHMMSS>-<tree12>`, forge's O-15 shape.
const testApprovedVersion = "20261003.114500-abcdef123456"

// startRequest is a fully-authorised start request against the canned plan:
// both halves of the target token, plus the content approval (the digest of the
// plan the operator read, and the release that plan was computed for).
func startRequest(dir, env, declaredContext, release string) forgeDeployStartRequest {
	return forgeDeployStartRequest{
		forgeDeployArgs:         forgeDeployArgs{ProjectPath: dir, Env: env},
		ExpectedDeclaredContext: declaredContext,
		ExpectedCurrentRelease:  release,
		ApproveDigest:           testApprovedDigest,
		ReleaseVersion:          testApprovedVersion,
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

// testApproval mints an approval for tests that need the apply argv directly.
//
// It is NOT a hole in the guarantee the approval type exists to provide. That
// guarantee is about PRODUCTION reachability: within this package the only
// non-test producer is forgeDeployStaleState, and
// TestForgeDeployApprovalHasExactlyOneProducer pins that by searching the
// non-test sources. A test may of course construct one — it is testing the
// argv, not the authorisation — and the mutation test below proves the
// production path cannot.
func testApproval() forgeDeployApproval {
	return forgeDeployApproval{
		declaredContext: "gke_prod",
		release:         "v1.5.15",
		digest:          "sha256:" + strings.Repeat("ab", 32),
		releaseVersion:  "20261003.120000-abcdef123456",
	}
}

func mustApplyArgs(t *testing.T, args forgeDeployArgs) []string {
	t.Helper()
	argv, err := args.applyArgs(testApproval())
	if err != nil {
		t.Fatalf("applyArgs with a validated approval: %v", err)
	}
	return argv
}

// The argv for the apply carries neither escape hatch either — the request shape
// is only half the guarantee.
func TestForgeDeployArgvNeverCarriesEscapeHatches(t *testing.T) {
	args := forgeDeployArgs{ProjectPath: "/p", Env: "prod"}
	for _, argv := range [][]string{args.planArgs(), mustApplyArgs(t, args)} {
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
	if strings.Contains(strings.Join(mustApplyArgs(t, args), " "), "--dry-run") {
		t.Error("the apply argv must NOT carry --dry-run")
	}
}

// =============================================================================
// THE DAEMON NEVER PASSES --yes. Approval is by plan digest.
// =============================================================================

// NO ARGV THIS DAEMON BUILDS MAY CARRY --yes, on any path.
//
// --yes means "I read the plan" and approves whatever forge computes at the
// moment that command runs. Under O-15 a versionless deploy builds new images
// and cuts a NEW auto-version from the checkout, so --yes on a UI's behalf
// approves a change set nobody has seen. That was the interim this replaced.
//
// The guarantee is structural — no code path produces the flag — and this test
// is the proof, swept over every builder rather than asserted on one, because
// the failure mode is a NEW path acquiring it quietly.
func TestForgeDaemonArgvNeverCarriesYes(t *testing.T) {
	deploy := forgeDeployArgs{ProjectPath: "/p", Env: "prod"}
	promote := forgePromoteArgs{ProjectPath: "/p", Env: "prod", Release: "v1.2.3"}

	argv := map[string][]string{
		"deploy preview":   deploy.planArgs(),
		"deploy plan-only": deploy.planOnlyArgs(),
		"deploy apply":     mustApplyArgs(t, deploy),
		"promote plan":     promote.planArgs(),
		"promote apply":    mustPromoteApplyArgs(t, promote),
		"checkouts":        forgeCheckoutsRequest{ProjectPath: "/p"}.args(),
		"env diff":         forgeEnvDiffRequest{ProjectPath: "/p", All: true}.args(),
	}

	for name, args := range argv {
		if slices.Contains(args, "--yes") {
			t.Errorf("%s argv %q carries --yes: the daemon must never tell forge a human "+
				"approved a plan it has not seen. Approval travels as --approve <digest>", name, args)
		}
	}
}

// The apply argv carries the APPROVAL instead: the digest, the release version
// stage one cut, and the acknowledged codes when there are any.
func TestForgeDeployApplyArgvApprovesByDigest(t *testing.T) {
	args := forgeDeployArgs{ProjectPath: "/p", Env: "prod"}

	apply := mustApplyArgs(t, args)
	joined := strings.Join(apply, " ")

	if !slices.Contains(apply, "--approve") {
		t.Errorf("the apply argv %q must carry --approve: it binds the deploy to the plan a "+
			"human read, and forge refuses a mismatch (exit 3, plan_stale)", apply)
	}
	if !strings.Contains(joined, testApproval().digest) {
		t.Errorf("the apply argv %q must carry the approved digest verbatim", apply)
	}

	// THE VERSION IS POSITIONAL AND LOAD-BEARING. Without it the deploy is
	// versionless, which under O-15 builds and cuts a SECOND release whose
	// plan the approved digest could never match.
	version := testApproval().releaseVersion
	if !slices.Contains(apply, version) {
		t.Errorf("the apply argv %q must name the release version the plan was computed for (%s), "+
			"or it would build and cut a second release", apply, version)
	}
	if got, want := apply[:4], []string{"env", "deploy", "prod", version}; !slices.Equal(got, want) {
		t.Errorf("the release version must be forge's SECOND positional; got %q, want %q", got, want)
	}

	// The read-only preview approves nothing and must carry no approval.
	if plan := args.planArgs(); slices.Contains(plan, "--approve") {
		t.Errorf("the preview argv %q must NOT carry --approve: it is read-only", plan)
	}
	if planOnly := args.planOnlyArgs(); slices.Contains(planOnly, "--approve") {
		t.Errorf("the plan-only argv %q must NOT carry --approve: it PRODUCES the plan to "+
			"approve, it does not consume one", planOnly)
	}
}

// Stop-class findings travel as --acknowledge-destructive, comma-joined, and
// ONLY when the approval named some.
func TestForgeDeployApplyArgvCarriesAcknowledgedFindings(t *testing.T) {
	args := forgeDeployArgs{ProjectPath: "/p", Env: "prod"}

	approval := testApproval()
	approval.acknowledgedFindings = []string{"stateful_deletion", "lb_identity_change"}
	apply, err := args.applyArgs(approval)
	if err != nil {
		t.Fatalf("applyArgs: %v", err)
	}
	if got := strings.Join(apply, " "); !strings.Contains(got,
		"--acknowledge-destructive stateful_deletion,lb_identity_change") {
		t.Errorf("the apply argv must name every acknowledged code, comma-joined; got %q", got)
	}

	// Absent when there is nothing to acknowledge: an empty flag value
	// would read to forge as a code named "".
	if plain := mustApplyArgs(t, args); slices.Contains(plain, "--acknowledge-destructive") {
		t.Errorf("the apply argv %q must omit --acknowledge-destructive when no finding was "+
			"acknowledged", plain)
	}
}

// The approval is unreachable without BOTH halves of the content claim. A
// target token alone is what the interim had, and it is what this replaced.
func TestForgeDeployApplyArgvRefusesIncompleteApproval(t *testing.T) {
	args := forgeDeployArgs{ProjectPath: "/p", Env: "prod"}

	for name, approval := range map[string]forgeDeployApproval{
		"no digest": {
			declaredContext: "gke_prod", release: "v1", releaseVersion: "20261003.120000-abc",
		},
		"no release version": {
			declaredContext: "gke_prod", release: "v1", digest: "sha256:abc",
		},
		"target token only": {
			declaredContext: "gke_prod", release: "v1",
		},
	} {
		if argv, err := args.applyArgs(approval); err == nil {
			t.Errorf("%s: applyArgs minted %q from an incomplete approval; a deploy must name "+
				"both the plan it approves and the release that plan was computed for", name, argv)
		}
	}
}

// THE MUTATION TEST. --yes must be unreachable without an approval, and an
// approval must be unreachable without validation.
//
// Deleting validateConfirmation's declared-context check turns this red: an
// empty ExpectedDeclaredContext would then reach forgeDeployStaleState, which
// compares it against the plan's real context, refuses on the mismatch, and
// returns a ZERO approval — and applyArgs refuses to mint --yes from one.
func TestForgeDeployYesIsUnreachableWithoutAValidatedToken(t *testing.T) {
	args := forgeDeployArgs{ProjectPath: "/p", Env: "prod"}

	// A fabricated zero-value approval must not produce an argv at all.
	if argv, err := args.applyArgs(forgeDeployApproval{}); err == nil {
		t.Errorf("applyArgs minted %q from a zero approval; --yes must require a validated "+
			"confirmation token, or a future caller acquires it silently", argv)
	}

	// And the only producer refuses to mint one for every unauthorised
	// request shape, so there is no route from a bad request to --yes.
	facts := forgeDeployPlanFacts{Env: "prod", Mode: forgeDeployModeDryRun, Release: "v1.5.15"}
	facts.Guard.DeclaredContext = "gke_prod"
	facts.Guard.Verdict = forgeDeployGuardVerdictAllow

	for _, tc := range []struct {
		name string
		req  forgeDeployStartRequest
	}{
		{"no declared context at all", forgeDeployStartRequest{
			forgeDeployArgs:        args,
			ExpectedCurrentRelease: "v1.5.15",
		}},
		{"a declared context the plan disagrees with", forgeDeployStartRequest{
			forgeDeployArgs:         args,
			ExpectedDeclaredContext: "k3d-control-plane",
			ExpectedCurrentRelease:  "v1.5.15",
		}},
		{"a release the plan disagrees with", forgeDeployStartRequest{
			forgeDeployArgs:         args,
			ExpectedDeclaredContext: "gke_prod",
			ExpectedCurrentRelease:  "v1.0.0",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refusal, approval := forgeDeployStaleState(tc.req, facts)
			if refusal == nil {
				t.Fatal("expected a refusal; this request must not authorise a deploy")
			}
			if argv, err := args.applyArgs(approval); err == nil {
				t.Errorf("a refused request still produced the apply argv %q, carrying --yes", argv)
			}
		})
	}
}

// The approval type has exactly ONE producer in non-test code, which is what
// makes the guarantee structural rather than a convention a future caller can
// forget. Asserted against the sources because no type system here can say it.
func TestForgeDeployApprovalHasExactlyOneProducer(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	// `forgeDeployApproval{` is the only way to construct one — it has no
	// constructor and every field is unexported, so a literal is required.
	literal := regexp.MustCompile(`forgeDeployApproval\{`)
	producers := map[string]int{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if n := len(literal.FindAll(src, -1)); n > 0 {
			producers[name] = n
		}
	}

	// cmd_forge_deploy.go holds the type declaration, the zero values its
	// refusal arms return, and the one real mint in forgeDeployStaleState.
	// Any OTHER file constructing one is the regression this test exists for.
	for file := range producers {
		if file != "cmd_forge_deploy.go" {
			t.Errorf("%s constructs a forgeDeployApproval; only forgeDeployStaleState may mint one, "+
				"because an approval is what produces --yes on a live-cluster deploy", file)
		}
	}
	if producers["cmd_forge_deploy.go"] == 0 {
		t.Error("no forgeDeployApproval literal found in cmd_forge_deploy.go; this test is " +
			"passing vacuously and no longer guards --yes")
	}
}

// Exit 5 is plan_unconfirmed, NOT the old timeout. It must surface as a loud
// non-success naming plan_unconfirmed, and must never read as success or as a
// rollout still in flight.
func TestForgeDeployStatus_PlanUnconfirmedIsALoudFailure(t *testing.T) {
	dir := forgeProject(t)
	plan := deployPlanJSON("prod", "dry_run", "gke_prod", "gke_prod", "allow", "v1.5.15")
	stubForge(t, forgeCommandResult{Stdout: []byte(plan)}, nil)
	// forge's refusal: exit 5, the message on stderr, no document on stdout.
	stubForgeDeploy(t, forgeCommandResult{
		ExitCode: 5,
		Stderr:   []byte("the deploy plan was not confirmed (plan_unconfirmed), so NO promotion was written."),
	}, nil)

	raw, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t,
		startRequest(dir, "prod", "gke_prod", "v1.5.15")))
	if err != nil {
		t.Fatalf("deploy_start: %v", err)
	}
	status := waitForDeployJob(t, decodeDeployStart(t, raw).Handle)

	// NOTHING was applied, and that is knowable — so this is failed, not the
	// "manifests may have landed" hedge.
	if status.JobStatus != forgeDeployJobStatusFailed {
		t.Errorf("job_status = %q, want %q: exit 5 means forge wrote no promotion and applied "+
			"nothing, which must never read as success or as a rollout in flight",
			status.JobStatus, forgeDeployJobStatusFailed)
	}
	if status.ExitCode != 5 {
		t.Errorf("exit_code = %d, want 5 carried through verbatim", status.ExitCode)
	}
	if !strings.Contains(status.JobStatusDetail, "plan_unconfirmed") {
		t.Errorf("detail must name plan_unconfirmed so it cannot be read as the old exit-5 "+
			"timeout; got %q", status.JobStatusDetail)
	}
}

// Exit 8 is the wait-budget expiry — the case 5 used to mean. Manifests WERE
// applied, so it is genuinely indeterminate: unknown, naming the budget.
func TestForgeDeployStatus_WaitBudgetExpiredIsUnknown(t *testing.T) {
	dir := forgeProject(t)
	plan := deployPlanJSON("prod", "dry_run", "gke_prod", "gke_prod", "allow", "v1.5.15")
	stubForge(t, forgeCommandResult{Stdout: []byte(plan)}, nil)
	stubForgeDeploy(t, forgeCommandResult{
		ExitCode: 8,
		Stderr:   []byte("the wait's budget expired while the rollout was still progressing"),
	}, nil)

	raw, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t,
		startRequest(dir, "prod", "gke_prod", "v1.5.15")))
	if err != nil {
		t.Fatalf("deploy_start: %v", err)
	}
	status := waitForDeployJob(t, decodeDeployStart(t, raw).Handle)

	if status.JobStatus != forgeDeployJobStatusUnknown {
		t.Errorf("job_status = %q, want %q: the manifests were applied and the rollout was still "+
			"progressing, so the outcome is not established",
			status.JobStatus, forgeDeployJobStatusUnknown)
	}
	if strings.Contains(status.JobStatusDetail, "plan_unconfirmed") {
		t.Errorf("exit 8 must not be described as plan_unconfirmed; got %q", status.JobStatusDetail)
	}
}

// The real deploy invocation carries --yes end to end. Pinned at the seam the
// daemon actually hands forge, not at the argv builder, so a call site that
// bypassed the builder could not satisfy it.
// END TO END THROUGH THE HANDLER: the deploy forge is actually invoked with
// approves the plan by digest and never says --yes.
//
// Distinct from the argv-builder tests above: those prove the builder cannot
// produce --yes, this proves the HANDLER does not route around the builder.
func TestForgeDeployStart_InvokesForgeWithTheApprovedDigest(t *testing.T) {
	dir := forgeProject(t)
	plan := deployPlanJSON("prod", "dry_run", "gke_prod", "gke_prod", "allow", "v1.5.15")
	stubForge(t, forgeCommandResult{Stdout: []byte(plan)}, nil)
	apply := stubForgeDeploy(t, forgeCommandResult{
		Stdout: []byte(`{"env":"prod","mode":"apply","ok":true,"exit_code":0}`),
	}, nil)

	raw, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t,
		startRequest(dir, "prod", "gke_prod", "v1.5.15")))
	if err != nil {
		t.Fatalf("deploy_start: %v", err)
	}
	waitForDeployJob(t, decodeDeployStart(t, raw).Handle)

	if slices.Contains(apply.Args, "--yes") {
		t.Errorf("the apply invocation %q must NOT carry --yes: the daemon never tells forge a "+
			"human approved a plan it has not seen", apply.Args)
	}
	if !slices.Contains(apply.Args, "--approve") ||
		!slices.Contains(apply.Args, testApprovedDigest) {
		t.Errorf("the apply invocation %q must approve the plan by digest (%s)",
			apply.Args, testApprovedDigest)
	}
	// The version stage one cut, positionally — without it forge would
	// build and cut a second release whose plan the digest cannot match.
	if !slices.Contains(apply.Args, testApprovedVersion) {
		t.Errorf("the apply invocation %q must name the approved release version %s",
			apply.Args, testApprovedVersion)
	}
	if slices.Contains(apply.Args, "--dry-run") {
		t.Errorf("the apply invocation %q must not carry --dry-run", apply.Args)
	}
}

// A start with no approved digest is refused BEFORE any forge process runs.
// The target token alone is what the interim accepted, and it is not enough.
func TestForgeDeployStart_RefusesAStartWithNoApprovedPlan(t *testing.T) {
	dir := forgeProject(t)
	call := stubForge(t, forgeCommandResult{
		Stdout: []byte(deployPlanJSON("prod", "dry_run", "gke_prod", "gke_prod", "allow", "v1.5.15")),
	}, nil)

	for name, req := range map[string]forgeDeployStartRequest{
		"no digest": {
			forgeDeployArgs:         forgeDeployArgs{ProjectPath: dir, Env: "prod"},
			ExpectedDeclaredContext: "gke_prod",
			ExpectedCurrentRelease:  "v1.5.15",
			ReleaseVersion:          testApprovedVersion,
		},
		"no release version": {
			forgeDeployArgs:         forgeDeployArgs{ProjectPath: dir, Env: "prod"},
			ExpectedDeclaredContext: "gke_prod",
			ExpectedCurrentRelease:  "v1.5.15",
			ApproveDigest:           testApprovedDigest,
		},
	} {
		if _, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t, req)); err == nil {
			t.Errorf("%s: deploy_start accepted a request with no complete content approval", name)
		}
	}

	if call.Count != 0 {
		t.Errorf("an unauthorised start ran %d forge processes; it must be refused before forge "+
			"is invoked at all, since a plan-only run builds and pushes", call.Count)
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
	var firstHandle string
	t.Cleanup(func() {
		// Let the first job finish and WAIT for it before restoring the
		// seam. Its goroutine reads runForgeDeploy, and closing release
		// does not order that read before this write — only the job's
		// own terminal state does. Restoring early is a data race, and
		// leaves a running deploy in the shared registry besides.
		close(release)
		if firstHandle != "" {
			waitForDeployJob(t, firstHandle)
		}
		runForgeDeploy = prev
	})

	first, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t,
		startRequest(dir, "prod", "gke_prod", "v1.5.15")))
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	firstHandle = decodeDeployStart(t, first).Handle
	// Without a handle here the RunningHandle comparison below would pass
	// against "" without proving anything.
	if firstHandle == "" {
		t.Fatal("the first start must start a job and return its handle")
	}

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

// The in-flight check and the job's registration are separated by the guard
// plan — a forge subprocess that takes seconds — and the daemon dispatches every
// command on its own goroutine. A second start arriving inside that window must
// be refused too, or both see "nothing running" and both apply.
func TestForgeDeployStart_RefusesSecondStartDuringGuardPlan(t *testing.T) {
	dir := forgeProject(t)
	plan := deployPlanJSON("prod", "dry_run", "gke_prod", "k3d", "allow", "v1.5.15")
	req := startRequest(dir, "prod", "gke_prod", "v1.5.15")

	var applies atomic.Int32
	prevApply := runForgeDeploy
	runForgeDeploy = func(_ context.Context, _ string, _ []string) (forgeCommandResult, error) {
		applies.Add(1)
		return forgeCommandResult{Stdout: []byte(deployAppliedJSON("prod", nil, true))}, nil
	}

	// The second start is issued from INSIDE the first start's guard plan.
	// On the daemon these are two commands on two goroutines; nesting them
	// makes that interleaving deterministic.
	var second forgeDeployStartResponse
	planCalls := 0
	prevPlan := runForge
	runForge = func(_ context.Context, _ string, _ []string) (forgeCommandResult, error) {
		planCalls++
		if planCalls == 1 {
			raw, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t, req))
			if err != nil {
				t.Errorf("second start: %v", err)
			} else {
				second = decodeDeployStart(t, raw)
			}
		}
		return forgeCommandResult{Stdout: []byte(plan)}, nil
	}

	var started []string
	t.Cleanup(func() {
		// Every job this test started must settle before the seams it
		// reads are restored.
		for _, handle := range started {
			waitForDeployJob(t, handle)
		}
		runForge = prevPlan
		runForgeDeploy = prevApply
	})

	raw, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t, req))
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	first := decodeDeployStart(t, raw)
	for _, handle := range []string{first.Handle, second.Handle} {
		if handle != "" {
			started = append(started, handle)
		}
	}

	if second.DeployRefused == nil || second.DeployRefused.Reason != forgeDeployRefusalReasonAlreadyRunning {
		t.Fatalf("a start arriving during another start's guard plan must be refused as already_running; "+
			"got handle %q, refusal %+v", second.Handle, second.DeployRefused)
	}
	if second.Handle != "" {
		t.Errorf("a refused start must return no handle, got %q", second.Handle)
	}
	// The first start has no job yet, so there is no handle to name — and
	// the refusal must not invent one.
	if second.DeployRefused.RunningHandle != "" {
		t.Errorf("no job exists yet, so no running handle can be named, got %q",
			second.DeployRefused.RunningHandle)
	}
	if planCalls != 1 {
		t.Errorf("the refused start must not run a guard plan: %d plans ran", planCalls)
	}

	// The first start is unaffected and is the one deploy that runs.
	if first.DeployRefused != nil || first.Handle == "" {
		t.Fatalf("the first start must proceed: handle %q, refusal %+v", first.Handle, first.DeployRefused)
	}
	waitForDeployJob(t, first.Handle)
	if n := applies.Load(); n != 1 {
		t.Errorf("exactly one apply may run for one env, got %d", n)
	}
}

// A start that ends WITHOUT starting a job — refused, or failed before the
// apply — must give its claim back. A leaked claim would refuse every later
// deploy of that env as already_running until the daemon restarted.
func TestForgeDeployStart_EarlyExitReleasesTheClaim(t *testing.T) {
	cases := map[string]struct {
		plan    forgeCommandResult
		planErr error
		claimed string // the declared context the failing start asserts
	}{
		"stale declared context": {
			plan:    forgeCommandResult{Stdout: []byte(deployPlanJSON("prod", "dry_run", "gke_prod", "k3d", "allow", "v1.5.15"))},
			claimed: "k3d-control-plane",
		},
		"forge guard refusal": {
			plan:    forgeCommandResult{Stdout: []byte(deployPlanJSON("prod", "dry_run", "gke_prod", "", "refuse", "v1.5.15")), ExitCode: 1},
			claimed: "gke_prod",
		},
		"preview that applied": {
			plan:    forgeCommandResult{Stdout: []byte(deployPlanJSON("prod", "apply", "gke_prod", "k3d", "allow", "v1.5.15"))},
			claimed: "gke_prod",
		},
		"forge could not run": {
			planErr: errors.New("exec: forge: not found"),
			claimed: "gke_prod",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := forgeProject(t)
			stubForge(t, tc.plan, tc.planErr)
			stubNoForgeDeploy(t)

			raw, err := handleForgeDeployStart(context.Background(), mustDeployPayload(t,
				startRequest(dir, "prod", tc.claimed, "v1.5.15")))
			if err == nil && decodeDeployStart(t, raw).Handle != "" {
				t.Fatal("the setup must not start a job")
			}

			// Same env, a plan that would be accepted: it must start.
			stubForge(t, forgeCommandResult{
				Stdout: []byte(deployPlanJSON("prod", "dry_run", "gke_prod", "k3d", "allow", "v1.5.15")),
			}, nil)
			stubForgeDeploy(t, forgeCommandResult{Stdout: []byte(deployAppliedJSON("prod", nil, true))}, nil)

			raw, err = handleForgeDeployStart(context.Background(), mustDeployPayload(t,
				startRequest(dir, "prod", "gke_prod", "v1.5.15")))
			if err != nil {
				t.Fatalf("follow-up start: %v", err)
			}
			got := decodeDeployStart(t, raw)
			if got.DeployRefused != nil {
				t.Fatalf("the earlier start leaked its claim — the env is refused with nothing running: %+v",
					got.DeployRefused)
			}
			waitForDeployJob(t, got.Handle)
		})
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
