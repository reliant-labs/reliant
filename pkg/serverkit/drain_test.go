package serverkit_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// drain_test.go — the graceful-shutdown sequence, through Run and through
// the pieces a hand-built main uses (Drainer, NewHTTPServer, Streams).

func TestDrainer_ReadinessGate(t *testing.T) {
	t.Parallel()
	d := &serverkit.Drainer{PreStopDelay: -1} // no pause: this test is about the gate
	srv := httptest.NewServer(d.ReadinessHandler(nil, 0, nil))
	defer srv.Close()

	status, body, raw := getJSON(t, srv.URL)
	if status != http.StatusServiceUnavailable || body["status"] != "not_ready" {
		t.Fatalf("before MarkReady: %d %s, want 503 not_ready", status, raw)
	}

	d.MarkReady()
	if status, _, raw := getJSON(t, srv.URL); status != http.StatusOK {
		t.Fatalf("after MarkReady: %d %s, want 200", status, raw)
	}

	d.Begin()
	status, body, raw = getJSON(t, srv.URL)
	if status != http.StatusServiceUnavailable || body["status"] != "draining" {
		t.Fatalf("after Begin: %d %s, want 503 draining", status, raw)
	}
	if d.Ready() || !d.Draining() {
		t.Fatal("a draining process is not ready")
	}
}

// A dependency check must not run once the process drains: no dependency
// can make a replica that is shutting down routable again.
func TestDrainer_DrainingShortCircuitsTheChecks(t *testing.T) {
	t.Parallel()
	d := &serverkit.Drainer{PreStopDelay: -1}
	d.MarkReady()
	called := make(chan struct{}, 1)
	h := d.ReadinessHandler([]serverkit.ReadyCheck{{Name: "database", Check: func(context.Context) error {
		called <- struct{}{}
		return nil
	}}}, time.Second, nil)
	srv := httptest.NewServer(h)
	defer srv.Close()

	d.Begin()
	if status, _, raw := getJSON(t, srv.URL); status != http.StatusServiceUnavailable {
		t.Fatalf("draining /readyz = %d %s, want 503", status, raw)
	}
	select {
	case <-called:
		t.Fatal("a dependency check ran while draining")
	default:
	}
}

func TestDrainer_BeginKeepsServingForThePreStopDelay(t *testing.T) {
	t.Parallel()
	d := &serverkit.Drainer{PreStopDelay: 150 * time.Millisecond}
	start := time.Now()
	d.Begin()
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("Begin returned after %s, before the %s pre-stop delay", elapsed, d.PreStopDelay)
	}
}

// The run context is already cancelled by the signal that starts a drain, so
// the shutdown budget must not inherit from it.
func TestDrainer_ShutdownContextIsItsOwnBudget(t *testing.T) {
	t.Parallel()
	d := &serverkit.Drainer{ShutdownTimeout: time.Minute}
	ctx, cancel := d.ShutdownContext()
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) < 50*time.Second {
		t.Fatalf("deadline = %v (set %v), want about a minute from now", deadline, ok)
	}

	var zero serverkit.Drainer
	zctx, zcancel := zero.ShutdownContext()
	defer zcancel()
	if zctx.Err() != nil {
		t.Fatal("a zero ShutdownTimeout must take the default budget, not expire at once")
	}

	// Negative is none, as for PreStopDelay: a window over as it opens.
	none := serverkit.Drainer{ShutdownTimeout: -1}
	nctx, ncancel := none.ShutdownContext()
	defer ncancel()
	if nctx.Err() == nil {
		t.Fatal("a negative ShutdownTimeout must be no shutdown window, not the default budget")
	}
}

// A negative PreStopDelay is no pause — the same rule as ShutdownTimeout.
func TestDrainer_NegativePreStopDelayIsNoPause(t *testing.T) {
	t.Parallel()
	d := &serverkit.Drainer{PreStopDelay: -1}
	start := time.Now()
	d.Begin()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Begin paused %s for a negative PreStopDelay, want no pause", elapsed)
	}
}

// serveOwnListener is a process that runs its own listener without Run: it
// builds the server with NewHTTPServer and serves it.
func serveOwnListener(t *testing.T, mux http.Handler, streams *serverkit.Streams) (*http.Server, string) {
	t.Helper()
	httpSrv := serverkit.NewHTTPServer(serverkit.Config{}, mux, streams)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = httpSrv.Serve(ln) }()
	t.Cleanup(func() { _ = httpSrv.Close() })
	return httpSrv, "http://" + ln.Addr().String()
}

// sseUntilDone is a LongLived SSE route that holds its response open until
// its context ends, then reports whether the drain ended it.
func sseUntilDone(ended chan<- bool) http.Handler {
	return serverkit.LongLived(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		http.NewResponseController(w).Flush() //nolint:errcheck // test stream
		<-r.Context().Done()
		ended <- serverkit.Ended(r.Context())
	}))
}

// Even a bare srv.Shutdown — a main that never heard of DrainHTTP — ends an
// open LongLived stream rather than waiting on it for the whole budget.
func TestNewHTTPServer_BareShutdownEndsLongLivedStreams(t *testing.T) {
	t.Parallel()
	ended := make(chan bool, 1)
	mux := http.NewServeMux()
	mux.Handle("/events", sseUntilDone(ended))
	httpSrv, base := serveOwnListener(t, mux, serverkit.NewStreams())

	body := openStream(t, base+"/events")
	defer func() { _ = body.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := httpSrv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown with an open stream = %v after %s, want nil: the stream must end, not hold the drain", err, time.Since(start))
	}
	if !<-ended {
		t.Fatal("the stream's handler did not see the drain cause")
	}
}

// DrainHTTP gives a stream that claimed the soft phase its goodbye: the soft
// signal arrives while its context is still live, and the drain finishes as
// soon as the handler returns — well before the reserve.
func TestDrainHTTP_ClaimedStreamSaysGoodbyeOnTheSoftSignal(t *testing.T) {
	t.Parallel()
	goodbye := make(chan bool, 1)
	mux := http.NewServeMux()
	mux.Handle("/events", serverkit.LongLived(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		soft := serverkit.Draining(r.Context())
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		http.NewResponseController(w).Flush() //nolint:errcheck // test stream
		<-soft
		live := r.Context().Err() == nil
		_, _ = io.WriteString(w, "event: goodbye\ndata: reconnect\n\n")
		goodbye <- live
	})))
	streams := serverkit.NewStreams()
	httpSrv, base := serveOwnListener(t, mux, streams)

	body := openStream(t, base+"/events")
	defer func() { _ = body.Close() }()
	read := make(chan string, 1)
	go func() {
		all, _ := io.ReadAll(body)
		read <- string(all)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	if err := serverkit.DrainHTTP(ctx, httpSrv, streams); err != nil {
		t.Fatalf("DrainHTTP = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("DrainHTTP took %s; a stream that said goodbye must not wait for the reserve", elapsed)
	}
	if !<-goodbye {
		t.Error("the soft signal arrived after the stream's context was already ended")
	}
	if got := <-read; !strings.Contains(got, "event: goodbye") {
		t.Errorf("client never received the goodbye event; body = %q", got)
	}
}

// openStream issues a GET whose response is a long-lived stream and returns
// once the headers have arrived, leaving the body open.
func openStream(t *testing.T, url string) io.ReadCloser {
	t.Helper()
	resp, err := http.Get(url) //nolint:noctx // the server ends this stream, not the client
	if err != nil {
		t.Fatalf("open stream %s: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("open stream %s: status %d", url, resp.StatusCode)
	}
	return resp.Body
}

// TestRun_OpenStreamDoesNotHoldShutdown is the #773 regression, for every
// forge app: a long-lived stream open at SIGTERM used to hold
// http.Server.Shutdown for the whole ShutdownTimeout, fail it with "context
// deadline exceeded" logged at ERROR, and leave the process to exit cutting
// the stream without a status. Now the stream ends as the drain begins, with
// the drain cause and a clean end of body, while a unary request already in
// flight is still drained the normal way — answered, not cut.
func TestRun_OpenStreamDoesNotHoldShutdown(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — sends SIGTERM.
	addr := freeAddr(t)
	var logs syncBuf

	streamEnded := make(chan bool, 1)
	inUnary := make(chan struct{})
	releaseUnary := make(chan struct{})

	mux := http.NewServeMux()
	mux.Handle("/events", serverkit.LongLived(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: hello\n\n")
		http.NewResponseController(w).Flush() //nolint:errcheck // test stream
		<-r.Context().Done()
		streamEnded <- serverkit.Ended(r.Context())
	})))
	mux.HandleFunc("/unary", func(w http.ResponseWriter, _ *http.Request) {
		close(inUnary)
		<-releaseUnary
		_, _ = io.WriteString(w, "done")
	})

	cfg := baseConfig(addr)
	cfg.ShutdownTimeout = 10 * time.Second
	errCh, _ := runInBackground(t, cfg, serverkit.Server{Handler: mux, Logger: bufLogger(&logs)})
	waitReady(t, addr, 2*time.Second)

	body := openStream(t, "http://"+addr+"/events")
	defer func() { _ = body.Close() }()
	streamRead := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, bufio.NewReader(body))
		streamRead <- err
	}()

	unaryCode := make(chan int, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/unary") //nolint:noctx // deliberately outlives the request ctx
		if err != nil {
			unaryCode <- 0
			return
		}
		_ = resp.Body.Close()
		unaryCode <- resp.StatusCode
	}()
	<-inUnary

	start := time.Now()
	runErr := make(chan error, 1)
	go func() { runErr <- shutdownAndWait(t, errCh, 15*time.Second) }()

	select {
	case drained := <-streamEnded:
		if !drained {
			t.Error("the stream ended, but not with the drain cause")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the open stream was not ended by the drain")
	}
	if err := <-streamRead; err != nil && !errors.Is(err, io.EOF) {
		t.Errorf("client read of the drained stream = %v, want a clean end of body", err)
	}

	// The unary request is still being served after the stream ended: the
	// drain waits for it rather than cutting it.
	time.Sleep(100 * time.Millisecond)
	close(releaseUnary)
	if code := <-unaryCode; code != http.StatusOK {
		t.Errorf("in-flight unary returned %d during the drain, want 200", code)
	}

	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("shutdown took %s with ShutdownTimeout=%s — an open stream held the drain", elapsed, cfg.ShutdownTimeout)
	}
	if out := logs.String(); strings.Contains(out, "server shutdown error") || strings.Contains(out, "level=ERROR") {
		t.Errorf("a clean drain with an open stream logged an error:\n%s", out)
	}
}
