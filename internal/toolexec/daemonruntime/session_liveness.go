// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"fmt"
	"sync"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec/transport"
)

// Session liveness: noticing a gateway stream that has died without the
// transport noticing.
//
// The daemon's transport keepalive is HTTP/2 PING (transport/client.go), and
// the nearest HTTP/2 peer is a load balancer, which answers pings itself. A
// successful stream.Send means only that the bytes were buffered. So neither
// notices the failure that matters: the gateway has stopped READING this
// stream. That is what happened in prod on 2026-10-11 — nothing the daemon
// sent was read for ~20 minutes, every command routed to it timed out, and the
// daemon, still receiving the gateway's heartbeats, believed itself connected
// the whole time and never redialed.
//
// Two end-to-end checks close that gap, each answered by the gateway
// application rather than anything in between:
//
//   - The gateway acknowledges, in every ServerHeartbeat, the newest daemon
//     heartbeat its receive loop has read. A heartbeat this daemon sent that
//     stays unacknowledged for heartbeatAckTimeout means the gateway is not
//     reading: end the session and redial.
//   - The gateway sends a ServerHeartbeat every 15s. Nothing at all for
//     gatewaySilenceTimeout means the other direction is dead.
//
// A gateway that predates acknowledgements always sends 0. The ack check
// arms only once a non-zero acknowledgement has been seen on the session, so
// a new daemon behind an old gateway is never cut off for silence it cannot
// help.

const (
	// gatewaySilenceTimeout: the gateway heartbeats every 15s, so 90s is six
	// missed in a row.
	gatewaySilenceTimeout = 90 * time.Second
	// heartbeatAckTimeout: an acknowledgement normally lands within one
	// gateway heartbeat (15s) of the daemon's heartbeat being read.
	heartbeatAckTimeout = 90 * time.Second
	// livenessCheckInterval is how often a session checks itself.
	livenessCheckInterval = 5 * time.Second
)

// sessionTiming is the cadence a daemon session keeps. The zero value means
// production defaults; tests shorten it.
type sessionTiming struct {
	heartbeatInterval     time.Duration
	gatewaySilenceTimeout time.Duration
	heartbeatAckTimeout   time.Duration
	livenessCheckInterval time.Duration
}

func (t sessionTiming) withDefaults() sessionTiming {
	if t.heartbeatInterval <= 0 {
		t.heartbeatInterval = transport.DaemonHeartbeatInterval
	}
	if t.gatewaySilenceTimeout <= 0 {
		t.gatewaySilenceTimeout = gatewaySilenceTimeout
	}
	if t.heartbeatAckTimeout <= 0 {
		t.heartbeatAckTimeout = heartbeatAckTimeout
	}
	if t.livenessCheckInterval <= 0 {
		t.livenessCheckInterval = livenessCheckInterval
	}
	return t
}

// sessionLiveness tracks one gateway session's evidence of life. Every method
// is safe for concurrent use and nil-safe (sessions with no gateway to watch —
// the gateway-dials-daemon server path — carry none).
type sessionLiveness struct {
	timing sessionTiming

	mu           sync.Mutex
	lastReceived time.Time
	// acksSupported is set by the first non-zero acknowledgement.
	acksSupported bool
	acked         int64
	// newestSent is the timestamp of the newest heartbeat this daemon sent;
	// unackedSince is when the oldest heartbeat not yet acknowledged was sent
	// (zero when every sent heartbeat is acknowledged).
	newestSent   int64
	unackedSince time.Time
}

func newSessionLiveness(timing sessionTiming, now time.Time) *sessionLiveness {
	return &sessionLiveness{timing: timing.withDefaults(), lastReceived: now}
}

// heartbeatSent records a heartbeat handed to the stream.
func (l *sessionLiveness) heartbeatSent(timestamp int64, now time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if timestamp > l.newestSent {
		l.newestSent = timestamp
	}
	if timestamp > l.acked && l.unackedSince.IsZero() {
		l.unackedSince = now
	}
}

// received records a message read from the gateway.
func (l *sessionLiveness) received(msg *reliantv1.ServerMessage, now time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lastReceived = now
	hb := msg.GetHeartbeat()
	if hb == nil {
		return
	}
	ack := hb.GetAckedDaemonHeartbeat()
	if ack <= 0 || ack <= l.acked {
		return
	}
	l.acksSupported = true
	l.acked = ack
	switch {
	case ack >= l.newestSent:
		l.unackedSince = time.Time{}
	default:
		// Heartbeats sent after the one just acknowledged are still in
		// flight. Their exact send times are not kept; restarting the clock
		// here can only make the check later, never wrongly early.
		l.unackedSince = now
	}
}

// check reports why the session should be abandoned, or nil while it is
// alive.
func (l *sessionLiveness) check(now time.Time) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if silent := now.Sub(l.lastReceived); silent > l.timing.gatewaySilenceTimeout {
		return fmt.Errorf("nothing received from the gateway for %s (it heartbeats every 15s); "+
			"treating the stream as dead", silent.Round(time.Second))
	}
	if l.acksSupported && !l.unackedSince.IsZero() {
		if waiting := now.Sub(l.unackedSince); waiting > l.timing.heartbeatAckTimeout {
			return fmt.Errorf("the gateway has not acknowledged a heartbeat sent %s ago, so it is "+
				"not reading this stream; treating the stream as dead", waiting.Round(time.Second))
		}
	}
	return nil
}
