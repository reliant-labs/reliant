// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// machinesSource answers one machines section for every org.
type machinesSource struct{ mode controlplanev1.OrgPolicyMode }

func (s machinesSource) ResolveOrgPolicy(context.Context, string) (accesstokenclient.OrgPolicy, error) {
	return accesstokenclient.OrgPolicy{Version: 9, Policy: &controlplanev1.OrgPolicy{Machines: &controlplanev1.MachinesPolicy{
		Mode: s.mode, ForceCredentialsNone: true,
	}}}, nil
}

func machinesCtx(mode controlplanev1.OrgPolicyMode) context.Context {
	return orgpolicy.WithResolver(context.Background(), orgpolicy.New(machinesSource{mode}))
}

func setMachines(t *testing.T, level orgpolicy.Enforcement) {
	t.Helper()
	prev := orgpolicy.EnforcementOf(orgpolicy.PointMachines)
	orgpolicy.SetProcessEnforcement(orgpolicy.PointMachines, level)
	t.Cleanup(func() { orgpolicy.SetProcessEnforcement(orgpolicy.PointMachines, prev) })
}

// capturingRouter records every tool request it is asked to send.
type capturingRouter struct {
	routerStub
	sent []*ToolExecutionRequest
}

func (r *capturingRouter) SendToolRequestSync(_ context.Context, _ string, req *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	r.sent = append(r.sent, req)
	return &ToolExecutionResponse{Success: true}, nil
}

func (r *capturingRouter) SendToolRequestSyncWithSelector(ctx context.Context, user string, req *ToolExecutionRequest, _ *DaemonSelector) (*ToolExecutionResponse, error) {
	return r.SendToolRequestSync(ctx, user, req)
}

func orgShellRequest(org, input string) *ToolRequest {
	return &ToolRequest{UserID: "u", ChatID: "c", ProjectID: "p", OrgID: org, ToolName: names.ToolShell, ToolCallID: "tc", ToolInput: input}
}

func sendThrough(t *testing.T, ctx context.Context, req *ToolRequest) (*capturingRouter, *ToolResult) {
	t.Helper()
	router := &capturingRouter{}
	executor := NewRemoteExecutor(router)
	executor.SetServerExecutor(&LocalToolExecutor{})
	res, err := executor.ExecuteTool(ctx, req)
	require.NoError(t, err)
	return router, res
}

// Under an enforced force_credentials_none the shell input is rewritten
// before dispatch: `credentials: none`, and the neutral environment over
// its env, which a daemon that predates the field still applies. The
// request carries the stamp, and the caller's request is left as it was.
func TestMachinePolicy_ShellInputForcedAndStamped(t *testing.T) {
	setMachines(t, orgpolicy.EnforcementEnforce)
	original := `{"command":"git push","env":{"GH_TOKEN":"ghp_model_wrote_this","KEEP":"1"},"credentials":"inherit"}`
	req := orgShellRequest("org-a", original)
	router, res := sendThrough(t, machinesCtx(controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE), req)
	require.True(t, res.Success, res.Content)
	require.Len(t, router.sent, 1)
	assert.Equal(t, original, req.ToolInput, "the caller's request must not be rewritten under it")

	var in struct {
		Command     string            `json:"command"`
		Credentials string            `json:"credentials"`
		Env         map[string]string `json:"env"`
	}
	require.NoError(t, json.Unmarshal([]byte(router.sent[0].ToolInput), &in))
	assert.Equal(t, "git push", in.Command)
	assert.Equal(t, daemonpolicy.CredentialsNone, in.Credentials)
	for k, v := range daemonpolicy.NeutralEnv() {
		assert.Equal(t, v, in.Env[k], "old-daemon path: neutral %s", k)
	}
	assert.Equal(t, "1", in.Env["KEEP"])
	assert.NotContains(t, router.sent[0].ToolInput, "ghp_model_wrote_this")

	stamp := daemonpolicy.OrgStampFrom(router.sent[0].Context)
	require.NotNil(t, stamp, "the request carries the org stamp")
	assert.True(t, stamp.CredentialsForced())
	assert.Equal(t, "org-a", stamp.OrgID)
	assert.EqualValues(t, 9, stamp.Version)
}

// Cloud and Reliant credentials a step or model put in env never reach the
// wire under an enforced rule: an old daemon applies env alone.
// So do those of a call that already asked for credentials: none, which
// changes nothing worth counting but is neutralised all the same.
func TestMachinePolicy_CallerCredentialsNeverReachAnOldDaemon(t *testing.T) {
	setMachines(t, orgpolicy.EnforcementEnforce)
	env := `"env":{"AWS_SECRET_ACCESS_KEY":"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",` +
		`"GOOGLE_APPLICATION_CREDENTIALS":"/home/u/key.json","RELIANT_TOKEN":"rlat_abcdefghijklmnop",` +
		`"OPENAI_API_KEY":"sk-abcdefghijklmnopqrstuvwxyz","KEEP":"1"}`
	for name, input := range map[string]string{
		"credentials unset":      `{"command":"aws s3 ls",` + env + `}`,
		"credentials none given": `{"command":"aws s3 ls","credentials":"none",` + env + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			router, res := sendThrough(t, machinesCtx(controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE), orgShellRequest("org-a", input))
			require.True(t, res.Success, res.Content)
			require.Len(t, router.sent, 1)
			wire := router.sent[0].ToolInput
			for _, secret := range []string{"AWS_SECRET_ACCESS_KEY", "wJalrXUtnFEMI", "GOOGLE_APPLICATION_CREDENTIALS",
				"RELIANT_TOKEN", "rlat_abcdefghijklmnop", "OPENAI_API_KEY", "sk-abcdefghij"} {
				assert.NotContains(t, wire, secret)
			}
			assert.Contains(t, wire, `"KEEP":"1"`)
			assert.Contains(t, wire, `"credentials":"none"`)
		})
	}
}

// bothRules is an org that enforces an MCP allowlist and forced
// credentials:none.
type bothRules struct{}

func (bothRules) ResolveOrgPolicy(context.Context, string) (accesstokenclient.OrgPolicy, error) {
	return accesstokenclient.OrgPolicy{Version: 11, Policy: &controlplanev1.OrgPolicy{
		Mcp:      &controlplanev1.McpPolicy{Mode: controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, AllowedServers: []string{"github"}},
		Machines: &controlplanev1.MachinesPolicy{Mode: controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, ForceCredentialsNone: true},
	}}, nil
}

// An org that enforces both the MCP and the machines rule sends one stamp
// carrying both, under one org id.
func TestMachinePolicy_OneStampCarriesMCPAndCredentials(t *testing.T) {
	setMachines(t, orgpolicy.EnforcementEnforce)
	prev := orgpolicy.EnforcementOf(orgpolicy.PointMCP)
	orgpolicy.SetProcessEnforcement(orgpolicy.PointMCP, orgpolicy.EnforcementEnforce)
	t.Cleanup(func() { orgpolicy.SetProcessEnforcement(orgpolicy.PointMCP, prev) })

	ctx := orgpolicy.WithResolver(context.Background(), orgpolicy.New(bothRules{}))
	router, res := sendThrough(t, ctx, orgShellRequest("org-a", `{"command":"ls"}`))
	require.True(t, res.Success, res.Content)
	require.Len(t, router.sent, 1)
	stamp := daemonpolicy.OrgStampFrom(router.sent[0].Context)
	require.NotNil(t, stamp)
	assert.Equal(t, "org-a", stamp.OrgID)
	assert.EqualValues(t, 11, stamp.Version)
	assert.True(t, stamp.CredentialsForced())
	require.NotNil(t, stamp.MCP)
	assert.Equal(t, []string{"github"}, stamp.MCP.AllowedServers)
}

// In audit (the org's section, or the platform switch), with the switch
// off, and outside any org, the request goes out exactly as it came, with
// no stamp.
func TestMachinePolicy_AuditOffAndNoOrgChangeNothing(t *testing.T) {
	input := `{"command":"git push"}`
	cases := []struct {
		name  string
		level orgpolicy.Enforcement
		mode  controlplanev1.OrgPolicyMode
		org   string
	}{
		{"section audit", orgpolicy.EnforcementEnforce, controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_AUDIT, "org-a"},
		{"switch audit", orgpolicy.EnforcementAudit, controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, "org-a"},
		{"switch off", orgpolicy.EnforcementOff, controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, "org-a"},
		{"no org", orgpolicy.EnforcementEnforce, controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setMachines(t, tc.level)
			router, res := sendThrough(t, machinesCtx(tc.mode), orgShellRequest(tc.org, input))
			require.True(t, res.Success, res.Content)
			require.Len(t, router.sent, 1)
			assert.Equal(t, input, router.sent[0].ToolInput)
			assert.Nil(t, daemonpolicy.OrgStampFrom(router.sent[0].Context))
		})
	}
}

// A tool that builds its command on the machine with no credentials input
// is refused while the rule is enforced: the worker cannot tell a machine
// that honours the stamp from one that does not.
func TestMachinePolicy_StampOnlyToolRefusedUnderEnforce(t *testing.T) {
	setMachines(t, orgpolicy.EnforcementEnforce)
	req := &ToolRequest{UserID: "u", ChatID: "c", ProjectID: "p", OrgID: "org-a", ToolName: names.ToolProveTest, ToolCallID: "tc",
		ToolInput: `{"command":"go test ./...","files":["a.go"]}`}
	router, res := sendThrough(t, machinesCtx(controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE), req)
	assert.Empty(t, router.sent, "a refused call is never dispatched")
	assert.True(t, res.IsError)
	assert.Equal(t, ErrorCodeOrgPolicy, res.ErrorCode)
	assert.Contains(t, res.Content, "shell tool")

	setMachines(t, orgpolicy.EnforcementAudit)
	router, res = sendThrough(t, machinesCtx(controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE), req)
	assert.True(t, res.Success, "audit refuses nothing")
	assert.Len(t, router.sent, 1)
}

// A command that cannot be policed is not run unpoliced: unparseable input
// under an enforced rule is refused.
func TestMachinePolicy_UnrewritableInputRefused(t *testing.T) {
	setMachines(t, orgpolicy.EnforcementEnforce)
	router, res := sendThrough(t, machinesCtx(controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE),
		orgShellRequest("org-a", `{"command":"git push","env":"GH_TOKEN=x"}`))
	assert.Empty(t, router.sent)
	assert.Equal(t, ErrorCodeOrgPolicy, res.ErrorCode)
}

// Every listed command tool exists and is placed where its reach says.
func TestCommandToolsAreRealDaemonTools(t *testing.T) {
	for name := range commandTools {
		p, err := tools.PlacementOf(name)
		require.NoError(t, err, name)
		assert.Equal(t, tools.PlacementDaemon, p, name)
	}
}

// recordingClient is a daemon client that records the commands it is sent.
type recordingClient struct {
	daemon.Client
	reqs []daemon.RunCommandRequest
}

func (c *recordingClient) RunCommand(_ context.Context, req *daemon.RunCommandRequest) (*daemon.CommandResult, error) {
	c.reqs = append(c.reqs, *req)
	return &daemon.CommandResult{}, nil
}

// The tool executor that runs a stamped call (on the worker, or on a machine
// that reads the stamp) hands the tool a client that forces every command
// it builds, even one whose input never asked.
func TestLocalExecutor_StampForcesEveryCommandTheToolBuilds(t *testing.T) {
	executor := NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{}))
	contextMap := func(stamp *daemonpolicy.OrgStamp) map[string]interface{} {
		m := map[string]interface{}{
			"user_id": "u", "chat_id": "c",
			"project": map[string]interface{}{"id": "p", "path": "/work/p", "name": "p"},
		}
		if v := stamp.ContextValue(); v != nil {
			// As it arrives over the wire: decoded JSON.
			raw, _ := json.Marshal(v)
			var decoded map[string]interface{}
			_ = json.Unmarshal(raw, &decoded)
			m[daemonpolicy.OrgStampContextKey] = decoded
		}
		return m
	}

	forced := &recordingClient{}
	res := executor.ExecuteToolWithDaemon(context.Background(), names.ToolShell, `{"command":"git push","env":{"GH_TOKEN":"x"}}`, "tc", 0,
		contextMap(&daemonpolicy.OrgStamp{OrgID: "org-a", Credentials: daemonpolicy.CredentialsNone}), forced)
	require.False(t, res.IsError, res.Content)
	require.Len(t, forced.reqs, 1)
	assert.Equal(t, daemonpolicy.CredentialsNone, forced.reqs[0].Credentials)
	assert.Equal(t, "", forced.reqs[0].Env["GH_TOKEN"])

	// No stamp: today's behaviour exactly.
	plain := &recordingClient{}
	res = executor.ExecuteToolWithDaemon(context.Background(), names.ToolShell, `{"command":"ls"}`, "tc", 0, contextMap(nil), plain)
	require.False(t, res.IsError, res.Content)
	require.Len(t, plain.reqs, 1)
	assert.Equal(t, "", plain.reqs[0].Credentials)
	assert.False(t, strings.Contains(strings.Join(mapKeys(plain.reqs[0].Env), ","), "GIT_CONFIG_COUNT"))
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
