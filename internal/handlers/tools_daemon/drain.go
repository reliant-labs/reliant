// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/serverkit"

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
//  1. The listener's drain begins (serverkit: the soft phase of its Streams
//     registry). Every ConnectDaemon stream claimed that phase, so none is
//     ended yet; each one's hand-over runs instead (handOver): this replica
//     refuses new streams, stops routing the daemon's new requests here
//     (DaemonDrainListener: the NATS bridge unsubscribes), and sends the
//     daemon GatewayDrain naming the drain's hard deadline
//     (serverkit.DrainDeadline). The daemon opens a stream to another
//     replica and moves its work there, keeping this one open for replies to
//     what is in flight on it.
//  2. The listener's drain waits, up to that deadline, for the daemons to
//     close their old streams, which each does once it is registered
//     elsewhere and has nothing in flight here.
//  3. The drain ends what is left with Unavailable. A waiter cut off there
//     answers "interrupted", which no longer fails the call: the worker waits
//     for the daemon to deliver the result through another replica
//     (routeToolResponse, and internal/toolexec/tool_result_redelivery.go).

// resultPublishTimeout bounds publishing one result that had no waiter here.
const resultPublishTimeout = 10 * time.Second

// handOverOnDrain arranges for conn's hand-over (handOver) to run when the
// drain of the listener serving ctx begins, with the deadline that drain
// ends the stream at. It is ConnectDaemon's half of the drain; call the
// returned stop when the stream ends. ctx must already have claimed the
// drain's soft phase (serverkit.Draining), or the drain ends the stream
// before the daemon hears why.
func (s *Service) handOverOnDrain(ctx context.Context, conn *daemonConnection) (stop func() bool) {
	return serverkit.AfterDrain(ctx, func() {
		// No deadline (a drain with no budget) is a notice naming none,
		// which the daemon reads as "move, with no deadline".
		var deadline time.Time
		if d, ok := serverkit.DrainDeadline(ctx); ok {
			deadline = d
		}
		s.handOver(conn, deadline)
	})
}

// handOver moves one daemon off this draining replica. From the first call
// on, the replica refuses new daemon streams. Then drain listeners stop
// routing the daemon's new requests here, and the daemon is sent GatewayDrain
// carrying deadline — when the drain will end its stream; a zero deadline
// names none. The stream stays open; replies to requests in flight on it are
// still delivered.
//
// A daemon that does not act on the notice (an older build, or one the
// gateway dialed, which has no way to move) is ended at once instead, as every
// daemon was before: left open it would only be unreachable until the
// deadline, since this replica no longer routes its requests.
//
// Idempotent per connection. Reports whether this call told the daemon to
// move.
func (s *Service) handOver(conn *daemonConnection, deadline time.Time) bool {
	s.draining.Store(true)
	if !conn.movesOnDrain {
		conn.closeWith(errGatewayDraining)
		return false
	}
	if !conn.drainNoticeSent.CompareAndSwap(false, true) {
		return false
	}
	// Listeners first: by the time the daemon can register anywhere else,
	// nothing here may still take its requests, or one request would reach
	// it twice.
	s.notifyDraining(conn.userID, conn.daemonID)
	var deadlineMs int64
	if !deadline.IsZero() {
		deadlineMs = deadline.UnixMilli()
	}
	notice := &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_Drain{
		Drain: &reliantv1.GatewayDrain{DeadlineUnixMs: deadlineMs},
	}}
	if err := s.sendToConn(conn, notice); err != nil {
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Could not tell a daemon this replica is draining; its stream ends at the deadline",
			"daemonID", conn.daemonID, "userID", conn.userID, "error", err)
		return true
	}
	logging.Debug(LOG_PREFIX_TOOLS_DAEMON+" Draining: told the daemon to move to another replica",
		"daemonID", conn.daemonID, "userID", conn.userID, "deadline", deadline)
	return true
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
	// A buffered local waiter is not delivery ownership: its gateway may die
	// before publishing the reply. Keep the daemon's durable copy until the
	// result subject publication has been confirmed, even with a local waiter.
	conn.deliverToolResponse(resp)
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
