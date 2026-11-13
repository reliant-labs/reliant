// Copyright (c) 2025 Reliant Labs

package daemonpolicy

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
)

// OrgStampContextKey is the key of a daemon-bound tool request's context map
// (toolexec.ToolRequest.toolContextMap) that carries the run's org policy
// stamp (control-plane docs/design/org-policies.md §5.4).
const OrgStampContextKey = "org_policy"

// OrgStamp is the daemon-relevant part of the org policy that governs a run,
// stamped by the worker into every request it sends to a machine for that
// run. Only rules the worker ENFORCES are stamped: an audited rule, or a
// platform switch that is off, stamps nothing, so a stamp is always a change
// the daemon must make.
//
// The worker enforces each rule itself (it rewrites what it sends), so the
// stamp is defence in depth: the daemon that honours it also covers what the
// worker cannot reach (a command a tool builds on the machine, a terminal).
// It is not signed yet, so nothing relies on it; a daemon that predates it
// ignores the unknown key. No stamp means today's behaviour exactly.
type OrgStamp struct {
	// OrgID is the org whose policy this is.
	OrgID string `json:"org_id"`
	// Version is the policy version the worker decided with, for attribution.
	Version int64 `json:"version,omitempty"`
	// Credentials is CredentialsNone when the org forces `credentials: none`
	// on the commands of its runs (machines.force_credentials_none): every
	// command the request leads to runs without GitHub, git, ssh or cloud
	// credentials, whatever the step or the model asked for.
	Credentials string `json:"credentials,omitempty"`
	// MCP, when set, is the org's MCP allowlist (mcp.allowed_servers /
	// allow_all) the worker enforces for the run: a machine that reads it
	// starts no other MCP server for the run and runs no other server's
	// tool. nil asks nothing of MCP.
	MCP *OrgStampMCP `json:"mcp,omitempty"`
}

// OrgStampMCP is the MCP part of an OrgStamp.
type OrgStampMCP struct {
	// AllowAll admits every server.
	AllowAll bool `json:"allow_all"`
	// AllowedServers are server names as in mcp__<server>__<tool>. Empty
	// (without AllowAll) admits none.
	AllowedServers []string `json:"allowed_servers"`
}

// MCPServerAllowed reports whether s lets the run start or use the MCP
// server named server. A stamp without an MCP rule allows every server.
func (s *OrgStamp) MCPServerAllowed(server string) bool {
	if s == nil || s.MCP == nil || s.MCP.AllowAll {
		return true
	}
	return slices.Contains(s.MCP.AllowedServers, server)
}

// MCPToolAllowed reports whether a tool named name could belong to a server
// s allows: an MCP tool (mcp__<server>__<tool>) whose name starts with an
// allowed server's prefix; any other tool. It is the check before anything
// starts the tool's server, and it is not enough on its own: a name does not
// identify its server (allowed "a" and forbidden "a__b" both give
// "mcp__a__b__x"), so the caller checks the server the name resolved to with
// MCPServerAllowed before running the tool.
func (s *OrgStamp) MCPToolAllowed(name string) bool {
	if s == nil || s.MCP == nil || s.MCP.AllowAll || !strings.HasPrefix(name, "mcp__") {
		return true
	}
	for _, server := range s.MCP.AllowedServers {
		if strings.HasPrefix(name, "mcp__"+server+"__") {
			return true
		}
	}
	return false
}

// CredentialsForced reports whether s forces `credentials: none`. A nil
// stamp forces nothing.
func (s *OrgStamp) CredentialsForced() bool {
	return s != nil && s.Credentials == CredentialsNone
}

// Empty reports whether s asks the daemon for nothing.
func (s *OrgStamp) Empty() bool { return s == nil || (!s.CredentialsForced() && s.MCP == nil) }

// ContextValue is s as a context-map value: the object a request carries to
// the daemon,
//
//	{"org_id": ..., "version": N, "credentials": "none",
//	 "mcp": {"allow_all": bool, "allowed_servers": [...]}}
//
// with only the rules s holds. nil when s asks for nothing, so a request
// without an enforced rule carries no stamp at all.
func (s *OrgStamp) ContextValue() map[string]interface{} {
	if s.Empty() {
		return nil
	}
	m := map[string]interface{}{"org_id": s.OrgID, "version": s.Version}
	if s.CredentialsForced() {
		m["credentials"] = s.Credentials
	}
	if s.MCP != nil {
		servers := make([]interface{}, 0, len(s.MCP.AllowedServers))
		for _, name := range s.MCP.AllowedServers {
			servers = append(servers, name)
		}
		m["mcp"] = map[string]interface{}{"allow_all": s.MCP.AllowAll, "allowed_servers": servers}
	}
	return m
}

// OrgStampFrom reads the stamp from a request's context map: nil when there is
// none, or when it does not decode (a stamp this build cannot read asks it
// for nothing it understands).
func OrgStampFrom(contextMap map[string]interface{}) *OrgStamp {
	v, ok := contextMap[OrgStampContextKey]
	if !ok || v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var s OrgStamp
	if json.Unmarshal(raw, &s) != nil {
		return nil
	}
	return &s
}

type orgStampKey struct{}

// WithOrgStamp returns ctx carrying s, so code below the request edge (the
// MCP manager starting a project's servers, a runtime that forwards it to a
// machine) honours it. A stamp that asks for nothing is not carried.
func WithOrgStamp(ctx context.Context, s *OrgStamp) context.Context {
	if s.Empty() {
		return ctx
	}
	return context.WithValue(ctx, orgStampKey{}, s)
}

// OrgStampFromContext is the stamp WithOrgStamp put on ctx, else nil.
func OrgStampFromContext(ctx context.Context) *OrgStamp {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(orgStampKey{}).(*OrgStamp)
	return s
}
