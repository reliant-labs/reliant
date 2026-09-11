// Copyright (c) 2025 Reliant Labs

// Package oauthhelper serves the localhost OAuth helper: the small HTTP
// surface a BROWSER calls to run a Claude/Codex OAuth flow on the machine it
// is running on.
//
// # Why a localhost listener is the whole point
//
// An OAuth provider redirects to `http://localhost:<port>/...`, and "localhost"
// resolves on the USER'S machine — the one the browser runs on. So the
// callback receiver must be co-located with the browser. Nothing else can
// stand in for it: not the API server (wrong machine), not a remote daemon
// (also the wrong machine), not the gateway (no browser at all).
//
// That makes this listener do double duty, and the second job is the
// non-obvious one:
//
//  1. it RECEIVES the OAuth callback, and
//  2. its reachability at 127.0.0.1 is the PROOF that whatever serves it is
//     on the same machine as the browser.
//
// Co-location cannot be asserted from the other end. A daemon knows its own
// hostname, instance id and platform, but nothing in its connection tells it
// which machine rendered the page — both legs terminate at the gateway, and
// behind NAT a dozen machines share one source address. A daemon that claimed
// "I am local" would be guessing, and the failure is ugly rather than loud:
// the browser redirects to localhost, reaches whatever IS listening there, and
// the authorization code goes somewhere it was never meant to go. So the
// browser probes, and a successful probe IS the answer.
//
// # Why the identity fields on /health exist
//
// A bare `{"status":"ok"}` proves only that SOMETHING holds port 19284. Any
// other dev server on that port answers a health probe the same way, and the
// UI would then offer an OAuth flow that silently fails. /health therefore
// names the product, the readiness, and the version, so the caller can tell
// "reliant is here" from "a port is here".
package oauthhelper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/reliant-labs/reliant/internal/auth/oauthcallback"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/version"
)

// DefaultPort is the fixed port the web app probes. It is FIXED on purpose:
// the browser has to know where to look before anything has told it, so this
// value is a published contract shared with web/src/lib/oauth-local.ts. Do not
// make it dynamic without changing that too.
const DefaultPort = 19284

// ServiceName is the `service` field on /health — the marker that
// distinguishes reliant from any other process holding this port.
const ServiceName = "reliant"

// hostedWebOrigin is the production reliant web app's origin — the same
// hosted address as web/src/lib/constants.ts's DEFAULT_APP_URL and
// electron/release.config.json's VITE_APP_URL.
const hostedWebOrigin = "https://app.reliantlabs.io"

// HealthResponse is the /health document. It is a stable contract with the web
// app's availability probe, so field names must not change casually.
type HealthResponse struct {
	// Status is "ok" when the helper can run a flow right now.
	Status string `json:"status"`
	// Service is always ServiceName. The field a caller checks to know that
	// RELIANT answered, rather than some other process on this port.
	Service string `json:"service"`
	// Ready reports whether an OAuth flow can start. Distinct from Status so a
	// future "reachable but not ready" state has somewhere to live without
	// breaking a client that only understands the two fields it has.
	Ready bool `json:"ready"`
	// Version is the reliant build serving this. Lets the UI warn about a
	// helper too old for a flow the app wants, and makes a bug report specific.
	Version string `json:"version"`
	// Source names which command is serving: "daemon" or "auth-serve". Purely
	// diagnostic — it answers "why is this already running?" without a process
	// hunt.
	Source string `json:"source"`
}

// Options configures a helper server.
type Options struct {
	// Port to listen on; 0 means DefaultPort.
	Port int
	// Source is the HealthResponse.Source value ("daemon" / "auth-serve").
	Source string
	// ExtraOrigins are additional allowed CORS origins beyond the defaults.
	// RELIANT_WEB_ORIGIN is folded in automatically.
	ExtraOrigins []string
	// IdleTimeout closes the listener after this long with no request. Zero
	// means "stay up until Shutdown" — the `auth serve` foreground case, where
	// the user's Ctrl-C is the lifecycle.
	//
	// The daemon sets this: the port is opened ON DEMAND for one linking
	// session and must not outlive it. An always-listening localhost port that
	// starts browsers and returns authorization codes is a standing surface
	// nobody asked for, on every machine running a daemon.
	IdleTimeout time.Duration
	// OnIdle is called when IdleTimeout elapses, after the listener closes.
	// Lets the owner drop its reference so a later request re-opens cleanly.
	OnIdle func()
}

// Server is a running helper. Use Start to create one.
type Server struct {
	srv     *http.Server
	ln      net.Listener
	allowed map[string]bool
	source  string

	mu       sync.Mutex
	shutOnce bool
	// inFlight counts requests currently being served. An OAuth flow holds
	// /oauth/start open for as long as the user takes in the browser, so the
	// idle timer must never fire under one — "idle" means no request in
	// flight AND nothing since lastSeen.
	inFlight int
	lastSeen time.Time

	idleTimeout time.Duration
	onIdle      func()
	stopIdle    chan struct{}
}

// Addr returns the address the server is listening on.
func (s *Server) Addr() string {
	if s == nil || s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Start binds the helper port and serves in the background. It returns
// ErrPortInUse when the port is already held, which callers embedding this in
// a long-running process should treat as non-fatal — another reliant process
// (or a stale one) already provides the surface, and taking down the daemon
// over it would be a bad trade.
func Start(opts Options) (*Server, error) {
	port := opts.Port
	if port == 0 {
		port = DefaultPort
	}

	s := &Server{
		allowed:     buildAllowedOrigins(opts.ExtraOrigins),
		source:      opts.Source,
		lastSeen:    time.Now(),
		idleTimeout: opts.IdleTimeout,
		onIdle:      opts.OnIdle,
		stopIdle:    make(chan struct{}),
	}

	// 127.0.0.1, never 0.0.0.0: this surface starts browsers and hands back
	// authorization codes, so it must not be reachable from the network.
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if isAddrInUse(err) {
			return nil, fmt.Errorf("%w: %s", ErrPortInUse, addr)
		}
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}
	s.ln = ln
	s.srv = &http.Server{Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second}

	go func() {
		if serveErr := s.srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logging.Error("OAuth helper server stopped", "error", serveErr)
		}
	}()
	if s.idleTimeout > 0 {
		go s.watchIdle()
	}
	return s, nil
}

// watchIdle closes the listener once nothing has used it for idleTimeout.
// Polls at a fraction of the timeout rather than arming a timer per request:
// the resolution needed here is coarse, and a poll cannot leak a timer if a
// request finishes during shutdown.
func (s *Server) watchIdle() {
	tick := s.idleTimeout / 4
	if tick < time.Second {
		tick = time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-s.stopIdle:
			return
		case <-t.C:
			s.mu.Lock()
			idle := s.inFlight == 0 && time.Since(s.lastSeen) >= s.idleTimeout
			s.mu.Unlock()
			if !idle {
				continue
			}
			logging.Info("OAuth helper idle — closing the port", "idle_timeout", s.idleTimeout)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = s.Shutdown(ctx)
			cancel()
			if s.onIdle != nil {
				s.onIdle()
			}
			return
		}
	}
}

// track marks a request in flight for the duration of fn, so the idle timer
// cannot close the port under a long OAuth flow.
func (s *Server) track(fn func()) {
	s.mu.Lock()
	s.inFlight++
	s.lastSeen = time.Now()
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inFlight--
		s.lastSeen = time.Now()
		s.mu.Unlock()
	}()
	fn()
}

// ErrPortInUse reports that the helper port is already bound.
var ErrPortInUse = errors.New("oauth helper port already in use")

// Shutdown stops the server gracefully. Safe to call more than once and from
// several goroutines — the idle watcher and the owner's defer race by design.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil || s.srv == nil {
		return nil
	}
	s.mu.Lock()
	if s.shutOnce {
		s.mu.Unlock()
		return nil
	}
	s.shutOnce = true
	s.mu.Unlock()

	close(s.stopIdle)
	return s.srv.Shutdown(ctx)
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		s.track(func() {
			s.setCORS(w, r)
			writeJSON(w, http.StatusOK, HealthResponse{
				Status:  "ok",
				Service: ServiceName,
				Ready:   true,
				Version: version.Version,
				Source:  s.source,
			})
		})
	})

	mux.HandleFunc("OPTIONS /", func(w http.ResponseWriter, r *http.Request) {
		s.setCORS(w, r)
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /oauth/start", func(w http.ResponseWriter, r *http.Request) {
		// Tracked for the WHOLE flow: this handler blocks until the user
		// finishes in the browser, which can be minutes. Untracked, the idle
		// timer would close the port out from under an in-progress login.
		s.track(func() { s.handleOAuthStart(w, r) })
	})

	return mux
}

func (s *Server) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	{
		s.setCORS(w, r)

		if origin := r.Header.Get("Origin"); origin != "" && !s.allowed[origin] {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin not allowed"})
			return
		}

		var req struct {
			AuthorizeURLTemplate string `json:"authorize_url_template"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if req.AuthorizeURLTemplate == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "authorize_url_template is required"})
			return
		}

		result, err := oauthcallback.Run(r.Context(), req.AuthorizeURLTemplate)
		if err != nil {
			logging.Error("OAuth callback failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func (s *Server) setCORS(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" && s.allowed[origin] {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

// buildAllowedOrigins is the CORS/CSRF allowlist, whose only legitimate caller
// is the reliant web app.
//
// RELIANT_WEB_ORIGIN (comma-separated) extends it, and is load-bearing rather
// than a convenience: the web dev server's port is allocated per worktree (see
// .dev-ports.sh FRONTEND_PORT), so a worktree's Vite origin is never one of the
// static defaults. Resolved here — in the shared package — so both `auth serve`
// and the daemon-hosted helper honour the same variable. Reading it in only one
// of the two is how the daemon path would 403 a request `auth serve` allows,
// with an error that names the origin and not the reason.
func buildAllowedOrigins(extra []string) map[string]bool {
	origins := []string{
		hostedWebOrigin,
		"http://localhost:5173", // vite dev default (web/README.md)
		"http://localhost:3000", // common dev port (web/src/routes.tsx, scripts/dev.sh)
	}
	origins = append(origins, extra...)
	if env := os.Getenv("RELIANT_WEB_ORIGIN"); env != "" {
		origins = append(origins, strings.Split(env, ",")...)
	}
	// FRONTEND_PORT is what forge publishes for the dev web server and what the
	// Electron main process already reads. Accepting the loopback origins it
	// implies means a worktree stack works without anyone also having to set
	// RELIANT_WEB_ORIGIN to the same number.
	if p := strings.TrimSpace(os.Getenv("FRONTEND_PORT")); p != "" {
		origins = append(origins,
			"http://localhost:"+p,
			"http://127.0.0.1:"+p,
		)
	}

	set := make(map[string]bool, len(origins))
	for _, o := range origins {
		if o = strings.TrimSpace(o); o != "" {
			set[o] = true
		}
	}
	return set
}

// isAddrInUse reports whether a listen error is "port already taken".
// Matched on the syscall errno rather than the message where possible;
// net.Listen wraps it in *net.OpError → *os.SyscallError, so errors.Is reaches
// it. The string check is the fallback for platforms/wrappers that do not
// surface the errno.
func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) ||
		strings.Contains(strings.ToLower(err.Error()), "address already in use")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
