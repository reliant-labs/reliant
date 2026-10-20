// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

func serverHeartbeat(acked int64) *reliantv1.ServerMessage {
	return &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_Heartbeat{
		Heartbeat: &reliantv1.ServerHeartbeat{Timestamp: time.Now().Unix(), AckedDaemonHeartbeat: acked},
	}}
}

var testTiming = sessionTiming{
	heartbeatInterval:     15 * time.Second,
	gatewaySilenceTimeout: 90 * time.Second,
	heartbeatAckTimeout:   90 * time.Second,
}

func TestSessionLivenessTripsWhenTheGatewayStopsAcknowledging(t *testing.T) {
	t0 := time.Date(2026, 10, 11, 11, 50, 0, 0, time.UTC)
	l := newSessionLiveness(testTiming, t0)

	// Healthy: each heartbeat is acknowledged within one gateway heartbeat.
	for i := int64(1); i <= 4; i++ {
		now := t0.Add(time.Duration(i) * 15 * time.Second)
		l.heartbeatSent(i, now)
		l.received(serverHeartbeat(i), now.Add(5*time.Second))
		require.NoError(t, l.check(now.Add(5*time.Second)))
	}

	// 11:51:30 — the gateway stops reading. Its heartbeats keep arriving,
	// still acknowledging heartbeat 4.
	stall := t0.Add(90 * time.Second)
	for i := int64(5); i <= 12; i++ {
		now := stall.Add(time.Duration(i-5) * 15 * time.Second)
		l.heartbeatSent(i, now)
		l.received(serverHeartbeat(4), now.Add(time.Second))
	}
	require.NoError(t, l.check(stall.Add(80*time.Second)), "inside the ack timeout")
	err := l.check(stall.Add(95 * time.Second))
	require.Error(t, err, "a heartbeat unacknowledged for longer than the timeout means the gateway is not reading")
	require.Contains(t, err.Error(), "not acknowledged")
}

// A gateway that predates acknowledgements always sends 0; it must never be
// mistaken for one that stopped reading.
func TestSessionLivenessIgnoresAGatewayThatNeverAcknowledges(t *testing.T) {
	t0 := time.Now()
	l := newSessionLiveness(testTiming, t0)
	for i := int64(1); i <= 40; i++ {
		now := t0.Add(time.Duration(i) * 15 * time.Second)
		l.heartbeatSent(i, now)
		l.received(serverHeartbeat(0), now)
		require.NoError(t, l.check(now))
	}
}

func TestSessionLivenessTripsWhenTheGatewayGoesSilent(t *testing.T) {
	t0 := time.Now()
	l := newSessionLiveness(testTiming, t0)
	l.received(serverHeartbeat(0), t0.Add(15*time.Second))
	require.NoError(t, l.check(t0.Add(100*time.Second)))
	err := l.check(t0.Add(106 * time.Second))
	require.Error(t, err)
	require.Contains(t, err.Error(), "nothing received from the gateway")
}

// Nil-safe: the gateway-dials-daemon path runs heartbeats with no liveness.
func TestSessionLivenessNilIsInert(t *testing.T) {
	var l *sessionLiveness
	l.heartbeatSent(1, time.Now())
	l.received(serverHeartbeat(1), time.Now())
	require.NoError(t, l.check(time.Now().Add(time.Hour)))
}

// stopsReadingGateway reproduces the prod failure end to end: it serves the
// first session normally until it has read and acknowledged one daemon
// heartbeat, then never reads that stream again — while still heartbeating to
// the daemon, as the real gateway kept doing for twenty minutes. Later
// sessions are served normally.
type stopsReadingGateway struct {
	reliantv1connect.UnimplementedToolsDaemonServiceHandler
	sessions atomic.Int32
	// neverAck makes the gateway behave like one that predates
	// acknowledgements: it reads everything and always acknowledges 0.
	neverAck bool
}

func (g *stopsReadingGateway) ConnectDaemon(ctx context.Context, stream *connect.BidiStream[reliantv1.DaemonMessage, reliantv1.ServerMessage]) error {
	session := g.sessions.Add(1)
	if _, err := stream.Receive(); err != nil {
		return err
	}
	if err := stream.Send(&reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_RegistrationAck{
		RegistrationAck: &reliantv1.RegistrationAck{Accepted: true, DaemonId: "daemon-1"},
	}}); err != nil {
		return err
	}

	var acked int64
	if g.neverAck {
		go func() {
			for {
				if _, err := stream.Receive(); err != nil {
					return
				}
			}
		}()
	} else if session == 1 {
		for acked == 0 {
			msg, err := stream.Receive()
			if err != nil {
				return err
			}
			acked = msg.GetHeartbeat().GetTimestamp()
		}
		// From here on this stream is never read again.
	} else {
		<-ctx.Done()
		return ctx.Err()
	}

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := stream.Send(serverHeartbeat(acked)); err != nil {
				return err
			}
		}
	}
}

func startGateway(t *testing.T, gw reliantv1connect.ToolsDaemonServiceHandler) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewToolsDaemonServiceHandler(gw))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.EnableHTTP2 = true
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.URL
}

var fastTiming = sessionTiming{
	heartbeatInterval:     50 * time.Millisecond,
	gatewaySilenceTimeout: 5 * time.Second,
	heartbeatAckTimeout:   400 * time.Millisecond,
	livenessCheckInterval: 20 * time.Millisecond,
}

// A gateway that stops reading the daemon's stream must not keep the daemon
// "connected" forever: the daemon notices its heartbeats going unacknowledged,
// ends the session, and redials.
func TestDaemonRedialsWhenTheGatewayStopsReadingItsStream(t *testing.T) {
	t.Parallel()
	gw := &stopsReadingGateway{}
	client := newDaemonClientForTest(t, t.TempDir(), startGateway(t, gw))
	client.timing = fastTiming

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = client.run(ctx) }()

	require.Eventually(t, func() bool { return gw.sessions.Load() >= 2 }, 8*time.Second, 20*time.Millisecond,
		"the daemon kept a stream the gateway had stopped reading; it must detect the unacknowledged "+
			"heartbeats and redial")
}

// The same daemon behind a gateway that never acknowledges (older than the
// ack field) and is otherwise healthy must keep its session.
func TestDaemonKeepsItsSessionWithAGatewayThatNeverAcknowledges(t *testing.T) {
	t.Parallel()
	gw := &stopsReadingGateway{neverAck: true}
	client := newDaemonClientForTest(t, t.TempDir(), startGateway(t, gw))
	client.timing = fastTiming

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = client.run(ctx) }()

	require.Eventually(t, func() bool { return gw.sessions.Load() == 1 }, 2*time.Second, 10*time.Millisecond)
	time.Sleep(4 * fastTiming.heartbeatAckTimeout)
	require.Equal(t, int32(1), gw.sessions.Load(), "a gateway that never acknowledges must not be treated as dead")
}
