// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// The org MCP policy at offer time (control-plane docs/design/org-policies.md
// §4.5): a disallowed server's tools are filtered before ResolveToolAccess,
// so they are never offered, never load_tool-able and never in tag:mcp.

// mcpPolicy answers org "acme" with an mcp section allowing "github" only, in
// mode; every other org has no policy. down is a control plane that cannot be
// asked.
type mcpPolicy struct {
	mode controlplanev1.OrgPolicyMode
	down bool
}

func (p mcpPolicy) ResolveOrgPolicy(_ context.Context, orgID string) (accesstokenclient.OrgPolicy, error) {
	if p.down {
		return accesstokenclient.OrgPolicy{}, errors.New("control plane unreachable")
	}
	if orgID != "acme" {
		return accesstokenclient.OrgPolicy{}, nil
	}
	return accesstokenclient.OrgPolicy{Version: 9, Policy: &controlplanev1.OrgPolicy{Mcp: &controlplanev1.McpPolicy{
		Mode: p.mode, AllowedServers: []string{"github"},
	}}}, nil
}

func setMCPSwitch(t *testing.T, e orgpolicy.Enforcement) {
	t.Helper()
	prev := orgpolicy.EnforcementOf(orgpolicy.PointMCP)
	orgpolicy.SetProcessEnforcement(orgpolicy.PointMCP, e)
	t.Cleanup(func() { orgpolicy.SetProcessEnforcement(orgpolicy.PointMCP, prev) })
}

func mcpTool(t *testing.T, server, name string) tools.Tool {
	t.Helper()
	a, err := tools.NewMCPToolAdapter(server, mcp.Tool{Name: name})
	require.NoError(t, err)
	return a
}

// The two servers a machine reports: one the org allows, one it does not,
// plus a server whose name begins like the allowed one.
func machineMCPTools(t *testing.T) []tools.Tool {
	return []tools.Tool{
		mcpTool(t, "github", "create_issue"),
		mcpTool(t, "slack", "post_message"),
		mcpTool(t, "github__evil", "exfiltrate"),
	}
}

func enforcingCtx(src orgpolicy.Source) context.Context {
	return orgpolicy.WithResolver(context.Background(), orgpolicy.New(src))
}

func TestMCPToolsAllowedByOrg_DisallowedServerAbsentFromLoadToolAndTagMCP(t *testing.T) {
	setMCPSwitch(t, orgpolicy.EnforcementEnforce)
	ctx := enforcingCtx(mcpPolicy{mode: controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE})

	offered := toolNames(mcpToolsAllowedByOrg(ctx, "acme", machineMCPTools(t)))
	assert.Equal(t, []string{"mcp__github__create_issue"}, offered,
		"by the adapter's exact server: github__evil is not github")

	// A declaration that preloads tag:mcp and may load everything.
	caps := tools.ResolveCapabilities(tools.CapabilityInputs{
		Access:   tools.ResolveToolAccess([]string{"tag:mcp"}, []string{tools.LoadableWildcard}, offered),
		MCPTools: offered,
	})
	assert.True(t, caps.Offers("mcp__github__create_issue"), "an allowed server is unaffected")
	for _, name := range []string{"mcp__slack__post_message", "mcp__github__evil__exfiltrate"} {
		assert.False(t, caps.Offers(name), "%s is offered through tag:mcp", name)
		assert.False(t, caps.CanLoad(name), "%s is load_tool-able", name)
	}
}

func TestMCPToolsAllowedByOrg_UnchangedWithoutAnEnforcedRule(t *testing.T) {
	all := []string{"mcp__github__create_issue", "mcp__github__evil__exfiltrate", "mcp__slack__post_message"}
	for name, tc := range map[string]struct {
		sw   orgpolicy.Enforcement
		mode controlplanev1.OrgPolicyMode
		org  string
		down bool
	}{
		"a run of no org":                 {orgpolicy.EnforcementEnforce, controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, "", false},
		"an org with no policy":           {orgpolicy.EnforcementEnforce, controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, "globex", false},
		"the section in audit":            {orgpolicy.EnforcementEnforce, controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_AUDIT, "acme", false},
		"the platform switch in audit":    {orgpolicy.EnforcementAudit, controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, "acme", false},
		"the platform switch off":         {orgpolicy.EnforcementOff, controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, "acme", false},
		"audit with an unreadable policy": {orgpolicy.EnforcementAudit, controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, "acme", true},
	} {
		t.Run(name, func(t *testing.T) {
			setMCPSwitch(t, tc.sw)
			got := toolNames(mcpToolsAllowedByOrg(enforcingCtx(mcpPolicy{mode: tc.mode, down: tc.down}), tc.org, machineMCPTools(t)))
			assert.ElementsMatch(t, all, got)
		})
	}
}

// An enforcing point that cannot read the policy offers no MCP tool.
func TestMCPToolsAllowedByOrg_UnreadableUnderEnforceOffersNone(t *testing.T) {
	setMCPSwitch(t, orgpolicy.EnforcementEnforce)
	got := mcpToolsAllowedByOrg(enforcingCtx(mcpPolicy{down: true}), "acme", machineMCPTools(t))
	assert.Empty(t, got)
}
