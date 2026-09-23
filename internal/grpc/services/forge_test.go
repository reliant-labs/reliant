// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// forgeTestRouter stubs the daemon round trip. It embeds
// worktreeTestDaemonRouter (worktree_test.go) to satisfy the full DaemonRouter
// interface and records what was dispatched so the tests can assert the
// per-command timeouts and payloads.
type forgeTestRouter struct {
	worktreeTestDaemonRouter
	lastCommandType string
	lastPayload     []byte
	lastTimeoutMs   int32
	reply           []byte
	replyErr        error
}

func (r *forgeTestRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *forgeTestRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	r.lastCommandType = commandType
	r.lastPayload = payload
	r.lastTimeoutMs = timeoutMs
	return r.reply, r.replyErr
}

// forgeTestProjects resolves one project owned by the authed test user.
type forgeTestProjects struct {
	path string
	err  error
}

func (p forgeTestProjects) GetProjectWithUserCheck(_ context.Context, id, _ string) (*db.Project, error) {
	if p.err != nil {
		return nil, p.err
	}
	return &db.Project{ID: id, Path: p.path}, nil
}

// newForgeTestService wires a ForgeService over a stub router and project lookup.
func newForgeTestService(router *forgeTestRouter) *ForgeService {
	return NewForgeService(router, forgeTestProjects{path: "/daemon/workspace/control-plane"})
}

// forgeDaemonReply builds a daemon-shaped reply envelope.
func forgeDaemonReply(t *testing.T, isProject, supported bool, exitCode int, report string) []byte {
	t.Helper()
	env := map[string]any{
		"is_forge_project": isProject,
		"supported":        supported,
		"forge_version":    "v0.1.15",
		"exit_code":        exitCode,
	}
	if report != "" {
		// RawMessage: the real daemon sends the report as an OBJECT, never as
		// a JSON string. See deployReply in forge_deploy_test.go.
		env["report"] = json.RawMessage(report)
	}
	b, err := json.Marshal(env)
	require.NoError(t, err)
	return b
}

// A successful topology call passes forge's document through byte-for-byte and
// stamps the metadata the UI needs.
func TestForgeService_GetTopology_PassesReportThroughVerbatim(t *testing.T) {
	// A document with a field this layer knows nothing about: passthrough means
	// it must survive anyway. That is the property a proto mirror would break.
	report := `{"project":"control-plane","latest_release":"v1.5.15","a_field_added_by_a_newer_forge":42}`
	router := &forgeTestRouter{reply: forgeDaemonReply(t, true, true, 0, report)}

	resp, err := newForgeTestService(router).GetTopology(authedCtx(),
		connect.NewRequest(&reliantv1.GetForgeTopologyRequest{ProjectId: "proj-1"}))
	require.NoError(t, err)

	assert.Equal(t, "forge.topology", router.lastCommandType)
	assert.JSONEq(t, report, resp.Msg.ReportJson,
		"forge's document must cross this layer unmodified")
	assert.True(t, resp.Msg.Meta.IsForgeProject)
	assert.True(t, resp.Msg.Meta.Supported)
	assert.Equal(t, "v0.1.15", resp.Msg.Meta.ForgeVersion)

	// No verify requested: no cluster was read, so reachability is
	// "not applicable" — claiming OK would imply an observation nobody made.
	assert.Equal(t, reliantv1.ForgeReachability_FORGE_REACHABILITY_UNSPECIFIED,
		resp.Msg.Meta.Reachability)

	// The daemon-side path is what gets sent, and verify stays off by default.
	var sent struct {
		ProjectPath string `json:"project_path"`
		Verify      bool   `json:"verify"`
	}
	require.NoError(t, json.Unmarshal(router.lastPayload, &sent))
	assert.Equal(t, "/daemon/workspace/control-plane", sent.ProjectPath)
	assert.False(t, sent.Verify)
}

// Timeouts are per-command, and the verify variant of topology gets the long one
// because it makes live cluster round trips.
func TestForgeService_Timeouts_ArePerCommand(t *testing.T) {
	svc := func(r *forgeTestRouter) *ForgeService { return newForgeTestService(r) }
	ok := func(t *testing.T) []byte { return forgeDaemonReply(t, true, true, 0, `{}`) }

	t.Run("topology ledger-only is short", func(t *testing.T) {
		r := &forgeTestRouter{reply: ok(t)}
		_, err := svc(r).GetTopology(authedCtx(),
			connect.NewRequest(&reliantv1.GetForgeTopologyRequest{ProjectId: "p"}))
		require.NoError(t, err)
		assert.EqualValues(t, forgeTopologyTimeoutMs, r.lastTimeoutMs)
	})

	t.Run("topology with verify reads clusters and gets the long budget", func(t *testing.T) {
		r := &forgeTestRouter{reply: ok(t)}
		_, err := svc(r).GetTopology(authedCtx(),
			connect.NewRequest(&reliantv1.GetForgeTopologyRequest{ProjectId: "p", Verify: true}))
		require.NoError(t, err)
		assert.EqualValues(t, forgeTopologyVerifyTimeoutMs, r.lastTimeoutMs)
		assert.Greater(t, forgeTopologyVerifyTimeoutMs, forgeTopologyTimeoutMs,
			"a live-cluster walk must not share the ledger read's budget")
	})

	t.Run("env_verify exceeds forge's own 60s default", func(t *testing.T) {
		r := &forgeTestRouter{reply: ok(t)}
		_, err := svc(r).VerifyEnv(authedCtx(),
			connect.NewRequest(&reliantv1.VerifyForgeEnvRequest{ProjectId: "p", Env: "prod"}))
		require.NoError(t, err)
		assert.EqualValues(t, forgeEnvVerifyTimeoutMs, r.lastTimeoutMs)
		assert.Greater(t, forgeEnvVerifyTimeoutMs, 60_000,
			"must outlast forge's own verify budget so forge's UNREACHABLE verdict wins over our timeout")
	})

	t.Run("audit gets a budget sized for a whole-project rollup", func(t *testing.T) {
		r := &forgeTestRouter{reply: ok(t)}
		_, err := svc(r).GetAudit(authedCtx(),
			connect.NewRequest(&reliantv1.GetForgeAuditRequest{ProjectId: "p"}))
		require.NoError(t, err)
		assert.EqualValues(t, forgeAuditTimeoutMs, r.lastTimeoutMs)
	})

	// No budget may exceed the daemon's own 2-minute per-invocation cap;
	// waiting longer than the daemon would just wait on a killed process.
	for name, ms := range map[string]int32{
		"topology":        forgeTopologyTimeoutMs,
		"topology+verify": forgeTopologyVerifyTimeoutMs,
		"env_verify":      forgeEnvVerifyTimeoutMs,
		"secret_list":     forgeSecretListTimeoutMs,
		"audit":           forgeAuditTimeoutMs,
		"env_status":      forgeEnvStatusTimeoutMs,
	} {
		assert.LessOrEqual(t, ms, int32(120_000),
			"%s budget exceeds the daemon's forgeInvocationTimeout", name)
	}
}

// Forge's exit 2 is a VERDICT about reachability, and must arrive as a
// successful RPC the UI can render as "unknown".
func TestForgeService_ForgeExit2_IsUnreachableData_NotAnError(t *testing.T) {
	report := `{"env":"prod","state":"unreachable"}`
	router := &forgeTestRouter{reply: forgeDaemonReply(t, true, true, 2, report)}

	resp, err := newForgeTestService(router).VerifyEnv(authedCtx(),
		connect.NewRequest(&reliantv1.VerifyForgeEnvRequest{ProjectId: "p", Env: "prod"}))

	require.NoError(t, err, "an unreachable cluster is an answer, not an RPC failure")
	assert.Equal(t, reliantv1.ForgeReachability_FORGE_REACHABILITY_UNREACHABLE,
		resp.Msg.Meta.Reachability)
	assert.EqualValues(t, 2, resp.Msg.Meta.ExitCode)
	assert.NotEmpty(t, resp.Msg.Meta.UnreachableReason)
	assert.JSONEq(t, report, resp.Msg.ReportJson)
}

// Exit 1 is DRIFT — state that WAS observed. It must not be downgraded to
// unreachable, because "your prod has drifted" is the most valuable verdict this
// path delivers and "unknown" would discard it.
func TestForgeService_ForgeExit1_IsDrift_ObservedNotUnreachable(t *testing.T) {
	router := &forgeTestRouter{reply: forgeDaemonReply(t, true, true, 1, `{"drift":true}`)}

	resp, err := newForgeTestService(router).VerifyEnv(authedCtx(),
		connect.NewRequest(&reliantv1.VerifyForgeEnvRequest{ProjectId: "p", Env: "prod"}))
	require.NoError(t, err)

	assert.EqualValues(t, 1, resp.Msg.Meta.ExitCode)
	assert.Equal(t, reliantv1.ForgeReachability_FORGE_REACHABILITY_OK,
		resp.Msg.Meta.Reachability,
		"drift is a conclusion about state that was read; it is not a reachability failure")
}

// Our OWN dispatch timeout on a cluster-reading command yields the same
// conclusion as forge's exit 2: live state is unknown. It must not surface as a
// failed RPC indistinguishable from a real error — a VPN blip is not evidence of
// a release defect.
func TestForgeService_DispatchTimeout_OnClusterRead_IsUnreachableNotError(t *testing.T) {
	router := &forgeTestRouter{
		replyErr: fmt.Errorf("daemon command forge.env_verify (subject x, timeout 75s): nats: timeout"),
	}

	resp, err := newForgeTestService(router).VerifyEnv(authedCtx(),
		connect.NewRequest(&reliantv1.VerifyForgeEnvRequest{ProjectId: "p", Env: "prod"}))

	require.NoError(t, err, "a timed-out cluster read must be UNREACHABLE data, not a failed RPC")
	require.NotNil(t, resp)
	assert.Equal(t, reliantv1.ForgeReachability_FORGE_REACHABILITY_UNREACHABLE,
		resp.Msg.Meta.Reachability)
	assert.Contains(t, resp.Msg.Meta.UnreachableReason, "unknown")
	assert.Empty(t, resp.Msg.ReportJson, "no report was produced")
	assert.NotEmpty(t, resp.Msg.Meta.ForgeVersion,
		"the UI still needs a version even when the read timed out")
}

// The reachability escape hatch is NARROW: it applies only to timeouts, and only
// on commands that actually read a cluster. Everything else keeps failing loudly.
func TestForgeService_NonTimeoutFailures_StillFailLoudly(t *testing.T) {
	t.Run("missing project dir is NotFound", func(t *testing.T) {
		router := &forgeTestRouter{
			replyErr: fmt.Errorf("daemon command forge.env_verify: forge project dir does not exist: /gone"),
		}
		_, err := newForgeTestService(router).VerifyEnv(authedCtx(),
			connect.NewRequest(&reliantv1.VerifyForgeEnvRequest{ProjectId: "p", Env: "prod"}))

		require.Error(t, err)
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
			"a deleted directory is a missing resource, not a retryable outage")
	})

	t.Run("dead daemon is Unavailable", func(t *testing.T) {
		router := &forgeTestRouter{replyErr: fmt.Errorf("no daemon connected")}
		_, err := newForgeTestService(router).VerifyEnv(authedCtx(),
			connect.NewRequest(&reliantv1.VerifyForgeEnvRequest{ProjectId: "p", Env: "prod"}))

		require.Error(t, err)
		assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	})

	t.Run("a forge command failure is not laundered into unreachable", func(t *testing.T) {
		// Contains the word "timeout" AND the command-failed marker. The
		// marker must win: forge ran and failed substantively.
		router := &forgeTestRouter{
			replyErr: fmt.Errorf("daemon command forge.env_verify: forge command failed: exit 1: timeout reading config"),
		}
		_, err := newForgeTestService(router).VerifyEnv(authedCtx(),
			connect.NewRequest(&reliantv1.VerifyForgeEnvRequest{ProjectId: "p", Env: "prod"}))

		require.Error(t, err, "a substantive forge failure must not be reported as 'cluster unknown'")
	})

	t.Run("timeout on a non-cluster command is a real error", func(t *testing.T) {
		router := &forgeTestRouter{replyErr: fmt.Errorf("daemon command forge.audit: nats: timeout")}
		_, err := newForgeTestService(router).GetAudit(authedCtx(),
			connect.NewRequest(&reliantv1.GetForgeAuditRequest{ProjectId: "p"}))

		require.Error(t, err, "audit reads no cluster, so a timeout says nothing about reachability")
		assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	})
}

// Not a forge project is a NORMAL state — most reliant projects are not forge
// projects — and must never light up the UI as an error.
func TestForgeService_NotAForgeProject_IsSuccessNotError(t *testing.T) {
	router := &forgeTestRouter{reply: forgeDaemonReply(t, false, false, 0, "")}

	resp, err := newForgeTestService(router).GetTopology(authedCtx(),
		connect.NewRequest(&reliantv1.GetForgeTopologyRequest{ProjectId: "p"}))

	require.NoError(t, err, "an ordinary non-forge repository is not an error condition")
	assert.False(t, resp.Msg.Meta.IsForgeProject)
	assert.Empty(t, resp.Msg.ReportJson)
}

// An old forge surfaces as supported=false WITH a version, so the UI can say
// "your forge is too old for this view" instead of rendering an empty screen
// that reads as "you have no environments".
func TestForgeService_Unsupported_IsSurfacedNotSwallowed(t *testing.T) {
	env := map[string]any{
		"is_forge_project":   true,
		"supported":          false,
		"forge_version":      "v0.1.13",
		"unsupported_reason": "unknown flag: --json",
		"exit_code":          1,
	}
	b, err := json.Marshal(env)
	require.NoError(t, err)

	resp, err := newForgeTestService(&forgeTestRouter{reply: b}).GetTopology(authedCtx(),
		connect.NewRequest(&reliantv1.GetForgeTopologyRequest{ProjectId: "p"}))
	require.NoError(t, err, "a version-capability miss is data, not an error")

	assert.True(t, resp.Msg.Meta.IsForgeProject)
	assert.False(t, resp.Msg.Meta.Supported)
	assert.Equal(t, "v0.1.13", resp.Msg.Meta.ForgeVersion)
	assert.Equal(t, "unknown flag: --json", resp.Msg.Meta.UnsupportedReason)
}

// Request validation stays in this layer and is cheap: no daemon hop for a
// request that cannot be served.
func TestForgeService_Validation(t *testing.T) {
	t.Run("env is required", func(t *testing.T) {
		router := &forgeTestRouter{}
		_, err := newForgeTestService(router).GetEnvStatus(authedCtx(),
			connect.NewRequest(&reliantv1.GetForgeEnvStatusRequest{ProjectId: "p", Env: "  "}))

		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		assert.Empty(t, router.lastCommandType, "must not dispatch an invalid request")
	})

	t.Run("project_id is required", func(t *testing.T) {
		router := &forgeTestRouter{}
		_, err := newForgeTestService(router).GetAudit(authedCtx(),
			connect.NewRequest(&reliantv1.GetForgeAuditRequest{ProjectId: ""}))

		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		assert.Empty(t, router.lastCommandType)
	})

	t.Run("unauthenticated is rejected", func(t *testing.T) {
		router := &forgeTestRouter{}
		_, err := newForgeTestService(router).GetAudit(context.Background(),
			connect.NewRequest(&reliantv1.GetForgeAuditRequest{ProjectId: "p"}))

		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
		assert.Empty(t, router.lastCommandType)
	})
}

// =============================================================================
// SECRET SAFETY
// =============================================================================

// forgeSecretResponseAllowedFields is the complete set of fields the secret
// response may carry. It is an ALLOW-LIST on purpose: a deny-list of
// scary-sounding names would pass a field called `resolved` or `data`.
//
// Mirrors the reflection test forge uses to pin its own secret report type
// graph. Adding a field here is a deliberate act that should require justifying
// why it cannot carry a value.
var forgeSecretResponseAllowedFields = map[string]bool{
	// The document forge produced: names, booleans and coordinates only, pinned
	// on forge's side by its own reflection test.
	"report_json": true,
	// Metadata about the invocation, not about any secret.
	"meta": true,
}

// The secret RPC's response has nowhere to put a secret VALUE.
//
// This is the last link in a chain: forge's report type graph cannot express a
// value, and the daemon withholds forge's stderr entirely on this path so a
// diagnostic line cannot become the leak channel. This test pins the final link
// — the wire type the browser receives. It fails if anyone adds a field, which
// is the point: the guard is only worth having if it is absolute.
func TestListForgeSecretsResponseCannotCarrySecretValues(t *testing.T) {
	rt := reflect.TypeOf(reliantv1.ListForgeSecretsResponse{})

	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		// Skip protobuf-internal plumbing (state, sizeCache, unknownFields).
		if !field.IsExported() {
			continue
		}

		name := protoJSONName(field)
		assert.True(t, forgeSecretResponseAllowedFields[name],
			"ListForgeSecretsResponse gained field %q (%s). Secret-bearing fields are forbidden on this "+
				"path: forge's report cannot express a value and the daemon withholds its stderr, so this "+
				"response type is the only remaining place a value could reach a browser. If the field "+
				"genuinely cannot carry one, add it to forgeSecretResponseAllowedFields deliberately.",
			name, field.Type)
	}

	// The response carries forge's document as an opaque string, so this layer
	// has no typed field a value could be decoded into even in principle.
	reportField, ok := rt.FieldByName("ReportJson")
	require.True(t, ok)
	assert.Equal(t, reflect.String, reportField.Type.Kind(),
		"report_json must stay an opaque passthrough string")
}

// protoJSONName extracts the canonical proto field name from a generated
// protobuf struct tag. Prefers `name=` (the snake_case name declared in the
// .proto) over `json=` (the lowerCamel JSON alias), so the allow-list is keyed
// by the names a reviewer reads in forge.proto.
func protoJSONName(field reflect.StructField) string {
	tag := field.Tag.Get("protobuf")
	for _, part := range strings.Split(tag, ",") {
		if strings.HasPrefix(part, "name=") {
			return strings.TrimPrefix(part, "name=")
		}
	}
	return field.Name
}

// The secret path never logs or echoes the report body. A value that forge
// somehow emitted must not be amplified by this layer into an error string.
func TestForgeService_ListSecrets_NeverEchoesReportBody(t *testing.T) {
	const canary = "SUPER-SECRET-CANARY-VALUE"

	// A report that (hypothetically) contains a value. Whatever happens, the
	// canary must not appear in an error message.
	router := &forgeTestRouter{
		reply: forgeDaemonReply(t, true, true, 1, fmt.Sprintf(`{"secrets":[{"name":"X","present":true,"leaked":%q}]}`, canary)),
	}

	resp, err := newForgeTestService(router).ListSecrets(authedCtx(),
		connect.NewRequest(&reliantv1.ListForgeSecretsRequest{ProjectId: "p", Env: "prod"}))
	require.NoError(t, err)

	// Exit 1 here means "a declared secret has no value" — a verdict, returned
	// as data rather than an error.
	assert.EqualValues(t, 1, resp.Msg.Meta.ExitCode)
	assert.Equal(t, "forge.secret_list", router.lastCommandType)

	// Secret presence is not a cluster-workload read, so a timeout on this path
	// is a genuine failure rather than a reachability verdict.
	assert.Equal(t, reliantv1.ForgeReachability_FORGE_REACHABILITY_UNSPECIFIED,
		resp.Msg.Meta.Reachability)

	// The body reaches the caller (that is the RPC's job) but never an error.
	assert.NotContains(t, resp.Msg.Meta.UnreachableReason, canary)
	assert.NotContains(t, resp.Msg.Meta.UnsupportedReason, canary)
}

// A dispatch failure on the secret path carries no report-derived text.
func TestForgeService_ListSecrets_ErrorCarriesNoReportText(t *testing.T) {
	const canary = "SUPER-SECRET-CANARY-VALUE"
	router := &forgeTestRouter{replyErr: fmt.Errorf("forge command failed: exit 1")}

	_, err := newForgeTestService(router).ListSecrets(authedCtx(),
		connect.NewRequest(&reliantv1.ListForgeSecretsRequest{ProjectId: "p", Env: "prod"}))

	require.Error(t, err)
	assert.NotContains(t, err.Error(), canary)
}

// THE DAEMON SENDS AN OBJECT, NOT A STRING, and this pins that contract.
//
// forgeReportReply.Report was declared `string` while
// daemonruntime.forgeReportResponse.Report is json.RawMessage, so every
// forge.* RPC failed at the decode with "cannot unmarshal object into Go
// struct field forgeReportReply.report of type string".
//
// The symptom was worse than the error: the sidebar's forge entry is gated on
// GetTopology SUCCEEDING, so a decode failure reads as "not a forge project"
// and the whole UI silently does not appear. Nothing in the product surfaces
// the error — it only exists in the api-server log.
func TestForgeReportReplyDecodesTheDaemonsObjectReport(t *testing.T) {
	// Byte-for-byte the shape the daemon marshals.
	const wire = `{
		"is_forge_project": true,
		"supported": true,
		"forge_version": "v0.1.17",
		"exit_code": 0,
		"report": {"envs": [{"name": "dev", "state": "ok"}], "project": "control-plane"}
	}`

	var reply forgeReportReply
	if err := json.Unmarshal([]byte(wire), &reply); err != nil {
		t.Fatalf("decoding the daemon's reply: %v", err)
	}
	if !reply.IsForgeProject || !reply.Supported {
		t.Errorf("flags lost in the decode: %+v", reply)
	}
	// The report stays OPAQUE — passed through verbatim, never re-encoded.
	// A newer forge adding a field must need no change here.
	var got map[string]any
	if err := json.Unmarshal([]byte(reply.reportJSON()), &got); err != nil {
		t.Fatalf("reportJSON is not valid JSON: %v", err)
	}
	if got["project"] != "control-plane" {
		t.Errorf("report body altered in transit: %v", got)
	}
}

// An absent report renders as empty, not the literal "null", so a caller
// checking for emptiness does not have to special-case JSON's null.
func TestForgeReportReplyRendersAbsentReportAsEmpty(t *testing.T) {
	for name, wire := range map[string]string{
		"omitted": `{"is_forge_project": false}`,
		"null":    `{"is_forge_project": false, "report": null}`,
	} {
		t.Run(name, func(t *testing.T) {
			var reply forgeReportReply
			if err := json.Unmarshal([]byte(wire), &reply); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := reply.reportJSON(); got != "" {
				t.Errorf("reportJSON() = %q, want empty", got)
			}
		})
	}
}
