package serverkit

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// readiness.go — GET /readyz.
//
// /readyz answers ONE question, and it is not "is this process alive":
// can this replica serve a request RIGHT NOW. A rolling deploy takes that
// answer literally. It marks the new pod Ready, shifts traffic to it, and
// proceeds to the next replica — so a /readyz that cannot be wrong is a
// deploy that cannot be stopped.
//
// It used to be unconditional: one atomic.Bool set the moment net.Listen
// returned, and thereafter a static "ok". With the database unreachable,
// every RPC returned 500 while /readyz kept answering 200, and a rollout
// carrying a bad DB secret would report Ready on every replica and reach
// 100% with green probes the whole way.
//
// So /readyz now runs the dependency checks the composition root
// registered (AddReadyCheck) on every request:
//
//	200  listener bound AND every registered check passed
//	503  not yet bound / draining, or any check failed
//
// /healthz stays LIVENESS-only — a static "ok". The distinction is load
// bearing: a liveness probe that fails on a database outage makes the
// kubelet restart every replica of a service whose only problem is
// downstream, turning a recoverable dependency incident into a crash loop.
//
// PUBLIC / NO-LEAK CONTRACT. Probes bypass the edge (no CORS, no auth), and
// nothing guarantees the route is unreachable from outside the cluster, so
// the body NAMES the failing dependency and stops there — no error text, no
// dial string, no host. `{"status":"not_ready","checks":[{"name":"database",
// "ok":false}]}` is enough for an operator to know where to look; the
// underlying error is logged server-side, where the same operator can read
// it and an attacker cannot.
//
// LOGGING IS PER TRANSITION, NOT PER PROBE. A failing check is logged once,
// at WARN, when it starts failing, and once, at INFO, when it recovers. The
// probe runs every few seconds on every replica, so a line per failing probe
// turned one dependency outage into a flood at probe frequency — into the
// log pipeline and, at the ERROR it used to be, into the error tracker.
// Never ERROR: a readiness check failing is the probe doing its job (the
// replica leaves rotation), not a fault in this process. While a check stays
// failing, each probe's error is at DEBUG.

// ReadyCheck is one DEPENDENCY probe evaluated on every /readyz request.
//
// Check must be CHEAP — it runs on the kubelet's probe interval, every few
// seconds, forever. A connection-pool ping is the intended shape. Anything
// that touches application data, iterates rows, or calls a third party
// belongs in a FlowCheck (/flow-health), which is polled deliberately
// rather than continuously.
//
// A non-nil error means NOT READY. The error is logged server-side (at WARN
// when the check starts failing) and never returned to the caller; only
// Name appears in the response.
type ReadyCheck struct {
	// Name identifies the dependency in the /readyz JSON ("database",
	// "cache", "queue"). It is the only per-check detail that goes out.
	Name string
	// Check probes the dependency. It receives a context already bounded
	// by Config.ReadinessTimeout and must respect it.
	Check func(ctx context.Context) error
}

// AddReadyCheck registers a dependency probe surfaced at /readyz. A check
// with a nil Check func or an empty Name is ignored, so a composition root
// can pass a conditional builder's result — including DBReadyCheck on a
// nil pool — without a guard.
func (s *Server) AddReadyCheck(c ReadyCheck) {
	if c.Check == nil || c.Name == "" {
		return
	}
	s.ReadyChecks = append(s.ReadyChecks, c)
}

// DBReadyCheck returns a ReadyCheck that pings a database pool.
//
// This is the check whose absence made /readyz a lie. db.PingContext takes
// a connection from the pool and round-trips it, so it fails when the
// server is unreachable, when credentials are wrong, and when the pool is
// exhausted — every way "this replica cannot serve" actually happens.
//
// A nil pool returns the zero ReadyCheck, which AddReadyCheck drops. That
// is deliberate: a project with no DATABASE_URL configured has no database
// dependency to be unready about, and a nil-pool check that reported
// FAILURE would make such a process permanently unroutable.
func DBReadyCheck(name string, db *sql.DB) ReadyCheck {
	if db == nil {
		return ReadyCheck{}
	}
	return ReadyCheck{
		Name:  name,
		Check: db.PingContext,
	}
}

// readyzResponse is the JSON body of /readyz. Additive: new fields may
// appear; existing keys keep their meaning.
type readyzResponse struct {
	Status string              `json:"status"` // "ready" | "not_ready" | "draining"
	Checks []readyzCheckStatus `json:"checks,omitempty"`
}

// readyzCheckStatus is one dependency's public status: its NAME and
// whether it passed. Never the error — see the no-leak contract above.
// Detail is a fixed word from serverkit's own vocabulary (the boot probes'
// "building"), never a message from the dependency.
type readyzCheckStatus struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// readyzHandler builds the GET /readyz handler (Drainer.ReadinessHandler).
//
// gate is the listener-bound / not-draining gate; when it is closed the
// checks are skipped entirely, because a process that has not bound or is
// already draining is not ready regardless of what its dependencies say
// (and probing them during shutdown only slows the drain).
//
// All checks share ONE deadline rather than getting one each, so the
// endpoint's worst-case latency stays bounded as checks are added — a
// probe with a timeoutSeconds cannot be asked to wait N × timeout.
func readyzHandler(gate *Drainer, checks []ReadyCheck, timeout time.Duration, logger *slog.Logger) http.HandlerFunc {
	return readyzHandlerFunc(gate, func() []ReadyCheck { return checks }, timeout, logger)
}

// readyzHandlerFunc is readyzHandler over a check set read per request, for
// Probes, whose checks can grow after the handler is built. Each check's
// transition state is keyed by its position, which is stable because the set
// only ever grows at the end.
func readyzHandlerFunc(gate *Drainer, source func() []ReadyCheck, timeout time.Duration, logger *slog.Logger) http.HandlerFunc {
	var (
		statesMu sync.Mutex
		states   []*checkState
	)
	stateFor := func(i int) *checkState {
		statesMu.Lock()
		defer statesMu.Unlock()
		for len(states) <= i {
			states = append(states, &checkState{})
		}
		return states[i]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		checks := source()
		if gate.Draining() {
			writeReadyz(w, http.StatusServiceUnavailable, readyzResponse{Status: "draining"})
			return
		}
		if !gate.Ready() {
			writeReadyz(w, http.StatusServiceUnavailable, readyzResponse{Status: "not_ready"})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		resp := readyzResponse{Status: "ready"}
		allOK := true
		for i, c := range checks {
			err := c.Check(ctx)
			if err != nil {
				allOK = false
			}
			// The operator's copy of the truth. The client's copy is the
			// check name and `"ok": false`.
			stateFor(i).observe(ctx, logger, c.Name, err)
			resp.Checks = append(resp.Checks, readyzCheckStatus{Name: c.Name, OK: err == nil})
		}

		if !allOK {
			resp.Status = "not_ready"
			writeReadyz(w, http.StatusServiceUnavailable, resp)
			return
		}
		writeReadyz(w, http.StatusOK, resp)
	}
}

// checkState is one check's last outcome, so only a change is logged.
// Concurrent probes race on it; the swap makes exactly one of them log each
// transition.
type checkState struct {
	failing atomic.Bool
	since   atomic.Int64 // UnixNano the current failure began
}

// observe logs err if it changes the check's state: WARN on the first
// failure, INFO on the recovery (with how long it failed), DEBUG for a
// failure that continues.
func (s *checkState) observe(ctx context.Context, logger *slog.Logger, name string, err error) {
	if err != nil {
		if s.failing.CompareAndSwap(false, true) {
			s.since.Store(time.Now().UnixNano())
			logger.WarnContext(ctx, "readiness check failing; this replica is not ready until it passes",
				"check", name, "error", err)
			return
		}
		logger.DebugContext(ctx, "readiness check still failing", "check", name, "error", err)
		return
	}
	if s.failing.CompareAndSwap(true, false) {
		logger.InfoContext(ctx, "readiness check recovered", "check", name,
			"failed_for", time.Since(time.Unix(0, s.since.Load())).Round(time.Millisecond))
	}
}

func writeReadyz(w http.ResponseWriter, status int, resp readyzResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}
