// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/reliant-labs/reliant/internal/logging"
)

// A tool call outlives the gateway stream it was sent on.
//
// The daemon runs every tool request independently of the stream that carried
// it, so a stream that drops — a daemon-gateway rollout, a network blip, a
// gateway replica that crashed — does not stop the work. What it used to stop
// was the RESULT: the only thing waiting for it was the gateway replica that
// forwarded the request, keyed by request id on that one stream, and when the
// stream died that waiter answered "connection interrupted, outcome unknown"
// and went away. Every gateway rollout failed every in-flight call that way
// (prod, 3.5 days: ~20 rollouts, ~85 interrupted calls), including a
// 20-minute shell_wait that was going to succeed.
//
// Now the result has an address that does not depend on any one stream:
//
//   - The worker sends each tools.request.sync with its reply subject set to
//     tools.result.{userID}.{daemonID}.{requestID} and listens there, instead
//     of on a private inbox only the forwarding gateway knows.
//   - The daemon keeps every tool result until a gateway acknowledges it
//     (ToolResultAck), and sends unacknowledged results again over each new
//     stream.
//   - A gateway that receives a result with no local waiter — the request was
//     forwarded by another replica, or by an earlier stream of its own —
//     publishes it to that subject (ToolResultPublisher) and only then
//     acknowledges it.
//   - A worker told the stream dropped does not fail the call: it waits for
//     the daemon to come back, asks it what became of the request
//     (ToolStatusCommand), and keeps waiting for the result while the daemon
//     says it is running or holds the answer (awaitRedelivery).
//
// The call fails only when its own timeout passes, when the daemon does not
// come back within DaemonReconnectGrace, or when the daemon that comes back no
// longer knows the request — it restarted, and everything it was running died
// with it. Duplicates are harmless by construction: a result is the same bytes
// however many times it is sent, the worker takes the first one and
// unsubscribes, and an ack for a result the daemon no longer holds is ignored.

// toolResultSubjectBase is where a tool request's result is delivered:
// tools.result.{userID}.{daemonID}.{requestID}.
const toolResultSubjectBase = "tools.result"

// toolResultSubject is the subject the result of one tool request is delivered
// on, by whichever gateway the daemon hands it to.
func toolResultSubject(userID, daemonID, requestID string) string {
	return daemonSubject(toolResultSubjectBase, userID, daemonID) + "." + requestID
}

// resultRequestIDPattern is what a request id must look like to become a
// subject token. Request ids are UUIDs minted by the worker (newRequestID),
// but the id on a result comes back from the daemon and is not trusted to be
// one: a '.', '*' or '>' in it would address a different subject.
var resultRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// ToolStatusCommand is the daemon command that asks a daemon what became of
// one tool request. Its payload is a ToolStatusRequest and its answer a
// ToolStatusResponse. A daemon answering "completed" also sends the held
// result again, so asking is enough to get a lost delivery retried.
const ToolStatusCommand = "tool.status"

// ToolStatusRequest is ToolStatusCommand's payload.
type ToolStatusRequest struct {
	RequestID string `json:"request_id"`
}

// ToolStatusResponse is ToolStatusCommand's answer.
type ToolStatusResponse struct {
	State ToolRequestState `json:"state"`
}

// ToolRequestState is what a daemon knows about one tool request.
type ToolRequestState string

const (
	// ToolRequestRunning: the request is executing.
	ToolRequestRunning ToolRequestState = "running"
	// ToolRequestCompleted: it finished and its result is held, waiting for a
	// gateway to acknowledge it.
	ToolRequestCompleted ToolRequestState = "completed"
	// ToolRequestDelivered: it finished and a gateway acknowledged its result.
	ToolRequestDelivered ToolRequestState = "delivered"
	// ToolRequestUnknown: the daemon has no record of it. For a request it
	// was sent, that means the daemon restarted since — or held the result
	// so long that it gave up on anyone wanting it.
	ToolRequestUnknown ToolRequestState = "unknown"
)

// ToolResultPublisher delivers a tool result to the worker waiting for it, from
// a gateway that has no local waiter for it. The daemon-gateway wires one
// into the tools-daemon service.
type ToolResultPublisher struct {
	nc *nats.Conn
}

// NewToolResultPublisher publishes results on nc.
func NewToolResultPublisher(nc *nats.Conn) *ToolResultPublisher {
	return &ToolResultPublisher{nc: nc}
}

// toolResultFlushTimeout bounds the flush that confirms the NATS server has a
// published result. The daemon forgets a result once it is acknowledged, so
// the acknowledgement must not go out on the strength of bytes still sitting
// in this process's write buffer.
const toolResultFlushTimeout = 5 * time.Second

// PublishToolResult publishes resp to its request's result subject and returns
// once the NATS server has it. A result too large even to chunk is replaced by
// the same structured RESPONSE_TOO_LARGE error the bridge sends.
func (p *ToolResultPublisher) PublishToolResult(ctx context.Context, userID, daemonID string, resp *ToolExecutionResponse) error {
	if p == nil || p.nc == nil {
		return errors.New("no NATS connection to publish tool results on")
	}
	if resp == nil || !resultRequestIDPattern.MatchString(resp.RequestID) {
		return fmt.Errorf("tool result carries an unusable request id")
	}
	subject := toolResultSubject(userID, daemonID, resp.RequestID)
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal tool result: %w", err)
	}
	if _, err := publishReply(p.nc, subject, data); err != nil {
		if !errors.Is(err, errReplyExceedsAbsoluteCap) {
			return fmt.Errorf("publish tool result: %w", err)
		}
		tooLarge, _ := json.Marshal(oversizeToolResult(resp.RequestID, len(data)))
		if err := p.nc.Publish(subject, tooLarge); err != nil {
			return fmt.Errorf("publish oversize-result error: %w", err)
		}
	}
	flushCtx, cancel := context.WithTimeout(ctx, toolResultFlushTimeout)
	defer cancel()
	if err := p.nc.FlushWithContext(flushCtx); err != nil {
		return fmt.Errorf("flush tool result: %w", err)
	}
	return nil
}

// oversizeToolResult is the result that stands in for one beyond the absolute
// reply cap. Content carries the message because Content is what the model
// reads, and it is what lets the model narrow its request.
func oversizeToolResult(requestID string, size int) *ToolExecutionResponse {
	msg := oversizeNATSPayloadError("tool result", size, maxChunkedReplyBytes, oversizeReplyHint)
	return &ToolExecutionResponse{
		RequestID:    requestID,
		Success:      false,
		IsError:      true,
		Content:      msg,
		ErrorMessage: msg,
		ErrorCode:    "RESPONSE_TOO_LARGE",
	}
}

// Redelivery wait cadence. See awaitRedelivery.
const (
	// toolStatusTimeout bounds one ToolStatusCommand round trip.
	toolStatusTimeout = 10 * time.Second
	// redeliveryRecheckInterval is how long a worker waits for a result the
	// daemon says is coming before asking again. Asking again is what notices
	// a daemon that restarted after it last answered, and it makes a daemon
	// holding the result send it again.
	redeliveryRecheckInterval = 30 * time.Second
)

// awaitRedelivery waits for the result of a tool request whose daemon stream
// dropped while it was in flight. interrupted is the gateway's report of the
// drop; inbox is the request's result subject, still subscribed.
//
// It never fails the request because a stream dropped. It returns:
//
//   - the result, whenever it arrives, from whichever gateway delivers it;
//   - interrupted (outcome unknown, dispatchFinal) when the daemon does not
//     come back within DaemonReconnectGrace of each drop;
//   - a lost-request report (dispatchInterrupted, so a request that only
//     observes state may be re-sent) when the daemon that came back no longer
//     knows the request — it restarted, and the request died with it. That
//     is reported at once: it is a clean, prompt failure, not a timeout;
//   - a timeout when the request's own deadline passes, as any request would.
func (r *NATSDaemonRouter) awaitRedelivery(
	ctx context.Context, inbox *replyInbox, userID, daemonID string,
	request *ToolExecutionRequest, interrupted *ToolExecutionResponse, deadline time.Time,
) (*ToolExecutionResponse, dispatchOutcome, error) {
	start := time.Now()
	logging.InfoContext(ctx, "[DaemonRouter] daemon stream dropped under a tool request; waiting for its result to be redelivered",
		"user_id", userID, "daemon_id", daemonID, "request_id", request.RequestID, "tool", request.ToolName,
		"deadline", deadline)
	delivered := func(resp *ToolExecutionResponse) (*ToolExecutionResponse, dispatchOutcome, error) {
		logging.InfoContext(ctx, "[DaemonRouter] tool result redelivered after its daemon stream dropped",
			"user_id", userID, "daemon_id", daemonID, "request_id", request.RequestID,
			"waited", time.Since(start).Round(time.Millisecond))
		return resp, dispatchFinal, nil
	}

	for {
		// Nothing can be asked of the daemon, and nothing delivered by it,
		// until a gateway holds its stream again. A result that lands first
		// is taken: the daemon sends held results as soon as it registers.
		msg, reachable, err := r.awaitDaemonOrReply(ctx, inbox, userID, daemonID, earliest(deadline, r.reconnectDeadline(ctx, userID, daemonID, time.Now().Add(DaemonReconnectGrace))))
		switch {
		case err != nil:
			return nil, dispatchFinal, redeliveryWaitError(ctx, err)
		case msg != nil:
			resp, err := decodeToolReply(msg)
			if err != nil {
				return nil, dispatchFinal, err
			}
			if resp.ErrorCode != ErrorCodeDaemonInterrupted {
				return delivered(resp)
			}
			continue // another drop, reported by a later stream's gateway
		case !reachable:
			if !time.Now().Before(deadline) {
				return nil, dispatchFinal, daemonRequestError("tool request", nats.ErrTimeout)
			}
			logging.InfoContext(ctx, "[DaemonRouter] gave up waiting for a redelivered tool result: the machine did not reconnect",
				"user_id", userID, "daemon_id", daemonID, "request_id", request.RequestID,
				"waited", time.Since(start).Round(time.Millisecond))
			return withMessage(interrupted, fmt.Sprintf("%s (the machine did not reconnect within %s)",
				interrupted.ErrorMessage, DaemonReconnectGrace)), dispatchFinal, nil
		}

		state, err := r.toolRequestState(ctx, userID, daemonID, request.RequestID)
		if err != nil && !errors.Is(err, errDaemonReportedFailure) {
			if ctx.Err() != nil {
				return nil, dispatchFinal, redeliveryWaitError(ctx, err)
			}
			// The daemon or its gateway went away again between the probe and
			// the question. Pause before probing again, so a daemon that
			// answers the probe but not the question is not asked in a loop.
			logging.DebugContext(ctx, "[DaemonRouter] could not ask the daemon about an interrupted tool request; retrying",
				"daemon_id", daemonID, "request_id", request.RequestID, "error", err)
			if resp, done, err := r.nextToolReply(ctx, inbox, earliest(deadline, time.Now().Add(reconnectProbeInterval))); done {
				if err == nil && resp.ErrorCode != ErrorCodeDaemonInterrupted {
					return delivered(resp)
				}
				if err != nil {
					return nil, dispatchFinal, err
				}
			}
			if !time.Now().Before(deadline) {
				return nil, dispatchFinal, daemonRequestError("tool request", nats.ErrTimeout)
			}
			continue
		}

		if err != nil || state == ToolRequestUnknown {
			// The daemon has no record of the request: it restarted, and the
			// request died with it (or never reached it). Nothing can be on
			// its way — a result the daemon delivered it would still report
			// as delivered — so the loss is reported now, not after a wait.
			// A daemon too old to answer is the same case: it keeps no
			// results to redeliver.
			logging.InfoContext(ctx, "[DaemonRouter] interrupted tool request is lost: the daemon no longer knows it",
				"user_id", userID, "daemon_id", daemonID, "request_id", request.RequestID, "status_error", err)
			return lostToolRequest(interrupted, request.RequestID), dispatchInterrupted, nil
		}

		// Running, or finished and on its way: wait for it, and ask again
		// after a while. A delivered result that never arrives was lost by a
		// gateway that died between acknowledging it and passing it on.
		wait := redeliveryRecheckInterval
		resp, done, err := r.nextToolReply(ctx, inbox, earliest(deadline, time.Now().Add(wait)))
		switch {
		case err != nil:
			return nil, dispatchFinal, err
		case done && resp.ErrorCode != ErrorCodeDaemonInterrupted:
			return delivered(resp)
		case !time.Now().Before(deadline):
			return nil, dispatchFinal, daemonRequestError("tool request", nats.ErrTimeout)
		case !done && state == ToolRequestDelivered:
			logging.WarnContext(ctx, "[DaemonRouter] a gateway acknowledged a tool result that never arrived; reporting it lost",
				"user_id", userID, "daemon_id", daemonID, "request_id", request.RequestID)
			return lostToolRequest(interrupted, request.RequestID), dispatchInterrupted, nil
		}
	}
}

// nextToolReply waits until deadline for the next reply on inbox. done is false
// when none came; err is set only for a failure other than that.
func (r *NATSDaemonRouter) nextToolReply(ctx context.Context, inbox *replyInbox, deadline time.Time) (resp *ToolExecutionResponse, done bool, err error) {
	msg, err := inbox.next(ctx, deadline)
	if err != nil {
		if errors.Is(err, nats.ErrTimeout) && ctx.Err() == nil {
			return nil, false, nil
		}
		return nil, true, redeliveryWaitError(ctx, err)
	}
	resp, err = decodeToolReply(msg)
	return resp, true, err
}

// awaitDaemonOrReply waits until a gateway holds daemonID's stream again (see
// awaitDaemon) or a reply lands on inbox, whichever is first, up to by.
func (r *NATSDaemonRouter) awaitDaemonOrReply(ctx context.Context, inbox *replyInbox, userID, daemonID string, by time.Time) (msg *nats.Msg, reachable bool, err error) {
	waitCtx, stop := context.WithCancel(ctx)
	defer stop()
	probe := make(chan error, 1)
	go func() {
		err := r.awaitDaemonSubject(waitCtx, userID, daemonID, by, false, toolRecoverySubject)
		probe <- err
		stop() // ends the inbox wait below
	}()
	msg, err = inbox.next(waitCtx, by)
	if err == nil {
		return msg, false, nil
	}
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	if !errors.Is(err, nats.ErrTimeout) && !errors.Is(err, context.Canceled) {
		return nil, false, err
	}
	return nil, <-probe == nil, nil
}

// toolRequestState asks daemonID what became of requestID. A daemon that
// answered with a failure (it does not know the command) is reported as
// errDaemonReportedFailure; anything else is the transport's error.
func (r *NATSDaemonRouter) toolRequestState(ctx context.Context, userID, daemonID, requestID string) (ToolRequestState, error) {
	payload, err := json.Marshal(ToolStatusRequest{RequestID: requestID})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, toolStatusTimeout)
	defer cancel()
	data, cmdID, err := r.buildDaemonCommand(ctx, ToolStatusCommand, payload, int32(toolStatusTimeout.Milliseconds()))
	if err != nil {
		return "", err
	}
	reply, err := r.sendDaemonCommandData(ctx, userID, daemonID, ToolStatusCommand, data, cmdID, int32(toolStatusTimeout.Milliseconds()))
	if err != nil {
		return "", err
	}
	var status ToolStatusResponse
	if err := json.Unmarshal(reply, &status); err != nil {
		return "", fmt.Errorf("%w: unreadable %s answer: %v", errDaemonReportedFailure, ToolStatusCommand, err)
	}
	return status.State, nil
}

// decodeToolReply reads one tools.request.sync reply.
func decodeToolReply(msg *nats.Msg) (*ToolExecutionResponse, error) {
	var resp ToolExecutionResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal tool response: %w", err)
	}
	return &resp, nil
}

// redeliveryWaitError names a redelivery wait that ended for the caller's
// reasons the way a round trip that did would be named.
func redeliveryWaitError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("tool request via NATS failed: %w", ctxErr)
	}
	return daemonRequestError("tool request", err)
}

// lostToolRequest reports an interrupted request whose daemon came back
// without it.
func lostToolRequest(interrupted *ToolExecutionResponse, requestID string) *ToolExecutionResponse {
	return withMessage(interrupted, fmt.Sprintf(
		"%s while waiting for tool request %q response, and the machine no longer knows the request: "+
			"its daemon restarted (or never received the request), so the outcome is unknown; "+
			"verify its effects before re-running it",
		ErrDaemonConnectionInterrupted, requestID))
}

// withMessage is resp saying message. A transport failure carries no Content
// of its own; the executor shows the model its ErrorMessage instead.
func withMessage(resp *ToolExecutionResponse, message string) *ToolExecutionResponse {
	out := *resp
	out.ErrorMessage = message
	return &out
}

func earliest(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
