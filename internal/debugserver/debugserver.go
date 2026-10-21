// Copyright (c) 2025 Reliant Labs

// Package debugserver is the localhost-only diagnostics listener every reliant
// server process runs: pprof, DB write-queue gauges, and Prometheus metrics.
//
// It binds 127.0.0.1 and nothing else. Nothing here is authenticated, and a
// goroutine dump carries prompts and request bodies, so it must never share a
// port with a public listener or bind a routable address. Reaching it on a
// cluster means `kubectl port-forward`, which is exactly the access it should
// require.
package debugserver

import (
	"fmt"
	"net/http"
	_ "net/http/pprof" //nolint:gosec // G108: registered on DefaultServeMux, served only on 127.0.0.1 below

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
)

// Start serves the diagnostics endpoints on 127.0.0.1:port in the background.
// A bind failure is logged, not returned: a process without a profiler still
// does its job, and failing startup over one would be the wrong trade. Port 0
// (PPROF_PORT=0) serves nothing: the listener is off, not on a random port.
func Start(port int) {
	if port == 0 {
		logging.Info("Debug server (pprof) disabled", "pprof_port", 0)
		return
	}
	addr := Addr(port)
	go func() {
		logging.Info("Starting debug server (pprof)", "address", addr)
		//nolint:gosec // G114: localhost-only diagnostics, no timeouts needed
		if err := http.ListenAndServe(addr, Handler()); err != nil {
			logging.Error("debug server failed", "address", addr, "error", err)
		}
	}()
}

// Addr is the diagnostics listener's address for port: loopback only.
func Addr(port int) string { return fmt.Sprintf("127.0.0.1:%d", port) }

// Handler is the diagnostics endpoints.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", http.DefaultServeMux.ServeHTTP)
	mux.HandleFunc("/debug/db", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"pending_writes": %d, "peak_pending_writes": %d}`,
			db.GetPendingWrites(), db.GetPeakPendingWrites())
	})
	mux.HandleFunc("/debug/db/reset-peak", func(w http.ResponseWriter, r *http.Request) {
		db.ResetPeakPendingWrites()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status": "ok", "message": "peak reset"}`)
	})
	mux.Handle("/metrics", observability.MetricsHandler())
	return mux
}
