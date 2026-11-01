// Copyright (c) 2025 Reliant Labs
package tools_daemon

// Link liveness: a registered stream is not a usable machine.
//
// A daemon registering proves only that its stream opened. A macOS laptop
// waking from sleep re-registers while it is mostly still asleep (or its link
// saturated), then answers nothing for about a minute; when it does answer,
// transit takes 5-8s while its handlers take 15-100ms. Everything this gateway
// and its listeners fanned out at registration ran into that link: the
// reconcile pass's repo.discover at 10s each, the project-dir heal's fs.mkdir
// and queued commands at 30s each, every one timing out in turn, and the pass
// then logging "complete" with nothing reconciled (prod 2026-10-30, daemon
// 317e0bb7: 197 of 297 repo.discover errors since 10-30).
//
// So the connection proves its link before work is fanned out to it:
//
//   - proven: the stream has carried one command round trip (any answer, a
//     failure included). Until then a command is held at the gateway rather
//     than sent (awaitLinkForCommand), for at most half its budget; one still
//     unproven then is answered toolexec.ErrDaemonReconnecting, unsent, so the
//     caller knows the machine is reconnecting and that asking again is safe.
//   - ready: a no-op probe (toolexec.LinkProbeCommand) came back within linkHealthyRTT.
//     Only then do the connect-time listeners run (OnDaemonLinkReady: the
//     connected event that starts queued deliveries and project-dir heals,
//     and the pending-command drain), and the reconcile pass. A probe that
//     fails, or answers too slowly, defers all of it with backoff; a
//     reconcile command that hits a transport failure stops the pass, which
//     is retried after the link proves ready again.
//
// The probe is an ordinary daemon command, so it measures what the deferred
// work would meet: the stream both ways and the daemon's own send backlog.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Link probe timings. Vars so tests can shrink them.
var (
	// linkProbeTimeout bounds one probe. A link that cannot carry a no-op
	// round trip in this long is not one to fan work out to.
	linkProbeTimeout = 5 * time.Second
	// linkHealthyRTT is the probe round trip above which the link counts as
	// too slow for connect-time work: the commands that work sends have 10s
	// budgets, and the prod laptop's 5-8s transit spent them all in transit.
	linkHealthyRTT = 2 * time.Second
	// linkRetryMin and linkRetryMax bound the backoff between connect-time
	// attempts on a link that is not ready.
	linkRetryMin = 2 * time.Second
	linkRetryMax = time.Minute
)

// reconcileMaxAttempts bounds how many reconcile passes one connection
// starts. A pass is retried only when a transport failure interrupted it;
// past this the next connect reconciles.
const reconcileMaxAttempts = 5

// linkState is a connection's link liveness. nil (a connection assembled
// outside the two registration paths, as tests do) reads as proven: holding
// commands for a probe nobody runs would wedge them.
type linkState struct {
	registeredAt time.Time
	proven       chan struct{} // closed on the first completed round trip
	provenOnce   sync.Once
	readyOnce    sync.Once
}

func newLinkState(registeredAt time.Time) *linkState {
	return &linkState{registeredAt: registeredAt, proven: make(chan struct{})}
}

var alwaysProven = func() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}()

// linkProven is closed once conn's stream has carried a round trip.
func (c *daemonConnection) linkProven() <-chan struct{} {
	if c.link == nil {
		return alwaysProven
	}
	return c.link.proven
}

// linkAnswered records that conn's stream carried a command round trip. The
// first one proves the link.
func (s *Service) linkAnswered(conn *daemonConnection, commandType string) {
	if conn.link == nil {
		return
	}
	conn.link.provenOnce.Do(func() {
		close(conn.link.proven)
		latency := time.Since(conn.link.registeredAt)
		observability.DaemonReconnectLatencySeconds.WithLabelValues("first_round_trip").Observe(latency.Seconds())
		logging.Info(LOG_PREFIX_TOOLS_DAEMON+" Daemon link proven: first command round trip completed",
			"userID", conn.userID, "daemonID", conn.daemonID,
			"commandType", commandType, "sinceRegisteredMs", latency.Milliseconds())
	})
}

// awaitLinkForCommand holds a command for a connection whose link has not
// carried a round trip yet, for at most half its budget, and reports how long
// it held. Sending it instead would put it in a stream that may not be moving,
// where it would spend its whole budget and fail as a timeout that says
// nothing about why. Held past half its budget, it is answered
// toolexec.ErrDaemonReconnecting, never sent; the other half is left for the
// round trip once the link proves itself.
func (s *Service) awaitLinkForCommand(ctx context.Context, conn *daemonConnection, req *reliantv1.DaemonCommandRequest, budget time.Duration) (time.Duration, error) {
	proven := conn.linkProven()
	select {
	case <-proven:
		return 0, nil
	default:
	}
	span := trace.SpanFromContext(ctx)
	span.AddEvent("daemon.link.await")
	start := time.Now()
	hold := time.NewTimer(budget / 2)
	defer hold.Stop()
	select {
	case <-proven:
		held := time.Since(start)
		observability.DaemonCommandsHeldTotal.WithLabelValues("released").Inc()
		span.SetAttributes(attribute.Int64("reliant.daemon.command.held_ms", held.Milliseconds()))
		return held, nil
	case <-hold.C:
		observability.DaemonCommandsHeldTotal.WithLabelValues("reconnecting").Inc()
		logging.InfoContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Released a held command unsent: the machine's fresh stream has not answered yet",
			"userID", conn.userID, "daemonID", conn.daemonID, "commandType", req.GetCommandType(),
			"heldMs", time.Since(start).Milliseconds())
		return 0, fmt.Errorf("daemon command %q held %s: %w", req.GetCommandType(), (budget / 2).Round(time.Millisecond), toolexec.ErrDaemonReconnecting)
	case <-conn.done:
		// The stream ended before it ever answered; the command was never
		// sent, and the daemon will redial.
		observability.DaemonCommandsHeldTotal.WithLabelValues("reconnecting").Inc()
		return 0, fmt.Errorf("daemon command %q: stream ended before it answered: %w", req.GetCommandType(), toolexec.ErrDaemonReconnecting)
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// probeLink sends conn the no-op probe and reports its round trip. Any answer
// counts; an error means none came back within linkProbeTimeout.
func (s *Service) probeLink(ctx context.Context, conn *daemonConnection) (time.Duration, error) {
	start := time.Now()
	_, err := s.sendCommand(ctx, conn, &reliantv1.DaemonCommandRequest{
		RequestId:   uuid.NewString(),
		CommandType: toolexec.LinkProbeCommand,
		Payload:     []byte("{}"),
		TimeoutMs:   int32(linkProbeTimeout / time.Millisecond),
	}, false)
	rtt := time.Since(start)
	outcome := "ok"
	switch {
	case err != nil:
		outcome = "failed"
	case rtt > linkHealthyRTT:
		outcome = "slow"
	}
	observability.DaemonLinkProbeSeconds.WithLabelValues(outcome).Observe(rtt.Seconds())
	return rtt, err
}

// errReconcileInterrupted stops a reconcile pass whose command met a
// transport failure: the link it needs is not there, and the rest of the pass
// would only run into it one command at a time.
var errReconcileInterrupted = errors.New("reconcile interrupted by a transport failure")

// runConnectWork proves conn's link ready, tells the link-ready listeners,
// and (reconcile=true, inbound connections) runs the reconcile pass. It runs
// for as long as the connection does, backing off while the link is not ready:
// a link that never answers costs one cheap probe a minute.
func (s *Service) runConnectWork(conn *daemonConnection, reconcile bool) {
	connCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-conn.done:
			cancel()
		case <-connCtx.Done():
		}
	}()
	ctx, span := otel.Tracer(daemonTracerName).Start(connCtx, "gateway.daemon.link.ready",
		trace.WithAttributes(daemonConnAttributes(conn)...))
	defer span.End()

	ready := false
	reconciles := 0
	backoff := linkRetryMin
	for attempt := 1; ; attempt++ {
		rtt, err := s.probeLink(ctx, conn)
		var reason string
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			reason = "probe_failed"
		case rtt > linkHealthyRTT:
			reason = "probe_slow"
		default:
			if !ready {
				ready = true
				s.linkReady(ctx, conn, attempt, rtt)
			}
			if !reconcile {
				return
			}
			reconciles++
			err = s.reconcileProjectDaemonsOnConnect(ctx, conn)
			if !errors.Is(err, errReconcileInterrupted) {
				return
			}
			if reconciles >= reconcileMaxAttempts {
				logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" reconcile: giving up for this connection; every pass met a transport failure",
					"userID", conn.userID, "daemonID", conn.daemonID, "passes", reconciles, "error", err)
				return
			}
			reason = "reconcile_interrupted"
		}
		observability.DaemonConnectWorkDeferredTotal.WithLabelValues(reason).Inc()
		span.AddEvent("daemon.link.deferred", trace.WithAttributes(
			attribute.String("reason", reason), attribute.Int64("rtt_ms", rtt.Milliseconds())))
		logging.Info(LOG_PREFIX_TOOLS_DAEMON+" Deferring connect-time work: the daemon's link is not ready",
			"userID", conn.userID, "daemonID", conn.daemonID, "reason", reason,
			"attempt", attempt, "probeRttMs", rtt.Milliseconds(), "error", err, "retryIn", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, linkRetryMax)
	}
}

// linkReady records that conn's link answered a probe fast enough to fan work
// out to, and tells the listeners waiting for that (toolexec.DaemonLinkReadyListener).
func (s *Service) linkReady(ctx context.Context, conn *daemonConnection, attempts int, rtt time.Duration) {
	select {
	case <-conn.done:
		// Superseded or gone while the probe was answering: the listeners
		// are the next connection's to tell.
		return
	default:
	}
	var latency time.Duration
	if conn.link != nil {
		fired := false
		conn.link.readyOnce.Do(func() { fired = true })
		if !fired {
			return
		}
		latency = time.Since(conn.link.registeredAt)
		observability.DaemonReconnectLatencySeconds.WithLabelValues("ready").Observe(latency.Seconds())
	}
	trace.SpanFromContext(ctx).AddEvent("daemon.link.ready", trace.WithAttributes(
		attribute.Int("attempts", attempts), attribute.Int64("rtt_ms", rtt.Milliseconds())))
	logging.Info(LOG_PREFIX_TOOLS_DAEMON+" Daemon link ready",
		"userID", conn.userID, "daemonID", conn.daemonID,
		"probeRttMs", rtt.Milliseconds(), "attempts", attempts, "sinceRegisteredMs", latency.Milliseconds())
	s.notifyLinkReady(conn)
}

// notifyLinkReady calls OnDaemonLinkReady on every listener that implements
// toolexec.DaemonLinkReadyListener. Must be called outside s.mu.
func (s *Service) notifyLinkReady(conn *daemonConnection) {
	s.listenersMu.RLock()
	defer s.listenersMu.RUnlock()
	for _, l := range s.listeners {
		if rl, ok := l.(toolexec.DaemonLinkReadyListener); ok {
			rl.OnDaemonLinkReady(conn.userID, conn.daemonID, conn.name, conn.hostname, conn.platform)
		}
	}
}
