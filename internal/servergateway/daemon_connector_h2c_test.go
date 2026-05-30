// Copyright (c) 2025 Reliant Labs

package servergateway

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
)

// testDaemonHandler mimics the real daemon's ConnectGateway implementation
// just enough to reproduce the h2c bidi handshake the gateway performs in prod.
// Specifically: on stream open, immediately send a DaemonRegister message —
// which is what the gateway blocks on in stream.Receive() upstream.
type testDaemonHandler struct {
	reliantv1connect.UnimplementedToolsDaemonServiceHandler
	// streamOpened is closed the moment the handler is entered, so the test
	// can distinguish "client never hit the handler" from "client hit the
	// handler but the SETTINGS handshake never completed end-to-end".
	streamOpened chan struct{}
}

func (h *testDaemonHandler) ConnectGateway(
	ctx context.Context,
	stream *connect.BidiStream[reliantv1.ServerMessage, reliantv1.DaemonMessage],
) error {
	close(h.streamOpened)

	register := &reliantv1.DaemonMessage{
		Message: &reliantv1.DaemonMessage_Register{Register: &reliantv1.DaemonRegister{
			Hostname:   "test-daemon",
			Platform:   "linux",
			DaemonType: "cloud",
		}},
	}
	if err := stream.Send(register); err != nil {
		return fmt.Errorf("sending registration: %w", err)
	}

	// Hold the stream open until the client cancels — mirrors the real
	// daemon's behaviour, lets the test cleanly close the client side.
	<-ctx.Done()
	return nil
}

// startTestDaemonServer spins up the daemon-side h2c server using the SAME
// pattern as internal/toolexec/daemonruntime/server.go:runServerMode —
// h2c.NewHandler wrapping the buf-generated handler, no TLS.
func startTestDaemonServer(t *testing.T) (baseURL string, opened <-chan struct{}, stop func()) {
	t.Helper()

	handler := &testDaemonHandler{streamOpened: make(chan struct{})}
	path, h := reliantv1connect.NewToolsDaemonServiceHandler(handler)

	mux := http.NewServeMux()
	mux.Handle(path, h)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := &http.Server{
		Handler:           h2c.NewHandler(mux, &http2.Server{}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = srv.Serve(listener)
	}()

	baseURL = fmt.Sprintf("http://%s", listener.Addr().String())
	stop = func() {
		_ = srv.Close()
		<-serveDone
	}
	return baseURL, handler.streamOpened, stop
}

// TestH2cBidiReceive reproduces the daemon-gateway → tools-daemon hang. It
// uses the EXACT h2cClient() from daemon_connector.go and the EXACT server
// setup pattern from daemonruntime/server.go. If the bug is in the local
// HTTP/2 client/server interaction (no SETTINGS frame sent), this test
// times out on stream.Receive() within 10s. If it passes, the bug must
// require the cluster environment (NetworkPolicy, MTU, kube-proxy, etc).
func TestH2cBidiReceive(t *testing.T) {
	baseURL, streamOpened, stop := startTestDaemonServer(t)
	defer stop()

	client := reliantv1connect.NewToolsDaemonServiceClient(
		h2cClient(),
		baseURL,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream := client.ConnectGateway(ctx)
	defer func() {
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
	}()

	// Kick the stream with an empty GatewayHello — mirrors the prod fix in
	// connectOnce. See the comment there for why an empty marker is used
	// rather than identity-bearing data.
	hello := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_Hello{Hello: &reliantv1.GatewayHello{}},
	}
	if err := stream.Send(hello); err != nil {
		t.Fatalf("stream.Send(GatewayHello): %v", err)
	}

	// Run Receive in a goroutine so we can race it against the deadline
	// and report which side hung (client never woke up vs. handler never
	// reached).
	type recvResult struct {
		msg *reliantv1.DaemonMessage
		err error
	}
	resCh := make(chan recvResult, 1)
	go func() {
		msg, err := stream.Receive()
		resCh <- recvResult{msg: msg, err: err}
	}()

	select {
	case res := <-resCh:
		if res.err != nil {
			t.Fatalf("stream.Receive() returned error: %v", res.err)
		}
		if res.msg.GetRegister() == nil {
			t.Fatalf("first message was not DaemonRegister: %+v", res.msg)
		}
		t.Logf("PASS: received DaemonRegister hostname=%s", res.msg.GetRegister().GetHostname())
	case <-ctx.Done():
		select {
		case <-streamOpened:
			t.Fatalf("REPRO: handler was entered on the server, but client.Receive() never returned within 10s — SETTINGS frame likely missed in flight: %v", ctx.Err())
		default:
			t.Fatalf("REPRO: client.Receive() hung for 10s and the server handler was never reached — h2c handshake never completed: %v", ctx.Err())
		}
	}
}
