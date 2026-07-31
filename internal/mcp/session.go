// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"strings"

	"github.com/reliant-labs/reliant/internal/config"
)

// Session-scoped MCP servers.
//
// Most MCP servers are stateless request/response: two callers sharing one
// subprocess get answers that do not depend on each other. chrome-devtools-mcp
// is not one of those. It keeps a SELECTED PAGE in the node process — a single
// mutable pointer, addressed by an INDEX into that process's page list — and
// every page-facing tool (`take_snapshot`, `take_screenshot`, `click`, `fill`,
// `evaluate_script`, `list_console_messages`, …) acts on whatever that pointer
// currently names. `select_page` moves it.
//
// Under the workflow engine's fan-out several agent threads drive the browser
// at the same time through ONE subprocess, so one thread's `select_page`
// silently repoints every other thread's next call. Measured on real-workflow
// run 1: five concurrent frontend leaves, one of which abandoned its charter's
// verification step and another of which clicked Submit on a peer's record.
// Nothing errors; the calls succeed against the wrong page.
//
// This cannot be fixed by locking. The race is across a SEQUENCE of calls
// (`select_page` then act), not within one call, and there is no signal that
// delimits an agent's browser session — so there is nothing to hold a lock
// across. Nor can it be fixed by having each caller re-select before acting:
// the pointer is an index, and the index space itself shifts when another
// thread opens or closes a tab, so a remembered index is not a stable identity.
//
// What IS process-global is only global within a process. So a session-scoped
// server gets its OWN subprocess per session key, and the pointer stops being
// shared. chrome-devtools-mcp is launched with `--isolated`, which already
// gives each subprocess a throwaway browser profile, so per-session processes
// are also per-session browsers with no cross-talk in storage either.
//
// The cost is one node + headless Chrome per concurrently-browsing session.
// That is bounded by the lazy client: a session's subprocess is only spawned on
// its first browser tool call and is reaped after lazyIdleTimeout of inactivity
// (see lazy_client.go), so idle sessions cost nothing.
//
// Kept name-scoped rather than a config.MCPServer field, for the same reason
// isLazyStartServer is: the persisted MCP config schema stays unchanged and
// user-defined servers keep today's shared-client behaviour.
func isSessionScopedServer(name string, cfg config.MCPServer) bool {
	return name == chromeDevtoolsServerName && cfg.Type == config.MCPStdio
}

// sessionKeySeparator joins a logical server name to a session key.
//
// "::" is chosen because validateMCPLogicalServerName rejects it outright, so a
// composite key can never collide with — or be mistaken for — a real server
// name, and can never reach the model as part of an `mcp__<server>__<tool>`
// tool name.
const sessionKeySeparator = "::session:"

// sessionClientKey is the internal key a session's private client is stored
// under. Never a server name: see sessionClients on Manager.
func sessionClientKey(serverName, session string) string {
	return serverName + sessionKeySeparator + session
}

// normalizeSessionKey trims a caller-supplied session key. An empty key means
// "no session" and routes to the shared client, which is what every caller
// without a thread (the CLI, one-off daemon commands) gets — the behaviour
// before session scoping existed.
func normalizeSessionKey(session string) string {
	return strings.TrimSpace(session)
}
