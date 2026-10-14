// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Gateway rollouts, from the gateway's side.
//
// A replica that is shutting down used to end every daemon stream it held at
// once, and every tool call in flight on them with it: the waiter for each
// call lived on that stream, in this process, and the stream's end was its
// answer — "connection interrupted, outcome unknown". The daemon reconnected
// within seconds and often finished the call; its result reached a replica
// with no waiter for it and was dropped.
//
// Now the drain is make-before-break, and results are not tied to a stream:
//
//  1. BeginDrain refuses new streams, stops routing each held daemon's new
//     requests here (DaemonDrainListener: the NATS bridge unsubscribes), and
//     sends each daemon GatewayDrain. The daemon opens a stream to another
//     replica and moves its work there, keeping this one open for replies to
//     what is in flight on it.
//  2. AwaitDrained waits, up to the drain deadline, for the daemons to close
//     their old streams, which each does once it is registered elsewhere and
//     has nothing in flight here.
//  3. Close ends what is left. A waiter cut off there answers "interrupted",
//     which no longer fails the call: the worker waits for the daemon to
//     deliver the result through another replica (routeToolResponse, and
//     internal/toolexec/tool_result_redelivery.go).

// drainPollInterval is how often AwaitDrained looks for streams still open.
const drainPollInterval = 100 * time.Millisecond

// resultPublishTimeout bounds publishing one result that had no waiter here.
const resultPublishTimeout = 10 * time.Second

// BeginDrain starts this replica's drain. From now on it refuses new daemon
// streams, and every daemon it holds is told to move: drain listeners first
// stop routing the daemon's new requests here, then the daemon is sent
// GatewayDrain carrying deadline — when Close will end its stream. The streams
// stay open; replies to requests in flight on them are still delivered.
//
// A daemon that does not act on the notice (an older build, or one the
// gateway dialed, which has no way to move) is ended at once instead, as every
// daemon was before: left open it would only be unreachable until the
// deadline, since this replica no longer routes its requests.
//
// Returns how many streams were told to move.
func (s *Service) BeginDrain(deadline time.Time) int {
	s.draining.Store(true)

	s.mu.RLock()
	conns := make([]*daemonConnection, 0, len(s.connections))
	var unmovable []*daemonConnection
	for _, conn := range s.connections {
		if conn.movesOnDrain {
			conns = append(conns, conn)
		} else {
			unmovable = append(unmovable, conn)
		}
	}
	s.mu.RUnlock()
	for _, conn := range unmovable {
		conn.closeWith(errGatewayDraining)
	}
	if len(conns) == 0 {
		return 0
	}

	logging.Info(LOG_PREFIX_TOOLS_DAEMON+" Draining: moving every daemon stream to another replica",
		"connections", len(conns), "ended_at_once", len(unmovable), "deadline", deadline)
	notice := &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_Drain{
		Drain: &reliantv1.GatewayDrain{DeadlineUnixMs: deadline.UnixMilli()},
	}}
	for _, conn := range conns {
		// Listeners first: by the time the daemon can register anywhere
		// else, nothing here may still take its requests, or one request
		// would reach it twice.
		s.notifyDraining(conn.userID, conn.daemonID)
		conn.drainNoticeSent.Store(true)
		if err := s.sendToConn(conn, notice); err != nil {
			logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Could not tell a daemon this replica is draining; its stream ends at the deadline",
				"daemonID", conn.daemonID, "userID", conn.userID, "error", err)
		}
	}
	return len(conns)
}

// AwaitDrained waits until every daemon stream this replica holds has ended,
// or ctx ends. It reports whether they all did.
func (s *Service) AwaitDrained(ctx context.Context) bool {
	ticker := time.NewTicker(drainPollInterval)
	defer ticker.Stop()
	for {
		if s.GetActiveConnections() == 0 {
			return true
		}
		select {
		case <-ctx.Done():
			return s.GetActiveConnections() == 0
		case <-ticker.C:
		}
	}
}

// notifyDraining tells every listener that cares that daemonID's stream here
// is draining. Must be called OUTSIDE s.mu.
func (s *Service) notifyDraining(userID, daemonID string) {
	s.listenersMu.RLock()
	defer s.listenersMu.RUnlock()
	for _, l := range s.listeners {
		if dl, ok := l.(DaemonDrainListener); ok {
			dl.OnDaemonDraining(userID, daemonID)
		}
	}
}

// routeToolResponse delivers a tool result to whoever is waiting for it and
// then acknowledges it to the daemon, which holds every result until a
// gateway does.
//
// The waiter is usually here: this replica forwarded the request, on this
// stream. When it is not — the request came in over another replica, or over
// an earlier stream that dropped, and the daemon is redelivering — the result
// is published to the request's result subject, where the worker is still
// waiting, and acknowledged only once it is safely published. A result that
// cannot be published is not acknowledged, so the daemon sends it again on
// its next stream or when the worker asks for it.
//
// Duplicates are expected (a result is resent until acknowledged) and
// harmless: the first copy satisfies the waiter, a later one is published to a
// subject nobody listens on any more, and every copy is acknowledged.
func (s *Service) routeToolResponse(conn *daemonConnection, tr *reliantv1.ToolResponse) {
	resp := &toolexec.ToolExecutionResponse{
		RequestID:    tr.RequestId,
		Success:      tr.Success,
		IsError:      tr.IsError,
		Content:      tr.Content,
		Metadata:     tr.Metadata,
		ErrorMessage: tr.ErrorMessage,
		ErrorCode:    tr.ErrorCode,
		Backgrounded: tr.Backgrounded,
		// The connection's identity is gateway-assigned, never asserted by
		// the daemon, so it is the trustworthy answer to "which machine ran
		// this".
		DaemonID: conn.daemonID,
	}
	if conn.deliverToolResponse(resp) {
		// Handed to the waiter, which publishes it to the worker at once.
		conn.ackToolResult(tr.RequestId)
		return
	}
	if s.deps.ResultPublisher == nil {
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Tool result with no waiter and nowhere to publish it; leaving it with the daemon",
			"daemonID", conn.daemonID, "requestID", tr.RequestId)
		return
	}
	// Off the receive loop: publishing waits for the NATS server to confirm.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), resultPublishTimeout)
		defer cancel()
		if err := s.deps.ResultPublisher.PublishToolResult(ctx, conn.userID, conn.daemonID, resp); err != nil {
			logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Could not deliver a redelivered tool result; the daemon keeps it",
				"daemonID", conn.daemonID, "requestID", tr.RequestId, "error", err)
			return
		}
		logging.Info(LOG_PREFIX_TOOLS_DAEMON+" Delivered a tool result whose request reached the daemon over another stream",
			"daemonID", conn.daemonID, "userID", conn.userID, "requestID", tr.RequestId)
		conn.ackToolResult(tr.RequestId)
	}()
}

// deliverToolResponse hands resp to the waiter for its request on this
// connection, if there is one.
func (c *daemonConnection) deliverToolResponse(resp *toolexec.ToolExecutionResponse) bool {
	if c.pendingToolRequests == nil {
		return false
	}
	c.pendingToolRequestsMu.Lock()
	ch, ok := c.pendingToolRequests[resp.RequestID]
	if ok {
		delete(c.pendingToolRequests, resp.RequestID)
	}
	c.pendingToolRequestsMu.Unlock()
	if ok {
		ch <- resp // buffered 1, and only this delete's winner sends
	}
	return ok
}

// ackToolResult tells the daemon its result for requestID was routed. Best
// effort: an ack that cannot be queued is only a duplicate delivery later.
func (c *daemonConnection) ackToolResult(requestID string) {
	ack := &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_ToolResultAck{
		ToolResultAck: &reliantv1.ToolResultAck{RequestId: requestID},
	}}
	select {
	case <-c.done:
	case c.sendCh <- ack:
	default:
		logging.Debug(LOG_PREFIX_TOOLS_DAEMON+" Send buffer full; tool result ack dropped (the daemon resends the result)",
			"daemonID", c.daemonID, "requestID", requestID)
	}
}
