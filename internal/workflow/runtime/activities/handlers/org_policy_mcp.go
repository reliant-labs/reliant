// Copyright (c) 2025 Reliant Labs

package handlers

import (
	"context"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// mcpServerTool is an MCP tool that knows its server's name exactly
// (*tools.MCPToolAdapter).
type mcpServerTool interface {
	MCPServer() string
}

// mcpToolsAllowedByOrg is mcpTools without the tools of the MCP servers
// orgID's policy does not let its agents see (control-plane
// docs/design/org-policies.md §4.5). Only an enforced rule drops anything:
// under audit every tool stays offered, and a call to one is counted where it
// runs (toolexec), so the count is of uses, not of turns. A policy that
// cannot be read where the point enforces offers no MCP tool at all (fail
// closed); the next turn asks again. A run of no org, or an org with no MCP
// policy, is offered every tool, as before.
func mcpToolsAllowedByOrg(ctx context.Context, orgID string, mcpTools []tools.Tool) []tools.Tool {
	if orgID == "" || len(mcpTools) == 0 {
		return mcpTools
	}
	out := make([]tools.Tool, 0, len(mcpTools))
	var dropped []string
	verdict := map[string]bool{}
	for _, t := range mcpTools {
		key, judge := "tool:"+t.Name(), func() (orgpolicy.Decision, error) { return orgpolicy.JudgeMCPTool(ctx, orgID, t.Name(), true) }
		if s, ok := t.(mcpServerTool); ok {
			server := s.MCPServer()
			key, judge = "server:"+server, func() (orgpolicy.Decision, error) { return orgpolicy.JudgeMCPServer(ctx, orgID, server, true) }
		}
		allowed, seen := verdict[key]
		if !seen {
			d, err := judge()
			if err != nil {
				logging.WarnContext(ctx, "[CallLLM] The organization's MCP policy cannot be read; its MCP tools are not offered this turn",
					"org_id", orgID, "error", err)
			}
			allowed = err == nil && !d.Refused
			verdict[key] = allowed
		}
		if allowed {
			out = append(out, t)
		} else {
			dropped = append(dropped, t.Name())
		}
	}
	if len(dropped) > 0 {
		logging.DebugContext(ctx, "[CallLLM] MCP tools withheld by the organization's policy", "org_id", orgID, "tools", dropped)
	}
	return out
}
