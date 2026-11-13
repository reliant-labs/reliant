package oauthcallback

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// Measure the thing that actually bit the user: how long after CANCEL the
// fixed port stays unbindable.
func TestPortFreedPromptlyAfterCancel(t *testing.T) {
	cfg := codexShapedConfig(t)
	orig := openBrowser
	// The "browser" holds a connection open at the consent screen. It is
	// handed over on a channel: openBrowser runs on RunWithConfig's goroutine.
	held := make(chan net.Conn, 1)
	openBrowser = func(string) error {
		c, err := net.Dial("tcp", listenAddr(cfg))
		if err != nil {
			close(held)
			return nil
		}
		held <- c
		return nil
	}
	defer func() { openBrowser = orig }()
	// Hold the drain open for the whole test: the held connection keeps
	// Shutdown waiting until this expires or the connection closes, which the
	// deferred c.Close below does once the port has been checked. (net/http
	// counts a connection that never sent a byte idle after 5s, so the drain
	// lasts at least that long: the port check below has 5s, not 2s.)
	prevBudget := shutdownBudget
	shutdownBudget = time.Minute
	defer func() { shutdownBudget = prevBudget }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = RunWithConfig(ctx, codexAuthorizeTemplate, cfg)
	}()
	defer func() { cancel(); <-done }()
	select {
	case c, ok := <-held:
		if !ok {
			t.Fatal("the browser could not connect to the callback listener")
		}
		defer c.Close()
	case <-time.After(10 * time.Second):
		t.Fatal("RunWithConfig never opened the browser")
	}

	start := time.Now()
	cancel()

	// The port must come free while the graceful drain is still running, not
	// when it ends: the drain waits on the browser's held connection, and a
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
}
