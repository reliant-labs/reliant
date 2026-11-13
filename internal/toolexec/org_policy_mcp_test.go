// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// The org MCP policy at call time (control-plane docs/design/org-policies.md
// §4.5): RemoteExecutor.ExecuteTool refuses a tool of a disallowed server
// before dispatch, whatever the model named.

// acmeAllowsGitHubMCP is org acme allowing only the "github" MCP server.
type acmeAllowsGitHubMCP struct{ down bool }

func (p acmeAllowsGitHubMCP) ResolveOrgPolicy(_ context.Context, orgID string) (accesstokenclient.OrgPolicy, error) {
	if p.down {
		return accesstokenclient.OrgPolicy{}, errors.New("control plane unreachable")
	}
	if orgID != "acme" {
		return accesstokenclient.OrgPolicy{}, nil
	}
	return accesstokenclient.OrgPolicy{Version: 9, Policy: &controlplanev1.OrgPolicy{Mcp: &controlplanev1.McpPolicy{
		Mode: controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, AllowedServers: []string{"github"},
	}}}, nil
}

func setMCPEnforcement(t *testing.T, e orgpolicy.Enforcement) {
	t.Helper()
	prev := orgpolicy.EnforcementOf(orgpolicy.PointMCP)
	orgpolicy.SetProcessEnforcement(orgpolicy.PointMCP, e)
	t.Cleanup(func() { orgpolicy.SetProcessEnforcement(orgpolicy.PointMCP, prev) })
}

func mcpCall(org, tool string) *ToolRequest {
	return &ToolRequest{
		ToolName: tool, ToolInput: `{}`, ToolCallID: "c1",
		UserID: "user-1", ChatID: "chat", ProjectID: "proj", OrgID: org,
		DaemonSelector: &DaemonSelector{ID: "daemon-B"},
	}
}

// contextCapturingRouter is selectorCapturingRouter that also keeps the
// request's context map, as the daemon receives it.
type contextCapturingRouter struct {
	selectorCapturingRouter
	context map[string]interface{}
}

func (r *contextCapturingRouter) SendToolRequestSyncWithSelector(ctx context.Context, user string, req *ToolExecutionRequest, sel *DaemonSelector) (*ToolExecutionResponse, error) {
	r.context = req.Context
	return r.selectorCapturingRouter.SendToolRequestSyncWithSelector(ctx, user, req, sel)
}

func policyExecutor() (*RemoteExecutor, *contextCapturingRouter) {
	router := &contextCapturingRouter{}
	exec := NewRemoteExecutor(router)
	exec.SetServerExecutor(NewLocalToolExecutor(nil))
	return exec, router
}

func TestExecuteTool_OrgPolicyRefusesADisallowedOrInventedMCPServer(t *testing.T) {
	setMCPEnforcement(t, orgpolicy.EnforcementEnforce)
	ctx := orgpolicy.WithResolver(context.Background(), orgpolicy.New(acmeAllowsGitHubMCP{}))

	for _, name := range []string{
		"mcp__slack__post_message",   // a server the org does not allow
		"mcp__invented__anything",    // a name the model made up
		"mcp__githubx__create_issue", // a server that only looks like an allowed one
		"mcp__github__",              // the allowed server's prefix, but no tool
	} {
		exec, router := policyExecutor()
		res, err := exec.ExecuteTool(ctx, mcpCall("acme", name))
		require.NoError(t, err, name)
		assert.True(t, res.IsError, name)
		assert.Equal(t, ErrorCodeMCPServerNotAllowed, res.ErrorCode, name)
		assert.Contains(t, res.Content, "your organization's policy does not allow this MCP server", name)
		assert.Empty(t, router.tool, "%s reached the daemon", name)
	}
}

func TestExecuteTool_OrgPolicyLetsAnAllowedMCPServerThrough(t *testing.T) {
	setMCPEnforcement(t, orgpolicy.EnforcementEnforce)
	ctx := orgpolicy.WithResolver(context.Background(), orgpolicy.New(acmeAllowsGitHubMCP{}))

	exec, router := policyExecutor()
	res, err := exec.ExecuteTool(ctx, mcpCall("acme", "mcp__github__create_issue"))
	require.NoError(t, err)
	assert.True(t, res.RanOnDaemon)
	assert.Equal(t, "mcp__github__create_issue", router.tool)
}

func TestExecuteTool_OrgPolicyRefusesNothingWithoutAnEnforcedRule(t *testing.T) {
	for name, tc := range map[string]struct {
		sw   orgpolicy.Enforcement
		org  string
		down bool
	}{
		"a run of no org":                 {orgpolicy.EnforcementEnforce, "", false},
		"an org with no policy":           {orgpolicy.EnforcementEnforce, "globex", false},
		"the platform switch in audit":    {orgpolicy.EnforcementAudit, "acme", false},
		"the platform switch off":         {orgpolicy.EnforcementOff, "acme", false},
		"audit with an unreadable policy": {orgpolicy.EnforcementAudit, "acme", true},
	} {
		t.Run(name, func(t *testing.T) {
			setMCPEnforcement(t, tc.sw)
			ctx := orgpolicy.WithResolver(context.Background(), orgpolicy.New(acmeAllowsGitHubMCP{down: tc.down}))
			exec, router := policyExecutor()
			res, err := exec.ExecuteTool(ctx, mcpCall(tc.org, "mcp__slack__post_message"))
			require.NoError(t, err)
			assert.True(t, res.RanOnDaemon)
			assert.Equal(t, "mcp__slack__post_message", router.tool)
		})
	}
}

// An enforcing point that cannot read the policy refuses the call for now,
// retryably, and never runs it unconstrained.
func TestExecuteTool_OrgPolicyUnreadableUnderEnforce(t *testing.T) {
	setMCPEnforcement(t, orgpolicy.EnforcementEnforce)
	ctx := orgpolicy.WithResolver(context.Background(), orgpolicy.New(acmeAllowsGitHubMCP{down: true}))
	exec, router := policyExecutor()
	res, err := exec.ExecuteTool(ctx, mcpCall("acme", "mcp__github__create_issue"))
	require.NoError(t, err)
	assert.Equal(t, ErrorCodeOrgPolicyUnavailable, res.ErrorCode)
	assert.Empty(t, router.tool)
}

// Tools that are not MCP tools are never judged by the MCP rule.
func TestExecuteTool_OrgPolicyMCPRuleIgnoresOtherTools(t *testing.T) {
	setMCPEnforcement(t, orgpolicy.EnforcementEnforce)
	ctx := orgpolicy.WithResolver(context.Background(), orgpolicy.New(acmeAllowsGitHubMCP{}))
	assert.Nil(t, refuseMCPByOrgPolicy(ctx, mcpCall("acme", "shell"), time.Now()))
}

// The enforced MCP rule rides to the daemon in the context map (§5.4); an
// audited rule, an org with no MCP policy and a run of no org stamp nothing.
func TestExecuteTool_StampsTheEnforcedMCPRuleIntoTheContextMap(t *testing.T) {
	stampOf := func(t *testing.T, sw orgpolicy.Enforcement, org string) map[string]interface{} {
		t.Helper()
		setMCPEnforcement(t, sw)
		ctx := orgpolicy.WithResolver(context.Background(), orgpolicy.New(acmeAllowsGitHubMCP{}))
		exec, router := policyExecutor()
		res, err := exec.ExecuteTool(ctx, mcpCall(org, "mcp__github__create_issue"))
		require.NoError(t, err)
		require.True(t, res.RanOnDaemon)
		v, _ := router.context[OrgPolicyContextKey].(map[string]interface{})
		return v
	}

	got := stampOf(t, orgpolicy.EnforcementEnforce, "acme")
	require.NotNil(t, got, "an enforced MCP rule is stamped")
	assert.Equal(t, "acme", got["org_id"])
	assert.Equal(t, int64(9), got["version"])
	assert.Equal(t, map[string]interface{}{"allow_all": false, "allowed_servers": []interface{}{"github"}}, got["mcp"])

	assert.Nil(t, stampOf(t, orgpolicy.EnforcementAudit, "acme"), "audit stamps nothing")
	assert.Nil(t, stampOf(t, orgpolicy.EnforcementEnforce, "globex"), "no policy stamps nothing")
	assert.Nil(t, stampOf(t, orgpolicy.EnforcementEnforce, ""), "no org stamps nothing")
}
