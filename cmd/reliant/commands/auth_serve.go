// Copyright (c) 2025 Reliant Labs
package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/reliant-labs/reliant/internal/auth/oauthcallback"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/spf13/cobra"
)

const defaultAuthServePort = 19284

// hostedWebOrigin is the production reliant web app's origin — the same
// hosted address as web/src/lib/constants.ts's DEFAULT_APP_URL and
// electron/release.config.json's VITE_APP_URL. There is no shared Go constant
// for it (builddefaults only carries the API/gateway/auth-provider origins),
// so it is declared here alongside the local dev origins the web app runs on.
const hostedWebOrigin = "https://app.reliantlabs.io"

// allowedOrigins is the CORS/CSRF allowlist for this localhost helper, whose
// only legitimate caller is the reliant web app. RELIANT_WEB_ORIGIN
// (comma-separated) extends it — needed because the web dev server's port is
// dynamically allocated per worktree (see .dev-ports.sh FRONTEND_PORT) and
// won't always be one of the defaults below.
var allowedOrigins = buildAllowedOrigins()

func buildAllowedOrigins() map[string]bool {
	origins := []string{
		hostedWebOrigin,
		"http://localhost:5173", // vite dev default (web/README.md)
		"http://localhost:3000", // common dev port (web/src/routes.tsx, scripts/dev.sh)
	}
	if extra := os.Getenv("RELIANT_WEB_ORIGIN"); extra != "" {
		for _, o := range strings.Split(extra, ",") {
			if o = strings.TrimSpace(o); o != "" {
				origins = append(origins, o)
			}
		}
	}
	set := make(map[string]bool, len(origins))
	for _, o := range origins {
		set[o] = true
	}
	return set
}

func newAuthServeCmd() *cobra.Command {
	var port int

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start a local OAuth helper server",
		Long: `Starts a lightweight HTTP server on localhost that handles OAuth
callback flows for Claude and Codex authentication.

This is required when using the Reliant web UI in a browser (not Electron)
to connect Claude Code or Codex accounts via OAuth, since the OAuth
callbacks must be received on localhost.

The server exposes:
  GET  /health       — Health check (for the frontend to detect availability)
  POST /oauth/start  — Start an OAuth flow (opens browser, waits for callback)

Example:
  reliant auth serve
  reliant auth serve --port 19284`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuthServe(cmd, port)
		},
	}

	cmd.Flags().IntVar(&port, "port", defaultAuthServePort, "Port to listen on")

	return cmd
}

func runAuthServe(cmd *cobra.Command, port int) error {
	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		setCORS(w, r)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// CORS preflight
	mux.HandleFunc("OPTIONS /", func(w http.ResponseWriter, r *http.Request) {
		setCORS(w, r)
		w.WriteHeader(http.StatusNoContent)
	})

	// Start OAuth flow
	mux.HandleFunc("POST /oauth/start", func(w http.ResponseWriter, r *http.Request) {
		setCORS(w, r)

		if origin := r.Header.Get("Origin"); origin != "" && !allowedOrigins[origin] {
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
	})

	addr := fmt.Sprintf("127.0.0.1:%d", port)

	fmt.Fprintf(cmd.OutOrStdout(), "OAuth helper server listening on http://%s\n", addr)
	fmt.Fprintf(cmd.OutOrStdout(), "Press Ctrl+C to stop\n")

	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	// Graceful shutdown on interrupt
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		fmt.Fprintln(cmd.OutOrStdout(), "\nShutting down...")
		_ = server.Close()
	}()

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server failed: %w", err)
	}

	return nil
}

func setCORS(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" && allowedOrigins[origin] {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
