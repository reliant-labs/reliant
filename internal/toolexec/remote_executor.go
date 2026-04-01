// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// DaemonClientFactory creates a daemon.Client for a given user ID.
// In monolith mode this returns the shared LocalClient; in distributed mode it
// creates a RemoteClient bound to the user's daemon via NATS.
type DaemonClientFactory func(userID string) daemon.Client

// RemoteExecutor executes tools via server-side execution or daemon routing.
type RemoteExecutor struct {
	// Daemon routing (for online/offline check and notifications)
	router DaemonRouter

	// serverExecutor runs server-side tools (ToolRunsOnServer / ToolRunsAnywhere) in-process,
	// avoiding a round-trip to the daemon. Nil means all tools route to the daemon.
	serverExecutor *LocalToolExecutor

	// daemonFactory creates per-request daemon clients for server-side tool execution.
	// When set, executeOnServer uses this to create a daemon.Client scoped to the
	// requesting user, which is thread-safe (no shared mutable state).
	// When nil, executeOnServer falls back to the serverExecutor's default daemon.
	daemonFactory DaemonClientFactory
}

// ToolExecutionRequest is the payload sent to daemon over WebSocket
type ToolExecutionRequest struct {
	RequestID      string                 `json:"request_id"`
	ToolName       string                 `json:"tool_name"`
	ToolInput      string                 `json:"tool_input"`
	ToolCallID     string                 `json:"tool_call_id"`
	ContentBlockID string                 `json:"content_block_id"`
	Context        map[string]interface{} `json:"context"`
	WorkingDir     string                 `json:"working_dir"`
	TimeoutMs      int                    `json:"timeout_ms"`
}

// NewRemoteExecutor creates a new remote tool executor.
func NewRemoteExecutor(router DaemonRouter) *RemoteExecutor {
	return &RemoteExecutor{
		router: router,
	}
}

// SetServerExecutor configures a local executor for server-side tools.
// When set, tools with RunsOn == ToolRunsOnServer or ToolRunsAnywhere execute
// in-process instead of routing through the daemon.
func (e *RemoteExecutor) SetServerExecutor(executor *LocalToolExecutor) {
	e.serverExecutor = executor
}

// SetDaemonClientFactory sets the factory used to create per-request daemon clients
// in executeOnServer. This is the thread-safe way to provide daemon access for
// server-side tools when different requests target different users' daemons.
func (e *RemoteExecutor) SetDaemonClientFactory(f DaemonClientFactory) {
	e.daemonFactory = f
}

// DaemonRouter returns the current daemon router, or nil if none is set.
func (e *RemoteExecutor) DaemonRouter() DaemonRouter {
	return e.router
}

// SetDaemonRouter swaps the daemon router used for daemon communication.
// Nil routers are not allowed: router wiring must be explicit and deterministic.
func (e *RemoteExecutor) SetDaemonRouter(router DaemonRouter) {
	if router == nil {
		panic("toolexec.RemoteExecutor: SetDaemonRouter called with nil router")
	}
	e.router = router
}

// ExecuteTool executes a tool via server-side or daemon-side execution based on the tool's RunsOn location.
// ToolRunsOnDaemon tools are dispatched to the user's daemon; all others execute in-process on the server.
func (e *RemoteExecutor) ExecuteTool(ctx context.Context, req *ToolRequest) (*ToolResult, error) {
	startTime := time.Now()

	// Validate required fields
	if req.UserID == "" {
		return &ToolResult{
			Success:      false,
			IsError:      true,
			Content:      "Missing user ID in tool request",
			ErrorMessage: "user_id is required",
			ErrorCode:    "INVALID_REQUEST",
			StartTime:    startTime,
			EndTime:      time.Now(),
		}, nil
	}
	if req.ChatID == "" {
		return &ToolResult{
			Success:      false,
			IsError:      true,
			Content:      "Missing chat ID in tool request",
			ErrorMessage: "chat_id is required",
			ErrorCode:    "INVALID_REQUEST",
			StartTime:    startTime,
			EndTime:      time.Now(),
		}, nil
	}
	if req.ProjectID == "" {
		return &ToolResult{
			Success:      false,
			IsError:      true,
			Content:      "Missing project ID in tool request",
			ErrorMessage: "project_id is required",
			ErrorCode:    "INVALID_REQUEST",
			StartTime:    startTime,
			EndTime:      time.Now(),
		}, nil
	}

	if e.serverExecutor == nil {
		return nil, fmt.Errorf("server executor not configured: wiring bug — all tools require server-side execution")
	}

	loc := toolRunsOn(req.ToolName)
	if loc == tools.ToolRunsOnDaemon {
		return e.executeOnDaemon(ctx, req, startTime)
	}

	// ToolRunsOnServer, ToolRunsAnywhere, and unknown tools (e.g. MCP) execute on the server.
	if loc == tools.ToolRunsOnServer || loc == tools.ToolRunsAnywhere || loc == "" {
		return e.executeOnServer(ctx, req, startTime)
	}

	// Should be unreachable — all ToolLocation values are covered above.
	return nil, fmt.Errorf("unexpected tool location %q for tool %q", loc, req.ToolName)
}

// executeOnServer runs a tool in-process via the server executor.
func (e *RemoteExecutor) executeOnServer(ctx context.Context, req *ToolRequest, startTime time.Time) (*ToolResult, error) {
	timeoutMs := 0
	if req.Timeout > 0 {
		timeoutMs = int(req.Timeout.Milliseconds())
	}

	contextMap := map[string]interface{}{
		"user_id":    req.UserID,
		"chat_id":    req.ChatID,
		"thread":     req.Thread,
		"message_id": req.MessageID,
		"project": map[string]interface{}{
			"id":   req.ProjectID,
			"path": req.ProjectPath,
			"name": req.ProjectName,
		},
	}
	if req.WorktreePath != "" {
		contextMap["worktree"] = map[string]interface{}{
			"id":   req.WorktreeID,
			"path": req.WorktreePath,
		}
	}

	// Create a per-request daemon client via the factory (thread-safe).
	// Falls back to the executor's default daemon when no factory is set.
	var daemonClient daemon.Client
	if e.daemonFactory != nil {
		daemonClient = e.daemonFactory(req.UserID)
	}
	result := e.serverExecutor.ExecuteToolWithDaemon(ctx, req.ToolName, req.ToolInput, req.ToolCallID, timeoutMs, contextMap, daemonClient)

	return &ToolResult{
		Success:      result.Success,
		IsError:      result.IsError,
		Backgrounded: result.Backgrounded,
		Content:      result.Content,
		Metadata:     result.Metadata,
		StartTime:    startTime,
		EndTime:      time.Now(),
		ErrorMessage: result.ErrorMessage,
		ErrorCode:    result.ErrorCode,
	}, nil
}

// executeOnDaemon dispatches a tool request to the user's daemon and waits for the result.
// Used for tools that must run in the user's environment (e.g., bash, shell commands).
func (e *RemoteExecutor) executeOnDaemon(ctx context.Context, req *ToolRequest, startTime time.Time) (*ToolResult, error) {
	if e.router == nil {
		return nil, fmt.Errorf("daemon router not configured: cannot execute tool %q on daemon", req.ToolName)
	}

	contextMap := map[string]interface{}{
		"user_id":    req.UserID,
		"chat_id":    req.ChatID,
		"thread":     req.Thread,
		"message_id": req.MessageID,
		"project": map[string]interface{}{
			"id":   req.ProjectID,
			"path": req.ProjectPath,
			"name": req.ProjectName,
		},
	}
	if req.WorktreePath != "" {
		contextMap["worktree"] = map[string]interface{}{
			"id":   req.WorktreeID,
			"path": req.WorktreePath,
		}
	}

	timeoutMs := 0
	if req.Timeout > 0 {
		timeoutMs = int(req.Timeout.Milliseconds())
	}

	execReq := &ToolExecutionRequest{
		RequestID:  fmt.Sprintf("%d", time.Now().UnixNano()),
		ToolName:   req.ToolName,
		ToolInput:  req.ToolInput,
		ToolCallID: req.ToolCallID,
		Context:    contextMap,
		TimeoutMs:  timeoutMs,
	}

	resp, err := e.router.SendToolRequestSync(ctx, req.UserID, execReq)
	if err != nil {
		return &ToolResult{
			Success:      false,
			IsError:      true,
			Content:      fmt.Sprintf("Failed to execute tool on daemon: %s", err.Error()),
			ErrorMessage: err.Error(),
			ErrorCode:    "DAEMON_EXECUTION_ERROR",
			StartTime:    startTime,
			EndTime:      time.Now(),
		}, nil
	}

	return &ToolResult{
		Success:      resp.Success,
		IsError:      resp.IsError,
		Backgrounded: resp.Backgrounded,
		Content:      resp.Content,
		Metadata:     resp.Metadata,
		StartTime:    startTime,
		EndTime:      time.Now(),
		ErrorMessage: resp.ErrorMessage,
		ErrorCode:    resp.ErrorCode,
	}, nil
}

// toolRunsOn returns the ToolLocation for the named tool from the registry.
// Returns empty string if the tool is not found (caller falls through to server execution).
func toolRunsOn(name string) tools.ToolLocation {
	for _, def := range tools.GetToolRegistry() {
		if def.Name == name {
			return def.RunsOn
		}
	}
	return "" // unknown tool — execute on server
}

// Close cleans up resources (no-op currently).
func (e *RemoteExecutor) Close() error {
	return nil
}
