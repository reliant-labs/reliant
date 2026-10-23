package serverkit_test

import (
	"bufio"
	"context"
	"crypto/sha1" //nolint:gosec // RFC 6455 mandates SHA-1 for the accept key
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// websocket_test.go — a hijacked connection and the drain.
//
// http.Server.Shutdown neither waits for nor closes a hijacked connection, so
// a websocket open at SIGTERM was cut by the process exit: the browser saw a
// dropped TCP connection instead of a close it could reconnect on, and the
// handler's deferred cleanup — closing a terminal's PTY on the machine behind
// it — never ran. These tests speak just enough RFC 6455 to stand in for a
// websocket library: a 101 handshake, and an unmasked server close frame.

// closeServiceRestart is RFC 6455 close code 1012: the server is restarting;
// a client reconnects.
const closeServiceRestart = 1012

// wsUpgrade completes a websocket handshake on the server side and returns the
// hijacked connection, with the deadline net/http left on it cleared — as a
// websocket library does.
func wsUpgrade(w http.ResponseWriter, r *http.Request) (net.Conn, error) {
	conn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11")) //nolint:gosec // RFC 6455
	fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
		base64.StdEncoding.EncodeToString(sum[:]))
	if err := brw.Flush(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// wsWriteClose writes a server close frame carrying code and reason.
func wsWriteClose(conn net.Conn, code uint16, reason string) error {
	payload := binary.BigEndian.AppendUint16(nil, code)
	payload = append(payload, reason...)
	_, err := conn.Write(append([]byte{0x88, byte(len(payload))}, payload...))
	return err
}

// wsDial opens a websocket to url (http://host/path) and returns the
// connection and a reader positioned after the handshake.
func wsDial(t *testing.T, url string) (net.Conn, *bufio.Reader) {
	t.Helper()
	hostPath := strings.TrimPrefix(url, "http://")
	host, path, _ := strings.Cut(hostPath, "/")
	conn, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatalf("dial %s: %v", host, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	fmt.Fprintf(conn, "GET /%s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", path, host)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("websocket handshake: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("websocket handshake: status %d", resp.StatusCode)
	}
	return conn, br
}

// wsReadClose reads one server frame and returns its close code, or an error
// when the connection ended without a close frame.
func wsReadClose(br *bufio.Reader) (uint16, string, error) {
	var head [2]byte
	if _, err := io.ReadFull(br, head[:]); err != nil {
		return 0, "", fmt.Errorf("no close frame: %w", err)
	}
	if head[0]&0x0f != 0x8 {
		return 0, "", fmt.Errorf("frame opcode %#x, want close", head[0]&0x0f)
	}
	payload := make([]byte, head[1]&0x7f)
	if _, err := io.ReadFull(br, payload); err != nil || len(payload) < 2 {
		return 0, "", fmt.Errorf("short close frame: %v", err)
	}
	return binary.BigEndian.Uint16(payload), string(payload[2:]), nil
}

// terminalHandler is the shape of a websocket terminal: it owns a session it
// must release however the connection ends (the deferred cleanup), and on the
// drain it closes with 1012 and bounds the wait for the client's answer.
func terminalHandler(cleanedUp *atomic.Bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrade(w, r)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		defer cleanedUp.Store(true) // the PTY close a dropped connection skipped

		stop := serverkit.AfterDrain(r.Context(), func() {
			deadline := time.Now().Add(2 * time.Second)
			_ = conn.SetWriteDeadline(deadline)
			_ = wsWriteClose(conn, closeServiceRestart, "server restarting")
			_ = conn.SetReadDeadline(deadline)
		})
		defer stop()
		buf := make([]byte, 512)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	})
}

// TestLongLived_WebsocketClosesWith1012AndCleansUp: on the drain the terminal
// tells its client "service restart", the client answers by closing, the
// handler's deferred cleanup runs, and the drain does not wait for the reserve.
func TestLongLived_WebsocketClosesWith1012AndCleansUp(t *testing.T) {
	t.Parallel()
	var cleanedUp atomic.Bool
	mux := http.NewServeMux()
	mux.Handle("/terminal", serverkit.LongLived(terminalHandler(&cleanedUp)))
	streams := serverkit.NewStreams()
	httpSrv, base := serveOwnListener(t, mux, streams)

	conn, br := wsDial(t, base+"/terminal")
	waitActive(t, streams, 1)

	drained := make(chan error, 1)
	start := time.Now()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		drained <- serverkit.DrainHTTP(ctx, httpSrv, streams)
	}()

	code, reason, err := wsReadClose(br)
	if err != nil {
		t.Fatalf("client: %v — the websocket was cut instead of closed", err)
	}
	if code != closeServiceRestart {
		t.Errorf("close code = %d (%q), want %d: the browser reconnects only on a restart", code, reason, closeServiceRestart)
	}
	_ = conn.Close() // the client's answer

	if err := <-drained; err != nil {
		t.Fatalf("DrainHTTP = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("drain took %s; a websocket that closed on the soft signal must not wait for the reserve", elapsed)
	}
	if !cleanedUp.Load() {
		t.Error("the websocket handler's deferred cleanup did not run")
	}
}

// TestLongLived_DrainClosesAHijackedConnTheHandlerIgnores is the last resort:
// a websocket handler that ignores its context entirely, blocked in a read the
// client never answers. The drain closes the hijacked connection at the end of
// the reserve, so the handler unwinds and its cleanup runs inside the budget —
// before, the drain waited out the budget and the exit dropped the connection
// with the cleanup never run.
func TestLongLived_DrainClosesAHijackedConnTheHandlerIgnores(t *testing.T) {
	t.Parallel()
	var cleanedUp atomic.Bool
	mux := http.NewServeMux()
	mux.Handle("/ws", serverkit.LongLived(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrade(w, r)
		if err != nil {
			return
		}
		defer cleanedUp.Store(true)
		_, _ = conn.Read(make([]byte, 1)) // blocks until the connection closes
	})))
	streams := &serverkit.Streams{DrainReserve: 2 * time.Second}
	httpSrv, base := serveOwnListener(t, mux, streams)

	wsDial(t, base+"/ws") // and never close it
	waitActive(t, streams, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := serverkit.DrainHTTP(ctx, httpSrv, streams); err != nil {
		t.Fatalf("DrainHTTP = %v: the drain must close a hijacked connection whose handler ignores it", err)
	}
	if !cleanedUp.Load() {
		t.Fatal("the handler's deferred cleanup did not run before the drain finished")
	}
}

// AfterDrain's hook never runs for a handler that finished on its own.
func TestOnDrain_StopPreventsTheHook(t *testing.T) {
	t.Parallel()
	streams := serverkit.NewStreams()
	ctx, release := streams.Bind(context.Background())
	ran := make(chan struct{}, 1)
	stop := serverkit.AfterDrain(ctx, func() { ran <- struct{}{} })
	if !stop() {
		t.Fatal("stop before the drain must report that it stopped the hook")
	}
	release()
	streams.BeginDrain()
	select {
	case <-ran:
		t.Fatal("a stopped hook ran")
	case <-time.After(100 * time.Millisecond):
	}

	if stop := serverkit.AfterDrain(context.Background(), func() {}); stop() {
		t.Fatal("AfterDrain without a registry registers nothing to stop")
	}
}

// waitActive waits until streams has n bindings: the handler under test has
// reached its bound section.
func waitActive(t *testing.T, streams *serverkit.Streams, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for streams.Active() != n {
		if time.Now().After(deadline) {
			t.Fatalf("Active = %d, want %d", streams.Active(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
