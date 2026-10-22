// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// A daemon stream's goroutines must be done with it before its owner lets it
// go. Connect forbids using a stream once its handler has returned, and it is
// not a formality: otelconnect finishes the stream's span state right after
// the handler, so a reader still inside Receive raced it (seen under -race in
// internal/grpc's daemon credential e2e tests, about one run in fifteen).
// handleIncoming used to return on ctx or done while its reader sat in
// Receive, on a daemon that was still connected and silent — what a healthy
// idle daemon is between heartbeats.

// handleIncomingUntil runs handleIncoming on conn, parks its reader inside
// Receive, ends it with end, and returns once handleIncoming has.
func handleIncomingUntil(t *testing.T, svc *Service, conn *daemonConnection, stream *parkedStream, end func(cancel context.CancelFunc)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		_ = svc.handleIncoming(ctx, conn)
	}()
	require.Eventually(t, func() bool { return stream.receiving.Load() == 1 }, 3*time.Second, time.Millisecond,
		"the reader never parked in Receive")

	end(cancel)

	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("handleIncoming did not return")
	}
}

func TestHandleIncomingReturnsOnlyAfterItsReaderLeftReceive(t *testing.T) {
	for name, end := range map[string]func(*Service, *daemonConnection, context.CancelFunc){
		// The stream's context ends: the listener drain's hard phase, or the
		// daemon going away while the reader is between reads.
		"context ends": func(_ *Service, _ *daemonConnection, cancel context.CancelFunc) { cancel() },
		// The sweeper or a shutdown tears the connection down.
		"torn down": func(svc *Service, conn *daemonConnection, _ context.CancelFunc) {
			svc.teardownConnection(conn, "test")
		},
		// Another daemon process claimed the daemon id.
		"superseded": func(svc *Service, conn *daemonConnection, _ context.CancelFunc) { svc.supersedeIncumbent(conn) },
	} {
		t.Run(name, func(t *testing.T) {
			svc := newDaemonlessService(t)
			stream := newParkedStream()
			conn := newTestConn("user-1", uuid.NewString(), stream)

			handleIncomingUntil(t, svc, conn, stream, func(cancel context.CancelFunc) { end(svc, conn, cancel) })

			require.Zero(t, stream.receiving.Load(),
				"handleIncoming returned with its reader still inside Receive: the stream is used after its handler returned")
		})
	}
}

// blockingSendStream is a parked stream whose Send blocks until released: a
// daemon that has stopped draining what it is sent.
type blockingSendStream struct {
	*parkedStream
	sending atomic.Int32
	release chan struct{}
}

func (s *blockingSendStream) Send(*reliantv1.ServerMessage) error {
	s.sending.Add(1)
	defer s.sending.Add(-1)
	<-s.release
	return nil
}

func TestReleaseStreamWaitsForTheSendInFlight(t *testing.T) {
	svc := newDaemonlessService(t)
	stream := &blockingSendStream{parkedStream: newParkedStream(), release: make(chan struct{})}
	conn := newTestConn("user-1", uuid.NewString(), stream)
	svc.startSender(conn)
	conn.sendCh <- &reliantv1.ServerMessage{}
	require.Eventually(t, func() bool { return stream.sending.Load() == 1 }, 3*time.Second, time.Millisecond)

	released := make(chan struct{})
	go func() {
		defer close(released)
		conn.releaseStream()
	}()
	select {
	case <-released:
		t.Fatal("releaseStream returned while a Send was still using the stream")
	case <-time.After(100 * time.Millisecond):
	}

	close(stream.release) // the write-stall guard's job in production
	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatal("releaseStream did not return once the Send finished")
	}
	require.Zero(t, stream.sending.Load())
}

// ConnectDaemon can stop its reader only through the abortable request body a
// serverkit listener serves (serverkit.AbortReceive), so a server built any
// other way refuses the stream rather than one day hanging on, or racing, a
// parked Receive.
func TestConnectDaemonRefusesAServerWithoutAbortableBodies(t *testing.T) {
	svc := newDaemonlessService(t)
	path, h := reliantv1connect.NewToolsDaemonServiceHandler(svc)
	mux := http.NewServeMux()
	mux.Handle(path, h)
	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.Start()
	t.Cleanup(srv.Close)

	cl := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream := reliantv1connect.NewToolsDaemonServiceClient(cl, srv.URL, connect.WithGRPC()).ConnectDaemon(ctx)
	defer func() { _ = stream.CloseRequest(); _ = stream.CloseResponse() }()
	_ = stream.Send(&reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_Register{
		Register: &reliantv1.DaemonRegister{Hostname: "h", Platform: "linux", DaemonType: "local"},
	}})
	_, err := stream.Receive()
	require.Equal(t, connect.CodeInternal, svcerr.Code(err))
}
