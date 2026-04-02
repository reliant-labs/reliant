// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
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
// Implementations:
//   - LocalDaemonRouter: routes directly to in-process ToolsDaemonService (monolith/daemon mode)
//   - NATSDaemonRouter: routes via NATS request-reply to the api-server holding the connection (distributed mode)
type DaemonRouter interface {
	// IsDaemonOnline checks if a daemon is connected for the given user.
	// Returns (online, nil) for definitive results, or (false, err) for infrastructure failures.
	IsDaemonOnline(ctx context.Context, userID string) (bool, error)

	// SendToolRequest routes a tool execution request to the user's daemon (fire-and-forget).
	SendToolRequest(ctx context.Context, userID string, request *ToolExecutionRequest) error

	// SendToolRequestSync routes a tool execution request to the user's daemon and waits
	// for the result. Used for tools with RunsOn == ToolRunsOnDaemon.
	SendToolRequestSync(ctx context.Context, userID string, request *ToolExecutionRequest) (*ToolExecutionResponse, error)

	// SendToolExecutionCancel cancels a running tool execution.
	SendToolExecutionCancel(ctx context.Context, userID, requestID, reason string) error

	// SendKillProcess sends a kill signal to a background process on the user's daemon.
	SendKillProcess(ctx context.Context, userID, processID string) error

	// SendDaemonCommand sends a generic command to the user's daemon and waits for a response.
	SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error)

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

// DaemonConnectionManager is the subset of ToolsDaemonService needed by LocalDaemonRouter
// and NATSToolBridge. This avoids importing the services package from toolexec.
type DaemonConnectionManager interface {
	IsDaemonOnline(ctx context.Context, userID string) bool
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
