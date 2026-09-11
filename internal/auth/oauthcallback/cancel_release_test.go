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
	orig := openBrowser
	var held net.Conn
	openBrowser = func(string) error {
		if c, err := net.Dial("tcp", "127.0.0.1:1455"); err == nil {
			held = c
		}
		return nil
	}
	defer func() {
		openBrowser = orig
		if held != nil {
			_ = held.Close()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _, _ = Run(ctx, "https://auth.openai.com/authorize?redirect_uri={redirect_uri}") }()
	time.Sleep(300 * time.Millisecond)

	start := time.Now()
	cancel()

	// Poll until the port is bindable again.
	var freed time.Duration
	for i := 0; i < 200; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:1455")
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
