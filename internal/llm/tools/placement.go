package tools

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/config"
)

// MCPToolPrefix begins the name of every MCP-provided tool: mcp__<server>__<tool>.
const MCPToolPrefix = "mcp__"

// Valid reports whether p is one of the three defined placements.
func (p Placement) Valid() bool {
	switch p {
	case PlacementDaemon, PlacementServer, PlacementAny:
		return true
	}
	return false
}

// MCPServerPlacement is where an MCP server may run. It is computed from the
// config, never declared by it: a user-typed config can not claim
// PlacementServer. stdio spawns a process, and a user's http/sse URL may be
// loopback or LAN-only, carry daemon-side secrets, or be an SSRF vector from
// inside the server network. Only a curated server-side catalog entry earns
// PlacementServer, and those never pass through config.MCPServer.
func MCPServerPlacement(_ config.MCPServer) Placement {
	return PlacementDaemon
}

// PlacementOf returns where the named tool executes. Built-in tools use their
// registry placement; mcp__<server>__<tool> inherits its server's placement.
// An unknown tool, or a registry entry with an undefined placement, is an
// error: the caller must never default to running on the server.
func PlacementOf(name string) (Placement, error) {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, MCPToolPrefix) {
		// The server config is not visible here; the only servers expressible
		// as config.MCPServer are user-typed ones, which are always daemon.
		return MCPServerPlacement(config.MCPServer{}), nil
	}
	for _, def := range GetToolRegistry() {
		if def.Name != name {
			continue
		}
		if !def.Placement.Valid() {
			return "", fmt.Errorf("tool %q has undefined placement %q", name, def.Placement)
		}
		return def.Placement, nil
	}
	return "", fmt.Errorf("unknown tool %q: no placement", name)
}
