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
// tool's, governs nothing; allow_all admits every server; otherwise the tool
// is admitted only when it is a tool OF a listed server, so an empty list
// admits none.
//
// A server's name may itself contain "__", so a tool name is never split to
// find its server: it is admitted when it begins with mcp__<allowed>__ and
// names a tool after that. A name no allowed server accounts for (one the
// model invented, a server renamed since) is refused. Where the server is
// known exactly (an offered tool's adapter), MCPServerAdmission is exact.
func MCPToolAdmission(doc *controlplanev1.OrgPolicy, toolName string) Admission {
	sec := doc.GetMcp()
	if sec == nil || !strings.HasPrefix(toolName, mcpToolPrefix) {
		return Admission{}
	}
	adm := Admission{Governed: true, Admitted: sec.GetAllowAll(), Mode: sec.GetMode()}
	for _, server := range sec.GetAllowedServers() {
		server = strings.TrimSpace(server)
		if server == "" {
			continue
		}
		if tool, ok := strings.CutPrefix(toolName, mcpToolPrefix+server+"__"); ok && tool != "" {
			adm.Admitted = true
			break
		}
	}
	return adm
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
