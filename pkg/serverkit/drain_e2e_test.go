package serverkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/serverkit"
)

// TestRun_RolloutWithOpenStreamsAndAWebsocket is the whole rollout, end to
// end, through serverkit.Run and the default observe chain: SIGTERM arrives
// while a browser holds a Connect watch, a daemon holds a stream it redials,
// a terminal websocket is open, an SSE feed is open and a unary call is in
// flight. Before the drain work every one of the long-lived ones held
// http.Server.Shutdown for the whole SHUTDOWN_TIMEOUT, the process logged
// "server shutdown error" at ERROR and then cut them all without a status.
//
// Now, inside the budget: the watch ends OK, marked drained; the daemon's
// stream ends Unavailable (RedialOnDrain); the websocket closes 1012 and its
// cleanup runs; the SSE feed ends cleanly; the unary call is answered; and
// nothing is logged at ERROR.
func TestRun_RolloutWithOpenStreamsAndAWebsocket(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — sends SIGTERM.
	addr := freeAddr(t)
	base := "http://" + addr
	var logs syncBuf
	logger := slog.New(observe.NewErrorClassHandler(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

	chain := connect.WithInterceptors(observe.Chain(observe.Deps{
		Logger: logger,
		Extras: []connect.Interceptor{serverkit.StreamDrainInterceptor()},
	})...)
	mux := http.NewServeMux()
	for _, procedure := range []string{updatesProcedure, daemonProcedure} {
		mux.Handle(procedure, connect.NewServerStreamHandler(procedure, blockUntilDone, chain))
	}
	inUnary, releaseUnary := make(chan struct{}), make(chan struct{})
	mux.Handle(unaryProcedure, connect.NewUnaryHandler(unaryProcedure,
		func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[wrapperspb.StringValue], error) {
			close(inUnary)
			<-releaseUnary
			return connect.NewResponse(wrapperspb.String("answered")), nil
		}, chain))
	var terminalCleanedUp atomic.Bool
	mux.Handle("/terminal", serverkit.LongLived(terminalHandler(&terminalCleanedUp)))
	mux.Handle("/events", serverkit.LongLived(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: hello\n\n")
		http.NewResponseController(w).Flush() //nolint:errcheck // test stream
		<-r.Context().Done()
	})))

	cfg := baseConfig(addr)
	cfg.ShutdownTimeout = 20 * time.Second
	cfg.RedialOnDrain = []string{daemonService}
	errCh, _ := runInBackground(t, cfg, serverkit.Server{Handler: mux, Logger: logger})
	waitReady(t, addr, 5*time.Second)

	// Everything a live replica is holding when the rollout reaches it.
	watch := openWatch(t, base, updatesProcedure)
	daemon := openWatch(t, base, daemonProcedure)
	wsConn, wsReader := wsDial(t, base+"/terminal")
	sse := openStream(t, base+"/events")
	defer func() { _ = sse.Close() }()
	unaryErr := make(chan error, 1)
	go func() {
		client := connect.NewClient[emptypb.Empty, wrapperspb.StringValue](http.DefaultClient, base+unaryProcedure)
		_, err := client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{}))
		unaryErr <- err
	}()
	<-inUnary

	type outcome struct {
		err     error
		trailer string
	}
	read := func(stream *connect.ServerStreamForClient[wrapperspb.StringValue]) <-chan outcome {
		ch := make(chan outcome, 1)
		go func() {
			for stream.Receive() {
			}
			ch <- outcome{stream.Err(), stream.ResponseTrailer().Get(serverkit.DrainedTrailer)}
			_ = stream.Close()
		}()
		return ch
	}
	watchEnd, daemonEnd := read(watch), read(daemon)
	sseEnd := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, sse)
		sseEnd <- err
	}()
	wsEnd := make(chan uint16, 1)
	go func() {
		code, _, err := wsReadClose(wsReader)
		if err != nil {
			code = 0
		}
		_ = wsConn.Close()
		wsEnd <- code
	}()

	start := time.Now()
	runErr := make(chan error, 1)
	go func() { runErr <- shutdownAndWait(t, errCh, 30*time.Second) }()

	select {
	case o := <-watchEnd:
		if o.err != nil || o.trailer != "true" {
			t.Errorf("watch ended with %v (trailer %q), want OK and %s: true", o.err, o.trailer, serverkit.DrainedTrailer)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the watch was not ended by the drain")
	}
	select {
	case o := <-daemonEnd:
		if connect.CodeOf(o.err) != connect.CodeUnavailable || o.trailer != "true" {
			t.Errorf("daemon stream ended with %v (trailer %q), want Unavailable, marked drained", o.err, o.trailer)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon stream was not ended by the drain")
	}
	select {
	case code := <-wsEnd:
		if code != closeServiceRestart {
			t.Errorf("websocket close code = %d, want %d (service restart)", code, closeServiceRestart)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the websocket was not closed by the drain")
	}
	select {
	case err := <-sseEnd:
		if err != nil && !errors.Is(err, io.EOF) {
			t.Errorf("SSE feed ended with %v, want a clean end of body", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the SSE feed was not ended by the drain")
	}

	close(releaseUnary)
	if err := <-unaryErr; err != nil {
		t.Errorf("the unary call in flight during the rollout = %v, want answered", err)
	}
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("shutdown took %s of a %s budget — something held the drain", elapsed, cfg.ShutdownTimeout)
	}
	if !terminalCleanedUp.Load() {
		t.Error("the terminal websocket's deferred cleanup did not run")
	}

	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec["level"] == "ERROR" {
			t.Errorf("a clean rollout logged at ERROR: %s", line)
		}
		if rec["msg"] == "stream failed" && rec["procedure"] == daemonProcedure && rec["expected"] != true {
			t.Errorf("the daemon stream's drain was not logged as an expected outcome: %s", line)
		}
	}
}
