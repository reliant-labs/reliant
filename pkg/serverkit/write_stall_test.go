package serverkit

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"
)

const testStall = 100 * time.Millisecond

func serveWriteStallTest(t *testing.T, cfg Config, handler http.Handler) string {
	t.Helper()
	cfg.Normalize()
	server := NewHTTPServer(cfg, handler, nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
	go func() { _ = server.Serve(listener) }()
	return "http://" + listener.Addr().String()
}

func TestWriteStallGuardStreamOutlivesBudget(t *testing.T) {
	t.Parallel()
	url := serveWriteStallTest(t, Config{Addr: "127.0.0.1:0", WriteStallTimeout: testStall}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for index := range 8 {
			_, _ = fmt.Fprintf(w, "data: %d\n\n", index)
			w.(http.Flusher).Flush()
			time.Sleep(testStall / 2)
		}
		_, _ = io.WriteString(w, "data: done\n\n")
	}))

	started := time.Now()
	response, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if !strings.HasSuffix(string(body), "data: done\n\n") {
		t.Fatalf("body = %q, want complete stream", body)
	}
	if elapsed := time.Since(started); elapsed < 3*testStall {
		t.Fatalf("stream finished in %s; it did not outlive the %s write-stall budget", elapsed, testStall)
	}
}

func TestWriteStallGuardStalledReaderIsCut(t *testing.T) {
	t.Parallel()
	writeErr := make(chan error, 1)
	url := serveWriteStallTest(t, Config{Addr: "127.0.0.1:0", WriteStallTimeout: testStall}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chunk := []byte(strings.Repeat("x", 64<<10))
		for range 4096 {
			if _, err := w.Write(chunk); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- nil
	}))

	response, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer response.Body.Close()

	select {
	case err := <-writeErr:
		if err == nil {
			t.Fatal("handler wrote 256 MiB to a client that never read")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("write remained blocked; the stall deadline did not fire")
	}
}

func TestWriteStallGuardHijackPreservesStallProtection(t *testing.T) {
	t.Parallel()
	writeErr := make(chan error, 1)
	url := serveWriteStallTest(t, Config{Addr: "127.0.0.1:0", WriteStallTimeout: testStall}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			writeErr <- err
			return
		}
		defer conn.Close()
		for range 4096 {
			if _, err := conn.Write([]byte(strings.Repeat("x", 64<<10))); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- nil
	}))

	conn, err := net.Dial("tcp", strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: test\r\n\r\n")

	select {
	case err := <-writeErr:
		if err == nil {
			t.Fatal("hijacked handler wrote 256 MiB to a peer that never read")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hijacked write remained blocked; the stall deadline did not fire")
	}
}

func TestWriteStallGuardHijackAllowsIdleConnection(t *testing.T) {
	t.Parallel()
	url := serveWriteStallTest(t, Config{Addr: "127.0.0.1:0", WriteStallTimeout: testStall}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		time.Sleep(3 * testStall)
		_, _ = brw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 8\r\nConnection: close\r\n\r\nhijacked")
		_ = brw.Flush()
	}))

	conn, err := net.Dial("tcp", strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: test\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read hijacked response: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	if string(body) != "hijacked" {
		t.Fatalf("body = %q, want hijacked", body)
	}
}

func TestNewHTTPServerNoWholeResponseWriteTimeout(t *testing.T) {
	t.Parallel()
	cfg := Config{Addr: ":0"}
	cfg.Normalize()
	server := NewHTTPServer(cfg, http.NewServeMux(), nil)
	if server.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %s; use WriteStallTimeout instead", server.WriteTimeout)
	}
	if cfg.WriteStallTimeout != DefaultWriteStallTimeout {
		t.Errorf("WriteStallTimeout = %s, want %s", cfg.WriteStallTimeout, DefaultWriteStallTimeout)
	}
}

func TestWriteStallGuardKeepAliveDeadlineDoesNotLeak(t *testing.T) {
	t.Parallel()
	url := serveWriteStallTest(t, Config{Addr: "127.0.0.1:0", WriteStallTimeout: testStall}, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/empty" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1}}
	defer client.CloseIdleConnections()

	response, err := client.Get(url)
	if err != nil {
		t.Fatalf("first GET: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	time.Sleep(3 * testStall)

	var reused bool
	request, err := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused },
	}), http.MethodGet, url+"/empty", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	response, err = client.Do(request)
	if err != nil {
		t.Fatalf("second GET: %v", err)
	}
	_ = response.Body.Close()
	if !reused {
		t.Fatal("second request did not reuse the connection")
	}
}

// The connection timeouts come from Config: zero takes the defaults
// (10s/30s/120s), a positive value is used as given, and a negative one
// disables the timeout.
func TestNewHTTPServerTimeoutsFromConfig(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		in                 Config
		header, read, idle time.Duration
		h2Max              int
	}{
		{"defaults", Config{}, 10 * time.Second, 30 * time.Second, 120 * time.Second, 0},
		{"given", Config{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: time.Minute, IdleTimeout: time.Hour, HTTP2MaxConcurrentStreams: 1000}, 5 * time.Second, time.Minute, time.Hour, 1000},
		{"disabled", Config{ReadHeaderTimeout: -1, ReadTimeout: -1, IdleTimeout: -1}, 0, 0, 0, 0},
	} {
		cfg := tc.in
		cfg.Addr = ":0"
		cfg.Normalize()
		s := NewHTTPServer(cfg, http.NewServeMux(), nil)
		if s.ReadHeaderTimeout != tc.header || s.ReadTimeout != tc.read || s.IdleTimeout != tc.idle {
			t.Errorf("%s: header/read/idle = %v/%v/%v, want %v/%v/%v", tc.name, s.ReadHeaderTimeout, s.ReadTimeout, s.IdleTimeout, tc.header, tc.read, tc.idle)
		}
		if s.HTTP2 == nil || s.HTTP2.MaxConcurrentStreams != tc.h2Max {
			t.Errorf("%s: HTTP2 = %+v, want MaxConcurrentStreams %d", tc.name, s.HTTP2, tc.h2Max)
		}
		if s.Protocols == nil || !s.Protocols.HTTP1() || !s.Protocols.HTTP2() || !s.Protocols.UnencryptedHTTP2() {
			t.Errorf("%s: Protocols = %v, want HTTP/1.1, HTTP/2 and h2c", tc.name, s.Protocols)
		}
	}
}
