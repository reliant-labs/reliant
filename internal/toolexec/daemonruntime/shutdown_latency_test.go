// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

// cancelUnblockGrace bounds how long a cancelled daemon may take to stop.
//
// The budget is a product requirement, not a round number: Electron SIGTERMs
// the daemon and waits for it before respawning one under the real principal
// (restartBackendForAuthPrincipalChange), and that whole restart has to fit in
// under a second for sign-in to feel instant. Respawn itself was measured at
// ~440ms, so the old daemon's exit gets the rest. 500ms is generous for work
// that should be a single stream teardown.
const cancelUnblockGrace = 500 * time.Millisecond

// silentGateway accepts the daemon's registration, acknowledges it, and then
// says nothing for the life of the stream.
//
// This is the whole bug's precondition, and it is the NORMAL state, not an
// exotic one: a daemon parked at awaiting_credentials has no work, so the
// gateway has nothing to send it. The stream is healthy and idle.
type silentGateway struct {
	reliantv1connect.UnimplementedToolsDaemonServiceHandler
}

func (g *silentGateway) ConnectDaemon(ctx context.Context, stream *connect.BidiStream[reliantv1.DaemonMessage, reliantv1.ServerMessage]) error {
	if _, err := stream.Receive(); err != nil {
		return err
	}
	if err := stream.Send(&reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_RegistrationAck{
			RegistrationAck: &reliantv1.RegistrationAck{Accepted: true, DaemonId: "daemon-1"},
		},
	}); err != nil {
		return err
	}
	<-ctx.Done() // hold the stream open, silently
	return ctx.Err()
}

// startSilentGateway runs a real h2c Connect gateway that acks and then goes
// quiet, and returns a daemon client pointed at it.
func startSilentGateway(t *testing.T) (client *daemonClient, dataDir string) {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewToolsDaemonServiceHandler(&silentGateway{}))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.EnableHTTP2 = true
	srv.Start()
	t.Cleanup(srv.Close)

	dataDir = t.TempDir()
	return newDaemonClientForTest(t, dataDir, srv.URL), dataDir
}

// Cancelling a connected daemon must stop it promptly, even when the gateway
// is idle and sends nothing.
//
// This is the SIGTERM path. Electron signals the daemon and waits for the
// process before respawning; anything spent here is dead time the user watches
// after clicking sign in.
//
// The failure it pins is not a slow path, it is an unbounded one. runSession's
// stream.Receive() bottoms out in http2's transportResponseBody.Read — a
// sync.Cond wait on the stream pipe, woken only by bytes from the peer or by
// the body being closed. Context cancellation is neither, and connect only
// checks ctx.Err() *before* entering a read, which cannot help a read that is
// already parked. Against a silent peer nothing ever arrives, so without an
// explicit CloseResponse this test does not merely exceed its budget: it hangs
// until the test timeout. Measured against a real dev gateway the transport
// eventually noticed at 4.54s; measured here it never did.
func TestCancelStopsAnIdleDaemonPromptly(t *testing.T) {
	client, dataDir := startSilentGateway(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stopped := make(chan error, 1)
	go func() { stopped <- client.run(ctx) }()

	// Cancel only once the gateway has acked, so this measures teardown of an
	// ESTABLISHED idle stream — the state the daemon is actually in when
	// SIGTERM lands — rather than racing the dial.
	waitForStream(t, dataDir, daemonstate.StreamConnected)

	start := time.Now()
	cancel()

	select {
	case err := <-stopped:
		elapsed := time.Since(start)
		require.ErrorIs(t, err, context.Canceled,
			"a cancelled daemon must report cancellation, not a stream fault")
		require.Less(t, elapsed, cancelUnblockGrace,
			"a cancelled daemon took %v to stop against an idle gateway; SIGTERM-to-exit must fit in the sign-in restart budget", elapsed)
		t.Logf("cancelled daemon stopped in %v", elapsed)
	case <-time.After(10 * time.Second):
		t.Fatalf("a cancelled daemon never stopped against an idle gateway: stream.Receive() ignores "+
			"ctx cancellation, so nothing wakes it until the peer speaks (budget was %v)", cancelUnblockGrace)
	}
}

// Shutdown must be quiet. run() and Start() both suppress context.Canceled, so
// a normal stop is only silent if runSession actually reports cancellation —
// and it cannot report what Receive returns, because the error there comes
// from the daemon closing its own read side, which looks like a torn stream.
//
// Getting this wrong is not cosmetic: recordStream would mark the daemon
// DISCONNECTED with a transport error on every clean exit, so `reliant daemon
// status` and the logs would show a fault where a user pressed quit.
func TestCancelledSessionReportsCancellationNotAStreamFault(t *testing.T) {
	client, dataDir := startSilentGateway(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sessionErr := make(chan error, 1)
	go func() { sessionErr <- client.runSession(ctx) }()

	waitForStream(t, dataDir, daemonstate.StreamConnected)
	cancel()

	select {
	case err := <-sessionErr:
		require.ErrorIs(t, err, context.Canceled,
			"cancellation must not surface as a stream error, or every clean shutdown logs a fault and records the stream disconnected")
	case <-time.After(10 * time.Second):
		t.Fatal("runSession never returned after cancel")
	}
}

// The cancel path must not become the only path. A stream that dies on its own
// mid-session — a gateway restart, a reaped connection — must still be
// classified as a real error so run() reconnects with backoff.
//
// This is the regression the shutdown fix could plausibly cause: suppressing
// too much, and turning a genuine disconnect into a silent stop.
func TestStreamLossWithoutCancelIsStillAnError(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewToolsDaemonServiceHandler(&hangUpGateway{}))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.EnableHTTP2 = true
	srv.Start()
	defer srv.Close()

	client := newDaemonClientForTest(t, t.TempDir(), srv.URL)

	// Live context: the daemon is not shutting down, the gateway simply ends
	// the stream after acking.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sessionErr := make(chan error, 1)
	go func() { sessionErr <- client.runSession(ctx) }()

	select {
	case err := <-sessionErr:
		require.Error(t, err, "a stream that ends on its own is a fault the reconnect loop must see")
		require.False(t, errors.Is(err, context.Canceled),
			"an uncancelled daemon must not report cancellation — run() would stop instead of reconnecting")
		require.False(t, isFatalError(err),
			"an ordinary stream loss must stay recoverable")
	case <-time.After(10 * time.Second):
		t.Fatal("runSession never returned after the gateway ended the stream")
	}
}
