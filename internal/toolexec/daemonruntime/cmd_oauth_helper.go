// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/auth/oauthhelper"
	"github.com/reliant-labs/reliant/internal/logging"
)

// The localhost OAuth helper is opened ON DEMAND, over the gateway connection
// the daemon already holds, and closed again when the linking session ends.
//
// WHY NOT JUST LISTEN ALL THE TIME. The helper starts browsers and hands back
// authorization codes. A port doing that, open for the entire life of every
// daemon on every machine, is a standing local surface nobody asked for — and
// it is unnecessary, because the app only needs it during the seconds a user
// spends linking an account. Opening it per session keeps the exposure equal
// to the task.
//
// WHY IT STILL ANSWERS THE CO-LOCATION QUESTION. Whether the daemon is on the
// same machine as the browser cannot be asserted from this end: the daemon
// knows its hostname and instance id, but nothing in its connection says which
// machine rendered the page, and behind NAT many machines share one address.
// The request/response here is the proof, in two steps — the UI asks the
// daemon (over the existing connection) to open the port, then probes
// 127.0.0.1 itself. If the probe succeeds, the daemon that just opened it is
// demonstrably on the browser's machine. If it fails, the daemon is remote and
// the UI can say so instead of offering a flow that would hang.
func init() {
	RegisterCommand("auth.open_oauth_helper", handleOpenOAuthHelper)
	RegisterCommand("auth.close_oauth_helper", handleCloseOAuthHelper)
}

// helperIdleTimeout closes the port when a session is abandoned — the user
// navigates away, or the browser never probes. Long enough to cover a real
// login (the provider page, credentials, MFA), short enough that a forgotten
// tab does not leave the port open indefinitely. Any request resets it, and an
// in-flight /oauth/start holds it open regardless of length.
const helperIdleTimeout = 10 * time.Minute

var (
	helperMu     sync.Mutex
	helperServer *oauthhelper.Server
)

type openOAuthHelperRequest struct {
	// WebOrigin is the browser origin that will call the helper. Passed from
	// the UI because the daemon cannot know it: a worktree's web dev server
	// gets a per-worktree port (.dev-ports.sh FRONTEND_PORT), so the origin is
	// not one of the static defaults and is not in the daemon's environment
	// either. Without it the CORS allowlist would 403 the very caller that
	// asked for the port.
	WebOrigin string `json:"web_origin"`
}

type openOAuthHelperResponse struct {
	// Port the helper is listening on, so the UI probes the right place rather
	// than assuming the default.
	Port int `json:"port"`
	// Addr is the full loopback address, for display and for the probe URL.
	Addr string `json:"addr"`
	// AlreadyRunning reports that the port was already served (by a previous
	// session, or a standalone `reliant auth serve`). Not an error: the
	// surface the browser needs exists either way.
	AlreadyRunning bool `json:"already_running"`
}

func handleOpenOAuthHelper(_ context.Context, payload []byte) ([]byte, error) {
	var req openOAuthHelperRequest
	// An empty payload is valid — the origin is optional.
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload: %w", err)
		}
	}

	helperMu.Lock()
	defer helperMu.Unlock()

	if helperServer != nil {
		return json.Marshal(openOAuthHelperResponse{
			Port:           oauthhelper.DefaultPort,
			Addr:           helperServer.Addr(),
			AlreadyRunning: true,
		})
	}

	var extra []string
	if req.WebOrigin != "" {
		extra = append(extra, req.WebOrigin)
	}

	srv, err := oauthhelper.Start(oauthhelper.Options{
		Source:       "daemon",
		ExtraOrigins: extra,
		IdleTimeout:  helperIdleTimeout,
		OnIdle:       clearHelperServer,
	})
	if err != nil {
		// Already held — by a standalone `auth serve`, or a daemon that did
		// not clean up. The browser's probe will succeed either way, so report
		// success rather than failing a request whose post-condition holds.
		if isPortInUse(err) {
			logging.Info("OAuth helper port already served — reusing it", "error", err)
			return json.Marshal(openOAuthHelperResponse{
				Port:           oauthhelper.DefaultPort,
				Addr:           fmt.Sprintf("127.0.0.1:%d", oauthhelper.DefaultPort),
				AlreadyRunning: true,
			})
		}
		return nil, fmt.Errorf("open oauth helper: %w", err)
	}

	helperServer = srv
	logging.Info("OAuth helper opened on demand", "addr", srv.Addr(), "idle_timeout", helperIdleTimeout)

	return json.Marshal(openOAuthHelperResponse{
		Port: oauthhelper.DefaultPort,
		Addr: srv.Addr(),
	})
}

// handleCloseOAuthHelper closes the port when the UI is finished — the flow
// completed, or the user cancelled. The idle timeout is the backstop for a UI
// that never gets to send this (a closed tab, a crash); this is the fast path
// that keeps the surface open only as long as it is actually needed.
func handleCloseOAuthHelper(_ context.Context, _ []byte) ([]byte, error) {
	helperMu.Lock()
	srv := helperServer
	helperServer = nil
	helperMu.Unlock()

	if srv == nil {
		return json.Marshal(map[string]bool{"closed": false})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logging.Warn("OAuth helper shutdown returned an error", "error", err)
	}
	logging.Info("OAuth helper closed on request")
	return json.Marshal(map[string]bool{"closed": true})
}

func clearHelperServer() {
	helperMu.Lock()
	helperServer = nil
	helperMu.Unlock()
}

func isPortInUse(err error) bool {
	return errors.Is(err, oauthhelper.ErrPortInUse)
}
