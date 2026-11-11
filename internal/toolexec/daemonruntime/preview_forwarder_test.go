// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// startForwarder boots a PreviewForwarder on an ephemeral loopback port and
// returns its base URL. Shut down via t.Cleanup.
func startForwarder(t *testing.T, reserved ...int) string {
	t.Helper()
	f := NewPreviewForwarder(reserved...)
	if err := f.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = f.Shutdown(ctx)
	})
	return "http://" + f.Addr().String()
}

// upstreamPort extracts the port an httptest server bound.
func upstreamPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("parsing upstream URL %q: %v", srv.URL, err)
	}
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	return port
}

func TestPreviewForwarder_ProxiesAndStripsPortSegment(t *testing.T) {
	var gotPath, gotHost, gotXFH, gotXFP string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHost = r.Host
		gotXFH = r.Header.Get("X-Forwarded-Host")
		gotXFP = r.Header.Get("X-Forwarded-Proto")
		if r.Header.Get("X-Reliant-Test") != "1" {
			t.Error("expected pass-through request header")
		}
		fmt.Fprint(w, "hello from upstream")
	}))
	defer upstream.Close()
	port := upstreamPort(t, upstream)

	base := startForwarder(t)
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/%d/some/path?q=1", base, port), nil)
	req.Header.Set("X-Reliant-Test", "1")
	// Simulate the workspace-proxy's edge-set forwarding headers.
	req.Header.Set("X-Forwarded-Host", "5174-abc.workspaces.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if string(body) != "hello from upstream" {
		t.Errorf("body = %q", body)
	}
	if gotPath != "/some/path" {
		t.Errorf("upstream path = %q, want /some/path (port segment stripped)", gotPath)
	}
	if wantHost := fmt.Sprintf("localhost:%d", port); gotHost != wantHost {
		t.Errorf("upstream Host = %q, want %q", gotHost, wantHost)
	}
	if gotXFH != "5174-abc.workspaces.example.com" {
		t.Errorf("X-Forwarded-Host = %q, want the edge value preserved", gotXFH)
	}
	if gotXFP != "https" {
		t.Errorf("X-Forwarded-Proto = %q, want https preserved", gotXFP)
	}
}

func TestPreviewForwarder_BarePortMapsToRoot(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
	}))
	defer upstream.Close()
	port := upstreamPort(t, upstream)

	base := startForwarder(t)
	resp, err := http.Get(fmt.Sprintf("%s/%d", base, port))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if gotPath != "/" {
		t.Errorf("upstream path = %q, want /", gotPath)
	}
}

func TestPreviewForwarder_BadPathAndReservedPort(t *testing.T) {
	base := startForwarder(t, 9190)

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/", http.StatusBadRequest},
		{"/notaport/x", http.StatusBadRequest},
		{"/99999/x", http.StatusBadRequest},
		{"/9190/", http.StatusForbidden}, // daemon RPC port refused
	} {
		resp, err := http.Get(base + tc.path)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("GET %s: status = %d, want %d", tc.path, resp.StatusCode, tc.want)
		}
	}
}

func TestPreviewForwarder_WebSocketUpgrade(t *testing.T) {
	upgrader := websocket.Upgrader{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upstream upgrade: %v", err)
			return
		}
		defer c.Close()
		mt, msg, err := c.ReadMessage()
		if err != nil {
			return
		}
		_ = c.WriteMessage(mt, append([]byte("echo: "), msg...))
	}))
	defer upstream.Close()
	port := upstreamPort(t, upstream)

	base := startForwarder(t)
	wsURL := strings.Replace(fmt.Sprintf("%s/%d/hmr", base, port), "http://", "ws://", 1)
	c, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("websocket dial through forwarder: %v (status %d)", err, status)
	}
	defer c.Close()

	if err := c.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(msg) != "echo: ping" {
		t.Errorf("ws round trip = %q, want %q", msg, "echo: ping")
	}
}

func TestPreviewForwarder_SSEStreamsUnbuffered(t *testing.T) {
	firstEventSent := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprint(w, "data: first\n\n")
		fl.Flush()
		close(firstEventSent)
		<-release // hold the stream open until the client saw the first event
		fmt.Fprint(w, "data: second\n\n")
		fl.Flush()
	}))
	defer upstream.Close()
	defer close(release)
	port := upstreamPort(t, upstream)

	base := startForwarder(t)
	resp, err := http.Get(fmt.Sprintf("%s/%d/events", base, port))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	<-firstEventSent
	// The first event must arrive while the upstream response is still open —
	// i.e. the forwarder streams instead of buffering the whole body.
	reader := bufio.NewReader(resp.Body)
	type lineResult struct {
		line string
		err  error
	}
	lineCh := make(chan lineResult, 1)
	go func() {
		line, err := reader.ReadString('\n')
		lineCh <- lineResult{line, err}
	}()
	select {
	case res := <-lineCh:
		if res.err != nil {
			t.Fatalf("reading first SSE line: %v", res.err)
		}
		if !strings.Contains(res.line, "first") {
			t.Errorf("first SSE line = %q", res.line)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("first SSE event did not arrive while stream was open — response is being buffered")
	}
}
