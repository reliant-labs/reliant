// Copyright (c) 2025 Reliant Labs

package toolexec

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// ErrorCodeMCPServerNotAllowed is the ToolResult.ErrorCode of a call to an
// MCP tool whose server the run's org does not allow (org-policies §4.5).
const ErrorCodeMCPServerNotAllowed = "MCP_SERVER_NOT_ALLOWED"

// ErrorCodeOrgPolicyUnavailable is the ToolResult.ErrorCode of a call the
// org's policy governs while that policy cannot be read and the point
// enforces: refused for now, never run unconstrained; the call may be tried
// again.
const ErrorCodeOrgPolicyUnavailable = "ORG_POLICY_UNAVAILABLE"

// refuseMCPByOrgPolicy applies the mcp section of the run's org policy to an
// MCP tool call, before it is dispatched to a machine (control-plane
// docs/design/org-policies.md §4.5): the server-side half that holds whatever
// the daemon's release. call_llm already withholds a disallowed server's
// tools; this covers a name the model invents and a call carried across a
// policy change. nil lets the call proceed: not an MCP tool, a run of no org,
// an org with no MCP policy, or audit (counted here, once per call).
//
// The refusal is a tool result the model reads, so the conversation goes on.
func refuseMCPByOrgPolicy(ctx context.Context, req *ToolRequest, startTime time.Time) *ToolResult {
	if req.OrgID == "" || !strings.HasPrefix(req.ToolName, tools.MCPToolPrefix) {
		return nil
	}
	d, err := orgpolicy.JudgeMCPTool(ctx, req.OrgID, req.ToolName, false)
	switch {
	case err != nil:
		return &ToolResult{
			IsError:      true,
			Content:      fmt.Sprintf("%s was not run: your organization's MCP policy cannot be read right now. Try again shortly.", req.ToolName),
			ErrorMessage: err.Error(),
			ErrorCode:    ErrorCodeOrgPolicyUnavailable,
			StartTime:    startTime,
			EndTime:      time.Now(),
		}
	case d.Refused:
		return &ToolResult{
			IsError: true,
			Content: fmt.Sprintf("%s was not run: your organization's policy does not allow this MCP server in its runs. "+
				"Use another tool, or ask an org admin to allow the server.", req.ToolName),
			ErrorMessage: orgpolicy.ReasonMCPServerNotAllowed,
			ErrorCode:    ErrorCodeMCPServerNotAllowed,
			StartTime:    startTime,
			EndTime:      time.Now(),
		}
	}
	return nil
}

// OrgPolicyContextKey is the context-map key of a daemon-bound request that
// carries the run's org policy stamp (control-plane
// docs/design/org-policies.md §5.4): the daemon-relevant rules the worker
// ENFORCES for the run, with the org and policy version they come from. The
// gateway forwards it untouched; a daemon that predates it ignores the
// unknown key; one that reads it refuses to spawn an MCP server the stamp
// does not allow. It is defence in depth, unsigned, and nothing relies on it:
// the worker's own refusal (refuseMCPByOrgPolicy) is the enforcement. No
// stamp means today's behaviour exactly.
const OrgPolicyContextKey = "org_policy"

// orgPolicyStampValue is req's stamp as a context-map value:
//
//	{"org_id": ..., "version": N, "mcp": {"allow_all": bool, "allowed_servers": [...]}}
//
// nil when the worker enforces nothing the daemon could also apply.
func orgPolicyStampValue(req *ToolRequest) map[string]interface{} {
	s := req.mcpStamp
	if s == nil {
		return nil
	}
	servers := make([]interface{}, 0, len(s.AllowedServers))
	for _, name := range s.AllowedServers {
		servers = append(servers, name)
	}
	return map[string]interface{}{
		"org_id":  s.OrgID,
		"version": s.Version,
		"mcp":     map[string]interface{}{"allow_all": s.AllowAll, "allowed_servers": servers},
	}
}
