// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// TerminalOutputEvent represents a terminal output message or session lifecycle event.
type TerminalOutputEvent struct {
	SessionID string
	Data      []byte
	Closed    bool   // true when the terminal session has closed
	ExitCode  int32  // meaningful when Closed is true
	Error     string // non-empty on error
}

// ProcessOutputEvent represents a chunk of process output.
type ProcessOutputEvent struct {
	ProcessID  string
	Data       string
	Stream     string // "stdout" or "stderr"
	Sequence   uint64
	IsComplete bool
	ExitCode   int32
}

// Transport-failure error codes. These say the tool NEVER RAN — the request
// did not complete a round trip to a daemon. They are emitted only by the
// transport itself:
//
//	ErrorCodeDaemonUnreached — RemoteExecutor, when the router call failed
//	                           (no daemon connected, NATS timeout, ...)
//	ErrorCodeDaemonRoundTrip — the NATS bridge, when the gateway could not
//	                           complete the round trip (the connection holding
//	                           the request closed under it)
//
// Everything the TOOL reports about its own execution — including a command
// that exited non-zero — comes back Success=true / IsError=false with the exit
// code inside the payload, or with a tool-owned code (EXECUTION_ERROR,
// PARSE_ERROR, TOOL_NOT_FOUND, CANCELLED). That is the whole distinction
// IsTransportErrorCode draws, and it is what keeps "the daemon vanished" from
// being recorded as "your lint failed".
const (
	ErrorCodeDaemonUnreached = "DAEMON_EXECUTION_ERROR"
	ErrorCodeDaemonRoundTrip = "DAEMON_ERROR"
)

// IsTransportErrorCode reports whether an ErrorCode means the tool never ran,
// as opposed to ran-and-failed. Callers that turn a tool result into a
// command verdict MUST branch on this: a lane that never ran is not a lane
// that failed, and must not consume a retry or be reported as a lint/test/build
// failure.
func IsTransportErrorCode(code string) bool {
	switch code {
	case ErrorCodeDaemonUnreached, ErrorCodeDaemonRoundTrip:
		return true
	default:
		return false
	}
}

// TransportError says a tool request never reached a daemon. It exists so
// callers that would otherwise flatten a failure into an exit code can tell
// "never ran" from "ran and failed" with errors.As rather than by scanning a
// message.
type TransportError struct {
	Code    string // one of the transport-failure codes above
	Command string // the command that never ran, for the operator
	Detail  string // the transport's own message
}

func (e *TransportError) Error() string {
	return "daemon transport failure (" + e.Code + "): the command never ran: " + e.Detail
}

// ToolExecutionResponse is the synchronous response from a daemon tool execution.
type ToolExecutionResponse struct {
	RequestID    string `json:"request_id"`
	Success      bool   `json:"success"`
	IsError      bool   `json:"is_error"`
	Content      string `json:"content"`
	Metadata     string `json:"metadata,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	Backgrounded bool   `json:"backgrounded,omitempty"`
}

// DaemonRouter abstracts all daemon-bound operations behind a transport-agnostic interface.
// Implementation:
//   - NATSDaemonRouter: routes via NATS request-reply to the api-server holding the connection
type DaemonRouter interface {
	// IsDaemonOnline checks if a daemon is connected for the given user.
	// Returns (online, nil) for definitive results, or (false, err) for infrastructure failures.
	IsDaemonOnline(ctx context.Context, userID string) (bool, error)

	// SendToolRequest routes a tool execution request to the user's daemon (fire-and-forget).
	SendToolRequest(ctx context.Context, userID string, request *ToolExecutionRequest) error

	// SendToolRequestSync routes a tool execution request to the user's daemon and waits
	// for the result. Used for tools with RunsOn == ToolRunsOnDaemon.
	SendToolRequestSync(ctx context.Context, userID string, request *ToolExecutionRequest) (*ToolExecutionResponse, error)

	// SendToolRequestSyncWithSelector routes a tool execution request to a specific daemon
	// matching the given selector. Falls back to SendToolRequestSync behavior if selector is nil.
	SendToolRequestSyncWithSelector(ctx context.Context, userID string, request *ToolExecutionRequest, selector *DaemonSelector) (*ToolExecutionResponse, error)

	// SendToolExecutionCancel cancels a running tool execution.
	SendToolExecutionCancel(ctx context.Context, userID, requestID, reason string) error

	// SendKillProcess sends a kill signal to a background process on the user's daemon.
	SendKillProcess(ctx context.Context, userID, processID string) error

	// SendDaemonCommand sends a generic command to the user's daemon and waits for a response.
	SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error)

	// SendDaemonCommandToDaemon sends a generic command to a SPECIFIC daemon id,
	// bypassing default resolution. Use when the target daemon is already known
	// so a multi-command operation and any recorded owner id all agree on one
	// daemon (e.g. every worktree.create for a worktree, plus the daemon_id
	// persisted on the row).
	SendDaemonCommandToDaemon(ctx context.Context, userID string, daemonID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error)

	// ResolveDaemonID returns the daemon id that SendDaemonCommand would route
	// to for this user (connected/local preferred, then control plane, then DB
	// fallback). Returns an error when no daemon can be resolved. Callers use
	// this to record which daemon an operation ran against (e.g. a
	// project_daemons row) so resolution and routing stay consistent.
	ResolveDaemonID(ctx context.Context, userID string) (string, error)

	// EnqueueDaemonCommand persists a fire-and-forget command to the
	// DAEMON_PENDING_COMMANDS JetStream stream for each of the user's
	// daemons. The gateway drains the stream on the daemon's next connect
	// and replays the command. Use for writes that must eventually run on
	// the daemon's filesystem (e.g. ensure-dir) when the daemon may not be
	// online right now — synchronous responses are not available, so this
	// is unsuitable for reads.
	//
	// Returns the number of daemons the command was enqueued for. Zero is
	// not an error: it just means the user has no managed daemons yet
	// (caller should be tolerant — the directory will be created on the
	// next successful create flow).
	EnqueueDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) (int, error)

	// SendLoadProjectConfigs asks the daemon to load and send project configs.
	SendLoadProjectConfigs(ctx context.Context, userID string, projectPath string, requestID string) error

	// SendWatchProjectConfigs asks the daemon to start watching a project for config changes.
	SendWatchProjectConfigs(ctx context.Context, userID string, projectPath string, includeInitial bool) error

	// SendTerminalInput sends raw PTY input bytes to a daemon terminal session.
	SendTerminalInput(ctx context.Context, userID string, sessionID string, data []byte) error

	// SendTerminalResize sends a terminal resize request to a daemon terminal session.
	SendTerminalResize(ctx context.Context, userID string, sessionID string, cols, rows uint32) error

	// SubscribeTerminalOutput subscribes to terminal output for a session.
	// Returns a channel that receives output events, an unsubscribe function, and an error.
	SubscribeTerminalOutput(ctx context.Context, userID string, sessionID string) (<-chan *TerminalOutputEvent, func(), error)

	// SubscribeProcessOutput subscribes to a process's output stream.
	// If newOnly is true, only new output is delivered (existing buffered output is skipped).
	// Returns a channel that receives output events, an unsubscribe function, and an error.
	SubscribeProcessOutput(ctx context.Context, userID string, processID string, newOnly bool) (<-chan *ProcessOutputEvent, func(), error)

	// Close cleans up resources.
	Close() error
}

// DaemonConnectionListener is notified when daemon connections change.
// Used by NATSToolBridge to manage per-daemon NATS subscriptions and by
// daemonevents.Publisher to mirror lifecycle to the control plane.
type DaemonConnectionListener interface {
	OnDaemonConnected(userID, daemonID string)
	OnDaemonDisconnected(userID, daemonID string)
}

// DaemonConnectionManager is the subset of ToolsDaemonService needed by NATSToolBridge.
// This avoids importing the services package from toolexec.
//
// All methods that accept userID route to the user's default daemon (prefer
// local, then most recently connected). For explicit daemon targeting, use
// DaemonResolver to enumerate daemons first.
type DaemonConnectionManager interface {
	IsDaemonOnline(ctx context.Context, userID string) bool
	ListConnectedDaemons(userID string) []DaemonInfo
	SendToolRequest(ctx context.Context, userID string, request *ToolExecutionRequest) error
	SendToolRequestSync(ctx context.Context, userID string, request *ToolExecutionRequest) (*ToolExecutionResponse, error)
	SendToolExecutionCancel(ctx context.Context, userID, requestID, reason string) error
	SendKillProcess(userID, processID string) error
	SendDaemonCommand(ctx context.Context, userID string, req *reliantv1.DaemonCommandRequest) (*reliantv1.DaemonCommandResponse, error)
	SendLoadProjectConfigs(ctx context.Context, userID string, projectPath string, requestID string) error
	SendWatchProjectConfigs(ctx context.Context, userID string, projectPath string, includeInitial bool) error
	SendTerminalInput(userID string, sessionID string, data []byte) error
	SendTerminalResize(userID string, sessionID string, cols, rows uint32) error
	SubscribeTerminalOutput(userID string, sessionID string) (<-chan *TerminalOutputEvent, func(), error)
	SubscribeProcessOutput(userID string, processID string, newOnly bool) (<-chan *ProcessOutputEvent, func(), error)
}
