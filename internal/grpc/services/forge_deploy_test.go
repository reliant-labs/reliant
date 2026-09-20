// Copyright (c) 2025 Reliant Labs
package services

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// NO TEST HERE REACHES A REAL FORGE, A REAL DAEMON OR A REAL CLUSTER, AND FOR
// THIS SURFACE THAT IS THE POINT.
//
// StartDeploy applies manifests to a LIVE CLUSTER. control-plane's prod env
// declares gke_reliant-labs-475814_us-central1_prod. Unlike promote — which moves
// a pointer in a file git can restore — a deploy is recoverable from nowhere.
// These tests stub the daemon round trip (forgeTestRouter, forge_test.go) and
// assert against canned replies.
//
// Under test: the three-method split, the confirmation token, the
// declared-context guard's wire shape, the job lifecycle, and that neither a
// rollout state nor an indeterminate job outcome can be laundered into success.

// deployReply builds a daemon reply for forge.deploy_plan / deploy_start /
// deploy_status: the standard forge envelope plus whatever extra fields the
// command carries.
func deployReply(t *testing.T, report string, extra map[string]any) []byte {
	t.Helper()
	env := map[string]any{
		"is_forge_project": true,
		"supported":        true,
		"forge_version":    "v0.1.15",
		"exit_code":        0,
	}
	if report != "" {
		// RawMessage, not a Go string. `env["report"] = report` marshals the
		// document as a JSON STRING, which the real daemon never sends — it
		// sends an OBJECT (daemonruntime.forgeReportResponse.Report is
		// json.RawMessage). That fixture bug is why a `string` field on
		// forgeReportReply passed every test here and then failed against a
		// live daemon with "cannot unmarshal object into Go struct field
		// forgeReportReply.report of type string", which surfaced as the
		// forge UI silently not appearing.
		env["report"] = json.RawMessage(report)
	}
	for k, v := range extra {
		env[k] = v
	}
	b, err := json.Marshal(env)
	require.NoError(t, err)
	return b
}

// deployPlanDoc is a document shaped like forge's real
// `env deploy --dry-run --json` output, trimmed to the fields a consumer reads.
func deployPlanDoc(env, mode, declaredContext, currentContext, verdict, release string) string {
	return fmt.Sprintf(`{"env":%q,"mode":%q,`+
		`"guard":{"declared_context":%q,"current_context":%q,"verdict":%q,"reason":"context_declared"},`+
		`"target":{"kube_context":%q,"namespace":"control-plane-%s","all_kube_contexts":[%q]},`+
		`"release":%q,"image_tag":"v1.5.15","tag_source":"release",`+
		`"preflight":{"status":"ran","findings":[],"blocking":0},`+
		`"images":{"images":[{"reference":"reg/admin-server@sha256:abc",`+
		`"repository":"reg/admin-server","pinning":"digest"}],`+
		`"digest_count":1,"tag_count":0,"no_digest_requested":false},`+
		`"resources":[{"api_version":"apps/v1","kind":"Deployment","name":"admin-server"}],`+
		`"rollout":{"mode":"wait","timeout_seconds":300,"results":[],`+
		`"ready":0,"failed":0,"timed_out":0,"not_waited":0},"ok":true,"exit_code":0}`,
		env, mode, declaredContext, currentContext, verdict,
		declaredContext, env, declaredContext, release)
}

// authorisedStart is a start request whose token matches deployPlanDoc's state.
func authorisedStart(env, declaredContext, release string) *reliantv1.StartForgeDeployRequest {
	return &reliantv1.StartForgeDeployRequest{
		ProjectId:               "p",
		Env:                     env,
		ExpectedDeclaredContext: declaredContext,
		ExpectedCurrentRelease:  release,
	}
}

// =============================================================================
// The three-method split
// =============================================================================

// TestForgeService_DeployIsThreeDistinctMethods pins the central safety
// decision. A single method with a dry_run bool would make FALSE — the
// destructive value — what a caller gets by OMITTING the field.
func TestForgeService_DeployIsThreeDistinctMethods(t *testing.T) {
	procedures := []string{
		"/reliant.v1.ForgeService/PlanDeploy",
		"/reliant.v1.ForgeService/StartDeploy",
		"/reliant.v1.ForgeService/GetDeployStatus",
	}
	seen := map[string]bool{}
	for _, p := range procedures {
		assert.False(t, seen[p], "duplicate procedure %s", p)
		seen[p] = true
	}

	// Distinct request TYPES, so a plan request cannot be sent to a deploy.
	types := []reflect.Type{
		reflect.TypeOf(reliantv1.PlanForgeDeployRequest{}),
		reflect.TypeOf(reliantv1.StartForgeDeployRequest{}),
		reflect.TypeOf(reliantv1.GetForgeDeployStatusRequest{}),
	}
	for i := range types {
		for j := i + 1; j < len(types); j++ {
			assert.NotEqual(t, types[i], types[j],
				"deploy request types must be distinct")
		}
	}

	// The methods exist on the service.
	svc := reflect.TypeOf(&ForgeService{})
	for _, name := range []string{"PlanDeploy", "StartDeploy", "GetDeployStatus"} {
		_, ok := svc.MethodByName(name)
		assert.True(t, ok, "ForgeService is missing %s", name)
	}
}

// TestForgeService_DeployRequestsCarryNoEscapeHatches is the reflection test,
// extended past dry_run to the two flags that must never be reachable over an
// RPC.
//
// --skip-preflight bypasses verifying that referenced Secret keys and container
// images exist on the LIVE target before applying. --no-digest ships a mutable
// tag in place of an immutable digest. Both are for a human at a terminal who has
// weighed the consequence; a field for either would be set once as a workaround
// and stay set.
func TestForgeService_DeployRequestsCarryNoEscapeHatches(t *testing.T) {
	banned := []string{
		"dry_run", "plan", "apply", "write", "commit", "confirm",
		"skip_preflight", "no_digest", "rollout", "prune", "force",
	}
	for _, typ := range []reflect.Type{
		reflect.TypeOf(reliantv1.PlanForgeDeployRequest{}),
		reflect.TypeOf(reliantv1.StartForgeDeployRequest{}),
		reflect.TypeOf(reliantv1.GetForgeDeployStatusRequest{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			name := protoJSONName(field)
			for _, b := range banned {
				assert.NotEqual(t, b, name,
					"%s carries %q. The plan/start split must be two METHODS, not a boolean "+
						"whose zero value is the destructive choice — and the preflight and "+
						"digest-pinning flags are deliberate human escape hatches that must not "+
						"be reachable from a browser", typ.Name(), name)
			}
		}
	}
}

// =============================================================================
// PlanDeploy — read-only
// =============================================================================

// The plan dispatches the read-only daemon command, passes forge's document
// through byte-for-byte including fields this layer does not know, and never
// reports mode apply.
func TestForgeService_PlanDeploy_PassesReportThroughVerbatim(t *testing.T) {
	report := strings.TrimSuffix(
		deployPlanDoc("prod", "dry_run", "gke_prod", "k3d-control-plane", "allow", "v1.5.15"),
		"}") + `,"a_field_added_by_a_newer_forge":42}`
	router := &forgeTestRouter{reply: deployReply(t, report, nil)}

	resp, err := newForgeTestService(router).PlanDeploy(authedCtx(),
		connect.NewRequest(&reliantv1.PlanForgeDeployRequest{ProjectId: "p", Env: "prod"}))
	require.NoError(t, err)

	assert.Equal(t, "forge.deploy_plan", router.lastCommandType,
		"the preview must dispatch the READ-ONLY daemon command")
	assert.JSONEq(t, report, resp.Msg.ReportJson,
		"forge's document must cross this layer unmodified")

	// The document the caller sees never claims an apply happened.
	var facts struct {
		Mode  string `json:"mode"`
		Guard struct {
			DeclaredContext string `json:"declared_context"`
			CurrentContext  string `json:"current_context"`
		} `json:"guard"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.Msg.ReportJson), &facts))
	assert.Equal(t, "dry_run", facts.Mode, "a preview must never report mode apply")

	// The declared context reaches the caller, which is what a confirmation
	// dialog is built around, and so does the ambient one — as information.
	assert.Equal(t, "gke_prod", facts.Guard.DeclaredContext)
	assert.Equal(t, "k3d-control-plane", facts.Guard.CurrentContext)

	// The env reaches the daemon as given.
	var sent struct {
		ProjectPath string `json:"project_path"`
		Env         string `json:"env"`
	}
	require.NoError(t, json.Unmarshal(router.lastPayload, &sent))
	assert.Equal(t, "prod", sent.Env)
	assert.Equal(t, "/daemon/workspace/control-plane", sent.ProjectPath)
}

// A guard verdict of refuse on the read-only plan is DATA: the RPC succeeds and
// carries forge's verdict, because "you cannot deploy this" is precisely the
// answer the preview was asked for. Swallowing it would make a UI render a
// deployable environment.
func TestForgeService_PlanDeploy_GuardRefuseIsSurfacedAsData(t *testing.T) {
	report := deployPlanDoc("prod", "dry_run", "gke_prod", "", "refuse", "v1.5.15")
	router := &forgeTestRouter{reply: forgeDaemonReply(t, true, true, 1, report)}

	resp, err := newForgeTestService(router).PlanDeploy(authedCtx(),
		connect.NewRequest(&reliantv1.PlanForgeDeployRequest{ProjectId: "p", Env: "prod"}))
	require.NoError(t, err, "a guard refusal is the answer, not a transport failure")

	assert.EqualValues(t, 1, resp.Msg.Meta.ExitCode,
		"forge's exit code must survive as data")

	var facts struct {
		Guard struct {
			Verdict string `json:"verdict"`
			Reason  string `json:"reason"`
		} `json:"guard"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.Msg.ReportJson), &facts))
	assert.Equal(t, "refuse", facts.Guard.Verdict,
		"the refusal verdict must reach the caller, not be swallowed")
}

func TestForgeService_PlanDeploy_RequiresEnv(t *testing.T) {
	router := &forgeTestRouter{}
	_, err := newForgeTestService(router).PlanDeploy(authedCtx(),
		connect.NewRequest(&reliantv1.PlanForgeDeployRequest{ProjectId: "p"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Empty(t, router.lastCommandType, "no daemon call for an invalid request")
}

// =============================================================================
// StartDeploy — the guards. Each of these must start NOTHING.
// =============================================================================

// THE HEADLINE GUARD. The environment's KCL now declares a different cluster than
// the one the operator saw named, so the deploy is refused — as a
// FailedPrecondition carrying BOTH contexts, because "someone changed it" is only
// actionable if the caller can see what it changed to.
func TestForgeService_StartDeploy_RefusesChangedDeclaredContext(t *testing.T) {
	const prodContext = "gke_reliant-labs-475814_us-central1_prod"
	refusal := map[string]any{
		"reason":                    "stale_declared_context",
		"expected_declared_context": "k3d-control-plane",
		"actual_declared_context":   prodContext,
		"guard_verdict":             "allow",
		"guard_reason":              "context_declared",
		"detail": `env "prod" now declares kubectl context "` + prodContext +
			`", not the "k3d-control-plane" this deploy was authorised against`,
	}
	router := &forgeTestRouter{
		reply: deployReply(t,
			deployPlanDoc("prod", "dry_run", prodContext, "k3d-control-plane", "allow", "v1.5.15"),
			map[string]any{"deploy_refused": refusal}),
	}

	// The caller authorised a deploy to the LOCAL cluster.
	_, err := newForgeTestService(router).StartDeploy(authedCtx(),
		connect.NewRequest(authorisedStart("prod", "k3d-control-plane", "v1.5.15")))

	require.Error(t, err, "a refusal must FAIL the RPC so an unchecked client fails closed")
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "nothing was applied")

	// The structured detail is what makes this recoverable without a blind retry
	// against production.
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	details := connectErr.Details()
	require.Len(t, details, 1, "the refusal must travel as a structured detail")
	msg, detailErr := details[0].Value()
	require.NoError(t, detailErr)
	got, ok := msg.(*reliantv1.ForgeDeployRefusal)
	require.True(t, ok, "detail must be a ForgeDeployRefusal, got %T", msg)

	assert.Equal(t,
		reliantv1.ForgeDeployRefusalReason_FORGE_DEPLOY_REFUSAL_REASON_STALE_DECLARED_CONTEXT,
		got.Reason)
	assert.Equal(t, prodContext, got.ActualDeclaredContext,
		"the refusal must name the cluster actually declared")
	assert.Equal(t, "k3d-control-plane", got.ExpectedDeclaredContext,
		"the refusal must echo the cluster the operator approved")
}

// The environment is bound to a different release than the caller reviewed, so
// the deploy would ship digests nobody looked at.
func TestForgeService_StartDeploy_RefusesChangedBoundRelease(t *testing.T) {
	refusal := map[string]any{
		"reason":                    "stale_current_release",
		"expected_declared_context": "vke-staging",
		"actual_declared_context":   "vke-staging",
		"expected_current_release":  "v1.5.15",
		"actual_current_release":    "v1.6.0",
		"actual_bound":              true,
		"detail":                    `env "staging" is bound to v1.6.0, not the expected v1.5.15`,
	}
	router := &forgeTestRouter{
		reply: deployReply(t,
			deployPlanDoc("staging", "dry_run", "vke-staging", "k3d", "allow", "v1.6.0"),
			map[string]any{"deploy_refused": refusal}),
	}

	_, err := newForgeTestService(router).StartDeploy(authedCtx(),
		connect.NewRequest(authorisedStart("staging", "vke-staging", "v1.5.15")))

	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	require.Len(t, connectErr.Details(), 1)
	msg, detailErr := connectErr.Details()[0].Value()
	require.NoError(t, detailErr)
	got := msg.(*reliantv1.ForgeDeployRefusal)

	assert.Equal(t,
		reliantv1.ForgeDeployRefusalReason_FORGE_DEPLOY_REFUSAL_REASON_STALE_CURRENT_RELEASE,
		got.Reason)
	assert.Equal(t, "v1.6.0", got.ActualCurrentRelease)
	assert.True(t, got.ActualBound)
}

// forge's OWN guard refusal, and an already-running deploy, both arrive as
// distinguishable FailedPrecondition reasons rather than as one generic failure.
func TestForgeService_StartDeploy_RefusalReasonsAreDistinguishable(t *testing.T) {
	cases := map[string]struct {
		refusal map[string]any
		want    reliantv1.ForgeDeployRefusalReason
		check   func(t *testing.T, got *reliantv1.ForgeDeployRefusal)
	}{
		"forge itself refuses": {
			refusal: map[string]any{
				"reason":        "guard_refused",
				"guard_verdict": "refuse",
				"guard_reason":  "declared_context_missing",
				"guard_fix":     "run: gcloud container clusters get-credentials ...",
				"detail":        "forge will not deploy env \"prod\"",
			},
			want: reliantv1.ForgeDeployRefusalReason_FORGE_DEPLOY_REFUSAL_REASON_GUARD_REFUSED,
			check: func(t *testing.T, got *reliantv1.ForgeDeployRefusal) {
				assert.Equal(t, "refuse", got.GuardVerdict)
				assert.Equal(t, "declared_context_missing", got.GuardReason)
				assert.NotEmpty(t, got.GuardFix, "forge's own fix must reach the caller")
			},
		},
		"a deploy is already in flight": {
			refusal: map[string]any{
				"reason":         "already_running",
				"running_handle": "handle-abc",
				"detail":         "a deploy of \"prod\" is already in flight",
			},
			want: reliantv1.ForgeDeployRefusalReason_FORGE_DEPLOY_REFUSAL_REASON_ALREADY_RUNNING,
			check: func(t *testing.T, got *reliantv1.ForgeDeployRefusal) {
				assert.Equal(t, "handle-abc", got.RunningHandle,
					"the caller must be told which handle to poll instead")
			},
		},
		"an unrecognised reason still refuses": {
			// A newer daemon's additional reason must not be laundered into a
			// started deploy: the RPC still fails, with UNSPECIFIED.
			refusal: map[string]any{
				"reason": "a_reason_from_a_newer_daemon",
				"detail": "something the daemon knows and this binary does not",
			},
			want:  reliantv1.ForgeDeployRefusalReason_FORGE_DEPLOY_REFUSAL_REASON_UNSPECIFIED,
			check: func(t *testing.T, got *reliantv1.ForgeDeployRefusal) {},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			router := &forgeTestRouter{
				reply: deployReply(t,
					deployPlanDoc("prod", "dry_run", "gke_prod", "k3d", "allow", "v1.5.15"),
					map[string]any{"deploy_refused": tc.refusal}),
			}
			_, err := newForgeTestService(router).StartDeploy(authedCtx(),
				connect.NewRequest(authorisedStart("prod", "gke_prod", "v1.5.15")))

			require.Error(t, err, "every refusal must fail the RPC")
			assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

			var connectErr *connect.Error
			require.ErrorAs(t, err, &connectErr)
			require.Len(t, connectErr.Details(), 1)
			msg, detailErr := connectErr.Details()[0].Value()
			require.NoError(t, detailErr)
			got := msg.(*reliantv1.ForgeDeployRefusal)

			assert.Equal(t, tc.want, got.Reason)
			tc.check(t, got)
		})
	}
}

// The confirmation token is mandatory on BOTH halves, and rejected before the
// daemon is called at all.
func TestForgeService_StartDeploy_RequiresConfirmationToken(t *testing.T) {
	cases := map[string]*reliantv1.StartForgeDeployRequest{
		"no declared context": {
			ProjectId: "p", Env: "prod", ExpectedCurrentRelease: "v1.5.15",
		},
		"no release claim": {
			ProjectId: "p", Env: "prod", ExpectedDeclaredContext: "gke_prod",
		},
		"contradictory release claim": {
			ProjectId: "p", Env: "prod", ExpectedDeclaredContext: "gke_prod",
			ExpectedCurrentRelease: "v1.5.15", ExpectUnbound: true,
		},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			router := &forgeTestRouter{}
			_, err := newForgeTestService(router).StartDeploy(authedCtx(), connect.NewRequest(req))
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			assert.Empty(t, router.lastCommandType,
				"an unauthorised request must never reach the daemon")
		})
	}
}

// An env with no binding is authorised with expect_unbound, and that reaches the
// daemon as an explicit claim rather than as an absent field.
func TestForgeService_StartDeploy_ExpectUnboundIsAnExplicitClaim(t *testing.T) {
	router := &forgeTestRouter{
		reply: deployReply(t,
			deployPlanDoc("dev", "dry_run", "k3d-control-plane", "k3d-control-plane", "allow", ""),
			map[string]any{
				"handle": "h1", "env": "dev",
				"job_status": "running", "started_at": "2026-07-01T00:25:15Z",
			}),
	}

	resp, err := newForgeTestService(router).StartDeploy(authedCtx(),
		connect.NewRequest(&reliantv1.StartForgeDeployRequest{
			ProjectId:               "p",
			Env:                     "dev",
			ExpectedDeclaredContext: "k3d-control-plane",
			ExpectUnbound:           true,
		}))
	require.NoError(t, err)
	assert.Equal(t, "h1", resp.Msg.Handle)

	var sent struct {
		ExpectedDeclaredContext string `json:"expected_declared_context"`
		ExpectedCurrentRelease  string `json:"expected_current_release"`
		ExpectUnbound           bool   `json:"expect_unbound"`
	}
	require.NoError(t, json.Unmarshal(router.lastPayload, &sent))
	assert.Equal(t, "k3d-control-plane", sent.ExpectedDeclaredContext)
	assert.True(t, sent.ExpectUnbound)
	assert.Empty(t, sent.ExpectedCurrentRelease)
}

// =============================================================================
// StartDeploy — the happy path returns a handle
// =============================================================================

// A successful start returns a handle and RUNNING, and its report is the GUARD
// PLAN (mode dry_run) rather than an apply report — no apply report exists yet.
func TestForgeService_StartDeploy_ReturnsHandleAndRunning(t *testing.T) {
	plan := deployPlanDoc("prod", "dry_run", "gke_prod", "k3d-control-plane", "allow", "v1.5.15")
	router := &forgeTestRouter{
		reply: deployReply(t, plan, map[string]any{
			"handle":     "deploy-7f3a",
			"env":        "prod",
			"job_status": "running",
			"started_at": "2026-07-01T00:25:15Z",
		}),
	}

	resp, err := newForgeTestService(router).StartDeploy(authedCtx(),
		connect.NewRequest(authorisedStart("prod", "gke_prod", "v1.5.15")))
	require.NoError(t, err)

	assert.Equal(t, "forge.deploy_start", router.lastCommandType)
	assert.Equal(t, "deploy-7f3a", resp.Msg.Handle)
	assert.Equal(t, "prod", resp.Msg.Env)
	assert.Equal(t, reliantv1.ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_RUNNING,
		resp.Msg.JobStatus,
		"a started deploy is RUNNING — an OK response here says a deploy is in flight, "+
			"never that one succeeded")
	assert.Equal(t, "2026-07-01T00:25:15Z", resp.Msg.StartedAt)

	var facts struct {
		Mode string `json:"mode"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.Msg.ReportJson), &facts))
	assert.Equal(t, "dry_run", facts.Mode,
		"the start reply carries the guard PLAN it was authorised against, not an apply report")

	// The whole token reaches the daemon; the guard is re-checked there, inside
	// the same command as the write, so there is no argv that applies without it.
	var sent struct {
		Env                     string `json:"env"`
		ExpectedDeclaredContext string `json:"expected_declared_context"`
		ExpectedCurrentRelease  string `json:"expected_current_release"`
	}
	require.NoError(t, json.Unmarshal(router.lastPayload, &sent))
	assert.Equal(t, "prod", sent.Env)
	assert.Equal(t, "gke_prod", sent.ExpectedDeclaredContext)
	assert.Equal(t, "v1.5.15", sent.ExpectedCurrentRelease)
}

// A forge too old for `env deploy --json` arrives as supported:false with no
// handle. That must not become an empty handle a client would poll forever.
func TestForgeService_StartDeploy_UnsupportedForgeReturnsNoHandle(t *testing.T) {
	router := &forgeTestRouter{reply: forgeDaemonReply(t, true, false, 1, "")}

	resp, err := newForgeTestService(router).StartDeploy(authedCtx(),
		connect.NewRequest(authorisedStart("prod", "gke_prod", "v1.5.15")))
	require.NoError(t, err)

	assert.False(t, resp.Msg.Meta.Supported)
	assert.Empty(t, resp.Msg.Handle, "no handle for a deploy that was never started")
	assert.Equal(t, reliantv1.ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_UNSPECIFIED,
		resp.Msg.JobStatus)
}

// =============================================================================
// GetDeployStatus — polling
// =============================================================================

// A running poll reports RUNNING with no report: the outcome does not exist yet,
// and a client must render neither a green nor a red terminal state.
func TestForgeService_GetDeployStatus_ReportsRunning(t *testing.T) {
	router := &forgeTestRouter{
		reply: deployReply(t, "", map[string]any{
			"handle": "deploy-7f3a", "env": "prod",
			"job_status": "running", "started_at": "2026-07-01T00:25:15Z",
		}),
	}

	resp, err := newForgeTestService(router).GetDeployStatus(authedCtx(),
		connect.NewRequest(&reliantv1.GetForgeDeployStatusRequest{
			ProjectId: "p", Handle: "deploy-7f3a",
		}))
	require.NoError(t, err)

	assert.Equal(t, "forge.deploy_status", router.lastCommandType)
	assert.Equal(t, reliantv1.ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_RUNNING,
		resp.Msg.JobStatus)
	assert.Empty(t, resp.Msg.ReportJson, "no report exists while the deploy is in flight")
	assert.Empty(t, resp.Msg.FinishedAt)
}

// The finished report crosses verbatim, and the rollout states survive as
// THEMSELVES. timed_out and not_waited are the ABSENCE of an answer; nothing on
// this path may turn either into a success.
func TestForgeService_GetDeployStatus_RolloutStatesSurviveVerbatim(t *testing.T) {
	report := `{"env":"prod","mode":"apply",` +
		`"guard":{"declared_context":"gke_prod","verdict":"allow","reason":"context_declared"},` +
		`"target":{"kube_context":"gke_prod","namespace":"control-plane-prod"},` +
		`"rollout":{"mode":"wait","timeout_seconds":300,"results":[` +
		`{"kind":"Deployment","name":"admin-server","state":"ready"},` +
		`{"kind":"Deployment","name":"reliant-api","state":"timed_out",` +
		`"detail":"readiness budget expired with no verdict"},` +
		`{"kind":"Deployment","name":"internal-console","state":"not_waited"},` +
		`{"kind":"Job","name":"migrate","state":"failed","detail":"condition=failed"}],` +
		`"ready":1,"failed":1,"timed_out":1,"not_waited":1},` +
		`"ok":false,"exit_code":1,"duration_ms":412345}`

	router := &forgeTestRouter{
		reply: func() []byte {
			env := map[string]any{
				"is_forge_project": true, "supported": true,
				"forge_version": "v0.1.15", "exit_code": 1,
				"report": json.RawMessage(report),
				"handle": "deploy-7f3a", "env": "prod",
				"job_status":  "completed",
				"started_at":  "2026-07-01T00:25:15Z",
				"finished_at": "2026-07-01T00:32:07Z",
			}
			b, err := json.Marshal(env)
			require.NoError(t, err)
			return b
		}(),
	}

	resp, err := newForgeTestService(router).GetDeployStatus(authedCtx(),
		connect.NewRequest(&reliantv1.GetForgeDeployStatusRequest{
			ProjectId: "p", Handle: "deploy-7f3a",
		}))
	require.NoError(t, err)

	// The JOB completed — forge ran and produced a report. That is NOT a claim
	// the deploy succeeded, and the report says it did not.
	assert.Equal(t, reliantv1.ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_COMPLETED,
		resp.Msg.JobStatus,
		"completed means forge produced a report, never that the deploy worked")
	assert.EqualValues(t, 1, resp.Msg.Meta.ExitCode)
	assert.JSONEq(t, report, resp.Msg.ReportJson)
	assert.Equal(t, "2026-07-01T00:32:07Z", resp.Msg.FinishedAt)

	var doc struct {
		OK      bool `json:"ok"`
		Rollout struct {
			Results []struct {
				Name  string `json:"name"`
				State string `json:"state"`
			} `json:"results"`
			Ready     int `json:"ready"`
			Failed    int `json:"failed"`
			TimedOut  int `json:"timed_out"`
			NotWaited int `json:"not_waited"`
		} `json:"rollout"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.Msg.ReportJson), &doc))
	assert.False(t, doc.OK, "ok must survive as false")

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
		assert.Equal(t, want, states[name],
			"%s: a rollout state must cross this layer as itself — collapsing timed_out or "+
				"not_waited into ready is the green-deploy-over-a-broken-environment failure", name)
	}
	// Separate tallies, because the whole point is three distinguishable
	// outcomes and one boolean can only distinguish two.
	assert.Equal(t, 1, doc.Rollout.TimedOut)
	assert.Equal(t, 1, doc.Rollout.NotWaited)
	assert.Equal(t, 1, doc.Rollout.Failed)
}

// A job killed by a timeout or by the process dying reports UNKNOWN — not
// success, which would paint a possibly half-converged cluster green, and not
// failure, which would invite a retry against a cluster mid-rollout.
func TestForgeService_GetDeployStatus_KilledJobIsUnknown(t *testing.T) {
	router := &forgeTestRouter{
		reply: deployReply(t, "", map[string]any{
			"handle": "deploy-7f3a", "env": "prod",
			"job_status": "unknown",
			"job_status_detail": "forge could not be run to completion; manifests may or may not " +
				"have reached the cluster: signal: killed",
			"started_at": "2026-07-01T00:25:15Z", "finished_at": "2026-07-01T01:55:15Z",
		}),
	}

	resp, err := newForgeTestService(router).GetDeployStatus(authedCtx(),
		connect.NewRequest(&reliantv1.GetForgeDeployStatusRequest{
			ProjectId: "p", Handle: "deploy-7f3a",
		}))
	require.NoError(t, err, "an indeterminate outcome is data, not a transport failure")

	assert.Equal(t, reliantv1.ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_UNKNOWN,
		resp.Msg.JobStatus,
		"a killed job must be UNKNOWN: neither success nor failure is knowable")
	assert.Contains(t, resp.Msg.JobStatusDetail, "may or may not",
		"the detail must not assert an outcome the daemon cannot know")
}

// A handle the daemon does not recognise is UNKNOWN too, and still a successful
// RPC. The registry is in-memory, so a daemon that restarted mid-apply has lost a
// handle for a deploy that may well have landed.
func TestForgeService_GetDeployStatus_UnknownHandleIsUnknownNotError(t *testing.T) {
	router := &forgeTestRouter{
		reply: deployReply(t, "", map[string]any{
			"handle":     "gone",
			"job_status": "unknown",
			"job_status_detail": "no deploy with this handle is known to the daemon: " +
				"manifests may or may not have reached the cluster",
		}),
	}

	resp, err := newForgeTestService(router).GetDeployStatus(authedCtx(),
		connect.NewRequest(&reliantv1.GetForgeDeployStatusRequest{
			ProjectId: "p", Handle: "gone",
		}))
	require.NoError(t, err)
	assert.Equal(t, reliantv1.ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_UNKNOWN,
		resp.Msg.JobStatus)
}

// TestForgeDeployJobStatusMapping is the unit test for the mapper, and the case
// that matters is the LAST one: a token this binary does not recognise must
// become UNKNOWN, never COMPLETED. A newer daemon's additional status decoded as
// completed would report a deploy of indeterminate outcome as finished.
func TestForgeDeployJobStatusMapping(t *testing.T) {
	cases := map[string]reliantv1.ForgeDeployJobStatus{
		"running":   reliantv1.ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_RUNNING,
		"completed": reliantv1.ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_COMPLETED,
		"failed":    reliantv1.ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_FAILED,
		"unknown":   reliantv1.ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_UNKNOWN,
		"":          reliantv1.ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_UNSPECIFIED,
		"a_status_from_a_newer_daemon": reliantv1.
			ForgeDeployJobStatus_FORGE_DEPLOY_JOB_STATUS_UNKNOWN,
	}
	for token, want := range cases {
		assert.Equal(t, want, forgeDeployJobStatus(token),
			"token %q must not be decoded as a safer-looking status than it is", token)
	}
}

func TestForgeService_GetDeployStatus_RequiresHandle(t *testing.T) {
	router := &forgeTestRouter{}
	_, err := newForgeTestService(router).GetDeployStatus(authedCtx(),
		connect.NewRequest(&reliantv1.GetForgeDeployStatusRequest{ProjectId: "p"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Empty(t, router.lastCommandType)
}

// =============================================================================
// Timeouts
// =============================================================================

// The deploy budgets, and the one that is a design statement rather than a
// number: the START's budget covers the guard plan and the spawn, NOT the deploy.
// The deploy has no budget on this surface at all, because it outlives the RPC.
func TestForgeService_DeployTimeouts(t *testing.T) {
	t.Run("plan gets a cluster-reading budget", func(t *testing.T) {
		router := &forgeTestRouter{
			reply: deployReply(t,
				deployPlanDoc("prod", "dry_run", "gke_prod", "k3d", "allow", "v1.5.15"), nil),
		}
		_, err := newForgeTestService(router).PlanDeploy(authedCtx(),
			connect.NewRequest(&reliantv1.PlanForgeDeployRequest{ProjectId: "p", Env: "prod"}))
		require.NoError(t, err)
		assert.EqualValues(t, forgeDeployPlanTimeoutMs, router.lastTimeoutMs)
		// The preflight reads the live target, so this is NOT a local-read
		// budget like the promote plan's.
		assert.Greater(t, forgeDeployPlanTimeoutMs, forgePromotePlanTimeoutMs,
			"the deploy preview renders the env AND preflights the live target, so it cannot "+
				"share the promote plan's local-read budget")
	})

	t.Run("start covers the guard plan and the spawn, not the deploy", func(t *testing.T) {
		router := &forgeTestRouter{
			reply: deployReply(t,
				deployPlanDoc("prod", "dry_run", "gke_prod", "k3d", "allow", "v1.5.15"),
				map[string]any{"handle": "h1", "job_status": "running"}),
		}
		_, err := newForgeTestService(router).StartDeploy(authedCtx(),
			connect.NewRequest(authorisedStart("prod", "gke_prod", "v1.5.15")))
		require.NoError(t, err)
		assert.EqualValues(t, forgeDeployStartTimeoutMs, router.lastTimeoutMs)
		assert.GreaterOrEqual(t, forgeDeployStartTimeoutMs, forgeDeployPlanTimeoutMs,
			"the start runs the guard plan before spawning, so its budget must cover it")
		// And it stays inside the daemon's 2-minute per-invocation cap — the
		// start really does complete in one invocation's worth of work, which
		// is only true BECAUSE the deploy is detached.
		assert.LessOrEqual(t, forgeDeployStartTimeoutMs, 120_000,
			"the start must fit the daemon's per-invocation cap; only the detached deploy escapes it")
	})

	t.Run("status is a short poll", func(t *testing.T) {
		router := &forgeTestRouter{
			reply: deployReply(t, "", map[string]any{"handle": "h1", "job_status": "running"}),
		}
		_, err := newForgeTestService(router).GetDeployStatus(authedCtx(),
			connect.NewRequest(&reliantv1.GetForgeDeployStatusRequest{
				ProjectId: "p", Handle: "h1",
			}))
		require.NoError(t, err)
		assert.EqualValues(t, forgeDeployStatusTimeoutMs, router.lastTimeoutMs)
		assert.Less(t, forgeDeployStatusTimeoutMs, forgeDeployStartTimeoutMs,
			"a poll reads one in-memory entry and sits on a UI repeat loop; a long budget there "+
				"would stack requests behind an unresponsive daemon")
	})
}
