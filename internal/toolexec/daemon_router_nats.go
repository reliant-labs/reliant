// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// NATS subject patterns for tool execution and daemon routing.
const (
	toolRequestSubject = "tools.request" // tool request notifications
	toolCancelSubject  = "tools.cancel"  // tool cancellation notifications
	toolOnlineSubject  = "tools.online"  // request-reply for online check

	daemonKillSubject      = "daemon.process.kill" // request-reply for kill process
	daemonCommandSubject   = "daemon.command"      // request-reply for generic daemon commands
	toolRequestSyncSubject = "tools.request.sync"  // request-reply for synchronous tool execution

	configLoadSubject  = "daemon.config.load"  // fire-and-forget
	configWatchSubject = "daemon.config.watch" // fire-and-forget

	// Terminal streaming subjects
	terminalInputSubject  = "daemon.terminal.input"  // fire-and-forget: publish input bytes
	terminalResizeSubject = "daemon.terminal.resize" // fire-and-forget: publish resize
	terminalOutputSubject = "daemon.terminal.output" // subscribe for output events

	// Process output streaming subjects
	processOutputSubscribeSubject = "daemon.process.subscribe" // fire-and-forget: subscribe request
	processOutputSubject          = "daemon.process.output"    // subscribe for output chunks
)

// NATSDaemonRouter implements DaemonRouter using NATS pub/sub.
// Used by workers and api-server replicas in distributed mode to route
// daemon operations to the api-server that holds the daemon's gRPC connection.
type NATSDaemonRouter struct {
	nc *nats.Conn
}

// NewNATSDaemonRouter creates a new NATS-based daemon router.
func NewNATSDaemonRouter(nc *nats.Conn) *NATSDaemonRouter {
	return &NATSDaemonRouter{nc: nc}
}

func (r *NATSDaemonRouter) IsDaemonOnline(userID string) (bool, error) {
	msg, err := r.nc.Request(toolOnlineSubject+"."+userID, nil, 2*time.Second)
	if err != nil {
		// No subscribers means no api-server holds this daemon's connection.
		// Timeout means the request went out but nobody answered (daemon genuinely offline).
		// Both are definitive "offline" — not infrastructure failures.
		if err == nats.ErrNoResponders || err == nats.ErrTimeout {
			return false, nil
		}
		// Other errors (connection closed, etc.) are infrastructure failures.
		return false, fmt.Errorf("NATS IsDaemonOnline request failed: %w", err)
	}
	return string(msg.Data) == "true", nil
}

func (r *NATSDaemonRouter) SendToolRequest(ctx context.Context, userID string, request *ToolExecutionRequest) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	return r.nc.Publish(toolRequestSubject+"."+userID, payload)
}

func (r *NATSDaemonRouter) SendToolExecutionCancel(userID, requestID, reason string) error {
	payload, err := json.Marshal(map[string]string{
		"request_id": requestID,
		"reason":     reason,
	})
	if err != nil {
		return err
	}
	return r.nc.Publish(toolCancelSubject+"."+userID, payload)
}

func (r *NATSDaemonRouter) SendKillProcess(ctx context.Context, userID, processID string) error {
	payload, err := json.Marshal(map[string]string{
		"process_id": processID,
	})
	if err != nil {
		return err
	}

	msg, err := r.nc.Request(daemonKillSubject+"."+userID, payload, 10*time.Second)
	if err != nil {
		return fmt.Errorf("kill process via NATS failed: %w", err)
	}

	// Check response for error.
	var resp struct {
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(msg.Data, &resp); err == nil && resp.Error != "" {
		return fmt.Errorf("remote kill failed: %s", resp.Error)
	}
	return nil
}

func (r *NATSDaemonRouter) SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	req := struct {
		RequestID   string          `json:"request_id"`
		CommandType string          `json:"command_type"`
		Payload     json.RawMessage `json:"payload"`
		TimeoutMs   int32           `json:"timeout_ms"`
	}{
		RequestID:   fmt.Sprintf("%d", time.Now().UnixNano()),
		CommandType: commandType,
		Payload:     json.RawMessage(payload),
		TimeoutMs:   timeoutMs,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal daemon command: %w", err)
	}

	timeout := 30 * time.Second
	if timeoutMs > 0 {
		timeout = time.Duration(timeoutMs) * time.Millisecond
	}

	// Respect the caller's context deadline if it's sooner than the explicit timeout.
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	// Run NATS request in a goroutine so we can also select on ctx.Done().
	type natsResult struct {
		msg *nats.Msg
		err error
	}
	resultCh := make(chan natsResult, 1)
	go func() {
		msg, err := r.nc.Request(daemonCommandSubject+"."+userID, data, timeout)
		resultCh <- natsResult{msg, err}
	}()

	var msg *nats.Msg
	select {
	case res := <-resultCh:
		if res.err != nil {
			return nil, fmt.Errorf("daemon command via NATS failed: %w", res.err)
		}
		msg = res.msg
	case <-ctx.Done():
		return nil, fmt.Errorf("daemon command via NATS failed: %w", ctx.Err())
	}

	var resp struct {
		Success      bool   `json:"success"`
		Payload      []byte `json:"payload"`
		ErrorMessage string `json:"error_message,omitempty"`
	}
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal daemon command response: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("daemon command %q failed: %s", commandType, resp.ErrorMessage)
	}
	return resp.Payload, nil
}

func (r *NATSDaemonRouter) SendToolRequestSync(ctx context.Context, userID string, request *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal tool request: %w", err)
	}

	timeout := 10 * time.Minute
	if request.TimeoutMs > 0 {
		timeout = time.Duration(request.TimeoutMs)*time.Millisecond + 30*time.Second // buffer for daemon overhead
	}

	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	type natsResult struct {
		msg *nats.Msg
		err error
	}
	resultCh := make(chan natsResult, 1)
	go func() {
		msg, err := r.nc.Request(toolRequestSyncSubject+"."+userID, payload, timeout)
		resultCh <- natsResult{msg, err}
	}()

	var msg *nats.Msg
	select {
	case res := <-resultCh:
		if res.err != nil {
			return nil, fmt.Errorf("tool request via NATS failed: %w", res.err)
		}
		msg = res.msg
	case <-ctx.Done():
		return nil, fmt.Errorf("tool request via NATS failed: %w", ctx.Err())
	}

	var resp ToolExecutionResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal tool response: %w", err)
	}
	return &resp, nil
}

func (r *NATSDaemonRouter) SendLoadProjectConfigs(userID string, projectPath string, requestID string) error {
	payload, err := json.Marshal(map[string]string{
		"project_path": projectPath,
		"request_id":   requestID,
	})
	if err != nil {
		return err
	}
	return r.nc.Publish(configLoadSubject+"."+userID, payload)
}

func (r *NATSDaemonRouter) SendWatchProjectConfigs(userID string, projectPath string, includeInitial bool) error {
	payload, err := json.Marshal(map[string]interface{}{
		"project_path":    projectPath,
		"include_initial": includeInitial,
	})
	if err != nil {
		return err
	}
	return r.nc.Publish(configWatchSubject+"."+userID, payload)
}

func (r *NATSDaemonRouter) SendTerminalInput(ctx context.Context, userID string, sessionID string, data []byte) error {
	payload, err := json.Marshal(struct {
		SessionID string `json:"session_id"`
		Data      []byte `json:"data"`
	}{SessionID: sessionID, Data: data})
	if err != nil {
		return err
	}
	return r.nc.Publish(terminalInputSubject+"."+userID+"."+sessionID, payload)
}

func (r *NATSDaemonRouter) SendTerminalResize(ctx context.Context, userID string, sessionID string, cols, rows uint32) error {
	payload, err := json.Marshal(struct {
		SessionID string `json:"session_id"`
		Cols      uint32 `json:"cols"`
		Rows      uint32 `json:"rows"`
	}{SessionID: sessionID, Cols: cols, Rows: rows})
	if err != nil {
		return err
	}
	return r.nc.Publish(terminalResizeSubject+"."+userID+"."+sessionID, payload)
}

func (r *NATSDaemonRouter) SubscribeTerminalOutput(ctx context.Context, userID string, sessionID string) (<-chan *TerminalOutputEvent, func(), error) {
	ch := make(chan *TerminalOutputEvent, 64)
	subject := terminalOutputSubject + "." + userID + "." + sessionID

	sub, err := r.nc.Subscribe(subject, func(msg *nats.Msg) {
		var evt TerminalOutputEvent
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			return
		}
		select {
		case ch <- &evt:
		default:
		}
	})
	if err != nil {
		return nil, nil, fmt.Errorf("subscribe to terminal output via NATS: %w", err)
	}

	unsub := func() {
		_ = sub.Unsubscribe()
	}
	return ch, unsub, nil
}

func (r *NATSDaemonRouter) SubscribeProcessOutput(ctx context.Context, userID string, processID string, newOnly bool) (<-chan *ProcessOutputEvent, func(), error) {
	// Publish subscribe request so the bridge/gateway knows to start forwarding.
	reqPayload, err := json.Marshal(struct {
		ProcessID string `json:"process_id"`
		NewOnly   bool   `json:"new_only"`
	}{ProcessID: processID, NewOnly: newOnly})
	if err != nil {
		return nil, nil, err
	}
	if err := r.nc.Publish(processOutputSubscribeSubject+"."+userID, reqPayload); err != nil {
		return nil, nil, fmt.Errorf("publish process output subscribe request via NATS: %w", err)
	}

	ch := make(chan *ProcessOutputEvent, 128)
	subject := processOutputSubject + "." + userID + "." + processID

	sub, err := r.nc.Subscribe(subject, func(msg *nats.Msg) {
		var evt ProcessOutputEvent
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			return
		}
		select {
		case ch <- &evt:
		default:
		}
	})
	if err != nil {
		return nil, nil, fmt.Errorf("subscribe to process output via NATS: %w", err)
	}

	unsub := func() {
		_ = sub.Unsubscribe()
	}
	return ch, unsub, nil
}

func (r *NATSDaemonRouter) Close() error {
	return nil
}

// Compile-time interface check.
var _ DaemonRouter = (*NATSDaemonRouter)(nil)
