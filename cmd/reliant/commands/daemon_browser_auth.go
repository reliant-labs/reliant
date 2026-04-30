// Copyright (c) 2025 Reliant Labs
package commands

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/reliant-labs/reliant/internal/auth/oauthcallback"
	"github.com/reliant-labs/reliant/internal/logging"
)

const browserAuthTimeout = 120 * time.Second

// browserAuthResult is the JSON payload the frontend POSTs to /callback.
type browserAuthResult struct {
	Token string `json:"token"`
	State string `json:"state"`
}

// browserAuth opens the user's default browser to the Reliant web UI's
// /daemon/auth page. If the user is already signed in, the frontend
// exchanges the session for an access token and POSTs it back to a
// temporary localhost server. Returns the access token on success.
func browserAuth(ctx context.Context, webURL string) (string, error) {
	// Generate a random state nonce for CSRF protection.
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", fmt.Errorf("generating state nonce: %w", err)
	}
	state := hex.EncodeToString(nonceBytes)

	// Start temporary HTTP server on an ephemeral port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("starting callback listener: %w", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port

	type callbackEvent struct {
		token string
		err   error
	}
	resultCh := make(chan callbackEvent, 1)

	mux := http.NewServeMux()

	// POST /callback — receives the access token from the frontend.
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		// Allow CORS from the web UI origin.
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = webURL
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var payload browserAuthResult
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if payload.State != state {
			logging.Warn("Browser auth callback: state mismatch")
			http.Error(w, "state mismatch", http.StatusForbidden)
			return
		}

		if payload.Token == "" {
			http.Error(w, "missing token", http.StatusBadRequest)
			return
		}

		// Send the token to the waiting goroutine.
		select {
		case resultCh <- callbackEvent{token: payload.Token}:
		default:
		}

		// Return success HTML.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><body style="font-family:system-ui;display:flex;justify-content:center;align-items:center;height:100vh;margin:0;background:#1a1a2e;color:#e0e0e0"><div style="text-align:center"><h2>Daemon Authenticated</h2><p>You can close this tab and return to your terminal.</p></div></body></html>`)
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutCancel()
		_ = srv.Shutdown(shutCtx)
	}()

	// Open the browser to the daemon auth page.
	authURL := fmt.Sprintf("%s/daemon/auth?callback_port=%d&state=%s", webURL, port, state)
	logging.Info("Opening browser for daemon authentication", "url", authURL)
	fmt.Printf("Opening browser to authenticate daemon...\n")
	if err := oauthcallback.OpenBrowser(authURL); err != nil {
		fmt.Printf("Could not open browser automatically.\nPlease visit:\n  %s\n", authURL)
	}

	// Wait for the callback with timeout.
	timeoutCtx, cancel := context.WithTimeout(ctx, browserAuthTimeout)
	defer cancel()

	select {
	case ev := <-resultCh:
		if ev.err != nil {
			return "", ev.err
		}
		return ev.token, nil
	case <-timeoutCtx.Done():
		return "", fmt.Errorf("browser authentication timed out after %s", browserAuthTimeout)
	}
}
