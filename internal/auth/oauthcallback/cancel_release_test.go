package oauthcallback

import (
	"context"
	"net"
	"testing"
	"time"
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

	// Poll until the port is bindable again.
	var freed time.Duration
	for i := 0; i < 200; i++ {
		ln, err := net.Listen("tcp", listenAddr(cfg))
		if err == nil {
			freed = time.Since(start)
			_ = ln.Close()
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if freed == 0 {
		t.Fatal("port never became bindable after cancel")
	}
	t.Logf("port bindable %v after cancel", freed)
	if freed > 1*time.Second {
		t.Errorf("port held %v after cancel — a user clicking Connect again hits EADDRINUSE", freed)
	}
}
