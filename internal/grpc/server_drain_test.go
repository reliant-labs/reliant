// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// The defect these pin: every prod rollout logged "Error stopping gRPC
// server: context deadline exceeded" from both api-server replicas.
//
// Stop is http.Server.Shutdown, which waits for every active request to
// finish. StreamUserUpdates — one per open browser tab — never finishes on
// its own, so Shutdown waited out the whole SHUTDOWN_TIMEOUT, failed, and the
// process then exited with the streams still open, cutting them with no
// status at all.
//
// They drive the real server: NewServer's mux and interceptor chain, the real
// StreamingService handler, over prior-knowledge HTTP/2 as in production.

const drainTestAPIKey = "drain-test-api-key"

// drainTestRepo answers the one query StreamUserUpdates makes for a client
// with nothing to catch up on. Any other query panics on the nil embedded
// Repository, which is how a test of this size says "not expected here".
type drainTestRepo struct{ db.Repository }

func (drainTestRepo) GetLatestUserUpdateSequence(context.Context, string) (int64, error) {
	return 0, nil
}

// drainTestAuthority stands in for the token authority, which nothing in
// these tests reaches: the API-key credential is checked by the auth
// interceptor's own validator.
type drainTestAuthority struct{ tokenauthority.Authority }

// startDrainTestServer builds the API server with NewServer and serves it on a
// loopback port, returning the server and its base URL.
func startDrainTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	t.Setenv("AUTH_MODE", "apikey")
	t.Setenv("AUTH_API_KEY", drainTestAPIKey)
	t.Setenv("RELIANT_AUTHORIZATION_SERVER", "")

	srv, err := NewServer(&Config{Database: drainTestRepo{}, TokenAuthority: drainTestAuthority{}})
	require.NoError(t, err)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	served := make(chan error, 1)
	go func() { served <- srv.server.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.server.Close()
		<-served
	})
	return srv, "http://" + ln.Addr().String()
}

// h2cClient speaks prior-knowledge HTTP/2, as the load balancer does to the pod.
func h2cClient() *http.Client {
	return &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
}

// openUserStream opens StreamUserUpdates and waits for its first event, so
// the handler is parked in its event loop when the test proceeds.
func openUserStream(t *testing.T, baseURL string) *connect.ServerStreamForClient[reliantv1.UserStreamEvent] {
	t.Helper()
	client := reliantv1connect.NewStreamingServiceClient(h2cClient(), baseURL)
	req := connect.NewRequest(&reliantv1.StreamUserUpdatesRequest{})
	req.Header().Set("Authorization", "Bearer "+drainTestAPIKey)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream, err := client.StreamUserUpdates(ctx, req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	require.True(t, stream.Receive(), "stream ended before its sync event: %v", stream.Err())
	require.NotNil(t, stream.Msg().GetSync(), "the first event is the sync")
	return stream
}

// streamEnd reports how a stream ended: the error Receive stopped with (nil
// for a clean end of stream), or a timeout if it is still open.
func streamEnd(stream *connect.ServerStreamForClient[reliantv1.UserStreamEvent], within time.Duration) error {
	ended := make(chan error, 1)
	go func() {
		for stream.Receive() {
		}
		ended <- stream.Err()
	}()
	select {
	case err := <-ended:
		return err
	case <-time.After(within):
		return errStreamStillOpen
	}
}

var errStreamStillOpen = errors.New("the stream is still open")

func TestStopEndsOpenStreamsAndReturnsPromptly(t *testing.T) {
	srv, baseURL := startDrainTestServer(t)
	first := openUserStream(t, baseURL)
	second := openUserStream(t, baseURL)

	// A budget well above what a drain needs and well below the test
	// timeout: before the fix Stop spent all of it.
	const budget = 3 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	start := time.Now()
	err := srv.Stop(ctx)
	elapsed := time.Since(start)

	require.NoError(t, err, "Stop waited out its deadline on the open streams")
	require.Less(t, elapsed, budget/2, "Stop must not wait on a stream that cannot finish by itself")

	// Ended with OK: the end of a watch, which the web client answers by
	// reconnecting from its cursor (web/src/api/streaming-grpc.ts, "Stream
	// ended normally"). Not Unavailable, which it treats as the network
	// under every read on the connection dying.
	require.NoError(t, streamEnd(first, time.Second), "the stream must end cleanly, not be cut or left open")
	require.NoError(t, streamEnd(second, time.Second), "the stream must end cleanly, not be cut or left open")
}

// A unary request in flight when Stop begins is drained, not cut: only
// long-lived streams are ended early.
func TestStopStillDrainsUnaryRequests(t *testing.T) {
	srv, baseURL := startDrainTestServer(t)

	slow := make(chan struct{})
	release := make(chan struct{})
	srv.Mux().Handle("/slow", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(slow)
		<-release
		select {
		case <-r.Context().Done():
			http.Error(w, "cancelled", http.StatusServiceUnavailable)
		default:
			_, _ = w.Write([]byte("done"))
		}
	}))

	result := make(chan int, 1)
	go func() {
		resp, err := h2cClient().Get(baseURL + "/slow")
		if err != nil {
			result <- -1
			return
		}
		_ = resp.Body.Close()
		result <- resp.StatusCode
	}()
	<-slow

	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stopped <- srv.Stop(ctx)
	}()

	select {
	case err := <-stopped:
		t.Fatalf("Stop returned (%v) while a unary request was still in flight", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	require.Equal(t, http.StatusOK, <-result, "the in-flight request must complete, uncancelled")
	require.NoError(t, <-stopped)
}
