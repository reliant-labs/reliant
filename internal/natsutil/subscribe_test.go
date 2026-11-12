package natsutil

import (
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/reliant-labs/reliant/internal/observability"
)

// fakeClock fires timers only when the test advances it, each on its own
// goroutine like time.AfterFunc.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	clock   *fakeClock
	at      time.Time
	f       func()
	stopped bool
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{clock: c, at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	was := !t.stopped
	t.stopped = true
	return was
}

// Advance moves the clock forward by d and fires every timer now due.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []func()
	kept := c.timers[:0]
	for _, t := range c.timers {
		switch {
		case t.stopped:
		case !t.at.After(c.now):
			t.stopped = true
			due = append(due, t.f)
		default:
			kept = append(kept, t)
		}
	}
	c.timers = kept
	c.mu.Unlock()
	for _, f := range due {
		go f()
	}
}

// events collects a subscription's recovery steps.
type events chan SubscriptionEvent

func (e events) hook(ev SubscriptionEvent) { e <- ev }

// await returns the next event matching want, skipping others (a retry's
// "resubscribed" and the server's refusal of it race).
func (e events) await(t *testing.T, what string, want func(SubscriptionEvent) bool) SubscriptionEvent {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev := <-e:
			if want(ev) {
				return ev
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func refusedEvent(attempt int) func(SubscriptionEvent) bool {
	return func(ev SubscriptionEvent) bool { return ev.Kind == SubscriptionRefused && ev.Attempt == attempt }
}

func resubscribedEvent(attempt int) func(SubscriptionEvent) bool {
	return func(ev SubscriptionEvent) bool { return ev.Kind == SubscriptionResubscribed && ev.Attempt == attempt }
}

// permissionedServer runs a NATS server where "gateway" may subscribe to
// allowed (plus inboxes) and "publisher" may publish anywhere.
type permissionedServer struct {
	srv  *server.Server
	port int
}

func gatewayOptions(port int, allowed ...string) *server.Options {
	opts := natstest.DefaultTestOptions
	opts.Port = port
	opts.Users = []*server.User{
		{
			Username: "gateway",
			Password: "pw",
			Permissions: &server.Permissions{
				Subscribe: &server.SubjectPermission{Allow: append([]string{"_INBOX.>"}, allowed...)},
			},
		},
		{Username: "publisher", Password: "pw"},
	}
	return &opts
}

func runPermissionedServer(t *testing.T, allowed ...string) *permissionedServer {
	t.Helper()
	srv := natstest.RunServer(gatewayOptions(-1, allowed...))
	t.Cleanup(srv.Shutdown)
	return &permissionedServer{srv: srv, port: srv.Addr().(*net.TCPAddr).Port}
}

// allow reloads the server's permissions so gateway may subscribe to subjects:
// what a permissions roll does to a broker the gateway is already on.
func (p *permissionedServer) allow(t *testing.T, subjects ...string) {
	t.Helper()
	if err := p.srv.ReloadOptions(gatewayOptions(p.port, subjects...)); err != nil {
		t.Fatalf("reload permissions: %v", err)
	}
}

func (p *permissionedServer) connect(t *testing.T, user string) *nats.Conn {
	t.Helper()
	nc, err := Connect(Endpoint{URL: p.srv.ClientURL(), User: user, Password: "pw"})
	if err != nil {
		t.Fatalf("connect %s: %v", user, err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func refusedGauge(subject string) float64 {
	return testutil.ToFloat64(observability.NATSSubscriptionsRefused.WithLabelValues(subject))
}

// TestSubscribeWithRecoveryRecoversWhenPermissionsArrive is reliant#2610: a
// gateway subscribed before the broker loaded the grant, the broker refused
// it, and the subscription stayed dead until the pod restarted. It must keep
// re-subscribing, and once the grant lands a message published afterwards
// must be delivered — with WARNs on the way, not the ERROR a missing grant
// gets.
func TestSubscribeWithRecoveryRecoversWhenPermissionsArrive(t *testing.T) {
	rec := recordLogs(t)
	const subject = "test.recovery.commands.disconnect"
	srv := runPermissionedServer(t /* subject not granted yet */)
	nc := srv.connect(t, "gateway")
	pub := srv.connect(t, "publisher")

	clk := newFakeClock()
	evs := make(events, 64)
	delivered := make(chan string, 1)
	sub, err := SubscribeWithRecovery(nc, subject, func(m *nats.Msg) { delivered <- string(m.Data) },
		withClock(clk), WithEvents(evs.hook))
	if err != nil {
		t.Fatalf("SubscribeWithRecovery: %v", err)
	}
	t.Cleanup(sub.Stop)

	if ev := evs.await(t, "the first refusal", refusedEvent(1)); ev.RetryIn != time.Second {
		t.Fatalf("first retry in %v, want 1s", ev.RetryIn)
	}
	if sub.Active() {
		t.Fatal("Active() = true for a subscription the server refused")
	}
	if got := refusedGauge(subject); got != 1 {
		t.Fatalf("subscriptions_refused = %v while refused, want 1", got)
	}

	// Still refused on the first retry: the backoff doubles.
	clk.Advance(time.Second)
	if ev := evs.await(t, "the retry's refusal", refusedEvent(2)); ev.RetryIn != 2*time.Second {
		t.Fatalf("second retry in %v, want 2s", ev.RetryIn)
	}

	// The permissions roll lands; the next retry is accepted.
	srv.allow(t, subject)
	clk.Advance(2 * time.Second)
	evs.await(t, "the re-subscription after the grant", resubscribedEvent(2))
	clk.Advance(time.Second) // the settle window passes with no refusal
	evs.await(t, "recovery", func(ev SubscriptionEvent) bool { return ev.Kind == SubscriptionRecovered })

	if !sub.Active() {
		t.Fatal("Active() = false after the server accepted the subscription")
	}
	if got := refusedGauge(subject); got != 0 {
		t.Fatalf("subscriptions_refused = %v after recovery, want 0", got)
	}
	if err := pub.Publish(subject, []byte("revoke d-1")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-delivered:
		if got != "revoke d-1" {
			t.Fatalf("delivered %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a message published after recovery was not delivered")
	}

	warns := rec.matching("NATS refused a subscription;")
	if len(warns) != 2 {
		t.Fatalf("got %d refusal records, want 2 (one per backoff step): %q", len(warns), warns)
	}
	for _, w := range warns {
		if !strings.HasPrefix(w, "WARN ") {
			t.Errorf("a refusal under recovery was not logged at WARN: %s", w)
		}
	}
	if got := rec.matching("NATS refused an operation"); len(got) != 0 {
		t.Errorf("a recovering subscription's refusal reached the ERROR log: %q", got)
	}
	if got := rec.matching("NATS accepted a subscription it had refused"); len(got) != 1 {
		t.Errorf("got %d recovery records, want 1: %q", len(got), got)
	}

	// Refusals of anything else on the connection still go to the ERROR log.
	if _, err := nc.SubscribeSync("test.recovery.not.granted"); err != nil {
		t.Fatal(err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := rec.await(t, "NATS refused an operation", 1, 5*time.Second); len(got) != 1 || !strings.HasPrefix(got[0], "ERROR ") {
		t.Errorf("a plain subscription's refusal must still be logged at ERROR, got %q", got)
	}
}

// TestSubscribeWithRecoveryBackoffIsBoundedAndLogsOncePerStep — the retry
// delay doubles from 1s and never exceeds 60s, each refusal is one WARN, and
// a refusal that outlasts any permissions roll is escalated to ERROR once.
func TestSubscribeWithRecoveryBackoffIsBoundedAndLogsOncePerStep(t *testing.T) {
	rec := recordLogs(t)
	const subject = "test.backoff.never.granted"
	srv := runPermissionedServer(t)
	nc := srv.connect(t, "gateway")

	clk := newFakeClock()
	evs := make(events, 64)
	sub, err := SubscribeWithRecovery(nc, subject, func(*nats.Msg) {}, withClock(clk), WithEvents(evs.hook))
	if err != nil {
		t.Fatalf("SubscribeWithRecovery: %v", err)
	}

	s := time.Second
	want := []time.Duration{1 * s, 2 * s, 4 * s, 8 * s, 16 * s, 32 * s, 60 * s, 60 * s, 60 * s, 60 * s, 60 * s, 60 * s}
	var elapsed time.Duration
	for i, delay := range want {
		ev := evs.await(t, "a refusal", refusedEvent(i+1))
		if ev.RetryIn != delay {
			t.Fatalf("refusal %d: retry in %v, want %v", i+1, ev.RetryIn, delay)
		}
		if got := rec.matching("NATS refused a subscription;"); len(got) != i+1 {
			t.Fatalf("after %d refusals: %d WARN records, want one per step", i+1, len(got))
		}
		// Refusals land at 0, 1, 3, ... 303s: only the one past 5m alerts.
		alerts := rec.matching("NATS has refused a subscription for longer")
		if wantAlerts := map[bool]int{true: 1, false: 0}[elapsed >= defaultEscalateAfter]; len(alerts) != wantAlerts {
			t.Fatalf("after %v refused: %d ERROR records, want %d: %q", elapsed, len(alerts), wantAlerts, alerts)
		}
		clk.Advance(delay)
		elapsed += delay
	}
	evs.await(t, "the last retry's refusal", refusedEvent(len(want)+1))
	if got := rec.matching("NATS has refused a subscription for longer"); len(got) != 1 || !strings.HasPrefix(got[0], "ERROR ") {
		t.Fatalf("a refusal lasting %v must be escalated to ERROR exactly once, got %q", elapsed, got)
	}

	sub.Stop()
	if got := refusedGauge(subject); got != 0 {
		t.Fatalf("subscriptions_refused = %v after Stop, want 0", got)
	}
	if sub.Active() {
		t.Fatal("Active() = true after Stop")
	}
}

func TestBackoffDelayIsCapped(t *testing.T) {
	c := subscribeConfig{initial: time.Second, max: time.Minute}
	for n, want := range map[int]time.Duration{0: time.Second, 1: 2 * time.Second, 5: 32 * time.Second, 6: time.Minute, 1000: time.Minute} {
		if got := c.delay(n); got != want {
			t.Errorf("delay(%d) = %v, want %v", n, got, want)
		}
	}
}

func TestRefusedSubscriptionParsesSubjectAndQueue(t *testing.T) {
	cases := []struct {
		text           string
		subject, queue string
		ok             bool
	}{
		{`nats: permissions violation: Permissions Violation for Subscription to "daemon.v1.commands.disconnect"`, "daemon.v1.commands.disconnect", "", true},
		{`nats: permissions violation: Permissions Violation for Subscription to "daemon.v1.registry.snapshot" using queue "snap"`, "daemon.v1.registry.snapshot", "snap", true},
		{`nats: permissions violation: Permissions Violation for Publish to "tools.result.u.d.r"`, "", "", false},
		{`nats: slow consumer, messages dropped`, "", "", false},
	}
	for _, c := range cases {
		subject, queue, ok := refusedSubscription(errString(c.text))
		if subject != c.subject || queue != c.queue || ok != c.ok {
			t.Errorf("refusedSubscription(%q) = %q, %q, %v; want %q, %q, %v", c.text, subject, queue, ok, c.subject, c.queue, c.ok)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func kindEvent(kind SubscriptionEventKind, attempt int) func(SubscriptionEvent) bool {
	return func(ev SubscriptionEvent) bool { return ev.Kind == kind && ev.Attempt == attempt }
}

func awaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// TestSubscribeWithRecoveryResumesAcrossADisconnectDuringTheRetry — the
// broker drops the connection in the middle of a retry, after the SUB is sent
// but before its round trip completes. That attempt was never proven, so
// the subscription must stay inactive, keep retrying on the same backoff
// while the connection is down, and recover once it is back: a message
// published afterwards is delivered.
func TestSubscribeWithRecoveryResumesAcrossADisconnectDuringTheRetry(t *testing.T) {
	recordLogs(t)
	const subject = "test.reconnect.commands.disconnect"
	srv := runPermissionedServer(t /* subject not granted yet */)
	disconnected, reconnected := make(chan struct{}, 8), make(chan struct{}, 8)
	nc, err := Connect(Endpoint{URL: srv.srv.ClientURL(), User: "gateway", Password: "pw"},
		nats.ReconnectWait(10*time.Millisecond),
		nats.DisconnectErrHandler(func(*nats.Conn, error) { disconnected <- struct{}{} }),
		nats.ReconnectHandler(func(*nats.Conn) { reconnected <- struct{}{} }))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(nc.Close)

	// The next retry's round trip loses the broker after its SUB is written.
	var dropOnFlush atomic.Bool
	flush := func(c *nats.Conn) error {
		if dropOnFlush.CompareAndSwap(true, false) {
			srv.srv.Shutdown()
			awaitSignal(t, disconnected, "the connection to drop")
			return c.FlushTimeout(50 * time.Millisecond)
		}
		return c.FlushTimeout(5 * time.Second)
	}

	clk := newFakeClock()
	evs := make(events, 64)
	delivered := make(chan string, 4)
	sub, err := SubscribeWithRecovery(nc, subject, func(m *nats.Msg) { delivered <- string(m.Data) },
		withClock(clk), WithEvents(evs.hook), withRetryFlush(flush))
	if err != nil {
		t.Fatalf("SubscribeWithRecovery: %v", err)
	}
	t.Cleanup(sub.Stop)
	evs.await(t, "the first refusal", refusedEvent(1))

	// The grant lands, so the retry's SUB is not refused; but the broker is
	// gone before the round trip that would prove it was accepted.
	srv.allow(t, subject)
	dropOnFlush.Store(true)
	clk.Advance(time.Second)
	if ev := evs.await(t, "the retry whose round trip failed", kindEvent(SubscriptionDeferred, 2)); ev.RetryIn != 2*time.Second {
		t.Fatalf("after a failed round trip: retry in %v, want 2s", ev.RetryIn)
	}
	if sub.Active() {
		t.Fatal("Active() = true for a re-subscription whose round trip failed")
	}

	// Still down at the next retry: deferred again, on the same backoff.
	clk.Advance(2 * time.Second)
	if ev := evs.await(t, "the retry while disconnected", kindEvent(SubscriptionDeferred, 3)); ev.RetryIn != 4*time.Second {
		t.Fatalf("while disconnected: retry in %v, want 4s", ev.RetryIn)
	}

	// The broker comes back, with the grant rolled out.
	back := natstest.RunServer(gatewayOptions(srv.port, subject))
	t.Cleanup(back.Shutdown)
	awaitSignal(t, reconnected, "the reconnect")
	if sub.Active() {
		t.Fatal("Active() = true on reconnect, before any retry proved the subscription accepted")
	}

	clk.Advance(4 * time.Second)
	evs.await(t, "the re-subscription after the reconnect", resubscribedEvent(3))
	if sub.Active() {
		t.Fatal("Active() = true before the settle window passed")
	}
	clk.Advance(time.Second)
	evs.await(t, "recovery", func(ev SubscriptionEvent) bool { return ev.Kind == SubscriptionRecovered })
	if !sub.Active() {
		t.Fatal("Active() = false after recovery")
	}

	pub, err := Connect(Endpoint{URL: back.ClientURL(), User: "publisher", Password: "pw"})
	if err != nil {
		t.Fatalf("connect publisher: %v", err)
	}
	t.Cleanup(pub.Close)
	if err := pub.Publish(subject, []byte("revoke d-2")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-delivered:
		if got != "revoke d-2" {
			t.Fatalf("delivered %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a message published after the reconnect and recovery was not delivered")
	}
	if got := refusedGauge(subject); got != 0 {
		t.Fatalf("subscriptions_refused = %v after recovery, want 0", got)
	}
}

// TestSubscribeWithRecoveryEndsWhenTheConnectionIsClosed — a closed
// *nats.Conn never reconnects (Connect retries forever, so only the process
// closes it). Recovery ends there: no retry spins on it, and the gauge does
// not report a subscription that no longer exists.
func TestSubscribeWithRecoveryEndsWhenTheConnectionIsClosed(t *testing.T) {
	recordLogs(t)
	const subject = "test.closed.commands.disconnect"
	srv := runPermissionedServer(t)
	nc := srv.connect(t, "gateway")

	clk := newFakeClock()
	evs := make(events, 64)
	sub, err := SubscribeWithRecovery(nc, subject, func(*nats.Msg) {}, withClock(clk), WithEvents(evs.hook))
	if err != nil {
		t.Fatalf("SubscribeWithRecovery: %v", err)
	}
	evs.await(t, "the first refusal", refusedEvent(1))

	nc.Close()
	clk.Advance(time.Second)
	evs.await(t, "recovery to end", func(ev SubscriptionEvent) bool { return ev.Kind == SubscriptionClosed })
	if sub.Active() {
		t.Fatal("Active() = true on a closed connection")
	}
	if got := refusedGauge(subject); got != 0 {
		t.Fatalf("subscriptions_refused = %v after the connection closed, want 0", got)
	}
	sub.Stop() // still safe
}

// registered reports whether sub's router still routes refusals to it.
func registered(sub *Subscription) bool {
	sub.router.mu.Lock()
	defer sub.router.mu.Unlock()
	_, ok := sub.router.subs[subKey{sub.subject, sub.cfg.queue}][sub]
	return ok
}

// TestSubscribeWithRecoveryRetryRaceWithTheConnection — the connection
// changes state between a retry's connectivity check and its Subscribe. Every
// such attempt must end in one of two outcomes, never neither: a closed
// connection ends the subscription with Stop's full cleanup, and a
// reconnectable drop schedules another retry that recovers.
func TestSubscribeWithRecoveryRetryRaceWithTheConnection(t *testing.T) {
	t.Run("closed", func(t *testing.T) {
		recordLogs(t)
		const subject = "test.race.closed.commands.disconnect"
		srv := runPermissionedServer(t)
		nc := srv.connect(t, "gateway")

		clk := newFakeClock()
		evs := make(events, 64)
		var closeNext atomic.Bool
		sub, err := SubscribeWithRecovery(nc, subject, func(*nats.Msg) {}, withClock(clk), WithEvents(evs.hook),
			withBeforeSubscribe(func(c *nats.Conn) {
				if closeNext.CompareAndSwap(true, false) {
					c.Close()
				}
			}))
		if err != nil {
			t.Fatalf("SubscribeWithRecovery: %v", err)
		}
		evs.await(t, "the first refusal", refusedEvent(1))
		if !registered(sub) {
			t.Fatal("a refused subscription must stay registered for its refusals")
		}

		closeNext.Store(true)
		clk.Advance(time.Second)
		ev := evs.await(t, "recovery to end", func(ev SubscriptionEvent) bool { return ev.Kind == SubscriptionClosed })
		if ev.Err == nil {
			t.Error("SubscriptionClosed carries no cause")
		}
		if sub.Active() {
			t.Fatal("Active() = true on a closed connection")
		}
		if registered(sub) {
			t.Fatal("the subscription is still registered after its connection closed")
		}
		if got := refusedGauge(subject); got != 0 {
			t.Fatalf("subscriptions_refused = %v after the connection closed, want 0", got)
		}
		sub.mu.Lock()
		pending := sub.retrying || sub.timer != nil
		sub.mu.Unlock()
		if pending {
			t.Fatal("a retry is still scheduled on a closed connection")
		}
	})

	t.Run("reconnectable drop", func(t *testing.T) {
		recordLogs(t)
		const subject = "test.race.drop.commands.disconnect"
		srv := runPermissionedServer(t)
		disconnected, reconnected := make(chan struct{}, 8), make(chan struct{}, 8)
		nc, err := Connect(Endpoint{URL: srv.srv.ClientURL(), User: "gateway", Password: "pw"},
			nats.ReconnectWait(10*time.Millisecond),
			nats.DisconnectErrHandler(func(*nats.Conn, error) { disconnected <- struct{}{} }),
			nats.ReconnectHandler(func(*nats.Conn) { reconnected <- struct{}{} }))
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
		t.Cleanup(nc.Close)

		clk := newFakeClock()
		evs := make(events, 64)
		delivered := make(chan string, 4)
		var dropNext atomic.Bool
		sub, err := SubscribeWithRecovery(nc, subject, func(m *nats.Msg) { delivered <- string(m.Data) },
			withClock(clk), WithEvents(evs.hook),
			withRetryFlush(func(c *nats.Conn) error { return c.FlushTimeout(200 * time.Millisecond) }),
			withBeforeSubscribe(func(*nats.Conn) {
				if dropNext.CompareAndSwap(true, false) {
					srv.srv.Shutdown()
					awaitSignal(t, disconnected, "the connection to drop")
				}
			}))
		if err != nil {
			t.Fatalf("SubscribeWithRecovery: %v", err)
		}
		t.Cleanup(sub.Stop)
		evs.await(t, "the first refusal", refusedEvent(1))

		dropNext.Store(true)
		clk.Advance(time.Second)
		if ev := evs.await(t, "a retry scheduled after the drop", kindEvent(SubscriptionDeferred, 2)); ev.RetryIn != 2*time.Second {
			t.Fatalf("after the drop: retry in %v, want 2s", ev.RetryIn)
		}
		if sub.Active() || !registered(sub) {
			t.Fatalf("after a reconnectable drop: Active()=%v registered=%v, want false, true", sub.Active(), registered(sub))
		}

		back := natstest.RunServer(gatewayOptions(srv.port, subject))
		t.Cleanup(back.Shutdown)
		awaitSignal(t, reconnected, "the reconnect")
		clk.Advance(2 * time.Second)
		evs.await(t, "the scheduled retry", resubscribedEvent(2))
		clk.Advance(time.Second)
		evs.await(t, "recovery", func(ev SubscriptionEvent) bool { return ev.Kind == SubscriptionRecovered })

		pub, err := Connect(Endpoint{URL: back.ClientURL(), User: "publisher", Password: "pw"})
		if err != nil {
			t.Fatalf("connect publisher: %v", err)
		}
		t.Cleanup(pub.Close)
		if err := pub.Publish(subject, []byte("revoke d-3")); err != nil {
			t.Fatal(err)
		}
		select {
		case <-delivered:
		case <-time.After(10 * time.Second):
			t.Fatal("a message published after recovery was not delivered")
		}
	})
}
