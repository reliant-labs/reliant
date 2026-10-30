// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/nomachine"
	"github.com/reliant-labs/reliant/internal/observability"
)

// daemonCancelPushTimeout bounds the detached "stop that tool" push sent when a
// call is abandoned. Short: it is one fire-and-forget message on an already-open
// connection, and the caller is unwinding.
const daemonCancelPushTimeout = 5 * time.Second

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

// DaemonClientFactory creates the daemon.Client a server-side tool uses for one
// request: bound to the user's daemon that selector names (the run's machine,
// as ExecuteTools chose it), or to default resolution for a nil selector. In
// distributed mode it is a RemoteClient over NATS (RemoteDaemonClients).
type DaemonClientFactory func(userID string, selector *DaemonSelector) daemon.Client

// RemoteExecutor executes tools via server-side execution or daemon routing.
type RemoteExecutor struct {
	// Daemon routing (for online/offline check and notifications)
	router DaemonRouter

	// serverExecutor runs server-side tools (PlacementServer / PlacementAny) in-process,
	// avoiding a round-trip to the daemon. Nil means all tools route to the daemon.
	serverExecutor *LocalToolExecutor

	// daemonFactory creates per-request daemon clients for server-side tool execution.
	// When set, executeOnServer uses this to create a daemon.Client scoped to the
	// requesting user, which is thread-safe (no shared mutable state).
	// When nil, executeOnServer falls back to the serverExecutor's default daemon.
	daemonFactory DaemonClientFactory

	// preemptGrace overrides defaultPreemptReplyGrace. Zero means the default;
	// tests shorten it.
	preemptGrace time.Duration

	// placementOf resolves a tool's placement. Nil means tools.PlacementOf;
	// tests substitute it to declare a tool the registry does not have.
	placementOf func(name string) (tools.Placement, error)

	// egress bounds the egress tools executeOnServer runs; nil admits all.
	egress EgressLimiter
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
// When set, tools with Placement == PlacementServer or PlacementAny execute
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

// ExecuteTool executes a tool via server-side or daemon-side execution based on the tool's placement.
// PlacementDaemon tools are dispatched to the user's daemon; server/any tools execute in-process;
// PlacementPreferDaemon tools try the daemon and fall back to the server (executePreferDaemon).
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

	// A tool call rides out a short disconnect of its daemon — a gateway
	// rollout drops every daemon for ~20s — instead of failing at once. This
	// covers both placements: daemon-placed tools go to the daemon through
	// the router, and server-placed ones (view, write, edit) reach the
	// user's files through it. A caller that wants to hear about the wait
	// (execute_tools, which shows the run as waiting for its machine) set
	// its own observer already.
	if reconnectWaitFrom(ctx) == nil {
		ctx = WithDaemonReconnectWait(ctx, nil)
	}

	resolvePlacement := e.placementOf
	if resolvePlacement == nil {
		resolvePlacement = tools.PlacementOf
	}
	placement, err := resolvePlacement(req.ToolName)
	if err != nil {
		return nil, err
	}
	switch placement {
	case tools.PlacementDaemon:
		// A run with no machine was never offered this tool and execute_tools
		// refuses it before dispatch; this is the transport's own backstop.
		if nomachine.Is(ctx) {
			return &ToolResult{
				Success:      false,
				IsError:      true,
				Content:      nomachine.Refusal(req.ToolName),
				ErrorMessage: nomachine.ErrNoMachine.Error(),
				ErrorCode:    "NO_MACHINE",
				StartTime:    startTime,
				EndTime:      time.Now(),
			}, nil
		}
		return e.executeOnDaemon(ctx, req, startTime)
	case tools.PlacementPreferDaemon:
		return e.executePreferDaemon(ctx, req, startTime)
	case tools.PlacementServer, tools.PlacementAny:
		return e.executeOnServer(ctx, req, startTime)
	}
	return nil, fmt.Errorf("unexpected placement %q for tool %q", placement, req.ToolName)
}

// executePreferDaemon runs a PlacementPreferDaemon tool on the run's machine
// when it has one and on the server otherwise; the semantics are documented on
// tools.PlacementPreferDaemon. It never fails a call for want of a daemon.
func (e *RemoteExecutor) executePreferDaemon(ctx context.Context, req *ToolRequest, startTime time.Time) (*ToolResult, error) {
	switch {
	case nomachine.Is(ctx):
		return e.runPreferDaemonOnServer(ctx, req, startTime, FallbackNoMachine)
	case e.router == nil:
		return e.runPreferDaemonOnServer(ctx, req, startTime, FallbackNoRouter)
	}

	// The server can run this tool, so do not wait for a disconnected daemon
	// to come back (rideOutDisconnect): fail over at once.
	res, err := e.executeOnDaemon(WithoutDaemonReconnectWait(ctx), req, startTime)
	if err != nil {
		return nil, err
	}
	// An abandoned call stays the daemon's: re-running it on the server would
	// do the work nobody is waiting for.
	if !preferDaemonDispatchFailed(res) || ctx.Err() != nil {
		noteDaemonRefusal(req.ToolName, res)
		return notePlacement(req.ToolName, res, ExecutedAtDaemon, ""), nil
	}
	logging.WarnContext(ctx, "Prefer-daemon tool could not run on the daemon; running it on the server",
		"toolName", req.ToolName, "toolCallID", req.ToolCallID, "errorCode", res.ErrorCode, "error", res.ErrorMessage)
	return e.runPreferDaemonOnServer(ctx, req, startTime, FallbackDispatchFailed)
}

// preferDaemonDispatchFailed reports whether a daemon attempt produced no
// answer from the tool itself: the round trip did not complete, or the daemon
// does not have the tool. A tool's own failure is an answer and is returned.
func preferDaemonDispatchFailed(res *ToolResult) bool {
	return IsTransportErrorCode(res.ErrorCode) || res.ErrorCode == "TOOL_NOT_FOUND"
}

func (e *RemoteExecutor) runPreferDaemonOnServer(ctx context.Context, req *ToolRequest, startTime time.Time, reason string) (*ToolResult, error) {
	res, err := e.executeOnServer(ctx, req, startTime)
	if err != nil {
		return nil, err
	}
	return notePlacement(req.ToolName, res, ExecutedAtServer, reason), nil
}

// notePlacement records where a prefer-daemon call ran: on the result, in the
// "_placement" key of its metadata document, and on the placement metric. The
// note goes into object-shaped (or empty) Metadata and leaves any other
// Metadata untouched, like the truncation note (tools.withTruncationMetadata);
// every other field of the result is identical wherever the call ran.
func notePlacement(toolName string, res *ToolResult, ranAt, reason string) *ToolResult {
	res.ExecutedAt, res.FallbackReason = ranAt, reason
	observability.ToolPlacementTotal.WithLabelValues(toolName, ranAt, reason).Inc()

	doc := map[string]any{}
	if res.Metadata != "" {
		if err := json.Unmarshal([]byte(res.Metadata), &doc); err != nil || doc == nil {
			return res
		}
	}
	if _, taken := doc[tools.PlacementMetadataKey]; taken {
		return res
	}
	note := map[string]any{"ran_at": ranAt}
	if reason != "" {
		note["fallback_reason"] = reason
	}
	doc[tools.PlacementMetadataKey] = note
	if merged, err := json.Marshal(doc); err == nil {
		res.Metadata = string(merged)
	}
	return res
}

// executeOnServer runs a tool in-process via the server executor.
func (e *RemoteExecutor) executeOnServer(ctx context.Context, req *ToolRequest, startTime time.Time) (*ToolResult, error) {
	// Every egress tool passes here, so this is the one place its rate and
	// concurrency are bounded. A refusal is a tool result, not an error: the
	// model reads "rate limited, retry after Ns" and the run continues.
	releaseEgress, refusal := e.admitEgress(ctx, req, startTime)
	if refusal != nil {
		return refusal, nil
	}
	defer releaseEgress()

	timeoutMs := 0
	if req.Timeout > 0 {
		timeoutMs = int(req.Timeout.Milliseconds())
	}

	contextMap := req.toolContextMap()

	// Create a per-request daemon client via the factory (thread-safe).
	// Falls back to the executor's default daemon when no factory is set.
	//
	// A run with no machine gets none: the client resolves (and can wake) the
	// user's default daemon, and tools that need one already report that it
	// is missing.
	//
	// The client is bound to the run's selector: view, write and edit reach
	// the user's files through it, and must reach the machine every other
	// tool of the run executes on.
	var daemonClient daemon.Client
	if e.daemonFactory != nil && !nomachine.Is(ctx) {
		daemonClient = e.daemonFactory(req.UserID, req.DaemonSelector)
	}
	// MCP tools bind a daemon-backed runtime from this context; carrying the
	// run's selector keeps them on the daemon built-in tools use.
	ctx = WithDaemonSelector(ctx, req.DaemonSelector)
	ctx, releasePreemption := cancelOnPreemption(ctx)
	defer releasePreemption()
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

// toolContextMap is the request's context as the executing tool's
// rctx.ToolContext is rebuilt from it (LocalToolExecutor.executeTool), on the
// worker for a server-placed tool and on the daemon for a daemon-placed one.
func (req *ToolRequest) toolContextMap() map[string]interface{} {
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
		worktree := map[string]interface{}{
			"id":   req.WorktreeID,
			"path": req.WorktreePath,
		}
		if req.WorktreeDaemonID != "" {
			worktree["daemon_id"] = req.WorktreeDaemonID
		}
		contextMap["worktree"] = worktree
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
	return contextMap
}

// executeOnDaemon dispatches a tool request to the user's daemon and waits for the result.
// Used for tools that must run in the user's environment (e.g., bash, shell commands).
// When the request has a DaemonSelector, it targets a specific daemon instead of the default.
func (e *RemoteExecutor) executeOnDaemon(ctx context.Context, req *ToolRequest, startTime time.Time) (*ToolResult, error) {
	if e.router == nil {
		return nil, fmt.Errorf("daemon router not configured: cannot execute tool %q on daemon", req.ToolName)
	}

	contextMap := req.toolContextMap()

	timeoutMs := 0
	if req.Timeout > 0 {
		timeoutMs = int(req.Timeout.Milliseconds())
	}

	execReq := &ToolExecutionRequest{
		RequestID:  newRequestID(),
		ToolName:   req.ToolName,
		ToolInput:  req.ToolInput,
		ToolCallID: req.ToolCallID,
		Context:    contextMap,
		TimeoutMs:  timeoutMs,
	}

	// Tell the daemon to stop when this call is abandoned.
	//
	// The daemon runs each execution under context.WithCancel(context.Background())
	// (daemonruntime/runtime.go), deliberately decoupled from the transport so a
	// dropped connection cannot kill a healthy command. The consequence is that
	// nothing about OUR context reaches it: when a pause or interrupt cancels the
	// activity, the server stops waiting and marks the call cancelled while the
	// user's `bash` keeps running to completion on their machine.
	//
	// The daemon already has the machinery to stop it — cancelToolExecution
	// cancels the registered context, which propagates to exec.CommandContext and
	// signals the process group. It just has to be told. This is the same push
	// InterruptThread sends for a user-cancelled tool, fired here so that ANY
	// abandonment of the call reaches the process, whatever cancelled it.
	//
	// Keyed on ToolCallID: the daemon registers the cancel under both that and
	// its transport request id, and ToolCallID is the one both sides agree on.
	// Best-effort and detached — our context is already dead, and a cancel that
	// arrives after the command finished is a no-op there.
	//
	// Gated on THIS call's own outcome, not on ambient context state. Every tool
	// in a turn runs against one shared activity context (see execute_tools.go),
	// so `ctx.Err() != nil` is true for a tool that finished perfectly well while
	// a SIBLING was cancelled. Pushing then would be wrong in two ways: it is a
	// stray cancel for a tool call id a later dispatch may legitimately reuse,
	// and for a BACKGROUNDED tool — which returns success precisely so it can
	// outlive the turn — it would kill the process group the user asked to keep.
	abandoned := false
	defer func() {
		if !abandoned || req.ToolCallID == "" {
			return
		}
		cancelCtx, cancel := context.WithTimeout(WithDaemonSelector(context.WithoutCancel(ctx), req.DaemonSelector), daemonCancelPushTimeout)
		defer cancel()
		if err := e.router.SendToolExecutionCancel(cancelCtx, req.UserID, req.ToolCallID, "tool execution abandoned"); err != nil {
			logging.WarnContext(ctx, "Could not tell the daemon to stop an abandoned tool; it may run to completion",
				"error", err, "toolCallID", req.ToolCallID, "toolName", req.ToolName)
		}
	}()

	// A preempted call is told to stop and then still waited on, for its own
	// answer; see relayPreemptionToDaemon.
	waitCtx, releasePreemption := e.relayPreemptionToDaemon(ctx, req)
	defer releasePreemption()

	// Use selector-aware routing if a daemon selector is provided
	var resp *ToolExecutionResponse
	var err error
	if req.DaemonSelector != nil {
		resp, err = e.router.SendToolRequestSyncWithSelector(waitCtx, req.UserID, execReq, req.DaemonSelector)
	} else {
		resp, err = e.router.SendToolRequestSync(waitCtx, req.UserID, execReq)
	}
	if err != nil {
		// This call was given up on, rather than answered. Only here is the
		// daemon still potentially running work nobody is waiting for — a
		// context cancellation surfaces as this error, and so does a transport
		// failure that leaves the command orphaned.
		abandoned = true
		return &ToolResult{
			Success:       false,
			IsError:       true,
			Content:       fmt.Sprintf("Failed to execute tool on daemon: %s", err.Error()),
			ErrorMessage:  err.Error(),
			ErrorCode:     ErrorCodeDaemonUnreached,
			DaemonPending: IsDaemonPending(err),
			NotDispatched: errors.Is(err, ErrNotDispatched),
			// The machine it was addressed to: no daemon answered, so this
			// names the one a caller would wait for.
			DaemonID:  NotDispatchedDaemonID(err),
			StartTime: startTime,
			EndTime:   time.Now(),
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
		// A transport failure the gateway answered for — the daemon's
		// connection dropped under the request, or the gateway was too busy
		// to deliver it — is a reply, but not from the daemon: it proves
		// nothing about the machine being reachable. A restarted daemon's
		// "interrupted by restart" is from the daemon, so it does.
		RanOnDaemon:  !IsTransportErrorCode(resp.ErrorCode) || resp.ErrorCode == ErrorCodeDaemonRestarted,
		DaemonID:     resp.DaemonID,
		StartTime:    startTime,
		EndTime:      time.Now(),
		ErrorMessage: resp.ErrorMessage,
		ErrorCode:    resp.ErrorCode,
	}, nil
}

// Close cleans up resources (no-op currently).
func (e *RemoteExecutor) Close() error {
	return nil
}
