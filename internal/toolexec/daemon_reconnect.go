// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/pkg/daemonwire"
)

// A tool call that finds its daemon gone waits for it to come back when it
// was there a moment ago, instead of failing at once.
//
// Every daemon-gateway rollout ends every daemon's stream, and each daemon
// redials 15-25s later; the daemon process, and everything it runs, carries
// on throughout (prod 2026-10-10: at least ten rollouts that day). A tool call
// in that window used to fail instantly with "no daemon connected for user".
// Each failure is an LLM turn, and one chat burned 25 of them in 16s while its
// daemon was about 10s from reconnecting.
//
// The wait is the caller's, bounded, and it re-sends only what is safe to
// send twice:
//
//   - Undelivered (NATS had no responder, or resolution says the machine is
//     still starting): nothing reached a daemon, so sending again is safe for
//     every request, shell included.
//   - Interrupted (the daemon's connection dropped with the request in
//     flight): the daemon may have run it and may still be running it. Only
//     a request that observes state the daemon keeps across a reconnect is
//     sent again (tools.ReissueAfterInterruption, reissuableDaemonCommands);
//     everything else keeps reporting its outcome as unknown.
//
// Queueing the request at the gateway or in JetStream until the daemon
// reconnects was the alternative, and it is worse for exactly the requests
// that matter: a shell command delivered after its caller gave up runs late,
// out of order with whatever the agent did next, and its answer has nobody to
// go to.

// DaemonReconnectGrace is how long after a daemon was last heard from a tool
// call still waits for it to reconnect. A call never waits past last seen +
// DaemonReconnectGrace, nor past its own deadline; a daemon away longer than
// this (a laptop closed an hour ago) fails as fast as it always did, so the
// workflow's daemon-offline pause (runtime.DaemonOfflinePauseGrace, 2m) and
// ChatActivity.WAITING_FOR_DAEMON still take over.
//
// 90s covers the worst rollout gap observed with room to spare: the last
// heartbeat can be 15s older than the drain, the gateway was unreachable for
// up to 26s, and the daemon's redial backoff is capped at 15s
// (transport.ReconnectMaxDelay), which puts the reconnect at most ~56s after
// the daemon was last seen. It is also the window daemonStaleThreshold
// already gives a quiet daemon before anything reads it as gone, and it stays
// under the offline pause's 2m, so a daemon that really is gone still pauses
// the run on the same schedule.
const DaemonReconnectGrace = 90 * time.Second

// reconnectProbeInterval is how often a waiting call asks whether its daemon
// answers again. The gateway's connect event wakes it at once; this is the
// backstop for a lost event (it rides plain NATS) and for a connect announced
// before every subscription of the daemon's was live.
const reconnectProbeInterval = 2 * time.Second

// reconnectProbeTimeout bounds one probe.
const reconnectProbeTimeout = 2 * time.Second

// errReconnectWaitExpired ends a wait whose grace ran out.
var errReconnectWaitExpired = errors.New("the machine did not reconnect in time")

// DaemonWaitObserver hears that a call started waiting for daemonID to
// reconnect, and returns what to call when that wait ends: reconnected is
// true when the daemon came back and the request went through to it.
type DaemonWaitObserver func(daemonID string) (ended func(reconnected bool))

type reconnectWaitKey struct{}

type reconnectWait struct {
	observe DaemonWaitObserver // optional
}

// WithDaemonReconnectWait makes the daemon round trips made with ctx ride out
// a short disconnect of the daemon they target (see rideOutDisconnect).
// observe, when non-nil, is told when a wait starts and ends, so the run can
// say it is waiting for its machine rather than hang silently.
//
// Opt-in, because waiting is right for a tool call and wrong for everything
// else: a file-tree poll should report the machine as reconnecting at once
// (the web's daemon-wait UI does), not hang for a minute.
func WithDaemonReconnectWait(ctx context.Context, observe DaemonWaitObserver) context.Context {
	return context.WithValue(ctx, reconnectWaitKey{}, &reconnectWait{observe: observe})
}

// WithoutDaemonReconnectWait undoes WithDaemonReconnectWait for the round
// trips made with ctx: a call that has somewhere else to run (a
// PlacementPreferDaemon tool) fails over at once instead of waiting out the
// daemon's grace period.
func WithoutDaemonReconnectWait(ctx context.Context) context.Context {
	return context.WithValue(ctx, reconnectWaitKey{}, (*reconnectWait)(nil))
}

func reconnectWaitFrom(ctx context.Context) *reconnectWait {
	w, _ := ctx.Value(reconnectWaitKey{}).(*reconnectWait)
	return w
}

// dispatchOutcome is how one daemon round trip ended, as far as waiting for a
// reconnect is concerned.
type dispatchOutcome int

const (
	// dispatchFinal: an answer, or a failure a reconnect would not change.
	dispatchFinal dispatchOutcome = iota
	// dispatchUndelivered: nothing reached a daemon. Safe to send again.
	dispatchUndelivered
	// dispatchInterrupted: the daemon's connection dropped with the request
	// in flight. Its outcome is unknown.
	dispatchInterrupted
)

// classifyDispatchErr reads a round trip's error: NATS had no responder for
// the daemon (errDaemonNotConnected), or the gateway reported the connection
// dropping under the request (ErrDaemonConnectionInterrupted).
func classifyDispatchErr(err error) dispatchOutcome {
	switch {
	case err == nil:
		return dispatchFinal
	case errors.Is(err, errDaemonNotConnected):
		return dispatchUndelivered
	case errors.Is(err, ErrDaemonConnectionInterrupted):
		return dispatchInterrupted
	default:
		return dispatchFinal
	}
}

// rideOutDisconnect runs send, one round trip to a daemon, and when that
// daemon turns out to be away waits for it to reconnect and runs send again.
// It does so only when ctx opted in (WithDaemonReconnectWait) and:
//
//   - the round trip was undelivered and the daemon was heard from within
//     DaemonReconnectGrace (daemonLastSeen), or
//   - it was interrupted and reissue agrees to send it again; reissue gets
//     how long the request has been out and prepares the next attempt. A nil
//     reissue never re-sends an interrupted request.
//
// The bound is set by the first failure and never moves: min(last seen +
// DaemonReconnectGrace, ctx's deadline). An interrupted request was in flight
// on a live connection a moment ago, so it counts as seen now.
//
// send reports the daemon it targeted ("" when there is none to wait for), how
// the round trip ended, and its error, which is what is returned. gaveUpAfter
// is how long a wait ran when it ended without the daemon, for the caller to
// put in what it reports; zero otherwise.
func (r *NATSDaemonRouter) rideOutDisconnect(
	ctx context.Context,
	userID string,
	reissue func(elapsed time.Duration) bool,
	send func() (daemonID string, outcome dispatchOutcome, err error),
) (gaveUpAfter time.Duration, err error) {
	wait := reconnectWaitFrom(ctx)
	start := time.Now()
	var (
		deadline  time.Time
		waitStart time.Time
		ended     func(bool)
		attempts  int
	)
	finish := func(reconnected bool) {
		if ended != nil {
			ended(reconnected)
			ended = nil
		}
	}
	for {
		daemonID, outcome, err := send()
		attempts++
		if wait == nil || daemonID == "" || outcome == dispatchFinal {
			// Reached a daemon, unless resolution found none at all.
			finish(daemonID != "")
			return 0, err
		}
		if outcome == dispatchInterrupted && (reissue == nil || !reissue(time.Since(start))) {
			finish(false)
			return 0, err
		}
		if deadline.IsZero() {
			seen := time.Now()
			if outcome == dispatchUndelivered {
				seen = r.daemonLastSeen(ctx, userID, daemonID)
			}
			deadline = r.reconnectDeadline(ctx, userID, daemonID, seen.Add(DaemonReconnectGrace))
			if !time.Now().Before(deadline) {
				// Away longer than a reconnect takes: fail as fast as ever.
				return 0, err
			}
			waitStart = time.Now()
			logging.InfoContext(ctx, "[DaemonRouter] daemon is away; waiting for it to reconnect",
				"user_id", userID, "daemon_id", daemonID, "last_seen", seen,
				"wait_until", deadline, "interrupted", outcome == dispatchInterrupted)
			if wait.observe != nil {
				ended = wait.observe(daemonID)
			}
		}
		// After a reconnect that still did not get the request through, wait
		// a probe interval before asking again: a daemon that answers the
		// probe but not the request must not be hammered in a tight loop.
		if werr := r.awaitDaemon(ctx, userID, daemonID, deadline, attempts > 1); werr != nil {
			finish(false)
			waited := time.Since(waitStart)
			logging.InfoContext(ctx, "[DaemonRouter] gave up waiting for the daemon to reconnect",
				"user_id", userID, "daemon_id", daemonID, "waited", waited.Round(time.Millisecond), "reason", werr)
			return waited, err
		}
		logging.InfoContext(ctx, "[DaemonRouter] daemon reconnected; sending the request again",
			"user_id", userID, "daemon_id", daemonID, "waited", time.Since(waitStart).Round(time.Millisecond))
	}
}

// withWaitNote says in a failure that the call already waited for the machine,
// so neither the model nor the user reads it as an instant refusal.
func withWaitNote(err error, waited time.Duration) error {
	if err == nil || waited <= 0 {
		return err
	}
	return fmt.Errorf("%w (waited %s for the machine to reconnect)", err, waited.Round(time.Second))
}

// awaitDaemon blocks until daemonID answers on NATS again, deadline passes or
// ctx ends. It wakes on the gateway's connect event for the daemon and, as a
// backstop, probes every reconnectProbeInterval; skipFirstProbe defers the
// first probe by one interval.
//
// The subscription is made before the first probe, so a connect that lands
// between the two is not missed: the probe goes out on the same connection
// after the subscription, and finds the daemon either way.
func (r *NATSDaemonRouter) awaitDaemon(ctx context.Context, userID, daemonID string, deadline time.Time, skipFirstProbe bool) error {
	return r.awaitDaemonSubject(ctx, userID, daemonID, deadline, skipFirstProbe, toolOnlineSubject)
}

func (r *NATSDaemonRouter) awaitDaemonSubject(ctx context.Context, userID, daemonID string, deadline time.Time, skipFirstProbe bool, subject string) error {
	if r.nc == nil {
		return errors.New("no NATS connection to wait on")
	}
	connected := make(chan struct{}, 1)
	sub, err := r.nc.Subscribe(daemonwire.Subject(daemonID, daemonwire.EventConnected), func(*nats.Msg) {
		select {
		case connected <- struct{}{}:
		default:
		}
	})
	if err != nil {
		// The probe below still finds the daemon, only later.
		logging.WarnContext(ctx, "[DaemonRouter] cannot subscribe to the daemon's connect event; probing only",
			"daemon_id", daemonID, "error", err)
	} else {
		defer func() { _ = sub.Unsubscribe() }()
	}

	expired := time.NewTimer(time.Until(deadline))
	defer expired.Stop()
	probe := time.NewTicker(reconnectProbeInterval)
	defer probe.Stop()

	if !skipFirstProbe && r.daemonAnswersSubject(ctx, userID, daemonID, deadline, subject) {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-expired.C:
			return errReconnectWaitExpired
		case <-connected:
		case <-probe.C:
		}
		if r.daemonAnswersSubject(ctx, userID, daemonID, deadline, subject) {
			return nil
		}
	}
}

// daemonAnswersSubject probes new-work or recovery reachability separately.
func (r *NATSDaemonRouter) daemonAnswersSubject(ctx context.Context, userID, daemonID string, deadline time.Time, subject string) bool {
	timeout := min(reconnectProbeTimeout, time.Until(deadline))
	if timeout <= 0 {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	msg, err := r.nc.RequestWithContext(probeCtx, daemonSubject(subject, userID, daemonID), nil)
	return err == nil && string(msg.Data) == "true"
}

// daemonLastSeen is when a gateway last heard from daemonID, per the
// registry; zero when it never did or the registry cannot say, which means
// "do not wait".
func (r *NATSDaemonRouter) daemonLastSeen(ctx context.Context, userID, daemonID string) time.Time {
	if r.db == nil {
		return time.Time{}
	}
	seen, err := r.db.DaemonLastSeenAt(ctx, userID, daemonID)
	if err != nil {
		logging.WarnContext(ctx, "[DaemonRouter] cannot read when the daemon was last seen; not waiting for it",
			"user_id", userID, "daemon_id", daemonID, "error", err)
		return time.Time{}
	}
	return seen
}

// startingDaemonID names the machine a resolution that failed with
// ErrDaemonPending is waiting on, when that machine is starting on its own
// (provisioning or cloning): it can be waited for like any reconnecting
// daemon. "" for anything else, a suspended machine included — nothing wakes
// one from tool-time resolution, so waiting for it would only delay the
// failure.
func (r *NATSDaemonRouter) startingDaemonID(ctx context.Context, userID string, selector *DaemonSelector, resolveErr error) string {
	if !IsDaemonPending(resolveErr) {
		return ""
	}
	rec, found, _, err := r.lookupDaemonRecord(ctx, userID, selector)
	if err != nil || !found || rec.state != recordStarting {
		return ""
	}
	return rec.id
}

// reissuableDaemonCommands are the daemon commands that only read state the
// daemon keeps across a reconnect, so a command interrupted by a dropped
// connection is sent again after the daemon redials. These are what the
// worker-side tools (view and its kin) send through daemon.RemoteClient to
// read files and background processes.
//
// Everything that writes (fs.write_file, fs.patch_file, fs.find_replace,
// fs.mkdir, fs.delete) or runs something (exec.run, exec.bg_start,
// exec.bg_kill) is absent on purpose: the first attempt may have happened.
var reissuableDaemonCommands = map[string]bool{
	"fs.read_file":        true,
	"fs.read_binary_file": true,
	"fs.pdf_page_count":   true,
	"fs.read_pdf_pages":   true,
	"fs.stat":             true,
	"fs.list_dir":         true,
	"fs.glob":             true,
	"fs.search":           true,
	"exec.bg_output":      true,
	"exec.bg_list":        true,
}

// interruption is ErrDaemonConnectionInterrupted in the gateway's own words,
// rebuilt on this side of NATS from a reply's error_code.
type interruption string

func (e interruption) Error() string        { return string(e) }
func (e interruption) Is(target error) bool { return target == ErrDaemonConnectionInterrupted }
