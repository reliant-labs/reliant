package natsutil

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
)

// A NATS server refuses a subscription outside the connection's grants
// asynchronously: Subscribe and Flush both succeed, the server answers the SUB
// with "-ERR 'Permissions Violation for Subscription to "<subject>"'", and
// nats.go reports it only to the connection's async error handler, with no
// *nats.Subscription attached. The client-side subscription stays registered
// but the server never delivers to it, so a process that subscribed a moment
// before the broker loaded its new grants is deaf on that subject until it
// restarts (reliant#2610: the gateway's disconnect command during the
// v1.14.0-rc.1 rollout).
//
// SubscribeWithRecovery owns that failure. It routes the refusal back to the
// subscription it belongs to, and re-subscribes with capped exponential
// backoff until the server accepts it.

const (
	// DefaultRetryInitial and DefaultRetryMax bound the re-subscribe backoff:
	// 1s, 2s, 4s, ... 60s, 60s.
	DefaultRetryInitial = time.Second
	DefaultRetryMax     = time.Minute

	// retryFlushTimeout bounds a retry's round trip, so a connection that
	// drops mid-retry defers it instead of holding it for nats.go's 10s.
	retryFlushTimeout = 5 * time.Second

	// defaultEscalateAfter is how long a subscription stays refused before it
	// is logged at ERROR, once: a permissions roll lands within seconds or a
	// minute or two, and a refusal that outlives that is a missing grant.
	defaultEscalateAfter = 5 * time.Minute
)

// SubscriptionEventKind names a step of a refused subscription's recovery.
type SubscriptionEventKind int

const (
	// SubscriptionRefused: the server refused the subscription; a retry is
	// scheduled RetryIn from now.
	SubscriptionRefused SubscriptionEventKind = iota + 1
	// SubscriptionResubscribed: a retry sent the subscription again.
	SubscriptionResubscribed
	// SubscriptionRecovered: a re-subscription went unrefused for the settle
	// window, so the server accepted it.
	SubscriptionRecovered
	// SubscriptionDeferred: a retry could not get the subscription accepted
	// because the connection is down (reconnecting, or its round trip
	// failed); another is scheduled RetryIn from now.
	SubscriptionDeferred
	// SubscriptionClosed: the connection was closed while the subscription
	// was refused; nothing is left to re-subscribe on, so recovery ended.
	SubscriptionClosed
)

func (k SubscriptionEventKind) String() string {
	switch k {
	case SubscriptionRefused:
		return "refused"
	case SubscriptionResubscribed:
		return "resubscribed"
	case SubscriptionRecovered:
		return "recovered"
	case SubscriptionDeferred:
		return "deferred"
	case SubscriptionClosed:
		return "closed"
	default:
		return fmt.Sprintf("SubscriptionEventKind(%d)", int(k))
	}
}

// SubscriptionEvent is one step of a refused subscription's recovery,
// delivered to the WithEvents hook.
type SubscriptionEvent struct {
	Kind    SubscriptionEventKind
	Subject string
	Queue   string
	// Attempt counts the refusals of the current episode (since the
	// subscription was last active).
	Attempt int
	// RetryIn is the delay before the next attempt (SubscriptionRefused).
	RetryIn time.Duration
	// Err is the server's refusal (SubscriptionRefused), or the error of a
	// retry that could not subscribe at all.
	Err error
}

// SubscribeOption configures SubscribeWithRecovery.
type SubscribeOption func(*subscribeConfig)

// WithQueue makes the subscription a queue subscription in group queue.
func WithQueue(queue string) SubscribeOption {
	return func(c *subscribeConfig) { c.queue = queue }
}

// WithBackoff sets the re-subscribe backoff: initial after the first refusal,
// doubling up to max. A re-subscription that goes unrefused for initial
// counts as accepted. Non-positive values keep the defaults.
func WithBackoff(initial, maxDelay time.Duration) SubscribeOption {
	return func(c *subscribeConfig) {
		if initial > 0 {
			c.initial = initial
		}
		if maxDelay > 0 {
			c.max = maxDelay
		}
	}
}

// WithEvents calls fn for every recovery step. fn runs on NATS's or a
// timer's goroutine and must not block.
func WithEvents(fn func(SubscriptionEvent)) SubscribeOption {
	return func(c *subscribeConfig) { c.events = fn }
}

// withClock replaces the wall clock (tests).
func withClock(clk clock) SubscribeOption {
	return func(c *subscribeConfig) { c.clock = clk }
}

// withRetryFlush replaces a retry's round trip (tests).
func withRetryFlush(f func(*nats.Conn) error) SubscribeOption {
	return func(c *subscribeConfig) { c.retryFlush = f }
}

type subscribeConfig struct {
	queue         string
	initial       time.Duration
	max           time.Duration
	escalateAfter time.Duration
	events        func(SubscriptionEvent)
	clock         clock
	retryFlush    func(*nats.Conn) error
}

// delay is the wait after the n-th refusal (n from 0): initial·2ⁿ, capped.
func (c *subscribeConfig) delay(n int) time.Duration {
	d := c.initial
	for i := 0; i < n && d < c.max; i++ {
		d *= 2
	}
	return min(d, c.max)
}

// clock is the time source of the backoff, injectable for tests.
type clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) timer
}

type timer interface{ Stop() bool }

type wallClock struct{}

func (wallClock) Now() time.Time                            { return time.Now() }
func (wallClock) AfterFunc(d time.Duration, f func()) timer { return time.AfterFunc(d, f) }

// Subscription is a NATS subscription that re-subscribes itself when the
// server refuses it. Safe for concurrent use.
type Subscription struct {
	nc      *nats.Conn
	subject string
	cb      nats.MsgHandler
	cfg     subscribeConfig
	router  *refusalRouter

	// attempt serializes the subscribe calls (the first and every retry), so
	// a refusal that schedules a retry while one is still in flight cannot
	// leave two live client-side subscriptions behind.
	attempt sync.Mutex

	mu       sync.Mutex
	sub      *nats.Subscription
	stopped  bool
	refused  bool // refused, and not accepted since
	retrying bool // a retry is scheduled
	gen      uint64
	refusals int // in the current episode
	since    time.Time
	alerted  bool // the episode has been logged at ERROR
	timer    timer
}

// SubscribeWithRecovery subscribes cb to subject on nc, like nc.Subscribe,
// and keeps the subscription alive through the server refusing it: on a
// permissions violation for this subscription it re-subscribes with capped
// exponential backoff (WithBackoff; 1s to 60s by default) until the server
// accepts it. A retry counts only after a round trip proves the server has
// the SUB; while the connection is down retries are deferred on the same
// backoff, so recovery ends only when the subscription is stopped or its
// connection closed. Each refusal is logged once at WARN with the next delay; a
// refusal that lasts past five minutes is logged once at ERROR, because by
// then a grant is missing rather than still rolling out. The metric
// reliant_nats_subscriptions_refused{subject} is nonzero while it is not
// active, and Active reports the same.
//
// The subscription is registered with the server before this returns, so a
// message published after it is not missed unless the server refused it.
//
// subject is a metric label: use it for subscriptions on a fixed subject
// pattern, not per-entity ones. Refusals are matched to their subscription by
// subject and queue, so one recovering subscription per subject and queue on
// a connection is the supported shape. nc's async error handler is wrapped on
// first use; do not replace it afterwards.
func SubscribeWithRecovery(nc *nats.Conn, subject string, cb nats.MsgHandler, opts ...SubscribeOption) (*Subscription, error) {
	if nc == nil {
		return nil, fmt.Errorf("subscribe %s: %w", subject, nats.ErrInvalidConnection)
	}
	cfg := subscribeConfig{
		initial:       DefaultRetryInitial,
		max:           DefaultRetryMax,
		escalateAfter: defaultEscalateAfter,
		clock:         wallClock{},
		retryFlush:    func(nc *nats.Conn) error { return nc.FlushTimeout(retryFlushTimeout) },
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	cfg.max = max(cfg.max, cfg.initial)
	s := &Subscription{nc: nc, subject: subject, cb: cb, cfg: cfg}

	s.attempt.Lock()
	defer s.attempt.Unlock()
	// Register before subscribing, so a refusal of the first SUB is routed.
	s.router = routerFor(nc)
	s.router.add(s)
	sub, err := s.subscribeOnce()
	if err == nil {
		err = flush(nc, subject)
	}
	if err != nil {
		s.router.remove(s)
		if sub != nil {
			_ = sub.Unsubscribe()
		}
		return nil, err
	}
	s.mu.Lock()
	s.sub = sub
	s.mu.Unlock()
	return s, nil
}

// Active reports whether the subscription is in place and the server has not
// refused it since it was last accepted.
func (s *Subscription) Active() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.stopped && s.sub != nil && !s.refused
}

// Stop unsubscribes and cancels any pending retry. Safe to call more than
// once.
func (s *Subscription) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	sub, wasRefused := s.sub, s.refused
	s.sub = nil
	s.mu.Unlock()
	if wasRefused {
		observability.NATSSubscriptionsRefused.WithLabelValues(s.subject).Dec()
	}
	s.router.remove(s)
	if sub != nil {
		_ = sub.Unsubscribe()
	}
}

func (s *Subscription) subscribeOnce() (*nats.Subscription, error) {
	var (
		sub *nats.Subscription
		err error
	)
	if s.cfg.queue != "" {
		sub, err = s.nc.QueueSubscribe(s.subject, s.cfg.queue, s.cb)
	} else {
		sub, err = s.nc.Subscribe(s.subject, s.cb)
	}
	if err != nil {
		return nil, fmt.Errorf("subscribe %s: %w", s.subject, err)
	}
	return sub, nil
}

func flush(nc *nats.Conn, subject string) error {
	if err := nc.Flush(); err != nil {
		return fmt.Errorf("subscribe %s: flush: %w", subject, err)
	}
	return nil
}

// onRefusal handles the server refusing this subscription.
func (s *Subscription) onRefusal(err error) {
	s.scheduleRetry(err, true)
}

// scheduleRetry schedules the next attempt after the current one was not
// accepted: refused by the server (refusal), or never confirmed because the
// connection was down. It logs once for this step of the backoff.
func (s *Subscription) scheduleRetry(cause error, refusal bool) {
	s.mu.Lock()
	if s.stopped || s.retrying {
		// A retry is already pending: this is the same outage.
		s.mu.Unlock()
		return
	}
	s.gen++
	now := s.cfg.clock.Now()
	if !s.refused {
		s.refused = true
		s.since = now
		s.refusals = 0
		s.alerted = false
		observability.NATSSubscriptionsRefused.WithLabelValues(s.subject).Inc()
	}
	if s.timer != nil { // a pending acceptance check: the attempt was refused
		s.timer.Stop()
	}
	delay := s.cfg.delay(s.refusals)
	s.refusals++
	attempt := s.refusals
	refusedFor := now.Sub(s.since)
	// Only the server's refusal escalates: a connection that is down is
	// reported by the connection's own handlers, not as a missing grant.
	alert := refusal && !s.alerted && refusedFor >= s.cfg.escalateAfter
	if alert {
		s.alerted = true
	}
	s.retrying = true
	s.timer = s.cfg.clock.AfterFunc(delay, s.retry)
	s.mu.Unlock()

	kind := SubscriptionDeferred
	if refusal {
		kind = SubscriptionRefused
		observability.NATSSubscriptionRefusalsTotal.WithLabelValues(s.subject).Inc()
		logging.Warn("NATS refused a subscription; re-subscribing with backoff until it is accepted (normal for a few seconds while a NATS permissions change rolls out)",
			"subject", s.subject, "queue", s.cfg.queue, "attempt", attempt,
			"retry_in", delay.String(), "refused_for", refusedFor.String(), "error", cause)
	} else {
		logging.Warn("NATS connection unavailable; a refused subscription is re-subscribed with backoff once it is back",
			"subject", s.subject, "queue", s.cfg.queue, "attempt", attempt,
			"retry_in", delay.String(), "refused_for", refusedFor.String(), "error", cause)
	}
	if alert {
		logging.Error("NATS has refused a subscription for longer than a permissions roll-out takes; grant the subject in control-plane deploy/nats/permissions.yaml if the code needs it",
			"subject", s.subject, "queue", s.cfg.queue, "attempt", attempt,
			"refused_for", refusedFor.String(), "error", cause)
	}
	s.emit(SubscriptionEvent{Kind: kind, Attempt: attempt, RetryIn: delay, Err: cause})
}

// errNotConnected defers a retry while the connection is reconnecting.
var errNotConnected = errors.New("nats: not connected (reconnecting)")

// retry replaces the refused subscription with a new one. Only once a round
// trip proves the server has the SUB does it wait one settle window (the
// initial backoff) for a refusal before calling it active. A connection that
// is down defers the retry on the same backoff; nats.go reconnects forever
// (Connect), so the episode ends only when the connection or the
// subscription is closed.
func (s *Subscription) retry() {
	s.attempt.Lock()
	defer s.attempt.Unlock()

	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.retrying = false
	s.timer = nil
	gen := s.gen
	attempt := s.refusals
	s.mu.Unlock()

	if s.connectionClosed() {
		return
	}
	if !s.nc.IsConnected() {
		// Keep the current SUB: nats.go resends it on reconnect, and the
		// next retry replaces it either way.
		s.scheduleRetry(errNotConnected, false)
		return
	}

	s.mu.Lock()
	old := s.sub
	s.sub = nil
	s.mu.Unlock()
	if old != nil {
		// The server never registered the refused SUB; this drops the
		// client side, and the server ignores the UNSUB.
		_ = old.Unsubscribe()
	}
	sub, err := s.subscribeOnce()
	if err != nil {
		if !s.connectionClosed() {
			s.scheduleRetry(err, false)
		}
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		_ = sub.Unsubscribe()
		return
	}
	s.sub = sub
	s.mu.Unlock()

	if err := s.cfg.retryFlush(s.nc); err != nil {
		// The SUB may never have reached the server, or its answer never
		// came back: it is not accepted. It stays registered client-side
		// (resent on reconnect); the next retry replaces it.
		if !s.connectionClosed() {
			s.scheduleRetry(fmt.Errorf("subscribe %s: flush: %w", s.subject, err), false)
		}
		return
	}

	s.mu.Lock()
	if gen == s.gen && !s.retrying && !s.stopped {
		s.timer = s.cfg.clock.AfterFunc(s.cfg.initial, func() { s.confirm(gen) })
	}
	s.mu.Unlock()
	s.emit(SubscriptionEvent{Kind: SubscriptionResubscribed, Attempt: attempt})
}

// connectionClosed ends the subscription if its connection is closed or
// draining. Both are terminal in nats.go — a closed *nats.Conn never
// reconnects, and Connect retries forever, so it closes only when the process
// closes it — and there is nothing left to re-subscribe on.
func (s *Subscription) connectionClosed() bool {
	if !s.nc.IsClosed() && !s.nc.IsDraining() {
		return false
	}
	logging.Info("NATS connection closed; a refused subscription stops re-subscribing",
		"subject", s.subject, "queue", s.cfg.queue)
	s.Stop()
	s.emit(SubscriptionEvent{Kind: SubscriptionClosed})
	return true
}

// confirm ends the refusal episode if attempt gen went unrefused.
func (s *Subscription) confirm(gen uint64) {
	s.mu.Lock()
	if s.stopped || s.retrying || gen != s.gen || !s.refused {
		s.mu.Unlock()
		return
	}
	s.refused = false
	s.timer = nil
	refusals, refusedFor := s.refusals, s.cfg.clock.Now().Sub(s.since)
	s.refusals = 0
	s.mu.Unlock()

	observability.NATSSubscriptionsRefused.WithLabelValues(s.subject).Dec()
	logging.Info("NATS accepted a subscription it had refused",
		"subject", s.subject, "queue", s.cfg.queue, "refusals", refusals, "refused_for", refusedFor.String())
	s.emit(SubscriptionEvent{Kind: SubscriptionRecovered, Attempt: refusals})
}

func (s *Subscription) emit(e SubscriptionEvent) {
	if s.cfg.events == nil {
		return
	}
	e.Subject, e.Queue = s.subject, s.cfg.queue
	s.cfg.events(e)
}

// ─── Routing refusals to their subscription ─────────────────────────────────

var (
	refusedSubjectRe = regexp.MustCompile(`(?i)subscription to "([^"]+)"`)
	refusedQueueRe   = regexp.MustCompile(`(?i)using queue "([^"]+)"`)
)

// refusedSubscription parses a subscription permissions violation into its
// subject and queue.
func refusedSubscription(err error) (subject, queue string, ok bool) {
	if err == nil {
		return "", "", false
	}
	text := err.Error()
	if !errors.Is(err, nats.ErrPermissionViolation) && !strings.Contains(strings.ToLower(text), "permissions violation") {
		return "", "", false
	}
	m := refusedSubjectRe.FindStringSubmatch(text)
	if m == nil {
		return "", "", false // a refused publish
	}
	if q := refusedQueueRe.FindStringSubmatch(text); q != nil {
		queue = q[1]
	}
	return m[1], queue, true
}

type subKey struct{ subject, queue string }

// refusalRouter is installed in front of one connection's async error
// handler, and hands each subscription refusal to the recovering
// subscriptions it names. Everything else goes on to the handler it wraps.
type refusalRouter struct {
	mu   sync.Mutex
	subs map[subKey]map[*Subscription]struct{}
}

var routers = struct {
	sync.Mutex
	byConn map[*nats.Conn]*refusalRouter
}{byConn: map[*nats.Conn]*refusalRouter{}}

// routerFor returns nc's router, installing it on first use. A router lives
// as long as its connection: one per connection, never re-wrapped.
func routerFor(nc *nats.Conn) *refusalRouter {
	routers.Lock()
	defer routers.Unlock()
	for c := range routers.byConn {
		if c.IsClosed() {
			delete(routers.byConn, c)
		}
	}
	if r, ok := routers.byConn[nc]; ok {
		return r
	}
	r := &refusalRouter{subs: map[subKey]map[*Subscription]struct{}{}}
	next := nc.ErrorHandler()
	nc.SetErrorHandler(func(c *nats.Conn, sub *nats.Subscription, err error) {
		if r.route(err) {
			return
		}
		if next != nil {
			next(c, sub, err)
		}
	})
	routers.byConn[nc] = r
	return r
}

func (r *refusalRouter) add(s *Subscription) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := subKey{s.subject, s.cfg.queue}
	if r.subs[k] == nil {
		r.subs[k] = map[*Subscription]struct{}{}
	}
	r.subs[k][s] = struct{}{}
}

func (r *refusalRouter) remove(s *Subscription) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := subKey{s.subject, s.cfg.queue}
	delete(r.subs[k], s)
	if len(r.subs[k]) == 0 {
		delete(r.subs, k)
	}
}

// route hands a subscription refusal to the recovering subscriptions on its
// subject and queue, and reports whether any took it.
func (r *refusalRouter) route(err error) bool {
	subject, queue, ok := refusedSubscription(err)
	if !ok {
		return false
	}
	r.mu.Lock()
	var owners []*Subscription
	for s := range r.subs[subKey{subject, queue}] {
		owners = append(owners, s)
	}
	r.mu.Unlock()
	for _, s := range owners {
		s.onRefusal(err)
	}
	return len(owners) > 0
}
