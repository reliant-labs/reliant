// Copyright (c) 2025 Reliant Labs
//
//go:build e2e

package stories

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
)

// Story 09: the in-pod preview forwarder — the ONLY component allowed to dial
// user dev-server ports — reaches loopback-bound servers on BOTH loopback
// families, streams responses, passes WebSocket upgrades through, and fails
// actionably when nothing is listening.
//
// Why this story exists: a production outage hid because nothing exercised a
// loopback-bound user server through the preview path. User dev servers
// (vite, next dev, python -m http.server) bind 127.0.0.1 or ::1 only; the
// workspace-proxy used to dial podIP:<userPort> from OUTSIDE the pod network
// namespace and got connection-refused (502) for every such server. The
// forwarder (internal/toolexec/daemonruntime/preview_forwarder.go) terminates
// preview traffic inside the netns instead; the workspace-proxy (control-plane
// repo, internal/proxy) now always dials the forwarder port and carries the
// user's target port as the first path segment (/<port>/...). This story pins
// that contract hermetically: everything here shares the test process's
// loopback, exactly as the forwarder and user servers share the pod's
// loopback in production.
//
// It intentionally needs no DB/Temporal/LLM — it must keep running even when
// the heavier story infrastructure is unavailable.
func TestStory09_PreviewForwarder(t *testing.T) {
	t.Parallel()

	// The forwarder under test. Ephemeral port: the fixed :9191 production
	// bind is config (DAEMON_FRONTEND_PORT), not contract. 9190 mirrors the
	// production wiring, which reserves the daemon's own RPC port.
	const reservedRPCPort = 9190
	fwd := daemonruntime.NewPreviewForwarder(reservedRPCPort)
	require.NoError(t, fwd.Start("127.0.0.1:0"))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = fwd.Shutdown(ctx)
	})
	fwdBase := "http://" + fwd.Addr().String()

	// get fetches path (already including the /<port> carrier segment when
	// targeting a user server) through the forwarder.
	get := func(t *testing.T, path string) (*http.Response, string) {
		t.Helper()
		resp, err := http.Get(fwdBase + path)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		return resp, string(body)
	}

	t.Run("ipv4_loopback_server", func(t *testing.T) {
		t.Parallel()
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		require.NoError(t, err)
		port := ln.Addr().(*net.TCPAddr).Port
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			// The carrier segment must be stripped: the user's app sees its
			// own path shape, never /<port>/...
			fmt.Fprintf(w, "hello-ipv4 path=%s host=%s", r.URL.Path, r.Host)
		})
		srv := &http.Server{Handler: mux}
		go srv.Serve(ln) //nolint:errcheck
		t.Cleanup(func() { _ = srv.Close() })

		resp, body := get(t, fmt.Sprintf("/%d/app/index.html?x=1", port))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Contains(t, body, "hello-ipv4 path=/app/index.html")
		// Outbound Host is a localhost form so dev-server host checks
		// (vite allowedHosts) accept the request by default.
		require.Contains(t, body, "host=localhost:")

		// A bare /<port> (no trailing slash) normalizes to "/".
		resp, body = get(t, fmt.Sprintf("/%d", port))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Contains(t, body, "hello-ipv4 path=/")
	})

	// The exact production failure: a dev server bound to ::1 ONLY (vite
	// resolving "localhost" to the v6 loopback). The forwarder must fall back
	// from 127.0.0.1 to ::1.
	t.Run("ipv6_only_loopback_server", func(t *testing.T) {
		t.Parallel()
		ln := listenV6OnlyPort(t)
		port := ln.Addr().(*net.TCPAddr).Port
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "hello-ipv6")
		})
		srv := &http.Server{Handler: mux}
		go srv.Serve(ln) //nolint:errcheck
		t.Cleanup(func() { _ = srv.Close() })

		resp, body := get(t, fmt.Sprintf("/%d/", port))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "hello-ipv6", body)
	})

	// Vite HMR is a WebSocket: without upgrade passthrough previews load once
	// then go stale. Echo across the forwarder, on the ::1-only server to pin
	// upgrade + family-fallback together.
	t.Run("websocket_echo_upgrade", func(t *testing.T) {
		t.Parallel()
		ln := listenV6OnlyPort(t)
		port := ln.Addr().(*net.TCPAddr).Port
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		mux := http.NewServeMux()
		mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			_ = conn.WriteMessage(mt, append([]byte("echo:"), msg...))
		})
		srv := &http.Server{Handler: mux}
		go srv.Serve(ln) //nolint:errcheck
		t.Cleanup(func() { _ = srv.Close() })

		wsURL := fmt.Sprintf("ws://%s/%d/ws", fwd.Addr().String(), port)
		conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
		require.NoError(t, err, "websocket upgrade through forwarder failed")
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		defer conn.Close()
		require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)

		require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte("hmr-ping")))
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
		_, msg, err := conn.ReadMessage()
		require.NoError(t, err)
		require.Equal(t, "echo:hmr-ping", string(msg))
	})

	// Streaming: the client must see bytes BEFORE the upstream handler
	// returns (SSE / incremental HTML would otherwise stall in a buffer).
	t.Run("streams_response_unbuffered", func(t *testing.T) {
		t.Parallel()
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		require.NoError(t, err)
		port := ln.Addr().(*net.TCPAddr).Port
		release := make(chan struct{})
		mux := http.NewServeMux()
		mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
			fl, ok := w.(http.Flusher)
			assert.True(t, ok)
			fmt.Fprintln(w, "chunk-1")
			fl.Flush()
			<-release // hold the response open until the client saw chunk-1
			fmt.Fprintln(w, "chunk-2")
		})
		srv := &http.Server{Handler: mux}
		go srv.Serve(ln) //nolint:errcheck
		t.Cleanup(func() { _ = srv.Close() })

		resp, err := http.Get(fmt.Sprintf("%s/%d/stream", fwdBase, port))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		reader := bufio.NewReader(resp.Body)
		lineCh := make(chan string, 1)
		go func() {
			line, _ := reader.ReadString('\n')
			lineCh <- line
		}()
		select {
		case line := <-lineCh:
			require.Equal(t, "chunk-1\n", line)
		case <-time.After(10 * time.Second):
			t.Fatal("first chunk not flushed through forwarder while upstream response still open")
		}
		release <- struct{}{}
		rest, err := io.ReadAll(reader)
		require.NoError(t, err)
		require.Equal(t, "chunk-2\n", string(rest))
	})

	t.Run("no_listener_gets_actionable_502", func(t *testing.T) {
		t.Parallel()
		port := unusedLoopbackPort(t)
		resp, body := get(t, fmt.Sprintf("/%d/", port))
		require.Equal(t, http.StatusBadGateway, resp.StatusCode)
		require.Contains(t, body, fmt.Sprintf("nothing is listening on port %d", port))
		require.Contains(t, body, "start your dev server")
	})

	t.Run("missing_or_invalid_port_segment", func(t *testing.T) {
		t.Parallel()
		for _, bad := range []string{"/", "/0/", "/-1/", "/65536/", "/http/foo", "/favicon.ico"} {
			resp, _ := get(t, bad)
			require.Equalf(t, http.StatusBadRequest, resp.StatusCode, "path=%q", bad)
		}
	})

	t.Run("reserved_ports_forbidden", func(t *testing.T) {
		t.Parallel()
		// The daemon's own RPC port and the forwarder's own port must never be
		// proxy targets (RPC exposure / self-loop).
		fwdPort := fwd.Addr().(*net.TCPAddr).Port
		for _, reserved := range []int{reservedRPCPort, fwdPort} {
			resp, body := get(t, fmt.Sprintf("/%d/", reserved))
			require.Equalf(t, http.StatusForbidden, resp.StatusCode, "port=%d body=%q", reserved, body)
		}
	})

	// End-to-end realism: the dev server is a REAL child process started
	// through the daemon's own background-process path (the same code the
	// bash tool's run_in_background uses), bound loopback-only — exactly how
	// a user's `npm run dev` comes to exist in a workspace pod.
	t.Run("daemon_runtime_background_process", func(t *testing.T) {
		t.Parallel()
		python, err := exec.LookPath("python3")
		if err != nil {
			t.Skip("python3 not on PATH; skipping daemon-spawned server case")
		}

		dir := t.TempDir()
		const marker = "story-09-preview-forwarder-marker"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>"+marker+"</html>"), 0o644))
		port := unusedLoopbackPort(t)

		client := daemon.NewLocalClient()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		procID, err := client.StartBackground(ctx, &daemon.RunCommandRequest{
			Command:    fmt.Sprintf("%s -m http.server %d --bind 127.0.0.1", python, port),
			WorkingDir: dir,
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = client.KillProcess(context.Background(), procID) })

		// Wait for the server to accept before fetching through the forwarder.
		require.Eventually(t, func() bool {
			conn, dialErr := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), 250*time.Millisecond)
			if dialErr != nil {
				return false
			}
			conn.Close()
			return true
		}, 30*time.Second, 200*time.Millisecond, "daemon-spawned python http.server never started listening")

		resp, body := get(t, fmt.Sprintf("/%d/index.html", port))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Contains(t, body, marker)
	})
}

// listenV6OnlyPort returns a listener bound to [::1] on a port that is FREE on
// 127.0.0.1, so the forwarder's IPv4-first dial gets a clean refusal and must
// fall back to IPv6 (the behavior under test). Retries around the rare case
// where an unrelated process owns the same port number on IPv4.
func listenV6OnlyPort(t *testing.T) net.Listener {
	t.Helper()
	for attempt := 0; attempt < 20; attempt++ {
		ln6, err := net.Listen("tcp6", "[::1]:0")
		if err != nil {
			t.Skipf("IPv6 loopback unavailable on this host: %v", err)
		}
		port := ln6.Addr().(*net.TCPAddr).Port
		ln4, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			// Port occupied on IPv4 → the fallback test would be ambiguous.
			ln6.Close()
			continue
		}
		ln4.Close() // freed: forwarder's v4 dial will be refused, v6 succeeds
		t.Cleanup(func() { _ = ln6.Close() })
		return ln6
	}
	t.Fatal("could not allocate a ::1-only port free on 127.0.0.1")
	return nil
}

// unusedLoopbackPort finds a port with no listener on either loopback family.
func unusedLoopbackPort(t *testing.T) int {
	t.Helper()
	for attempt := 0; attempt < 20; attempt++ {
		ln4, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("allocating probe port: %v", err)
		}
		port := ln4.Addr().(*net.TCPAddr).Port
		ln4.Close()
		if ln6, err := net.Listen("tcp6", fmt.Sprintf("[::1]:%d", port)); err == nil {
			ln6.Close()
			return port
		}
		// Something owns the port on IPv6; try again.
	}
	t.Fatal("could not find a port unused on both loopback families")
	return 0
}
