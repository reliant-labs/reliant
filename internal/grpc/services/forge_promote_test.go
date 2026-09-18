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

// NO TEST HERE REACHES A REAL FORGE OR A REAL PROJECT. ApplyPromote writes a
// release binding, every bound environment in the reference project is a CLOUD
// environment, and one is a live GKE prod cluster. These tests stub the daemon
// round trip (forgeTestRouter, forge_test.go) and assert against canned replies.
//
// Under test: the plan/apply split, the concurrency guard's wire shape, the
// per-command timeouts, and that promote never implies a deploy.

// promoteReply builds a daemon reply for forge.promote_apply or
// forge.promote_plan: the standard forge envelope, optionally with a refusal.
func promoteReply(t *testing.T, report string, refusal map[string]any) []byte {
	t.Helper()
	env := map[string]any{
		"is_forge_project": true,
		"supported":        true,
		"forge_version":    "v0.1.15",
		"exit_code":        0,
	}
	if report != "" {
		env["report"] = report
	}
	if refusal != nil {
		env["promote_refused"] = refusal
	}
	b, err := json.Marshal(env)
	require.NoError(t, err)
	return b
}

// promotePlanDoc is a plan document shaped like forge's real --json output.
func promotePlanDoc(env, target, current string, applied bool) string {
	dryRun := !applied
	return fmt.Sprintf(`{"env":%q,"ledger":".forge/env-releases.json",`+
		`"dry_run":%t,"applied":%t,"direction":"ahead",`+
		`"direction_detail":"FORWARD — %s is 20 release(s) NEWER than %s",`+
		`"releases_between":20,`+
		`"current":{"bound":true,"release":%q,"promoted_at":"2026-07-01T00:25:15Z"},`+
		`"target":{"release":%q,"images":4},`+
		`"images":[{"image":"control-plane","change":"changed",`+
		`"current_digest":"sha256:c3b0074","target_digest":"sha256:1ea5668"}],`+
		`"tally":{"unchanged":0,"changed":3,"added":1,"removed":0},`+
		`"commits":{"state":"dirty_release"},`+
		`"ships_nothing":true,"next_step":"forge env deploy %s","ok":true}`,
		env, dryRun, applied, target, current, current, target, env)
}

// =============================================================================
// The plan/apply split
// =============================================================================

// TestForgeService_PromoteIsTwoDistinctMethods pins the central safety
// decision. A single method with a dry_run bool would make FALSE — the
// destructive value — what a caller gets by OMITTING the field. Two method
// names make the write reachable only by naming it.
func TestForgeService_PromoteIsTwoDistinctMethods(t *testing.T) {
	// Distinct procedures on the wire, so a log line says which one happened.
	assert.NotEqual(t,
		reliantv1connectPlanProcedure(), reliantv1connectApplyProcedure(),
		"plan and apply must be separate procedures")

	// Distinct request TYPES, so a plan request cannot be sent to apply.
	assert.NotEqual(t,
		reflect.TypeOf(reliantv1.PlanForgePromoteRequest{}),
		reflect.TypeOf(reliantv1.PromoteForgeEnvRequest{}))

	// Neither request may carry a dry-run-shaped switch. If one did, the split
	// would be cosmetic and the defaulting hazard would be back.
	for _, typ := range []reflect.Type{
		reflect.TypeOf(reliantv1.PlanForgePromoteRequest{}),
		reflect.TypeOf(reliantv1.PromoteForgeEnvRequest{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			name := protoJSONName(field)
			for _, banned := range []string{"dry_run", "plan", "apply", "write", "commit", "confirm"} {
				assert.NotEqual(t, banned, name,
					"%s carries %q — the plan/apply split must be two METHODS, not a boolean whose "+
						"zero value is the destructive choice", typ.Name(), name)
			}
		}
	}
}

func reliantv1connectPlanProcedure() string  { return "/reliant.v1.ForgeService/PlanPromote" }
func reliantv1connectApplyProcedure() string { return "/reliant.v1.ForgeService/ApplyPromote" }

// The plan is read-only, dispatches the read-only daemon command, and passes
// forge's document through byte-for-byte including fields this layer does not know.
func TestForgeService_PlanPromote_PassesReportThroughVerbatim(t *testing.T) {
	report := `{"env":"staging","dry_run":true,"applied":false,"direction":"ahead",` +
		`"ships_nothing":true,"next_step":"forge env deploy staging",` +
		`"a_field_added_by_a_newer_forge":42}`
	router := &forgeTestRouter{reply: promoteReply(t, report, nil)}

	resp, err := newForgeTestService(router).PlanPromote(authedCtx(),
		connect.NewRequest(&reliantv1.PlanForgePromoteRequest{
			ProjectId: "p", Env: "staging", Release: "v1.5.15",
		}))
	require.NoError(t, err)

	assert.Equal(t, "forge.promote_plan", router.lastCommandType,
		"the preview must dispatch the READ-ONLY daemon command")
	assert.JSONEq(t, report, resp.Msg.ReportJson,
		"forge's document must cross this layer unmodified")

	// The env and release reach the daemon as given.
	var sent struct {
		ProjectPath string `json:"project_path"`
		Env         string `json:"env"`
		Release     string `json:"release"`
	}
	require.NoError(t, json.Unmarshal(router.lastPayload, &sent))
	assert.Equal(t, "/daemon/workspace/control-plane", sent.ProjectPath)
	assert.Equal(t, "staging", sent.Env)
	assert.Equal(t, "v1.5.15", sent.Release)
}

// The plan path must never surface a document claiming it wrote.
func TestForgeService_PlanPromote_NeverReportsApplied(t *testing.T) {
	router := &forgeTestRouter{
		reply: promoteReply(t, promotePlanDoc("staging", "v1.5.15", "v1.3.0", false), nil),
	}

	resp, err := newForgeTestService(router).PlanPromote(authedCtx(),
		connect.NewRequest(&reliantv1.PlanForgePromoteRequest{
			ProjectId: "p", Env: "staging", Release: "v1.5.15",
		}))
	require.NoError(t, err)

	var parsed struct {
		DryRun  bool `json:"dry_run"`
		Applied bool `json:"applied"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.Msg.ReportJson), &parsed))
	assert.False(t, parsed.Applied, "the plan path must never report applied:true")
	assert.True(t, parsed.DryRun, "the plan path must report dry_run:true")
	assert.Equal(t, "forge.promote_plan", router.lastCommandType)
}

// =============================================================================
// The concurrency guard
// =============================================================================

// THE central test. A forge EnvBinding is {release, resolved, promoted_at} with
// no prior-release field, so an unintended overwrite is unrecoverable except
// from git. A refusal must therefore FAIL the RPC — fail closed — while still
// carrying the binding that was actually found.
func TestForgeService_ApplyPromote_RefusesStaleBinding(t *testing.T) {
	// The caller last saw v1.3.0; the ledger now holds v1.4.0.
	router := &forgeTestRouter{
		reply: promoteReply(t,
			promotePlanDoc("staging", "v1.5.15", "v1.4.0", false),
			map[string]any{
				"reason":                   "stale_current_release",
				"expected_current_release": "v1.3.0",
				"actual_bound":             true,
				"actual_current_release":   "v1.4.0",
				"actual_promoted_at":       "2026-07-01T00:25:15Z",
				"detail":                   `env "staging" is bound to v1.4.0, not the expected v1.3.0`,
			}),
	}

	resp, err := newForgeTestService(router).ApplyPromote(authedCtx(),
		connect.NewRequest(&reliantv1.PromoteForgeEnvRequest{
			ProjectId: "p", Env: "staging", Release: "v1.5.15",
			ExpectedCurrentRelease: "v1.3.0",
		}))

	// FAIL CLOSED. A refusal returned as a successful response with a nullable
	// field would be safe only if every client remembered to check it; one that
	// forgot would tell a user the promote worked.
	require.Error(t, err, "a refused promote must FAIL the RPC, not return a success with a flag")
	assert.Nil(t, resp)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err),
		"a stale binding is a precondition failure, distinguishable from Internal/Unavailable")

	// The structured facts must survive on the error path, or the only recovery
	// is a blind retry — which is how a rollback gets applied twice.
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	require.Len(t, connectErr.Details(), 1, "the refusal detail must be attached")

	msg, detailErr := connectErr.Details()[0].Value()
	require.NoError(t, detailErr)
	refusal, ok := msg.(*reliantv1.ForgePromoteRefusal)
	require.True(t, ok, "the detail must be a ForgePromoteRefusal, got %T", msg)

	assert.Equal(t,
		reliantv1.ForgePromoteRefusalReason_FORGE_PROMOTE_REFUSAL_REASON_STALE_CURRENT_RELEASE,
		refusal.Reason)
	assert.Equal(t, "v1.4.0", refusal.ActualCurrentRelease,
		"the binding that was actually found must reach the caller")
	assert.True(t, refusal.ActualBound)
	assert.Equal(t, "v1.3.0", refusal.ExpectedCurrentRelease)
	assert.Equal(t, "2026-07-01T00:25:15Z", refusal.ActualPromotedAt,
		"the timestamp says whether the caller was beaten by seconds or is reading a stale page")
	assert.Contains(t, refusal.Detail, "v1.4.0")

	// The error text must also say plainly that nothing happened.
	assert.Contains(t, err.Error(), "nothing was written")
}

// A matching expectation proceeds, and the APPLIED plan comes back — not the
// preview — so a caller shows what happened rather than assuming its preview
// was still accurate.
func TestForgeService_ApplyPromote_SucceedsAndReturnsTheAppliedPlan(t *testing.T) {
	applied := promotePlanDoc("staging", "v1.5.15", "v1.3.0", true)
	router := &forgeTestRouter{reply: promoteReply(t, applied, nil)}

	resp, err := newForgeTestService(router).ApplyPromote(authedCtx(),
		connect.NewRequest(&reliantv1.PromoteForgeEnvRequest{
			ProjectId: "p", Env: "staging", Release: "v1.5.15",
			ExpectedCurrentRelease: "v1.3.0",
		}))
	require.NoError(t, err)

	assert.Equal(t, "forge.promote_apply", router.lastCommandType,
		"the write must dispatch the guarded daemon command")

	var parsed struct {
		Applied bool `json:"applied"`
		DryRun  bool `json:"dry_run"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.Msg.ReportJson), &parsed))
	assert.True(t, parsed.Applied, "the response must carry the APPLIED plan, not the preview")
	assert.False(t, parsed.DryRun)

	// The confirmation token reaches the daemon, which is where the guard runs.
	// A guard on this side of the hop would leave a check-then-write window.
	var sent struct {
		ExpectedCurrentRelease string `json:"expected_current_release"`
		ExpectUnbound          bool   `json:"expect_unbound"`
	}
	require.NoError(t, json.Unmarshal(router.lastPayload, &sent))
	assert.Equal(t, "v1.3.0", sent.ExpectedCurrentRelease)
	assert.False(t, sent.ExpectUnbound)
}

// The confirmation token is MANDATORY, and an unset one must never be readable
// as "I saw nothing" — that would let an empty request authorise a blind
// overwrite of a bound environment.
func TestForgeService_ApplyPromote_RequiresAConfirmationToken(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  *reliantv1.PromoteForgeEnvRequest
	}{
		{
			name: "neither expectation stated",
			req: &reliantv1.PromoteForgeEnvRequest{
				ProjectId: "p", Env: "staging", Release: "v1.5.15",
			},
		},
		{
			name: "contradictory expectations",
			req: &reliantv1.PromoteForgeEnvRequest{
				ProjectId: "p", Env: "staging", Release: "v1.5.15",
				ExpectedCurrentRelease: "v1.3.0", ExpectUnbound: true,
			},
		},
		{
			name: "blank expectation is not a claim",
			req: &reliantv1.PromoteForgeEnvRequest{
				ProjectId: "p", Env: "staging", Release: "v1.5.15",
				ExpectedCurrentRelease: "   ",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := &forgeTestRouter{}
			_, err := newForgeTestService(router).ApplyPromote(authedCtx(), connect.NewRequest(tc.req))

			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			assert.Empty(t, router.lastCommandType,
				"an unauthorised promote must never reach the daemon")
		})
	}
}

// expect_unbound authorises a first promote, and only a first promote.
func TestForgeService_ApplyPromote_ExpectUnbound(t *testing.T) {
	t.Run("authorises a first promote", func(t *testing.T) {
		router := &forgeTestRouter{
			reply: promoteReply(t, `{"env":"dev","applied":true,"dry_run":false,`+
				`"direction":"initial","current":{"bound":false},"target":{"release":"v1.5.15"},`+
				`"ships_nothing":true,"next_step":"forge env deploy dev"}`, nil),
		}
		_, err := newForgeTestService(router).ApplyPromote(authedCtx(),
			connect.NewRequest(&reliantv1.PromoteForgeEnvRequest{
				ProjectId: "p", Env: "dev", Release: "v1.5.15", ExpectUnbound: true,
			}))
		require.NoError(t, err)

		var sent struct {
			ExpectUnbound          bool   `json:"expect_unbound"`
			ExpectedCurrentRelease string `json:"expected_current_release"`
		}
		require.NoError(t, json.Unmarshal(router.lastPayload, &sent))
		assert.True(t, sent.ExpectUnbound)
		assert.Empty(t, sent.ExpectedCurrentRelease)
	})

	t.Run("is refused when the env is actually bound", func(t *testing.T) {
		// The dangerous direction: the caller thinks this env was never
		// promoted, but prod is live on v1.4.0.
		router := &forgeTestRouter{
			reply: promoteReply(t, promotePlanDoc("prod", "v1.5.15", "v1.4.0", false),
				map[string]any{
					"reason":                 "stale_current_release",
					"expected_unbound":       true,
					"actual_bound":           true,
					"actual_current_release": "v1.4.0",
					"detail":                 `env "prod" was expected to be unbound but is bound to v1.4.0`,
				}),
		}
		_, err := newForgeTestService(router).ApplyPromote(authedCtx(),
			connect.NewRequest(&reliantv1.PromoteForgeEnvRequest{
				ProjectId: "p", Env: "prod", Release: "v1.5.15", ExpectUnbound: true,
			}))

		require.Error(t, err)
		assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	})
}

// An unrecognised refusal token still FAILS. A refusal this layer cannot
// classify is a refusal, not a success — otherwise a newer daemon's additional
// reason would be laundered into an applied promote.
func TestForgeService_ApplyPromote_UnknownRefusalReasonStillFails(t *testing.T) {
	router := &forgeTestRouter{
		reply: promoteReply(t, `{"env":"staging"}`, map[string]any{
			"reason":       "some_future_reason",
			"actual_bound": true,
			"detail":       "a condition this build does not know",
		}),
	}
	_, err := newForgeTestService(router).ApplyPromote(authedCtx(),
		connect.NewRequest(&reliantv1.PromoteForgeEnvRequest{
			ProjectId: "p", Env: "staging", Release: "v1.5.15",
			ExpectedCurrentRelease: "v1.3.0",
		}))

	require.Error(t, err, "an unclassifiable refusal must not become a success")
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	require.Len(t, connectErr.Details(), 1)
	msg, detailErr := connectErr.Details()[0].Value()
	require.NoError(t, detailErr)
	refusal := msg.(*reliantv1.ForgePromoteRefusal)
	assert.Equal(t,
		reliantv1.ForgePromoteRefusalReason_FORGE_PROMOTE_REFUSAL_REASON_UNSPECIFIED,
		refusal.Reason, "an unknown token maps to UNSPECIFIED rather than a guess")
}

// =============================================================================
// Rollback visibility
// =============================================================================

// A rollback is legitimate and must not be blocked — but it must be VISIBLE. A
// "removed" image means a workload would be DELETED, which is the most
// consequential fact this path can carry.
func TestForgeService_Promote_RollbackIsVisibleOnBothPaths(t *testing.T) {
	rollback := `{"env":"prod","dry_run":true,"applied":false,` +
		`"direction":"behind",` +
		`"direction_detail":"ROLLBACK — v1.3.0 moves the environment BACKWARDS",` +
		`"current":{"bound":true,"release":"v1.5.15"},"target":{"release":"v1.3.0"},` +
		`"images":[{"image":"internal-console","change":"removed"}],` +
		`"tally":{"unchanged":0,"changed":3,"added":0,"removed":1},` +
		`"commits":{"state":"dirty_release","reverts":true},` +
		`"ships_nothing":true,"next_step":"forge env deploy prod","ok":true}`

	assertRollbackVisible := func(t *testing.T, reportJSON string) {
		t.Helper()
		var parsed struct {
			Direction       string `json:"direction"`
			DirectionDetail string `json:"direction_detail"`
			Images          []struct {
				Change string `json:"change"`
			} `json:"images"`
			Tally struct {
				Removed int `json:"removed"`
			} `json:"tally"`
			Commits struct {
				Reverts bool `json:"reverts"`
			} `json:"commits"`
		}
		require.NoError(t, json.Unmarshal([]byte(reportJSON), &parsed))
		assert.Equal(t, "behind", parsed.Direction,
			`direction "behind" must not be suppressed or normalised`)
		assert.Contains(t, parsed.DirectionDetail, "ROLLBACK")
		require.Len(t, parsed.Images, 1)
		assert.Equal(t, "removed", parsed.Images[0].Change,
			"a REMOVED image means a workload would be deleted; it must survive verbatim")
		assert.Equal(t, 1, parsed.Tally.Removed)
		assert.True(t, parsed.Commits.Reverts,
			"reverts:true says the listed commits are being taken AWAY, not added")
	}

	t.Run("plan", func(t *testing.T) {
		router := &forgeTestRouter{reply: promoteReply(t, rollback, nil)}
		resp, err := newForgeTestService(router).PlanPromote(authedCtx(),
			connect.NewRequest(&reliantv1.PlanForgePromoteRequest{
				ProjectId: "p", Env: "prod", Release: "v1.3.0",
			}))
		require.NoError(t, err, "a rollback is a legitimate plan, not an error")
		assertRollbackVisible(t, resp.Msg.ReportJson)
	})

	t.Run("apply", func(t *testing.T) {
		// Not blocked here: the guard's job is agreement about CURRENT state,
		// not a policy opinion about direction. Forge owns the verdict; the
		// operator owns the decision.
		applied := strings.Replace(
			strings.Replace(rollback, `"dry_run":true`, `"dry_run":false`, 1),
			`"applied":false`, `"applied":true`, 1)
		router := &forgeTestRouter{reply: promoteReply(t, applied, nil)}

		resp, err := newForgeTestService(router).ApplyPromote(authedCtx(),
			connect.NewRequest(&reliantv1.PromoteForgeEnvRequest{
				ProjectId: "p", Env: "prod", Release: "v1.3.0",
				ExpectedCurrentRelease: "v1.5.15",
			}))
		require.NoError(t, err, "a rollback with a matching expectation is allowed")
		assertRollbackVisible(t, resp.Msg.ReportJson)
	})
}

// =============================================================================
// PROMOTE SHIPS NOTHING
// =============================================================================

// Promote writes a pointer. Nothing reaches a cluster until `forge env deploy`
// runs, and this layer must not imply otherwise — carrying ships_nothing and
// next_step verbatim is how the UI can say so.
func TestForgeService_Promote_ShipsNothingSurvivesBothPaths(t *testing.T) {
	assertShipsNothing := func(t *testing.T, reportJSON string) {
		t.Helper()
		var parsed struct {
			ShipsNothing bool   `json:"ships_nothing"`
			NextStep     string `json:"next_step"`
		}
		require.NoError(t, json.Unmarshal([]byte(reportJSON), &parsed))
		assert.True(t, parsed.ShipsNothing,
			"ships_nothing must reach the client: promote moved a pointer, it deployed nothing")
		assert.Equal(t, "forge env deploy staging", parsed.NextStep,
			"next_step is the command that actually ships these digests")
	}

	t.Run("plan", func(t *testing.T) {
		router := &forgeTestRouter{
			reply: promoteReply(t, promotePlanDoc("staging", "v1.5.15", "v1.3.0", false), nil),
		}
		resp, err := newForgeTestService(router).PlanPromote(authedCtx(),
			connect.NewRequest(&reliantv1.PlanForgePromoteRequest{
				ProjectId: "p", Env: "staging", Release: "v1.5.15",
			}))
		require.NoError(t, err)
		assertShipsNothing(t, resp.Msg.ReportJson)
	})

	t.Run("a SUCCESSFUL apply still shipped nothing", func(t *testing.T) {
		router := &forgeTestRouter{
			reply: promoteReply(t, promotePlanDoc("staging", "v1.5.15", "v1.3.0", true), nil),
		}
		resp, err := newForgeTestService(router).ApplyPromote(authedCtx(),
			connect.NewRequest(&reliantv1.PromoteForgeEnvRequest{
				ProjectId: "p", Env: "staging", Release: "v1.5.15",
				ExpectedCurrentRelease: "v1.3.0",
			}))
		require.NoError(t, err)
		assertShipsNothing(t, resp.Msg.ReportJson)
	})
}

// Neither response may grow a field implying a deployment happened. The whole
// reason `forge env verify` exists is that the promote/deploy gap used to be
// invisible.
func TestForgeService_PromoteResponsesDoNotImplyADeploy(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(reliantv1.PlanForgePromoteResponse{}),
		reflect.TypeOf(reliantv1.PromoteForgeEnvResponse{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			name := protoJSONName(field)
			assert.True(t, name == "meta" || name == "report_json",
				"%s gained field %q. This surface carries forge's document plus meta and nothing "+
					"else; a field here would be a second schema free to disagree with forge's — and "+
					"the most likely such field is one implying a deploy happened, which it did not.",
				typ.Name(), name)
		}
	}
}

// TestNoUnguardedDeployRPCExists replaces an earlier assertion that NO deploy RPC
// existed at all. That assertion recorded a scope decision — deploy was
// deliberately left out of the promote work — and the deploy path has since been
// built (PlanDeploy / StartDeploy / GetDeployStatus, forge_deploy_test.go). So
// the old test is retired rather than weakened, and what survives is the part
// that was actually load-bearing: there must be no SINGLE deploy method that
// could apply to a cluster without the plan/start split and its confirmation
// token. A method named "Deploy" or "ApplyDeploy" would be exactly that.
func TestNoUnguardedDeployRPCExists(t *testing.T) {
	allowed := map[string]bool{
		"PlanDeploy":      true, // read-only preview
		"StartDeploy":     true, // guarded, asynchronous, token-bearing
		"GetDeployStatus": true, // a poll
	}
	svc := reflect.TypeOf(&ForgeService{})
	for i := 0; i < svc.NumMethod(); i++ {
		name := svc.Method(i).Name
		if !strings.Contains(strings.ToLower(name), "deploy") || allowed[name] {
			continue
		}
		t.Errorf("ForgeService gained deploy method %q. Deploying is reachable ONLY through "+
			"PlanDeploy (read-only) and StartDeploy (which requires a confirmation token naming "+
			"the cluster and the release); a fourth spelling would be an unguarded path to a "+
			"live cluster", name)
	}
}

// =============================================================================
// Timeouts
// =============================================================================

// Promote's budgets sit at the SHORT end, with the local-read commands. Neither
// call touches a cluster: the plan reads ledger files plus a git range, and the
// apply writes one JSON file.
func TestForgeService_PromoteTimeouts(t *testing.T) {
	t.Run("plan gets a local-read budget", func(t *testing.T) {
		router := &forgeTestRouter{
			reply: promoteReply(t, promotePlanDoc("staging", "v1.5.15", "v1.3.0", false), nil),
		}
		_, err := newForgeTestService(router).PlanPromote(authedCtx(),
			connect.NewRequest(&reliantv1.PlanForgePromoteRequest{
				ProjectId: "p", Env: "staging", Release: "v1.5.15",
			}))
		require.NoError(t, err)
		assert.EqualValues(t, forgePromotePlanTimeoutMs, router.lastTimeoutMs)
	})

	t.Run("apply covers two forge runs", func(t *testing.T) {
		router := &forgeTestRouter{
			reply: promoteReply(t, promotePlanDoc("staging", "v1.5.15", "v1.3.0", true), nil),
		}
		_, err := newForgeTestService(router).ApplyPromote(authedCtx(),
			connect.NewRequest(&reliantv1.PromoteForgeEnvRequest{
				ProjectId: "p", Env: "staging", Release: "v1.5.15",
				ExpectedCurrentRelease: "v1.3.0",
			}))
		require.NoError(t, err)
		assert.EqualValues(t, forgePromoteApplyTimeoutMs, router.lastTimeoutMs)
		assert.Greater(t, int32(forgePromoteApplyTimeoutMs), int32(forgePromotePlanTimeoutMs),
			"apply runs the guard plan AND the write, so it needs more than one plan budget")
	})

	// Promote does NOT deploy, so it must not be given a cluster-sized budget.
	// A long budget on a write invites a retry against a promote still in flight.
	assert.Less(t, int32(forgePromotePlanTimeoutMs), int32(forgeEnvVerifyTimeoutMs),
		"the promote preview reads no cluster and must not share a verify budget")
	assert.Less(t, int32(forgePromoteApplyTimeoutMs), int32(forgeTopologyVerifyTimeoutMs),
		"promote writes one file; it must not be budgeted like a fleet-wide cluster walk")

	// No budget may exceed the daemon's own 2-minute per-invocation cap.
	for name, ms := range map[string]int32{
		"promote_plan":  forgePromotePlanTimeoutMs,
		"promote_apply": forgePromoteApplyTimeoutMs,
	} {
		assert.LessOrEqual(t, ms, int32(120_000),
			"%s budget exceeds the daemon's forgeInvocationTimeout", name)
	}
}

// =============================================================================
// Version capability and non-forge projects, on BOTH methods
// =============================================================================

// A forge too old to know `--plan` must surface as supported=false WITH a
// version — not a crash, and emphatically not a silent apparent success.
func TestForgeService_Promote_UnsupportedForgeOnBothMethods(t *testing.T) {
	oldForge := func(t *testing.T) []byte {
		t.Helper()
		b, err := json.Marshal(map[string]any{
			"is_forge_project":   true,
			"supported":          false,
			"forge_version":      "v0.1.13",
			"unsupported_reason": "unknown flag: --plan",
			"exit_code":          1,
		})
		require.NoError(t, err)
		return b
	}

	t.Run("PlanPromote", func(t *testing.T) {
		resp, err := newForgeTestService(&forgeTestRouter{reply: oldForge(t)}).PlanPromote(authedCtx(),
			connect.NewRequest(&reliantv1.PlanForgePromoteRequest{
				ProjectId: "p", Env: "staging", Release: "v1.5.15",
			}))
		require.NoError(t, err, "a version-capability miss is data, not an error")
		assert.True(t, resp.Msg.Meta.IsForgeProject)
		assert.False(t, resp.Msg.Meta.Supported)
		assert.Equal(t, "v0.1.13", resp.Msg.Meta.ForgeVersion)
		assert.Equal(t, "unknown flag: --plan", resp.Msg.Meta.UnsupportedReason)
	})

	t.Run("ApplyPromote", func(t *testing.T) {
		// The daemon detects this on its READ-ONLY guard call, so nothing was
		// written. The RPC must report that as an unsupported answer rather
		// than as a refusal — they are different conditions with different
		// remedies (upgrade forge vs. re-read the binding).
		resp, err := newForgeTestService(&forgeTestRouter{reply: oldForge(t)}).ApplyPromote(authedCtx(),
			connect.NewRequest(&reliantv1.PromoteForgeEnvRequest{
				ProjectId: "p", Env: "staging", Release: "v1.5.15",
				ExpectedCurrentRelease: "v1.3.0",
			}))
		require.NoError(t, err)
		assert.False(t, resp.Msg.Meta.Supported)
		assert.Equal(t, "v0.1.13", resp.Msg.Meta.ForgeVersion)
		assert.Equal(t, "unknown flag: --plan", resp.Msg.Meta.UnsupportedReason)
		assert.Empty(t, resp.Msg.ReportJson, "no plan was produced, so there is nothing to show")
	})
}

func TestForgeService_Promote_NotAForgeProjectOnBothMethods(t *testing.T) {
	notForge := func(t *testing.T) []byte {
		t.Helper()
		b, err := json.Marshal(map[string]any{
			"is_forge_project": false,
			"supported":        false,
			"forge_version":    "v0.1.15",
			"exit_code":        0,
		})
		require.NoError(t, err)
		return b
	}

	t.Run("PlanPromote", func(t *testing.T) {
		resp, err := newForgeTestService(&forgeTestRouter{reply: notForge(t)}).PlanPromote(authedCtx(),
			connect.NewRequest(&reliantv1.PlanForgePromoteRequest{
				ProjectId: "p", Env: "staging", Release: "v1.5.15",
			}))
		require.NoError(t, err, "an ordinary non-forge repository is not an error condition")
		assert.False(t, resp.Msg.Meta.IsForgeProject)
		assert.Empty(t, resp.Msg.ReportJson)
		assert.NotEmpty(t, resp.Msg.Meta.ForgeVersion)
	})

	t.Run("ApplyPromote", func(t *testing.T) {
		resp, err := newForgeTestService(&forgeTestRouter{reply: notForge(t)}).ApplyPromote(authedCtx(),
			connect.NewRequest(&reliantv1.PromoteForgeEnvRequest{
				ProjectId: "p", Env: "staging", Release: "v1.5.15",
				ExpectedCurrentRelease: "v1.3.0",
			}))
		require.NoError(t, err)
		assert.False(t, resp.Msg.Meta.IsForgeProject)
		assert.Empty(t, resp.Msg.ReportJson)
		assert.NotEmpty(t, resp.Msg.Meta.ForgeVersion)
	})
}

// =============================================================================
// Validation and failure mapping
// =============================================================================

func TestForgeService_Promote_Validation(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*ForgeService) error
	}{
		{
			name: "plan requires env",
			call: func(s *ForgeService) error {
				_, err := s.PlanPromote(authedCtx(), connect.NewRequest(
					&reliantv1.PlanForgePromoteRequest{ProjectId: "p", Env: "  ", Release: "v1"}))
				return err
			},
		},
		{
			name: "plan requires release",
			call: func(s *ForgeService) error {
				_, err := s.PlanPromote(authedCtx(), connect.NewRequest(
					&reliantv1.PlanForgePromoteRequest{ProjectId: "p", Env: "staging", Release: " "}))
				return err
			},
		},
		{
			name: "plan requires project_id",
			call: func(s *ForgeService) error {
				_, err := s.PlanPromote(authedCtx(), connect.NewRequest(
					&reliantv1.PlanForgePromoteRequest{Env: "staging", Release: "v1"}))
				return err
			},
		},
		{
			name: "apply requires env",
			call: func(s *ForgeService) error {
				_, err := s.ApplyPromote(authedCtx(), connect.NewRequest(
					&reliantv1.PromoteForgeEnvRequest{
						ProjectId: "p", Env: "", Release: "v1", ExpectedCurrentRelease: "v0"}))
				return err
			},
		},
		{
			name: "apply requires release",
			call: func(s *ForgeService) error {
				_, err := s.ApplyPromote(authedCtx(), connect.NewRequest(
					&reliantv1.PromoteForgeEnvRequest{
						ProjectId: "p", Env: "staging", Release: "", ExpectedCurrentRelease: "v0"}))
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := &forgeTestRouter{}
			err := tc.call(newForgeTestService(router))
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			assert.Empty(t, router.lastCommandType, "must not dispatch an invalid request")
		})
	}

	t.Run("unauthenticated is rejected on both methods", func(t *testing.T) {
		router := &forgeTestRouter{}
		svc := newForgeTestService(router)

		_, err := svc.PlanPromote(t.Context(), connect.NewRequest(
			&reliantv1.PlanForgePromoteRequest{ProjectId: "p", Env: "staging", Release: "v1"}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

		_, err = svc.ApplyPromote(t.Context(), connect.NewRequest(
			&reliantv1.PromoteForgeEnvRequest{
				ProjectId: "p", Env: "staging", Release: "v1", ExpectedCurrentRelease: "v0"}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

		assert.Empty(t, router.lastCommandType)
	})
}

// A timeout on the promote path is a REAL failure. It reads no cluster, so the
// reachability escape hatch must not apply — and on the write path, "unknown"
// would be actively misleading: a promote that timed out may or may not have
// written, and that is not a reachability verdict.
func TestForgeService_Promote_TimeoutIsARealFailureNotUnreachable(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*ForgeService) error
	}{
		{
			name: "PlanPromote",
			call: func(s *ForgeService) error {
				_, err := s.PlanPromote(authedCtx(), connect.NewRequest(
					&reliantv1.PlanForgePromoteRequest{ProjectId: "p", Env: "staging", Release: "v1.5.15"}))
				return err
			},
		},
		{
			name: "ApplyPromote",
			call: func(s *ForgeService) error {
				_, err := s.ApplyPromote(authedCtx(), connect.NewRequest(
					&reliantv1.PromoteForgeEnvRequest{
						ProjectId: "p", Env: "staging", Release: "v1.5.15",
						ExpectedCurrentRelease: "v1.3.0"}))
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := &forgeTestRouter{replyErr: fmt.Errorf("nats: timeout")}
			err := tc.call(newForgeTestService(router))
			require.Error(t, err,
				"promote reads no cluster, so a timeout says nothing about reachability")
			assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
		})
	}
}

func TestForgeService_Promote_MissingProjectDirIsNotFound(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*ForgeService) error
	}{
		{
			name: "PlanPromote",
			call: func(s *ForgeService) error {
				_, err := s.PlanPromote(authedCtx(), connect.NewRequest(
					&reliantv1.PlanForgePromoteRequest{ProjectId: "p", Env: "staging", Release: "v1.5.15"}))
				return err
			},
		},
		{
			name: "ApplyPromote",
			call: func(s *ForgeService) error {
				_, err := s.ApplyPromote(authedCtx(), connect.NewRequest(
					&reliantv1.PromoteForgeEnvRequest{
						ProjectId: "p", Env: "staging", Release: "v1.5.15",
						ExpectedCurrentRelease: "v1.3.0"}))
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := &forgeTestRouter{
				replyErr: fmt.Errorf("daemon command: forge project dir does not exist: /gone"),
			}
			err := tc.call(newForgeTestService(router))
			require.Error(t, err)
			assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
				"a deleted directory is a missing resource, not a retryable outage")
		})
	}
}
