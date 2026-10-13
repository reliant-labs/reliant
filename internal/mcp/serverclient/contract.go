// Package serverclient is the MCP client for server processes (api-server,
// worker). It speaks streamable HTTP only and cannot express a command, so a
// stdio server can not be constructed here.
//
// This package must not depend on os/exec, directly or transitively; a test
// enforces it. That rules out the MCP SDK (its CommandTransport imports
// os/exec) and internal/config, so the wire protocol is implemented here.
//
// forge:outbound-io
//
// A client talks to one curated catalog entry's URL over MCP's streamable
// HTTP: a POST per JSON-RPC request or notification, and a DELETE to end the
// session.
//
//forge:not-a-component: nothing builds it yet: it is the server role's MCP client for a curated integration backed by a vendor's hosted MCP (research/INTEGRATIONS.md, research/TOOL_PLACEMENT.md §4.2), and its first caller constructs it per catalog entry and credentials
package serverclient

import (
	"context"
	"encoding/json"
	"net/http"
)

// Service is a streamable-HTTP MCP client bound to one catalog entry.
type Service interface {
	// Initialize performs the MCP handshake. ListTools and CallTool perform
	// it first when it has not been done.
	Initialize(ctx context.Context) error
	// ListTools returns the server's tools.
	ListTools(ctx context.Context) ([]Tool, error)
	// CallTool invokes a tool and returns the raw result object.
	CallTool(ctx context.Context, name string, args map[string]any) (json.RawMessage, error)
	// Close ends the session, best effort.
	Close() error
}

// DialGuard builds the transport every request goes through. *netguard.Guard
// satisfies it: its transport refuses any connection to a non-public address
// AFTER DNS resolution, which New's URL checks cannot see (a hostname may
// resolve to 10.x or the metadata address).
type DialGuard interface {
	Transport() *http.Transport
}

// Deps configure one client.
type Deps struct {
	// Entry is the catalog entry the client is bound to. New refuses one
	// that is not https, or whose host is not allowed.
	Entry Entry
	// Creds authenticate every call.
	Creds Creds
	// Guard builds the dialing transport. nil is netguard.New().
	Guard DialGuard
}
