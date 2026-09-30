// Copyright (c) 2025 Reliant Labs

// Package drain implements the rollout drain sequence shared by reliant's
// three server mains (api-server, daemon-gateway, temporal-worker).
//
// The sequence exists because a pod is removed from EndpointSlices and from
// the GKE NEG ASYNCHRONOUSLY with respect to SIGTERM. A process that closes
// its listener the moment it is signalled is still a routing target for
// several seconds, and every request the LB sends during that window gets
// connection-refused. Flipping readiness first and then WAITING is what gives
// the control plane time to notice.
//
// The order, matching forge's serverkit (pkg/serverkit/run.go, "Graceful
// shutdown sequence"), is:
//
//  1. Begin()          — /ready starts answering 503 "draining"
//  2. sleep PRE_STOP_DELAY, so LBs observe the failure
//  3. ShutdownContext() — stop serving, bounded by SHUTDOWN_TIMEOUT
//  4. the caller's own teardown
//  5. the health server LAST, so /ready keeps answering
//     (with 503) for the whole drain rather than going
//     to connection-refused, which kubelet only treats as
//     a failure after its next probe period.
//
// Step 5 is the one that is easy to get backwards: shutting the health server
// down FIRST looks like "stop advertising ourselves" but actually blinds the
// probe at the exact moment it needs an answer.
package drain

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

const (
	// EnvPreStopDelay and EnvShutdownTimeout are set on every deployed
	// reliant workload by control-plane's render. forge derives the pod's
	// terminationGracePeriodSeconds from them
	// (PRE_STOP_DELAY + SHUTDOWN_TIMEOUT + 5), so a process that ignores
	// them cannot stay inside the grace period it was given.
	EnvPreStopDelay    = "PRE_STOP_DELAY"
	EnvShutdownTimeout = "SHUTDOWN_TIMEOUT"

	// Defaults match serverkit's (forge/pkg/serverkit/serverkit.go
	// applyDefaults). Keeping them identical means a workload that sets
	// neither env var gets the same grace arithmetic whichever binary
	// happens to serve it.
	DefaultPreStopDelay    = 5 * time.Second
	DefaultShutdownTimeout = 30 * time.Second
)

// Drainer carries a process's drain budget and its draining flag.
//
// One per process. It is safe for concurrent use: readiness handlers read the
// flag from request goroutines while the shutdown path sets it.
type Drainer struct {
	preStopDelay    time.Duration
	shutdownTimeout time.Duration
	draining        atomic.Bool
}

// New reads the budget from the environment, falling back to the serverkit
// defaults. An unparseable value is treated as unset rather than fatal: a
// malformed duration must not stop a server from booting, and the default is
// always a safe budget.
func New() *Drainer {
	return NewWithBudget(
		durationFromEnv(EnvPreStopDelay, DefaultPreStopDelay),
		durationFromEnv(EnvShutdownTimeout, DefaultShutdownTimeout),
	)
}

// NewWithBudget builds a Drainer with an explicit budget. Used by tests, which
// need millisecond windows rather than the production tens of seconds.
func NewWithBudget(preStopDelay, shutdownTimeout time.Duration) *Drainer {
	return &Drainer{preStopDelay: preStopDelay, shutdownTimeout: shutdownTimeout}
}

func durationFromEnv(name string, fallback time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return fallback
	}
	return d
}

// PreStopDelay is how long the process keeps serving after readiness flips.
func (d *Drainer) PreStopDelay() time.Duration { return d.preStopDelay }

// ShutdownTimeout bounds everything after the pre-stop pause.
func (d *Drainer) ShutdownTimeout() time.Duration { return d.shutdownTimeout }

// Draining reports whether the process has started shutting down.
func (d *Drainer) Draining() bool { return d.draining.Load() }

// Begin flips readiness to failing and then blocks for PreStopDelay so load
// balancers observe the failing probe before anything stops serving. The
// listener stays open for the whole pause — that is the point: requests
// already in flight, and requests the LB has not yet stopped sending, are
// still answered normally.
func (d *Drainer) Begin() {
	d.draining.Store(true)
	if d.preStopDelay > 0 {
		time.Sleep(d.preStopDelay)
	}
}

// ShutdownContext returns the context that bounds every teardown step after
// the pre-stop pause. Derived from context.Background, not from the run
// context, because the run context is typically already cancelled by the
// signal that started the drain.
func (d *Drainer) ShutdownContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d.shutdownTimeout)
}

// ReadinessCheck is one named dependency probe contributing to /ready.
type ReadinessCheck struct {
	Name  string
	Check func(ctx context.Context) error
}

// ReadinessHandler builds the /ready handler for a service.
//
// The draining flag is checked FIRST and short-circuits: once the process is
// shutting down, no dependency probe can make it ready again, and running them
// would only add latency to a probe whose answer is already decided.
func (d *Drainer) ReadinessHandler(service string, checks ...ReadinessCheck) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if d.Draining() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":  "draining",
				"service": service,
			})
			return
		}

		var failures []string
		for _, c := range checks {
			if c.Check == nil {
				continue
			}
			if err := c.Check(r.Context()); err != nil {
				failures = append(failures, c.Name+": "+err.Error())
			}
		}

		if len(failures) > 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":  "not_ready",
				"service": service,
				"reason":  failures,
			})
			return
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "ready",
			"service": service,
		})
	}
}

// ShutdownHealthServer stops the health server, LAST in the drain sequence.
//
// It gets its own short budget rather than the caller's remaining shutdown
// context: by the time this runs the shutdown context may already be expired
// (that is the normal outcome of a slow drain), and passing an expired context
// to Shutdown skips the graceful close of a server whose only remaining
// clients are probe requests that take microseconds.
func ShutdownHealthServer(srv *http.Server) error {
	if srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}
