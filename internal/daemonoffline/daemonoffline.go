// Copyright (c) 2025 Reliant Labs

// Package daemonoffline provides the shared "daemon not connected" detection
// helpers used by both the toolexec router (where the condition originates)
// and the workflow runtime (where the consecutive-turn halt logic lives).
//
// Living in a dependency-free package lets both sides import it without
// pulling in each other's transitive deps — important because
// internal/toolexec depends on internal/llm/tools, and
// internal/workflow/runtime is imported by internal/llm/tools.
package daemonoffline

import (
	"errors"
	"strings"

	"connectrpc.com/connect"
)

// ErrorSubstring is the stable substring planted in the daemon-offline connect
// error message by NATSDaemonRouter.SendDaemonCommand / SendToolRequestSync /
// SendKillProcess when daemonRequestError classifies the request as having
// reached no connected daemon.
// The wrapping path is:
//
//	connect.NewError(connect.CodeUnavailable, fmt.Errorf("no daemon connected for user"))
//
// Callers downstream (e.g. RemoteExecutor.ExecuteTool) often fmt.Errorf-wrap
// this further, and some sites flatten it into a ToolResult.Content string.
// The substring is the only artifact that reliably survives every wrapping
// path, so detection must scan it rather than rely on errors.As /
// connect.CodeOf alone.
//
// DO NOT RENAME without updating the producer sites in internal/toolexec.
const ErrorSubstring = "no daemon connected"

// IsError reports whether err is the specific "daemon not connected for this
// user" condition emitted by the daemon router, as opposed to other
// CodeUnavailable cases (e.g. backend temporarily unreachable, reconnecting,
// etc.).
//
// Detection is intentionally narrow:
//
//  1. The chain's combined message must include ErrorSubstring. This filters
//     out CodeUnavailable cases that genuinely aren't about the daemon (e.g.
//     "daemon unreachable; cannot validate path" from repo.go — different
//     surface, different remediation).
//  2. If a wrapped connect.Error exists, it MUST carry CodeUnavailable. This
//     guards against any future code path that embeds the substring under a
//     different status code (e.g. an Internal error that happens to mention
//     the daemon in its message).
//
// When no connect.Error is in the chain (RemoteExecutor.ExecuteTool drops the
// connect wrapping when it turns the daemon error into a ToolResult.Content
// string and the caller re-fmt.Errorf-wraps it), the substring alone is
// load-bearing.
func IsError(err error) bool {
	if err == nil {
		return false
	}

	if !strings.Contains(err.Error(), ErrorSubstring) {
		return false
	}

	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return connectErr.Code() == connect.CodeUnavailable
	}

	// No connect.Error in the chain — accept on substring alone.
	return true
}

// IsToolResultContent reports whether a stringified tool-result content blob
// signals the daemon-offline condition. RemoteExecutor.ExecuteTool turns the
// daemon-offline connect.Error into:
//
//	ToolResult{IsError: true, ErrorCode: "DAEMON_EXECUTION_ERROR",
//	           Content: "Failed to execute tool on daemon: unavailable: no daemon connected for user"}
//
// The workflow loop receives this as a plain string inside the ExecuteTools
// activity output — no Go error to errors.As against. The substring check is
// enough because the wrapper path is the only one that emits this exact
// substring into tool-result content.
func IsToolResultContent(content string) bool {
	return strings.Contains(content, ErrorSubstring)
}
