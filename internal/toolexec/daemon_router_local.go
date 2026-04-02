// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
)

// LazyDaemonStartFunc is called when a daemon command targets a user with no
// connected daemon. It should start the in-process daemon for that user.
// Returns (started, error). The caller will wait briefly for the connection
// to be established after a successful start.
type LazyDaemonStartFunc func(userID string) (bool, error)

// LocalDaemonRouter routes daemon operations directly to the in-process
// ToolsDaemonService. Used in monolith/daemon mode where a single process
// holds both the API server and the daemon gRPC streams.
type LocalDaemonRouter struct {
	mgr         DaemonConnectionManager
	lazyStarter LazyDaemonStartFunc
}

// NewLocalDaemonRouter creates a router that delegates to the given manager.
func NewLocalDaemonRouter(mgr DaemonConnectionManager) *LocalDaemonRouter {
	return &LocalDaemonRouter{mgr: mgr}
}

// SetLazyStarter registers a function that will be called to start the
// in-process daemon when a command is sent but no daemon is connected.
// This enables lazy daemon startup for first-time users who haven't logged in
// at server boot time.
func (r *LocalDaemonRouter) SetLazyStarter(fn LazyDaemonStartFunc) {
	r.lazyStarter = fn
}

// ensureDaemon triggers lazy daemon startup for the given userID if no daemon
// is connected and a lazy starter is registered. It waits briefly for the
// daemon to connect after triggering startup.
func (r *LocalDaemonRouter) ensureDaemon(ctx context.Context, userID string) {
	if r.mgr.IsDaemonOnline(ctx, userID) {
		return
	}
	if r.lazyStarter == nil {
		return
	}

	started, err := r.lazyStarter(userID)
	if err != nil {
		logging.Error("Lazy daemon startup failed", "error", err, "userID", userID)
		return
	}
	if !started {
		return // already running or no-op
	}

	// Wait briefly for the daemon to register its connection.
	// The daemon runtime connects via bidi stream which takes a moment.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r.mgr.IsDaemonOnline(ctx, userID) {
			logging.Info("In-process daemon connected after lazy start", "userID", userID)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	logging.Warn("In-process daemon did not connect within timeout after lazy start", "userID", userID)
}

func (r *LocalDaemonRouter) IsDaemonOnline(ctx context.Context, userID string) (bool, error) {
	r.ensureDaemon(ctx, userID)
	return r.mgr.IsDaemonOnline(ctx, userID), nil
}

func (r *LocalDaemonRouter) SendToolRequest(ctx context.Context, userID string, request *ToolExecutionRequest) error {
	r.ensureDaemon(ctx, userID)
	return r.mgr.SendToolRequest(ctx, userID, request)
}

func (r *LocalDaemonRouter) SendToolExecutionCancel(ctx context.Context, userID, requestID, reason string) error {
	return r.mgr.SendToolExecutionCancel(ctx, userID, requestID, reason)
}

func (r *LocalDaemonRouter) SendKillProcess(ctx context.Context, userID, processID string) error {
	r.ensureDaemon(ctx, userID)
	return r.mgr.SendKillProcess(userID, processID)
}

func (r *LocalDaemonRouter) SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	r.ensureDaemon(ctx, userID)

	req := &reliantv1.DaemonCommandRequest{
		RequestId:   uuid.New().String(),
		CommandType: commandType,
		Payload:     payload,
		TimeoutMs:   timeoutMs,
	}
	resp, err := r.mgr.SendDaemonCommand(ctx, userID, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("daemon command %q failed: %s", commandType, resp.ErrorMessage)
	}
	return resp.Payload, nil
}

func (r *LocalDaemonRouter) SendToolRequestSync(ctx context.Context, userID string, request *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	r.ensureDaemon(ctx, userID)
	return r.mgr.SendToolRequestSync(ctx, userID, request)
}

func (r *LocalDaemonRouter) SendLoadProjectConfigs(ctx context.Context, userID string, projectPath string, requestID string) error {
	r.ensureDaemon(ctx, userID)
	return r.mgr.SendLoadProjectConfigs(ctx, userID, projectPath, requestID)
}

func (r *LocalDaemonRouter) SendWatchProjectConfigs(ctx context.Context, userID string, projectPath string, includeInitial bool) error {
	r.ensureDaemon(ctx, userID)
	return r.mgr.SendWatchProjectConfigs(ctx, userID, projectPath, includeInitial)
}

func (r *LocalDaemonRouter) SendTerminalInput(ctx context.Context, userID string, sessionID string, data []byte) error {
	r.ensureDaemon(ctx, userID)
	return r.mgr.SendTerminalInput(userID, sessionID, data)
}

func (r *LocalDaemonRouter) SendTerminalResize(ctx context.Context, userID string, sessionID string, cols, rows uint32) error {
	r.ensureDaemon(ctx, userID)
	return r.mgr.SendTerminalResize(userID, sessionID, cols, rows)
}

func (r *LocalDaemonRouter) SubscribeTerminalOutput(ctx context.Context, userID string, sessionID string) (<-chan *TerminalOutputEvent, func(), error) {
	r.ensureDaemon(ctx, userID)
	return r.mgr.SubscribeTerminalOutput(userID, sessionID)
}

func (r *LocalDaemonRouter) SubscribeProcessOutput(ctx context.Context, userID string, processID string, newOnly bool) (<-chan *ProcessOutputEvent, func(), error) {
	r.ensureDaemon(ctx, userID)
	return r.mgr.SubscribeProcessOutput(userID, processID, newOnly)
}

func (r *LocalDaemonRouter) Close() error {
	return nil
}

// Compile-time interface check.
var _ DaemonRouter = (*LocalDaemonRouter)(nil)
