// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// maxLLMToolContentBytes caps tool-result content surfaced to the LLM.
// Per-tool truncation (tools.MaxOutputSize, enforced in the ToolWrapper —
// see internal/llm/tools/output_limiter.go) normally caps output well below
// this; this is the consumption-layer backstop for results that bypassed it.
// The NATS transport now chunks oversize replies, so a multi-MB tool result
// arrives intact — user RPCs need the full payload, but dumping it into
// model context is still wrong. 2x the per-tool cap leaves room for the
// wrapper's own truncation warnings so wrapper-truncated output is never
// touched twice.
const maxLLMToolContentBytes = 2 * tools.MaxOutputSize

// capLLMToolContent truncates tool content to maxLLMToolContentBytes with an
// actionable tail. The model gets partial results plus guidance instead of an
// error (or an unbounded context dump).
func capLLMToolContent(content string) string {
	if len(content) <= maxLLMToolContentBytes {
		return content
	}
	keep := maxLLMToolContentBytes
	// Don't split a UTF-8 rune at the cut point.
	for keep > 0 && !utf8.RuneStart(content[keep]) {
		keep--
	}
	return content[:keep] + fmt.Sprintf(
		"\n… [output truncated: %s total — narrow your search or request less data]",
		formatByteSize(int64(len(content))))
}

// DaemonClientFactory creates a daemon.Client for a given user ID.
// In distributed mode it creates a RemoteClient bound to the user's daemon via NATS.
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
	if len(req.Repos) > 0 {
		repos := make([]map[string]interface{}, 0, len(req.Repos))
		for _, r := range req.Repos {
			if r == nil {
				continue
			}
			repos = append(repos, map[string]interface{}{
				"id":            r.ID,
				"name":          r.Name,
				"relative_path": r.RelativePath,
			})
		}
		contextMap["repos"] = repos
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
		Content:      capLLMToolContent(result.Content),
		Metadata:     result.Metadata,
		BinaryParts:  result.BinaryParts,
		StartTime:    startTime,
		EndTime:      time.Now(),
		ErrorMessage: result.ErrorMessage,
		ErrorCode:    result.ErrorCode,
	}, nil
}

// executeOnDaemon dispatches a tool request to the user's daemon and waits for the result.
// Used for tools that must run in the user's environment (e.g., bash, shell commands).
// When the request has a DaemonSelector, it targets a specific daemon instead of the default.
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
	if len(req.Repos) > 0 {
		repos := make([]map[string]interface{}, 0, len(req.Repos))
		for _, r := range req.Repos {
			if r == nil {
				continue
			}
			repos = append(repos, map[string]interface{}{
				"id":            r.ID,
				"name":          r.Name,
				"relative_path": r.RelativePath,
			})
		}
		contextMap["repos"] = repos
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

	// Use selector-aware routing if a daemon selector is provided
	var resp *ToolExecutionResponse
	var err error
	if req.DaemonSelector != nil {
		resp, err = e.router.SendToolRequestSyncWithSelector(ctx, req.UserID, execReq, req.DaemonSelector)
	} else {
		resp, err = e.router.SendToolRequestSync(ctx, req.UserID, execReq)
	}
	if err != nil {
		return &ToolResult{
			Success:      false,
			IsError:      true,
			Content:      fmt.Sprintf("Failed to execute tool on daemon: %s", err.Error()),
			ErrorMessage: err.Error(),
			ErrorCode:    ErrorCodeDaemonUnreached,
			StartTime:    startTime,
			EndTime:      time.Now(),
		}, nil
	}

	// Error envelopes from the transport layer (e.g. the bridge's
	// oversize-reply protection or DAEMON_ERROR replies) may carry only
	// ErrorMessage. The LLM sees the tool result's Content, so fall back to
	// ErrorMessage — otherwise the actionable text is swallowed and the model
	// gets a generic "failed with no error message".
	content := resp.Content
	if content == "" && (!resp.Success || resp.IsError) && resp.ErrorMessage != "" {
		content = resp.ErrorMessage
	}

	return &ToolResult{
		Success:      resp.Success,
		IsError:      resp.IsError,
		Backgrounded: resp.Backgrounded,
		Content:      capLLMToolContent(content),
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
