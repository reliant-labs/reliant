package oauthcallback

import (
	"context"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// heldPath is the request the test's "browser" keeps in flight.
const heldPath = "/__test_held"

// Measure the thing that actually bit the user: after CANCEL, the fixed port
// must be bindable again while the graceful drain is still running.
func TestPortFreedPromptlyAfterCancel(t *testing.T) {
	cfg := codexShapedConfig(t)

	// The "browser" holds a request in flight at the consent screen. The
	// request is held inside the server's handler chain, so the drain is
	// provably running on an accepted, active request (not on a raw
	// connection Serve may not have accepted yet, nor on net/http's
	// five-second grace for a connection that never sent a byte).
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	prevWrap := wrapHandler
	wrapHandler = func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != heldPath {
				h.ServeHTTP(w, r)
				return
			}
			close(entered)
			<-release
		})
	}
	defer func() { wrapHandler = prevWrap }()

	browserDone := make(chan error, 1)
	orig := openBrowser
	openBrowser = func(string) error {
		go func() {
			resp, err := http.Get("http://" + listenAddr(cfg) + heldPath)
			if err == nil {
				_ = resp.Body.Close()
			}
			browserDone <- err
		}()
		return nil
	}
	defer func() { openBrowser = orig }()
	// The drain must outlast the checks below on a loaded machine; the held
	// request ends it explicitly.
	prevBudget := shutdownBudget
	shutdownBudget = time.Minute
	defer func() { shutdownBudget = prevBudget }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = RunWithConfig(ctx, codexAuthorizeTemplate, cfg)
	}()
	defer func() { unblock(); cancel(); <-done }()

	testkit.Receive(t, "the browser's request reaches the callback server's handler", entered)

	start := time.Now()
	cancel()

	// The port must come free while the graceful drain is still running, not
	// when it ends: the drain waits on the browser's in-flight request, and a
	// user clicking Connect again inside it hits EADDRINUSE.
	testkit.Eventually(t, "the callback port is bindable again after cancel", func() bool {
		ln, err := net.Listen("tcp", listenAddr(cfg))
		if err != nil {
			return false
		}
		_ = ln.Close()
		return true
	})
	t.Logf("port bindable %v after cancel", time.Since(start))
	select {
	case <-done:
		t.Error("the port came free only once the shutdown drain ended — a user clicking Connect again during it hits EADDRINUSE")
	default:
	}

	// Let the held request finish: the drain then ends and RunWithConfig
	// returns.
	unblock()
	if err := testkit.Receive(t, "the browser's held request completes", browserDone); err != nil {
		t.Errorf("the held request failed once released: %v", err)
	}
	testkit.Receive(t, "RunWithConfig returns once the drain ends", done)
}
