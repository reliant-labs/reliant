// Copyright (c) 2025 Reliant Labs

package orgpolicy

import (
	"context"
	"strings"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
)

// PointMCP is the mcp section (§4.5): which MCP servers' tools the org's
// agents are offered and may call.
const PointMCP Point = "mcp"

// ReasonMCPServerNotAllowed is the reason a tool of an MCP server the org's
// policy does not admit is refused with. It is part of the API and never
// changes.
const ReasonMCPServerNotAllowed = "mcp_server_not_allowed"

// mcpToolPrefix begins every MCP tool's name: mcp__<server>__<tool>.
const mcpToolPrefix = "mcp__"

// MCPToolAdmission is what doc's mcp section says about the MCP tool
// toolName (mcp__<server>__<tool>). No section, or a name that is not an MCP
// tool's, governs nothing; allow_all admits every server; otherwise a name is
// admitted only when EVERY server it could belong to is listed, so an empty
// list admits none.
//
// A name does not say which server it belongs to: a server's name, and a
// tool's, may contain "__", so mcp__github__evil__x is github's tool evil__x
// or github__evil's tool x, and the daemon runs whichever it has. Every split
// of the name into mcp__<server>__<tool> (both non-empty) is a server it could
// be; one that is not listed refuses the name. Fail closed: a name is never
// admitted on the strength of the one split that happens to be allowed. Where
// the server is known exactly (an offered tool's adapter), MCPServerAdmission
// is exact.
func MCPToolAdmission(doc *controlplanev1.OrgPolicy, toolName string) Admission {
	sec := doc.GetMcp()
	if sec == nil || !strings.HasPrefix(toolName, mcpToolPrefix) {
		return Admission{}
	}
	adm := Admission{Governed: true, Admitted: sec.GetAllowAll(), Mode: sec.GetMode()}
	if adm.Admitted {
		return adm
	}
	servers := mcpServersOf(toolName)
	if len(servers) == 0 {
		return adm
	}
	for _, server := range servers {
		if !MCPServerAdmission(doc, server).Admitted {
			return adm
		}
	}
	adm.Admitted = true
	return adm
}

// mcpServersOf is every server an MCP tool name could belong to: the <server>
// of each split of the name into mcp__<server>__<tool>, both non-empty.
func mcpServersOf(toolName string) []string {
	rest := strings.TrimPrefix(toolName, mcpToolPrefix)
	var servers []string
	for i := 0; i < len(rest); {
		j := strings.Index(rest[i:], "__")
		if j < 0 {
			break
		}
		cut := i + j
		if server, tool := rest[:cut], rest[cut+2:]; server != "" && tool != "" {
			servers = append(servers, server)
		}
		i = cut + 1
	}
	return servers
}

// MCPServerAdmission is what doc's mcp section says about the server named
// server, exactly.
func MCPServerAdmission(doc *controlplanev1.OrgPolicy, server string) Admission {
	sec := doc.GetMcp()
	if sec == nil {
		return Admission{}
	}
	server = strings.TrimSpace(server)
	adm := Admission{Governed: true, Admitted: sec.GetAllowAll(), Mode: sec.GetMode()}
	for _, allowed := range sec.GetAllowedServers() {
		if strings.TrimSpace(allowed) == server && server != "" {
			adm.Admitted = true
		}
	}
	return adm
}

// JudgeMCPServer decides offering server's tools in orgID's runs (Judge, at
// PointMCP, by the exact server name).
func JudgeMCPServer(ctx context.Context, orgID, server string, quiet bool) (Decision, error) {
	return Judge(ctx, PointMCP, orgID, server, ReasonMCPServerNotAllowed, quiet,
		func(doc *controlplanev1.OrgPolicy) Admission { return MCPServerAdmission(doc, server) })
}

// JudgeMCPTool decides a use of the MCP tool toolName in orgID's runs
// (Judge, at PointMCP). The decision's Subject is the tool name.
func JudgeMCPTool(ctx context.Context, orgID, toolName string, quiet bool) (Decision, error) {
	return Judge(ctx, PointMCP, orgID, toolName, ReasonMCPServerNotAllowed, quiet,
		func(doc *controlplanev1.OrgPolicy) Admission { return MCPToolAdmission(doc, toolName) })
}

// MCPStamp is the MCP part of the org policy a worker stamps into a
// daemon-bound request (§5.4): which servers the org's agents may use there.
// It is set only while the rule is ENFORCED (the platform switch and the
// section's mode): an audited rule asks the daemon for nothing.
type MCPStamp struct {
	OrgID   string
	Version int64
	// AllowAll admits every server; otherwise only AllowedServers.
	AllowAll       bool
	AllowedServers []string
}

// EnforcedMCPStamp is orgID's MCP stamp, or nil when there is nothing to
// stamp: no org, no mcp section, the rule audited or switched off, or a
// policy that cannot be read (the worker's own refusal holds then; the stamp
// is defence in depth, never the enforcement). Judges nothing, counts nothing.
func EnforcedMCPStamp(ctx context.Context, orgID string) *MCPStamp {
	if orgID == "" || EnforcementOf(PointMCP) != EnforcementEnforce {
		return nil
	}
	pol, err := For(ctx, orgID)
	if err != nil {
		return nil
	}
	sec := pol.Doc.GetMcp()
	if sec == nil || !Enforced(PointMCP, sec.GetMode()) {
		return nil
	}
	return &MCPStamp{OrgID: orgID, Version: pol.Version, AllowAll: sec.GetAllowAll(),
		AllowedServers: append([]string(nil), sec.GetAllowedServers()...)}
}
