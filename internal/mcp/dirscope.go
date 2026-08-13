// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"os"
	"strings"

	"github.com/reliant-labs/reliant/internal/config"
)

// Directory-scoped MCP servers.
//
// The manager keeps ONE client per logical server name and associates it with
// every project that asks for it. For a stateless request/response server that
// is correct and efficient: two callers sharing a subprocess get answers that do
// not depend on each other.
//
// A server that INDEXES A TREE is not one of those. A language server resolves
// its workspace once — from its working directory, or from a path argument — at
// launch, and every later answer is scoped to that tree. Share one process
// across two projects and the second project gets answers about the first.
//
// Measured, with a Go language server registered globally: a chat in project B
// asked for a symbol that exists in project B, and the server answered from the
// daemon's own checkout, returning confident matches from unrelated dependencies
// instead of no matches. Nothing errored. A wrong answer delivered confidently
// is worse than a missing tool, because the caller acts on it.
//
// This CANNOT be fixed by passing the project on each call: the scope is fixed
// at process start, before any tool call exists. The only fix is one process per
// tree, which is what keying the client by project achieves.
//
// Unlike session scoping (session.go) and lazy start, this is declared in CONFIG
// rather than matched on a server name here. Which servers index a tree is a
// fact about the servers a USER installs — reliant cannot enumerate them, and a
// name match would silently miss every server it had not been taught.
func isDirScopedServer(cfg config.MCPServer) bool {
	return cfg.DirScoped
}

// dirKeySeparator joins a logical server name to a project path.
//
// Shares the "::" prefix rule with sessionKeySeparator: validateMCPLogicalServerName
// rejects it outright, so a composite key can never collide with a real server
// name or reach the model as part of an `mcp__<server>__<tool>` tool name.
const dirKeySeparator = "::dir:"

// dirClientKey is the internal key a project's private client is stored under.
// Never a server name: see dirClients on Manager.
func dirClientKey(serverName, projectPath string) string {
	return serverName + dirKeySeparator + projectPath
}

// resolveServerDir returns the working directory a server's process should start
// in: its explicit Dir when set (expanded, so a config can reference $HOME),
// else the project the tool call belongs to.
//
// An explicit Dir wins over the caller's project because pinning a server to one
// tree is the reason to write the field at all. Returning "" means "inherit the
// daemon's directory", which is the pre-existing behaviour and the right answer
// for a server that does not care.
func resolveServerDir(cfg config.MCPServer, projectPath string) string {
	if d := strings.TrimSpace(cfg.Dir); d != "" {
		return os.ExpandEnv(d)
	}
	return normalizeProjectPath(projectPath)
}

// projectPathPlaceholders are the spellings a config may use in `args` to mean
// "the project this tool call belongs to".
//
// Args need this because some servers take the tree as an ARGUMENT rather than
// reading their working directory (`--workspace <path>`), and a config file is
// written once for every project. Without expansion such a server can only be
// configured by hard-coding one project's absolute path, which is the same
// wrong-tree failure in a different disguise.
var projectPathPlaceholders = []string{"${projectPath}", "$projectPath", "${workspaceFolder}"}

// expandArgs substitutes the project path into a server's args. Env-style
// ${VAR} expansion is applied afterwards so a config can also reference $HOME.
func expandArgs(args []string, projectPath string) []string {
	if len(args) == 0 {
		return args
	}
	out := make([]string, len(args))
	for i, a := range args {
		for _, ph := range projectPathPlaceholders {
			a = strings.ReplaceAll(a, ph, projectPath)
		}
		out[i] = os.ExpandEnv(a)
	}
	return out
}
