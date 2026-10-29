package tools_daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonactivity"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// toolVerdictGrace is how long past a tool request's own timeout the gateway
// keeps waiting for the daemon's answer.
//
// The daemon enforces that timeout itself and answers with the verdict — a
// shell command it killed reports exit 124, timed out — after up to
// ExecGraceDelay (3s) for the process group to exit and ExecWaitDelay (10s)
// for its pipes to drain. A gateway that gave up at exactly the timeout won
// that race every time and reported a transport failure instead: every one of
// the 65 "refusing to re-execute shell command" retries in prod over 14 days
// (2026-10-29) began as a 5m run step that ran, hit its timeout, and had its
// verdict dropped here. It stays under the requester's own +30s
// (toolRequestRoundTrip), so the requester hears this answer, not its own
// timeout.
const toolVerdictGrace = 20 * time.Second

// defaultDaemonForUser returns the "best" connected daemon for a user.
// It prefers local daemons over cloud daemons, then most recently connected.
// Returns nil if no daemon is connected.
func (s *Service) defaultDaemonForUser(userID string) *daemonConnection {
	// Must be called with s.mu held (at least RLock).
	daemonIDs := s.userDaemons[userID]
	if len(daemonIDs) == 0 {
		return nil
	}
	// Single daemon: fast path.
	if len(daemonIDs) == 1 {
		return s.connections[daemonIDs[0]]
	}
	// Prefer local daemons, then most recently connected.
	var best *daemonConnection
	for _, dID := range daemonIDs {
		c := s.connections[dID]
		if c == nil {
			continue
		}
		if best == nil {
			best = c
			continue
		}
		// Prefer local over cloud. Empty daemonType is treated as "local" for backward compat.
		cIsLocal := c.daemonType == "local" || c.daemonType == ""
		bestIsLocal := best.daemonType == "local" || best.daemonType == ""
		if cIsLocal && !bestIsLocal {
			best = c
			continue
		}
		// Same type: prefer most recently connected.
		if cIsLocal == bestIsLocal && c.connectedAt.After(best.connectedAt) {
			best = c
		}
	}
	return best
}

// sendToDaemon enqueues a message on ONE named daemon's connection. It never
// falls back to another of the user's daemons: the subject that carried the
// request named this daemon, and a different machine must not act on it.
func (s *Service) sendToDaemon(userID, daemonID string, msg *reliantv1.ServerMessage) error {
	if msg == nil {
		return nil
	}
	conn, err := s.connForUser(userID, daemonID)
	if err != nil {
		return err
	}
	return s.sendToConn(conn, msg)
}

// sendToConn enqueues a message on a specific connection's send channel. Used
// when the caller already holds the exact connection (e.g. reconcile) rather
// than resolving the user's default daemon.
func (s *Service) sendToConn(conn *daemonConnection, msg *reliantv1.ServerMessage) error {
	if msg == nil {
		return nil
	}
	if conn == nil {
		return fmt.Errorf("nil daemon connection")
	}

	select {
	case conn.sendCh <- msg:
		return nil
	case <-conn.done:
		return fmt.Errorf("daemon connection closed for user %s", conn.userID)
	default:
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" daemon send buffer full, message dropped", "userID", conn.userID)
		return fmt.Errorf("daemon send buffer full, message dropped for user %s", conn.userID)
	}
}

// startSender runs conn's sender as one of the stream's users, so the
// stream's owner joins it before letting the stream go (releaseStream).
// Called from the owner's goroutine, before the owner can release.
func (s *Service) startSender(conn *daemonConnection) {
	conn.streamUsers.Add(1)
	go func() {
		defer conn.streamUsers.Done()
		s.runSender(conn)
	}()
}

// runSender handles sending messages to the daemon
func (s *Service) runSender(conn *daemonConnection) {
	for {
		select {
		case <-conn.done:
			return
		case msg := <-conn.sendCh:
			if err := conn.stream.Send(msg); err != nil {
				logging.Error(LOG_PREFIX_TOOLS_DAEMON+" Failed to send message",
					"error", err,
					"userID", conn.userID)
				conn.closeDone()
				return
			}
			s.mu.Lock()
			conn.lastActivity = time.Now().UTC()
			s.mu.Unlock()
			// The one place a cloud machine's idle clock is restarted: work a
			// person or an agent asked this daemon to do, judged by
			// daemonactivity's policy, at the moment it is handed over.
			if reason, ok := daemonactivity.ReasonForServerMessage(msg); ok && s.deps.StatePublisher != nil {
				s.deps.StatePublisher.Activity(conn.daemonID, conn.userID, conn.daemonType, reason)
			}
		}
	}
}

// reassertInFlightActivity keeps the machine awake while an agent's tool call
// is still running on it. The call's activity is published once, when it is
// sent; a test suite or build that outlasts the idle timeout would otherwise
// be suspended mid-run. Called on every gateway heartbeat, so the re-assertion
// costs no timer of its own and stops the moment the call returns.
//
// The daemon's own count matters as much as this stream's waiters: a call
// that outlived a drained replica's stream is still running here, and this
// replica never sent it.
func (s *Service) reassertInFlightActivity(conn *daemonConnection) {
	conn.pendingToolRequestsMu.Lock()
	inFlight := len(conn.pendingToolRequests)
	conn.pendingToolRequestsMu.Unlock()
	if (inFlight > 0 || conn.daemonToolCallsInFlight.Load() > 0) && s.deps.StatePublisher != nil {
		s.deps.StatePublisher.Activity(conn.daemonID, conn.userID, conn.daemonType, daemonactivity.ReasonToolCallInFlight)
	}
}

// runHeartbeat sends periodic heartbeats to keep the connection alive
func (s *Service) runHeartbeat(conn *daemonConnection) {
	ticker := time.NewTicker(daemonHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-conn.done:
			return
		case <-ticker.C:
			select {
			case conn.sendCh <- newServerHeartbeat(conn, time.Now()):
			default:
				// Buffer full, skip heartbeat
			}
			s.reassertInFlightActivity(conn)
		}
	}
}

// newServerHeartbeat builds the gateway's keepalive for one connection. It
// carries the newest daemon heartbeat the receive loop has read, which is how
// the daemon tells a gateway that is reading its stream from one that has
// stopped (see ServerHeartbeat.acked_daemon_heartbeat).
func newServerHeartbeat(conn *daemonConnection, now time.Time) *reliantv1.ServerMessage {
	return &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_Heartbeat{
			Heartbeat: &reliantv1.ServerHeartbeat{
				Timestamp:            now.Unix(),
				AckedDaemonHeartbeat: conn.ackedDaemonHeartbeat.Load(),
			},
		},
	}
}

// SendToolRequest pushes a tool request to the daemon
// Context is accepted for interface compliance but not used - daemon connections have their own lifecycle
func (s *Service) SendToolRequest(ctx context.Context, userID, daemonID string, request *toolexec.ToolExecutionRequest) error {
	conn, err := s.connForUser(userID, daemonID)
	if err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Cannot send request - named daemon not connected",
			"userID", userID,
			"daemonID", daemonID,
			"requestID", request.RequestID)
		return fmt.Errorf("daemon %s offline for user %s, request %s could not be delivered: %w", daemonID, userID, request.RequestID, err)
	}

	// Convert context map to JSON
	contextJSON := ""
	if request.Context != nil {
		if data, err := json.Marshal(request.Context); err == nil {
			contextJSON = string(data)
		}
	}

	// Create proto message
	toolReq := &reliantv1.ToolRequest{
		RequestId:      request.RequestID,
		ToolName:       request.ToolName,
		ToolInput:      request.ToolInput,
		ToolCallId:     request.ToolCallID,
		ContentBlockId: request.ContentBlockID,
		ContextJson:    contextJSON,
		TimeoutMs:      int32(request.TimeoutMs),
		TraceContext:   traceContextOf(ctx),
	}

	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_ToolRequest{
			ToolRequest: toolReq,
		},
	}

	select {
	case conn.sendCh <- msg:
		return nil
	case <-conn.done:
		return fmt.Errorf("daemon connection closed for user %s while sending tool request %s", userID, request.RequestID)
	default:
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Send buffer full",
			"userID", userID,
			"requestID", request.RequestID)
		return fmt.Errorf("daemon send buffer full for user %s, request %s dropped", userID, request.RequestID)
	}
}

// SendKillProcess sends a kill request to the daemon for a specific background process.
func (s *Service) SendKillProcess(userID, daemonID, processID string) error {
	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_KillProcess{
			KillProcess: &reliantv1.DaemonKillProcessRequest{
				ProcessId: processID,
			},
		},
	}
	return s.sendToDaemon(userID, daemonID, msg)
}

// SendDaemonCommand sends a generic command to the user's default daemon and
// waits for a correlated response. For commands that must target a specific
// connection (e.g. reconcile asking THIS daemon what's on its disk) use
// sendCommandToConn directly.
func (s *Service) SendDaemonCommand(ctx context.Context, userID string, req *reliantv1.DaemonCommandRequest) (*reliantv1.DaemonCommandResponse, error) {
	s.mu.RLock()
	conn := s.defaultDaemonForUser(userID)
	s.mu.RUnlock()
	if conn == nil {
		// The user's machine is not connected: theirs to fix, not a fault.
		return nil, svcerr.WithClass(fmt.Errorf("no daemon connected for user %s", userID), svcerr.ClassUser)
	}
	return s.sendCommandToConn(ctx, conn, req)
}

// SendDaemonCommandToDaemon sends a generic command to ONE named daemon and
// waits for its response. It never falls back to another of the user's
// daemons: a command addressed to a daemon (everything drained from that
// daemon's own pending queue) must run there or not at all. With a local
// daemon and a cloud daemon both connected, the default pick is the LOCAL one,
// so a git.clone queued for the cloud machine used to land on the user's
// laptop instead.
//
// The daemon must also belong to userID, so a caller cannot reach another
// user's machine by naming its id.
func (s *Service) SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID string, req *reliantv1.DaemonCommandRequest) (*reliantv1.DaemonCommandResponse, error) {
	s.mu.RLock()
	conn := s.connections[daemonID]
	s.mu.RUnlock()
	if conn == nil || conn.userID != userID {
		return nil, fmt.Errorf("daemon %s is not connected for user %s", daemonID, userID)
	}
	return s.sendCommandToConn(ctx, conn, req)
}

// sendCommandToConn sends a generic command to a specific daemon connection and
// waits for the correlated DaemonCommandResponse. It mirrors SendDaemonCommand's
// correlation logic but targets the passed conn directly rather than resolving
// the user's default daemon — required when a user has multiple daemons and the
// caller needs to ask one particular connection about its own disk.
func (s *Service) sendCommandToConn(ctx context.Context, conn *daemonConnection, req *reliantv1.DaemonCommandRequest) (*reliantv1.DaemonCommandResponse, error) {
	if conn == nil {
		return nil, fmt.Errorf("nil daemon connection")
	}
	// A connection assembled without its correlation map cannot carry a
	// command: registering the waiter below would panic on a nil map write
	// and take the whole server down. Refusing is correct and survivable —
	// only a connection built outside newDaemonConnection can be in this
	// state, and every caller here already tolerates a send failure.
	if conn.pendingCommands == nil {
		return nil, fmt.Errorf("daemon connection for user %s cannot carry commands", conn.userID)
	}

	// Register a pending response channel keyed by request id on this conn.
	respCh := make(chan *reliantv1.DaemonCommandResponse, 1)
	conn.pendingCommandsMu.Lock()
	conn.pendingCommands[req.RequestId] = respCh
	conn.pendingCommandsMu.Unlock()

	// Ensure cleanup on all exit paths.
	defer func() {
		conn.pendingCommandsMu.Lock()
		delete(conn.pendingCommands, req.RequestId)
		conn.pendingCommandsMu.Unlock()
	}()

	// Send the command to this specific connection.
	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_DaemonCommand{
			DaemonCommand: req,
		},
	}
	if err := s.sendToConn(conn, msg); err != nil {
		return nil, fmt.Errorf("send daemon command: %w", err)
	}

	// Wait for the correlated response.
	timeout := 30 * time.Second
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}

	select {
	case resp := <-respCh:
		return resp, nil
	case <-time.After(timeout):
		s.cancelDaemonCommand(conn.userID, conn.daemonID, req.RequestId, "daemon command timed out")
		return nil, fmt.Errorf("daemon command %q timed out after %s", req.CommandType, timeout)
	case <-conn.done:
		return nil, fmt.Errorf("%w while waiting for command %q response; the command may still complete after reconnect", toolexec.ErrDaemonConnectionInterrupted, req.CommandType)
	case <-ctx.Done():
		s.cancelDaemonCommand(conn.userID, conn.daemonID, req.RequestId, "daemon command caller cancelled")
		return nil, ctx.Err()
	}
}

// SendToolRequestSync sends a tool execution request to the user's default
// daemon and waits for the correlated response. Resolution happens exactly
// once, here; everything downstream operates on that one connection.
func (s *Service) SendToolRequestSync(ctx context.Context, userID, daemonID string, request *toolexec.ToolExecutionRequest) (*toolexec.ToolExecutionResponse, error) {
	conn, err := s.connForUser(userID, daemonID)
	if err != nil {
		return nil, err
	}
	return s.sendToolRequestToConn(ctx, conn, request)
}

// sendToolRequestToConn sends a tool request to ONE specific connection and
// waits for that connection to answer. The correlation map, the send, and the
// liveness the wait is bound to must all name the same connection — mixing
// them is how an answer gets delivered to a connection nobody is listening on.
// Mirrors sendCommandToConn.
func (s *Service) sendToolRequestToConn(ctx context.Context, conn *daemonConnection, request *toolexec.ToolExecutionRequest) (*toolexec.ToolExecutionResponse, error) {
	if conn == nil {
		return nil, fmt.Errorf("nil daemon connection")
	}

	// Register a pending response channel.
	respCh := make(chan *toolexec.ToolExecutionResponse, 1)
	conn.pendingToolRequestsMu.Lock()
	conn.pendingToolRequests[request.RequestID] = respCh
	conn.pendingToolRequestsMu.Unlock()

	// Ensure cleanup on all exit paths.
	defer func() {
		conn.pendingToolRequestsMu.Lock()
		delete(conn.pendingToolRequests, request.RequestID)
		conn.pendingToolRequestsMu.Unlock()
	}()

	// Convert context map to JSON
	contextJSON := ""
	if request.Context != nil {
		if data, err := json.Marshal(request.Context); err == nil {
			contextJSON = string(data)
		}
	}

	// Create and send the tool request message.
	toolReq := &reliantv1.ToolRequest{
		RequestId:      request.RequestID,
		ToolName:       request.ToolName,
		ToolInput:      request.ToolInput,
		ToolCallId:     request.ToolCallID,
		ContentBlockId: request.ContentBlockID,
		ContextJson:    contextJSON,
		TimeoutMs:      int32(request.TimeoutMs),
		TraceContext:   traceContextOf(ctx),
	}

	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_ToolRequest{
			ToolRequest: toolReq,
		},
	}
	// Send on the SAME connection the pending channel was registered on.
	// Re-resolving the user's default daemon here reintroduced a race that
	// silently loses the answer: if the slot was replaced between the resolve
	// above and this send, the request goes to connection B while the waiter
	// listens on connection A. B's response finds no pending entry and is
	// dropped, and A — still open — never fires done, so the caller waits out
	// the FULL tool timeout (330s at the NATS hop) for a reply that can never
	// come. Pinning the connection makes the wait honest: either the answer
	// arrives, or A closes and we fail immediately.
	if err := s.sendToConn(conn, msg); err != nil {
		return nil, fmt.Errorf("send tool request: %w", err)
	}

	// Wait for the correlated response.
	// Default to 10 minutes for bash commands; use request timeout if set,
	// plus toolVerdictGrace for the daemon's own answer at that timeout.
	timeout := 600 * time.Second
	if request.TimeoutMs > 0 {
		timeout = time.Duration(request.TimeoutMs)*time.Millisecond + toolVerdictGrace
	}

	select {
	case resp := <-respCh:
		return resp, nil
	case <-time.After(timeout):
		// The daemon has the request: it may have run it, and may still be.
		return nil, fmt.Errorf("tool request %q got no answer from the machine in %s; it may have run", request.RequestID, timeout)
	case <-conn.done:
		return nil, fmt.Errorf("%w while waiting for tool request %q response; the request outcome is unknown and the daemon may still complete it after reconnect", toolexec.ErrDaemonConnectionInterrupted, request.RequestID)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Service) cancelDaemonCommand(userID, daemonID, requestID, reason string) {
	if err := s.SendToolExecutionCancel(context.Background(), userID, daemonID, requestID, reason); err != nil {
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Failed to send daemon command cancel", "userID", userID, "requestID", requestID, "error", err)
	}
}

// SendToolExecutionBackground asks the connected daemon to detach an in-flight
// execution into a background process.
//
// Same delivery contract as SendToolExecutionCancel below: the request is a
// best-effort nudge to a RUNNING execution. If the daemon is offline, the
// connection closed, or its buffer is full, the command simply keeps running in
// the foreground — the caller surfaces that rather than claiming success.
func (s *Service) SendToolExecutionBackground(_ context.Context, userID, daemonID, requestID, toolCallID string) error {
	if strings.TrimSpace(requestID) == "" {
		return fmt.Errorf("cannot background without a request id for tool call %s", toolCallID)
	}

	conn, err := s.connForUser(userID, daemonID)
	if err != nil {
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Cannot background - named daemon not connected", "userID", userID, "daemonID", daemonID, "requestID", requestID)
		return fmt.Errorf("daemon %s offline for user %s, cannot deliver background for %s: %w", daemonID, userID, requestID, err)
	}

	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_ToolBackground{
			ToolBackground: &reliantv1.ToolExecutionBackground{
				RequestId:  requestID,
				ToolCallId: toolCallID,
			},
		},
	}

	select {
	case conn.sendCh <- msg:
		return nil
	case <-conn.done:
		return fmt.Errorf("daemon connection closed for user %s, background for %s dropped", userID, requestID)
	default:
		return fmt.Errorf("daemon buffer full for user %s, background for %s dropped", userID, requestID)
	}
}

// SendToolExecutionCancel sends a cancellation request to connected daemon.
// This currently reuses the tool_cancel transport and request_id correlation for
// both tool executions and generic daemon commands.
func (s *Service) SendToolExecutionCancel(_ context.Context, userID, daemonID, requestID, reason string) error {
	if strings.TrimSpace(requestID) == "" {
		return nil
	}

	conn, err := s.connForUser(userID, daemonID)
	if err != nil {
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Cannot cancel - named daemon not connected", "userID", userID, "daemonID", daemonID, "requestID", requestID)
		return fmt.Errorf("daemon %s offline for user %s, cannot deliver cancel for %s: %w", daemonID, userID, requestID, err)
	}

	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_ToolCancel{
			ToolCancel: &reliantv1.ToolExecutionCancel{RequestId: requestID, Reason: reason},
		},
	}

	select {
	case conn.sendCh <- msg:
		return nil
	case <-conn.done:
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Cancel dropped - connection closed", "userID", userID, "requestID", requestID)
		return fmt.Errorf("daemon connection closed for user %s, cancel for %s dropped", userID, requestID)
	default:
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Cancel dropped - buffer full", "userID", userID, "requestID", requestID)
		return fmt.Errorf("daemon buffer full for user %s, cancel for %s dropped", userID, requestID)
	}
}

// traceContextOf is ctx's W3C trace context as a ToolRequest carries it, so
// the daemon executing the call continues the trace that dispatched it. Nil
// when ctx carries none.
func traceContextOf(ctx context.Context) map[string]string {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	if len(carrier) == 0 {
		return nil
	}
	return carrier
}
