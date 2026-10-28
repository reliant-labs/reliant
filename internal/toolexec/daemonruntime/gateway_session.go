// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Gateway rollouts, from the daemon's side: make-before-break, and results
// that outlive the stream their request came in on.
//
// A daemon-gateway replica that is shutting down sends GatewayDrain on every
// stream it holds. The daemon does not drop that stream. It dials a new one at
// once — the load balancer routes it to a replica that is still serving — and
// keeps the old one open meanwhile:
//
//   - New work moves when the new stream is registered (RegistrationAck): from
//     then on heartbeats, discovery and everything else the daemon volunteers
//     go there, and the draining replica has already stopped routing requests
//     here, so new requests arrive there too.
//   - A reply goes back on the stream its request came in on, while that
//     stream is open: the draining replica is the one holding the waiter.
//   - The old stream is retired once it has nothing in flight and a successor
//     is registered, or at the replica's deadline, whichever is first.
//
// The deadline is short (the replica's shutdown budget), and a shell command
// can run for ten minutes, so the grace period alone cannot carry a long call.
// What carries it is the result outbox: every tool result is held until a
// gateway acknowledges it (ToolResultAck), and an unacknowledged result is
// sent again over the next stream — on registration, when the stream it went
// out on ends, and when the worker asks about it (toolexec.ToolStatusCommand).
// The gateway that receives it with no waiter of its own publishes it to the
// request's result subject, where the worker is still listening. See
// internal/toolexec/tool_result_redelivery.go for the whole path.

// gatewaySession is the daemon's half of one stream to a gateway replica: the
// queue its single writer drains, and what the daemon needs to retire the
// stream gracefully when the replica drains.
type gatewaySession struct {
	// sendCh decouples message producers from the stream I/O. All goroutines
	// push messages here; the session's single runSender goroutine drains it
	// and calls stream.Send(), so no work goroutine blocks on the stream's
	// write mutex or I/O.
	sendCh   chan *reliantv1.DaemonMessage
	done     chan struct{} // closed when the session is ending; send() stops queueing
	endOnce  sync.Once
	sendDone chan struct{} // closed when runSender exits

	// draining is set when the gateway sent GatewayDrain on this stream.
	draining atomic.Bool
	// inFlight counts requests that arrived on this stream and are not done
	// with it: a daemon command until its response is queued, a tool call
	// until a gateway acknowledges its result. A draining session is retired
	// only once it reaches zero (or at the deadline).
	inFlight atomic.Int64
	// retire ends the stream from the daemon's side. Nil for a session that
	// cannot be ended early (the gateway-dials-daemon server mode).
	retire     func()
	retireOnce sync.Once
}

// sessionSendBuffer is how many messages may queue for one session's writer.
const sessionSendBuffer = 256

func newGatewaySession() *gatewaySession {
	return &gatewaySession{
		sendCh:   make(chan *reliantv1.DaemonMessage, sessionSendBuffer),
		done:     make(chan struct{}),
		sendDone: make(chan struct{}),
	}
}

// send queues msg for this session's writer. It returns errSessionEnded once
// the session is ending, including for a nil session.
func (s *gatewaySession) send(msg *reliantv1.DaemonMessage) error {
	if s == nil {
		return errSessionEnded
	}
	// An ended session must not take a message just because its queue has
	// room: a select with both cases ready picks at random, and a result
	// queued on a dead stream is a result nobody resends until much later.
	select {
	case <-s.done:
		return errSessionEnded
	default:
	}
	select {
	case s.sendCh <- msg:
		return nil
	case <-s.done:
		return errSessionEnded
	}
}

// open reports whether the session can still send.
func (s *gatewaySession) open() bool {
	if s == nil {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

// end stops the session taking messages. Idempotent.
func (s *gatewaySession) end() { s.endOnce.Do(func() { close(s.done) }) }

// backlog reports how many messages are waiting for the writer.
func (s *gatewaySession) backlog() int {
	if s == nil {
		return 0
	}
	return len(s.sendCh)
}

// runSender is the single goroutine that drains sendCh and writes to the
// bidi stream. This serialises writes (required — stream.Send is not
// thread-safe) without blocking producers on I/O.
func (s *gatewaySession) runSender(stream messageSender) {
	defer close(s.sendDone)
	for {
		select {
		case msg := <-s.sendCh:
			if msg == nil {
				return
			}
			if err := stream.Send(msg); err != nil {
				logging.Warn(logPrefix+" runSender: stream.Send failed", "error", err)
				return
			}
		case <-s.done:
			return
		}
	}
}

// senderDrainTimeout bounds how long session teardown waits for runSender.
const senderDrainTimeout = 3 * time.Second

// drain ends the session and waits — with a deadline — for runSender to stop.
// It reports whether the sender actually stopped.
//
// The deadline is the point. stream.Send writes into an io.Pipe that feeds the
// HTTP/2 request body, so it blocks whenever the peer stops reading: a gateway
// that has evicted this connection, or one whose flow-control window is full.
// runSender is then inside Send and cannot reach its `case <-done`, so an
// unbounded wait here never returns — and it sits between SIGTERM and process
// exit, which is how a daemon takes SIGTERM, prints "Shutting down", and then
// lives forever.
//
// Abandoning the goroutine is safe: the session is over, its stream is being
// discarded, and the process is on its way out.
func (s *gatewaySession) drain(timeout time.Duration) bool {
	s.end() // signal send() and runSender to stop
	select {
	case <-s.sendDone:
		return true
	case <-time.After(timeout):
		logging.Warn(logPrefix+" sender did not stop within the drain deadline; abandoning it",
			"timeout", timeout.String())
		return false
	}
}

// retireNow ends the stream from this side, once.
func (s *gatewaySession) retireNow() {
	if s == nil || s.retire == nil {
		return
	}
	s.retireOnce.Do(s.retire)
}

// drainRetireGrace is how long past the gateway's own deadline a draining
// session is left before the daemon ends it itself. The gateway ends it at
// the deadline; this only covers a gateway that never does.
const drainRetireGrace = 5 * time.Second

// errGatewayMoving ends runSession when the gateway replica serving it starts
// draining. The session is still open and keeps serving in the background
// until it is retired; run() dials its successor at once.
var errGatewayMoving = errors.New("gateway replica is draining; moving to another replica")

// send queues msg on the daemon's current session — the newest registered
// stream. It returns errSessionEnded while there is none.
func (d *daemonClient) send(msg *reliantv1.DaemonMessage) error {
	return d.session.Load().send(msg)
}

// sendBacklog reports how many messages are waiting for the current stream's
// writer.
func (d *daemonClient) sendBacklog() int {
	return d.session.Load().backlog()
}

// reply answers a request on the stream it arrived on while that stream is
// open — a draining gateway still holds the waiter — and on the current stream
// otherwise.
func (d *daemonClient) reply(origin *gatewaySession, msg *reliantv1.DaemonMessage) error {
	if origin != nil && origin.send(msg) == nil {
		return nil
	}
	return d.send(msg)
}

// holdsOpenSession reports whether the daemon still has a stream that can
// carry work, as it does while it moves off a draining replica.
func (d *daemonClient) holdsOpenSession() bool {
	return d.session.Load().open()
}

// activateSession makes s the stream new work goes on, once the gateway has
// registered it, and sends it every result still waiting for a gateway.
func (d *daemonClient) activateSession(s *gatewaySession) {
	d.session.Store(s)
	d.sendHeldResults(s, func(h *heldResult) bool { return !h.sentOn.open() })
	d.retireIdleDrainingSessions()
	d.restartMu.Lock()
	d.publishRestartLocked()
	d.restartMu.Unlock()
}

// beginSessionDrain records that the gateway serving s is draining. It reports
// whether this is the first notice for s.
func (d *daemonClient) beginSessionDrain(s *gatewaySession, notice *reliantv1.GatewayDrain) bool {
	if !s.draining.CompareAndSwap(false, true) {
		return false
	}
	d.drainingMu.Lock()
	if d.drainingSessions == nil {
		d.drainingSessions = make(map[*gatewaySession]struct{})
	}
	d.drainingSessions[s] = struct{}{}
	d.drainingMu.Unlock()

	deadline := time.UnixMilli(notice.GetDeadlineUnixMs())
	logging.Info(logPrefix+" Gateway replica is draining; moving to another replica",
		"deadline", deadline, "in_flight", s.inFlight.Load())
	if wait := time.Until(deadline) + drainRetireGrace; notice.GetDeadlineUnixMs() > 0 && wait > 0 {
		time.AfterFunc(wait, s.retireNow)
	}
	return true
}

// requestDone marks one request that arrived on s finished with it, and
// retires s if that was the last thing keeping a draining stream open.
func (d *daemonClient) requestDone(s *gatewaySession) {
	if s == nil {
		return
	}
	s.inFlight.Add(-1)
	d.maybeRetire(s)
}

// maybeRetire ends a draining session that has nothing left in flight, once a
// successor is registered to take its place.
func (d *daemonClient) maybeRetire(s *gatewaySession) {
	if s == nil || !s.draining.Load() || s.inFlight.Load() > 0 {
		return
	}
	if cur := d.session.Load(); cur == s || !cur.open() {
		return // make before break: never retire the only stream
	}
	logging.Info(logPrefix + " Retiring the drained gateway stream; its work has moved")
	s.retireNow()
}

func (d *daemonClient) retireIdleDrainingSessions() {
	d.drainingMu.Lock()
	sessions := make([]*gatewaySession, 0, len(d.drainingSessions))
	for s := range d.drainingSessions {
		sessions = append(sessions, s)
	}
	d.drainingMu.Unlock()
	for _, s := range sessions {
		d.maybeRetire(s)
	}
}

// sessionEnded runs when s is gone for good. Results whose last send was on s
// may never have reached a gateway, so they go again on the current stream.
func (d *daemonClient) sessionEnded(s *gatewaySession) {
	d.drainingMu.Lock()
	delete(d.drainingSessions, s)
	d.drainingMu.Unlock()
	if cur := d.session.Load(); cur != s && cur.open() {
		d.sendHeldResults(cur, func(h *heldResult) bool { return h.sentOn == s || !h.sentOn.open() })
	}
}

// sendToolResult holds a tool result until a gateway acknowledges it, and
// sends it: on the stream the request came in on while that is open, on the
// current stream otherwise.
func (d *daemonClient) sendToolResult(origin *gatewaySession, requestID string, result *toolexec.ExecutionResult) {
	d.restartMu.Lock()
	if d.restartAborted[requestID] {
		d.restartMu.Unlock()
		return
	}
	resp := d.toolResponse(requestID, result)
	if requestID == "" {
		d.restartMu.Unlock()
		// Nothing could ever acknowledge it, or ask for it.
		if err := d.reply(origin, resp); err != nil {
			logSendFailure(logPrefix+" Failed to send tool response via stream", err, "request_id", requestID)
		}
		return
	}
	d.results.hold(requestID, resp, origin)
	d.restartMu.Unlock()
	for _, s := range []*gatewaySession{origin, d.session.Load()} {
		if s.send(resp) == nil {
			d.results.markSent(requestID, s)
			return
		}
	}
	logging.Info(logPrefix+" No gateway stream for a finished tool call; holding its result for the next one",
		"request_id", requestID)
}

// sendHeldResults sends s every held result pick selects, oldest first.
func (d *daemonClient) sendHeldResults(s *gatewaySession, pick func(*heldResult) bool) {
	for _, h := range d.results.matching(pick) {
		if err := s.send(h.msg); err != nil {
			return
		}
		d.results.markSent(h.requestID, s)
		logging.Info(logPrefix+" Resent a held tool result", "request_id", h.requestID,
			"held_for", time.Since(h.heldAt).Round(time.Millisecond))
	}
}

// handleToolResultAck forgets an acknowledged result. Idempotent: an ack for a
// result already forgotten is a no-op.
func (d *daemonClient) handleToolResultAck(requestID string) {
	d.restartMu.Lock()
	defer d.restartMu.Unlock()
	defer d.restartProgressLocked()
	h := d.results.ack(requestID)
	if h == nil {
		return
	}
	d.requestDone(h.origin)
}

// handleToolStatus answers toolexec.ToolStatusCommand, and sends a held result
// again so that asking is enough to get a lost delivery retried.
func (d *daemonClient) handleToolStatus(payload []byte) ([]byte, error) {
	var req toolexec.ToolStatusRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid %s payload: %w", toolexec.ToolStatusCommand, err)
	}
	if strings.TrimSpace(req.RequestID) == "" {
		return nil, fmt.Errorf("%s: request_id is required", toolexec.ToolStatusCommand)
	}
	state := d.results.state(req.RequestID)
	if state == toolexec.ToolRequestCompleted {
		d.sendHeldResults(d.session.Load(), func(h *heldResult) bool { return h.requestID == req.RequestID })
	}
	return json.Marshal(toolexec.ToolStatusResponse{State: state})
}

// Result outbox bounds, in memory and on disk alike. A worker waits for a result at most its request's
// timeout plus slack — 20 minutes for the longest shell_wait — so a result
// held longer than resultHoldTTL has nobody waiting for it. The byte budget
// only matters if gateways stop acknowledging altogether.
const (
	resultHoldTTL    = 30 * time.Minute
	resultHoldBudget = 64 << 20
)

// heldResult is one tool result waiting for a gateway's acknowledgement.
type heldResult struct {
	requestID string
	msg       *reliantv1.DaemonMessage
	origin    *gatewaySession // the stream the request came in on
	sentOn    *gatewaySession // the stream it last went out on; nil if none took it
	heldAt    time.Time
	size      int
}

// resultOutbox tracks every tool call from the moment its request arrives
// until a gateway acknowledges its result, and remembers acknowledged ones for
// as long as anyone might ask. The zero value is ready to use.
type resultOutbox struct {
	mu        sync.Mutex
	running   map[string]struct{}
	held      map[string]*heldResult
	delivered map[string]time.Time
	heldBytes int

	// store mirrors running calls and held results to disk so they outlive
	// the process (result_outbox_store.go). Nil keeps the outbox in memory.
	store *outboxStore
	// ttl and budget override resultHoldTTL and resultHoldBudget when set.
	ttl    time.Duration
	budget int
}

func (o *resultOutbox) holdTTL() time.Duration {
	if o.ttl > 0 {
		return o.ttl
	}
	return resultHoldTTL
}

func (o *resultOutbox) holdBudget() int {
	if o.budget > 0 {
		return o.budget
	}
	return resultHoldBudget
}

// restore reloads what the previous process left in store and keeps writing
// to it. A result comes back held and unsent; a call that was running then is
// held as an "interrupted by daemon restart" result. It runs before the
// outbox is shared, at construction.
func (o *resultOutbox) restore(store *outboxStore) outboxLoadReport {
	o.store = store
	held, report := store.load(time.Now(), o.holdTTL(), o.holdBudget())
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.held == nil {
		o.held = make(map[string]*heldResult)
	}
	for _, h := range held {
		o.held[h.requestID] = h
		o.heldBytes += h.size
	}
	return report
}

// start records a tool call that began executing.
func (o *resultOutbox) start(requestID string) {
	if requestID == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.running == nil {
		o.running = make(map[string]struct{})
	}
	o.running[requestID] = struct{}{}
}

// persistRunning writes the marker that lets a restarted daemon say a call was
// interrupted. It is separate from start, which runs on the stream's receive
// loop and must not wait on a disk; the executing goroutine calls this before
// it runs the tool.
func (o *resultOutbox) persistRunning(requestID string) {
	o.store.markRunning(requestID)
}

// hold keeps a finished call's result until it is acknowledged.
//
// With a store, the result is on disk before it is visible to anything that
// could send or acknowledge it — a resend racing an ack must not find the file
// missing and then see it written — so a result a gateway could have seen
// survives a restart.
func (o *resultOutbox) hold(requestID string, msg *reliantv1.DaemonMessage, origin *gatewaySession) {
	now := time.Now()
	persisted := o.store.putResult(requestID, msg, now) == nil

	o.mu.Lock()
	delete(o.running, requestID)
	if o.held == nil {
		o.held = make(map[string]*heldResult)
	}
	if prev, ok := o.held[requestID]; ok {
		o.heldBytes -= prev.size
	}
	h := &heldResult{requestID: requestID, msg: msg, origin: origin, heldAt: now, size: messageSize(msg)}
	o.held[requestID] = h
	o.heldBytes += h.size
	o.expireLocked(now)
	kept := o.held[requestID] == h
	o.mu.Unlock()

	if !kept {
		o.store.removeResult(requestID) // over budget on its own
	}
	// A result that could not be written keeps its running marker, so a
	// restart still says the call was interrupted rather than forgetting it.
	if persisted {
		o.store.clearRunning(requestID)
	}
}

// markSent records the stream a held result last went out on.
func (o *resultOutbox) markSent(requestID string, s *gatewaySession) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if h, ok := o.held[requestID]; ok {
		h.sentOn = s
	}
}

// ack forgets a held result and returns it, the first time only.
func (o *resultOutbox) ack(requestID string) *heldResult {
	now := time.Now()
	o.mu.Lock()
	defer o.mu.Unlock()
	h, ok := o.held[requestID]
	if !ok {
		return nil
	}
	delete(o.held, requestID)
	o.heldBytes -= h.size
	if o.delivered == nil {
		o.delivered = make(map[string]time.Time)
	}
	o.delivered[requestID] = now
	o.expireLocked(now)
	o.store.removeResult(requestID)
	return h
}

// state is what the daemon knows about requestID.
func (o *resultOutbox) state(requestID string) toolexec.ToolRequestState {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.expireLocked(time.Now())
	if _, ok := o.running[requestID]; ok {
		return toolexec.ToolRequestRunning
	}
	if _, ok := o.held[requestID]; ok {
		return toolexec.ToolRequestCompleted
	}
	if _, ok := o.delivered[requestID]; ok {
		return toolexec.ToolRequestDelivered
	}
	return toolexec.ToolRequestUnknown
}

// matching returns the held results pick selects, oldest first.
func (o *resultOutbox) matching(pick func(*heldResult) bool) []*heldResult {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.expireLocked(time.Now())
	var out []*heldResult
	for _, h := range o.held {
		if pick(h) {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].heldAt.Before(out[j].heldAt) })
	return out
}

// runningCount is how many tool calls are executing.
func (o *resultOutbox) runningCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.running)
}

// expireLocked drops held results nobody can still be waiting for, then the
// oldest ones while the outbox is over its byte budget, and forgets
// deliveries as old. Must hold o.mu.
func (o *resultOutbox) expireLocked(now time.Time) {
	for id, h := range o.held {
		if now.Sub(h.heldAt) > o.holdTTL() {
			logging.Warn(logPrefix+" Dropping a tool result no gateway acknowledged", "request_id", id,
				"held_for", now.Sub(h.heldAt).Round(time.Second))
			delete(o.held, id)
			o.heldBytes -= h.size
			o.store.removeResult(id)
		}
	}
	for o.heldBytes > o.holdBudget() && len(o.held) > 0 {
		var oldest *heldResult
		for _, h := range o.held {
			if oldest == nil || h.heldAt.Before(oldest.heldAt) {
				oldest = h
			}
		}
		logging.Warn(logPrefix+" Dropping the oldest unacknowledged tool result: the outbox is over budget",
			"request_id", oldest.requestID, "held_bytes", o.heldBytes, "budget", o.holdBudget())
		delete(o.held, oldest.requestID)
		o.heldBytes -= oldest.size
		o.store.removeResult(oldest.requestID)
	}
	for id, at := range o.delivered {
		if now.Sub(at) > o.holdTTL() {
			delete(o.delivered, id)
		}
	}
}

// messageSize approximates a held result's memory: its payload strings.
func messageSize(msg *reliantv1.DaemonMessage) int {
	r := msg.GetToolResponse()
	return len(r.GetContent()) + len(r.GetMetadata()) + len(r.GetErrorMessage()) + 64
}
