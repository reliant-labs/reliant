// Copyright (c) 2025 Reliant Labs
package daemonruntime

// Preview forwarder: the in-pod HTTP reverse proxy that terminates workspace
// preview traffic INSIDE the workspace network namespace.
//
// Why this exists: user dev servers (vite, next dev, python -m http.server,
// rails s, ...) overwhelmingly bind loopback only — 127.0.0.1, ::1, or both.
// The control-plane workspace-proxy runs OUTSIDE the pod's netns, so dialing
// podIP:<userPort> reaches nothing (connection refused → 502) for any
// loopback-bound server. Only a process inside the pod's netns can reach the
// user's loopback. The tools-daemon is that process, so it exposes ONE fixed
// listener (the "frontend" port, 9191 — the workspace-controller already
// declares containerPort {Name: "frontend", ContainerPort: 9191} and sets
// DAEMON_FRONTEND_PORT=9191 on every workspace pod) and reverse-proxies each
// request to loopback:<targetPort> in-namespace.
//
// Contract with the workspace-proxy (control-plane repo, internal/proxy):
//   - The proxy no longer dials user ports directly — it dials
//     podIP:9191/<targetPort> and the forwarder consumes the FIRST PATH
//     SEGMENT as the target port, stripping it before proxying upstream
//     (GET /5174/src/main.tsx → GET /src/main.tsx against loopback:5174).
//     The path segment was chosen over a header because it rides the proxy's
//     cached target URL exactly like the port did before (no per-request
//     header plumbing), mirrors the dev-path /proxy/{slug}/{port}/... shape,
//     and is curl-able in-pod for debugging.
//   - All per-port access control (public/authenticated/token) stays enforced
//     at the workspace-proxy edge; the forwarder trusts the proxy because the
//     pod NetworkPolicy (allow-workspace-proxy-ingress) makes the proxy the
//     only route into the pod.
//   - The forwarder dials BOTH loopback literals — 127.0.0.1:<port> first,
//     then [::1]:<port> — because users bind either family (a vite server
//     bound ::1-only was the original production failure).
//   - WebSocket upgrades (vite HMR!) and streaming responses (SSE, chunked)
//     pass through unbuffered: httputil.ReverseProxy handles the 101
//     Switching Protocols hijack natively, and FlushInterval=-1 flushes
//     every write immediately. Bodies are streamed, never fully buffered.
//   - Target not listening → 502 with an actionable plain-text body telling
//     the user nothing is listening on that port inside the workspace.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	// DefaultPreviewPort is the fixed in-pod listen port. 9191 was already
	// reserved for the daemon: the workspace-controller (control-plane repo)
	// declares containerPort {Name: "frontend", ContainerPort: 9191}, sets
	// DAEMON_FRONTEND_PORT=9191, and the allow-workspace-proxy-ingress
	// NetworkPolicy permits the proxy to reach it (that rule is
	// port-unrestricted). It does not collide with the daemon's Connect/gRPC
	// "backend" port 9190. Mirrored in control-plane internal/proxy
	// (previewForwarderPort) — keep the two literals in sync.
	DefaultPreviewPort = 9191

	// previewDialTimeout bounds each per-family loopback dial attempt.
	// Loopback connect either succeeds or is refused near-instantly; the
	// timeout only guards pathological states (SYN backlog exhaustion on a
	// wedged server).
	previewDialTimeout = 2 * time.Second

	previewLogPrefix = "[🔎 PreviewForwarder]"
)

// PreviewForwarder serves the in-pod preview listener. Construct with
// NewPreviewForwarder, then Start; Shutdown to stop.
type PreviewForwarder struct {
	ln  net.Listener
	srv *http.Server

	// reservedPorts are the daemon's own listeners (RPC 9190, this forwarder).
	// Refused as proxy targets: proxying to the forwarder itself would loop,
	// and the daemon RPC surface must never be exposed through a preview URL.
	reservedPorts map[int]bool

	// transport is shared across requests so upstream keep-alive pooling works.
	transport *http.Transport
}

// NewPreviewForwarder returns an unstarted forwarder. reservedPorts are the
// daemon's own listen ports (see PreviewForwarder.reservedPorts).
func NewPreviewForwarder(reservedPorts ...int) *PreviewForwarder {
	reserved := make(map[int]bool, len(reservedPorts)+1)
	for _, p := range reservedPorts {
		if p > 0 {
			reserved[p] = true
		}
	}
	return &PreviewForwarder{
		reservedPorts: reserved,
		transport:     newLoopbackTransport(),
	}
}

// Start binds addr (e.g. ":9191", or "127.0.0.1:0" in tests) and serves in a
// background goroutine. It returns once the listener is bound, so callers can
// immediately read Addr(). Errors from a dead listener surface via logging;
// the forwarder is deliberately non-fatal to the daemon (a broken preview
// surface must never take down tool execution).
func (f *PreviewForwarder) Start(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("preview forwarder listen %s: %w", addr, err)
	}
	f.ln = ln
	if tcpAddr, ok := ln.Addr().(*net.TCPAddr); ok {
		f.reservedPorts[tcpAddr.Port] = true
	}

	f.srv = &http.Server{
		Handler: http.HandlerFunc(f.handle),
		// Slowloris guard on headers only. No WriteTimeout/global ReadTimeout:
		// previews hold long-lived streams (SSE, HMR websockets) open
		// indefinitely.
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if serveErr := f.srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logging.Error(previewLogPrefix+" Serve exited", "error", serveErr)
		}
	}()
	logging.Info(previewLogPrefix+" Listening", "addr", ln.Addr().String())
	return nil
}

// Addr returns the bound listener address (nil before Start).
func (f *PreviewForwarder) Addr() net.Addr {
	if f.ln == nil {
		return nil
	}
	return f.ln.Addr()
}

// Shutdown gracefully stops the listener.
func (f *PreviewForwarder) Shutdown(ctx context.Context) error {
	if f.srv == nil {
		return nil
	}
	return f.srv.Shutdown(ctx)
}

func (f *PreviewForwarder) handle(w http.ResponseWriter, r *http.Request) {
	port, rest, rawRest, err := splitPreviewPath(r.URL.Path, r.URL.RawPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if f.reservedPorts[port] {
		http.Error(w, fmt.Sprintf("port %d is reserved by the workspace runtime", port), http.StatusForbidden)
		return
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			// The literal host is only a label — the loopback transport
			// ignores it and tries both loopback families. "localhost" keeps
			// the outbound Host header a name dev servers accept by default
			// (vite's host check allows localhost and IPs; a public preview
			// domain would be rejected without user config).
			pr.Out.URL.Host = net.JoinHostPort("localhost", strconv.Itoa(port))
			pr.Out.Host = pr.Out.URL.Host
			// Strip the port carrier segment so the upstream sees the path
			// the user's app expects.
			pr.Out.URL.Path = rest
			pr.Out.URL.RawPath = rawRest
			// Preserve the edge-set forwarding headers verbatim: the
			// workspace-proxy is the trust boundary and already stamped the
			// real client/host/proto. (Rewrite strips them from Out by
			// default; calling SetXForwarded here would overwrite the public
			// host/proto with this pod-internal hop's.)
			for _, key := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
				if vals := pr.In.Header.Values(key); len(vals) > 0 {
					pr.Out.Header[key] = vals
				}
			}
		},
		Transport: f.transport,
		// Flush every write immediately: SSE and incremental HTML render as
		// the upstream produces them instead of stalling in a buffer.
		// WebSocket (101) responses bypass this via the hijack path.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			f.upstreamError(w, r, port, err)
		},
	}
	proxy.ServeHTTP(w, r)
}

// splitPreviewPath parses "/<port>/rest..." into the target port and the
// remaining path. rawRest carries the corresponding suffix of the
// escaped-form path ("" when the request had no distinct RawPath).
// "/<port>" with no trailing slash maps to "/" — same normalization as the
// workspace-proxy's dev path route.
func splitPreviewPath(path, rawPath string) (port int, rest, rawRest string, err error) {
	trimmed := strings.TrimPrefix(path, "/")
	seg, tail, hasTail := strings.Cut(trimmed, "/")
	port, convErr := strconv.Atoi(seg)
	if seg == "" || convErr != nil || port < 1 || port > 65535 {
		return 0, "", "", fmt.Errorf("preview path must start with a target port segment (/<port>/...), got %q", path)
	}
	rest = "/"
	if hasTail {
		rest = "/" + tail
	}
	// The carrier segment is pure digits, so its escaped form is identical:
	// strip the same "/<seg>" prefix from RawPath when present.
	if rawPath != "" {
		if cut, ok := strings.CutPrefix(rawPath, "/"+seg); ok && cut != "" {
			rawRest = cut
		}
	}
	return port, rest, rawRest, nil
}

// upstreamError translates dial/stream failures into responses. Connection
// refused gets the actionable "nothing is listening" body; everything else a
// generic 502. Mid-stream failures (headers already written) can only be
// logged — the connection is torn down by ReverseProxy.
func (f *PreviewForwarder) upstreamError(w http.ResponseWriter, r *http.Request, port int, err error) {
	if errors.Is(err, context.Canceled) {
		// Client went away; nothing to write.
		return
	}
	logging.Warn(previewLogPrefix+" Upstream error", "port", port, "path", r.URL.Path, "error", err)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Reliant-Preview-Error", "upstream")
	w.WriteHeader(http.StatusBadGateway)
	if errors.Is(err, syscall.ECONNREFUSED) || isTimeout(err) {
		fmt.Fprintf(w, "nothing is listening on port %d inside this workspace — start your dev server (binding localhost/127.0.0.1/::1 is fine) and reload.\n", port)
		return
	}
	fmt.Fprintf(w, "preview upstream on port %d failed: %v\n", port, err)
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// newLoopbackTransport returns a transport whose dialer ignores the request
// host and connects to the target port on loopback, trying IPv4 then IPv6.
// This is the heart of the fix: the user may have bound 127.0.0.1 only, ::1
// only (vite's default resolution of "localhost" on some images), or both.
func newLoopbackTransport() *http.Transport {
	return &http.Transport{
		DialContext:           dialLoopback,
		ForceAttemptHTTP2:     false, // dev servers are HTTP/1.1; WS needs 1.1
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// No ResponseHeaderTimeout: dev servers may compile for a long time
		// before first byte (vite cold transform, next dev first build).
	}
}

// dialLoopback connects to <port from addr> on 127.0.0.1, falling back to
// [::1]. Returns the first success; if both fail, returns an error that
// preserves ECONNREFUSED (via errors.Join → errors.Is) so the 502 body stays
// actionable.
func dialLoopback(ctx context.Context, network, addr string) (net.Conn, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("preview dial: bad address %q: %w", addr, err)
	}
	d := &net.Dialer{Timeout: previewDialTimeout}
	conn4, err4 := d.DialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", port))
	if err4 == nil {
		return conn4, nil
	}
	conn6, err6 := d.DialContext(ctx, "tcp6", net.JoinHostPort("::1", port))
	if err6 == nil {
		return conn6, nil
	}
	return nil, fmt.Errorf("dial loopback:%s (tried 127.0.0.1 and ::1): %w", port, errors.Join(err4, err6))
}

// previewListenAddr resolves the forwarder bind address from the environment.
// DAEMON_FRONTEND_PORT is stamped by the workspace-controller pod spec (9191);
// absent (local dev, tests) the default applies. The forwarder binds all
// interfaces — the workspace-proxy dials the POD IP, not loopback.
func previewListenAddr(getenv func(string) string) string {
	if raw := getenv("DAEMON_FRONTEND_PORT"); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 && p < 65536 {
			return fmt.Sprintf(":%d", p)
		}
	}
	return fmt.Sprintf(":%d", DefaultPreviewPort)
}
