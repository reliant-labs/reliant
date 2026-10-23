package serverkit_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// teardown_order_test.go — shutdown runs in dependency order.
//
// OnShutdown is where the serving *sql.DB gets closed, and it used to run
// BEFORE http.Server.Shutdown. An in-flight request that was mid-query when
// SIGTERM arrived therefore had its connection pool closed underneath it and
// failed during the very drain window whose purpose is to let it finish.
// Meanwhile the telemetry flush ran before the drain too, so the records
// describing that failure were discarded.

// TestRun_InFlightRequestCompletesBeforeOnShutdown is the ordering guard: a
// request already executing when SIGTERM lands must observe that its
// dependencies are still open, and OnShutdown must not have run yet.
func TestRun_InFlightRequestCompletesBeforeOnShutdown(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — sends SIGTERM.
	addr := freeAddr(t)

	var (
		mu               sync.Mutex
		shutdownRan      bool
		shutdownRanFirst bool
	)
	inHandler := make(chan struct{})
	releaseHandler := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(inHandler)
		<-releaseHandler
		// The question the drain window exists to answer: were my
		// dependencies still there when I finished?
		mu.Lock()
		shutdownRanFirst = shutdownRan
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	srv := serverkit.Server{
		Handler: mux,
		OnShutdown: func(context.Context) error {
			mu.Lock()
			shutdownRan = true
			mu.Unlock()
			return nil
		},
	}

	cfg := baseConfig(addr)
	cfg.ShutdownTimeout = 5 * time.Second
	errCh, _ := runInBackground(t, cfg, srv)
	waitReady(t, addr, 2*time.Second)

	respCh := make(chan int, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/slow") //nolint:noctx // deliberately outlives the request ctx
		if err != nil {
			respCh <- 0
			return
		}
		_ = resp.Body.Close()
		respCh <- resp.StatusCode
	}()

	<-inHandler // the request is executing

	// SIGTERM now, with the request still in the handler.
	sigCh := make(chan error, 1)
	go func() { sigCh <- shutdownAndWait(t, errCh, 15*time.Second) }()

	// Let shutdown get past the readiness flip and the pre-stop delay so the
	// teardown sequence is genuinely underway before the handler returns.
	time.Sleep(200 * time.Millisecond)
	close(releaseHandler)

	select {
	case code := <-respCh:
		if code != http.StatusOK {
			t.Errorf("in-flight request returned %d during the drain window, want 200", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("in-flight request never completed")
	}

	mu.Lock()
	ranFirst := shutdownRanFirst
	mu.Unlock()
	if ranFirst {
		t.Error("OnShutdown ran while a request was still executing — the teardown that closes the DB pool must come AFTER the HTTP drain")
	}

	if err := <-sigCh; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !shutdownRan {
		t.Error("OnShutdown never ran")
	}
}

// wedgedWorker ignores its cancelled context, which is the only way to prove
// the drain wait is bounded.
type wedgedWorker struct {
	name    string
	release chan struct{}
}

func (w *wedgedWorker) Name() string { return w.name }
func (w *wedgedWorker) Start(context.Context) error {
	<-w.release
	return nil
}
func (w *wedgedWorker) Stop(context.Context) error { return nil }

// TestRun_WedgedWorkerCannotOutlastShutdownTimeout pins the budget.
// sync.WaitGroup.Wait takes no context, so a bare Wait sat entirely outside
// ShutdownTimeout: one worker ignoring cancellation pinned the process until
// the platform SIGKILLed it, skipping every remaining teardown step.
func TestRun_WedgedWorkerCannotOutlastShutdownTimeout(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — sends SIGTERM.
	addr := freeAddr(t)

	w := &wedgedWorker{name: "wedged", release: make(chan struct{})}
	defer close(w.release) // let the goroutine exit once the test is done

	srv := serverkit.Server{Handler: emptyHandler()}
	srv.AddWorker(w)

	cfg := baseConfig(addr)
	cfg.ShutdownTimeout = 300 * time.Millisecond
	errCh, _ := runInBackground(t, cfg, srv)
	waitReady(t, addr, 2*time.Second)

	start := time.Now()
	if err := shutdownAndWait(t, errCh, 5*time.Second); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	elapsed := time.Since(start)

	// PreStopDelay (10ms) + ShutdownTimeout (300ms) plus slack. Without the
	// bound this never returns at all.
	if elapsed > 3*time.Second {
		t.Errorf("shutdown took %s with ShutdownTimeout=%s — a wedged worker is holding the process past its budget",
			elapsed, cfg.ShutdownTimeout)
	}
}
