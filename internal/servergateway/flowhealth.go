// Copyright (c) 2025 Reliant Labs

package servergateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/grpc/services"
)

// flowhealth.go — the daemon-gateway's APP-FLOW health endpoint.
//
// GET /flow-health asserts the daemon-connected invariant from the gateway's
// OWN vantage point: for every daemon stream THIS process currently holds,
// the connection registry (daemon_attachment) must still vouch for that
// daemon. The registry lease (last_stream_activity) is renewed ONLY by
// INBOUND traffic — the heartbeat handler's direct TouchDaemonAttachmentIfNewer
// and the daemonstate activity events, both in handleIncoming — while the
// in-memory conn.lastActivity ALSO bumps on our own sends (runSender). So a
// held stream whose registry row has gone stale means nothing is arriving
// from that daemon even though the gateway is still talking to it: a dead
// stream the gateway is looping on, whose only other symptom is
// `GetFileTree → "unavailable: no daemon connected for user"` (readers gate
// on the same 90s–2m lease window).
//
// SCOPE — WHY NOT "the whole table has no stale rows". That was the original
// assertion and it was unachievable by construction. daemon_attachment rows
// are deleted only on graceful teardown (teardownConnection), so any gateway
// that crashes, is rescheduled, or is redeployed strands every row it owned,
// forever. Dev's registry on 2026-08-24 held exactly that: one live daemon
// (last_stream_activity ~5s old) plus 0c9cff04 (Seans-MacBook-Pro-2.local,
// 29 days stale) and 81dc53c1 (ws-ws-81dc53c1, a workspace pod deleted 51
// days earlier). Two rows nobody ever cleaned up pinned /flow-health at 503
// for the entire environment, permanently — and because `forge env smoke`
// folds that 503 into PASS/FAIL, smoke was red for a reason that had nothing
// to do with daemon flow. A check that can never go green is a check people
// stop reading, so the assertion now covers only what this process can be
// held responsible for: the streams it is holding right now. An orphan row is
// GARBAGE, not evidence — it is counted for context and never a fault. (The
// garbage itself is now collected at the source: daemonstate.Derivation runs
// a TTL reaper over the table. See derivation.go.)
//
// MULTI-REPLICA. Each gateway asserts over its own connection map, so a
// replica holding no streams asserts nothing (status "undetermined", 200 —
// see below) and never reports on a peer's daemons. That is deliberate: the
// faults below are ways THIS process can be lying, and no replica can observe
// another's memory. The union over replicas is the whole truth.
//
// This is the daemon-flow analogue of /readyz: /readyz proves the gateway can
// serve; /flow-health proves the daemon flow is actually live. `forge smoke`
// curls it and folds the 200/503 into its PASS/FAIL report, so a green smoke
// can no longer hide a broken daemon flow.
//
// PUBLIC / NO-LEAK CONTRACT. The response is STATUS-ONLY: aggregate counts
// and a status — NO daemon_id, user_id, or pod IP. The endpoint is
// anonymous-safe so smoke can curl it without creds. The per-daemon
// archaeology (which daemon, which user) stays in the auth'd
// `control-plane doctor daemon-flow` CLI, not here.

// flowHealthAttachmentLister is the narrow read the flow-health assertion
// needs: the attachment registry. Satisfied by *db.Repo.
type flowHealthAttachmentLister interface {
	ListAllDaemonAttachments(ctx context.Context) ([]*db.DaemonAttachment, error)
}

// flowHealthStreamSource is the gateway's own answer to "which daemons do I
// believe I am serving right now". Satisfied by *services.ToolsDaemonService.
// Without it the endpoint cannot tell a daemon that SHOULD be connected from
// a row nobody cleaned up — the distinction the whole check rests on.
type flowHealthStreamSource interface {
	HeldDaemonStreams() []services.HeldDaemonStream
}

const (
	// flowHealthStale bounds how old a held stream's registry lease may be
	// before the gateway treats that stream as dead. Matches the daemon-flow
	// CLI's 2m window and comfortably exceeds the 15s daemon heartbeat that
	// renews the lease (8 missed heartbeats). Kept here (not app config)
	// because it's a diagnostic threshold. Overridable per-request via
	// ?stale-after=.
	flowHealthStale = 2 * time.Minute

	// flowHealthAttachGrace is how long a stream must have been held before
	// it counts as evidence. ConnectDaemon/RegisterOutboundConnection upsert
	// the row BEFORE registering the connection, so the gap is normally
	// sub-millisecond; the grace exists so a probe that lands mid-attach
	// (or right after a reconnect whose derivation event is still in flight)
	// cannot manufacture a fault out of a race.
	flowHealthAttachGrace = 30 * time.Second

	// flowHealthUnreapedAfter is how long a held stream may be silent in
	// BOTH directions before its continued presence in the connections map
	// is itself the fault. ToolsDaemonService's sweeper reaps at
	// staleConnectionThreshold (90s) on a staleConnectionSweepInterval (60s)
	// tick — worst case ~150s — so 5m is ~2x that ceiling. Below it we are
	// inside the sweeper's own grace band and must NOT report a fault, or
	// every ordinary disconnect would flap the endpoint red for 30s on its
	// way to being cleaned up.
	flowHealthUnreapedAfter = 5 * time.Minute
)

// flowHealthResult is the outcome of the pure assertion. Stalled+Unreaped are
// the only fault counts; Registry/Held/Asserted are context.
type flowHealthResult struct {
	// Registry is the total number of daemon_attachment rows. Context only:
	// rows this gateway does not hold a stream for are NEVER a fault (see
	// SCOPE above).
	Registry int
	// Held is how many daemon streams this gateway currently holds.
	Held int
	// Asserted is how many of those were held longer than
	// flowHealthAttachGrace — i.e. how many the check actually ruled on.
	Asserted int
	// Stalled counts asserted streams the registry no longer vouches for
	// (row missing, or lease older than staleAfter) while the gateway is
	// still exchanging messages with them. FAULT: readers see "no daemon
	// connected" for a daemon this process is actively serving.
	Stalled int
	// Unreaped counts held streams silent in both directions for longer than
	// flowHealthUnreapedAfter. FAULT: the stale-connection sweeper should
	// have torn these down and hasn't.
	Unreaped int
}

func (r flowHealthResult) faults() int { return r.Stalled + r.Unreaped }

// status collapses the result to the public status string.
//   - unhealthy  → at least one attributable fault.
//   - undetermined → nothing to assert (no stream held past the grace). NOT a
//     fault: an environment where nobody happens to be running a daemon is
//     not broken, and asserting "some daemon must be attached" is exactly the
//     unachievable assertion this file used to make.
//   - healthy    → every stream this gateway holds is vouched for.
func (r flowHealthResult) status() string {
	switch {
	case r.faults() > 0:
		return "unhealthy"
	case r.Asserted == 0:
		return "undetermined"
	default:
		return "healthy"
	}
}

// daemonFlowHealth is the pure assertion: given the streams this gateway
// holds, the registry rows, and the current time, classify each held stream.
// No I/O — split out so the decision logic is unit-testable without a DB.
//
// staleAfter applies to the REGISTRY side only. The in-memory liveness side
// keeps its fixed windows on purpose: ?stale-after=1ns must be able to force
// a fault (that is what it is for), and a knob that tightened both sides at
// once would cancel itself out — every stream would look dead in memory too,
// landing in the sweeper's grace band instead of reporting a fault.
func daemonFlowHealth(held []services.HeldDaemonStream, registry []*db.DaemonAttachment, staleAfter time.Duration, now time.Time) flowHealthResult {
	res := flowHealthResult{Registry: len(registry), Held: len(held)}

	leases := make(map[string]time.Time, len(registry))
	for _, att := range registry {
		if att == nil {
			continue
		}
		leases[att.DaemonID] = att.LastStreamActivity
	}

	for _, h := range held {
		lastActivity := h.LastActivity
		if lastActivity.IsZero() {
			// A snapshot with no activity stamp would otherwise read as
			// "silent since the epoch" and manufacture a fault — the exact
			// class of false 503 this endpoint exists to stop telling.
			lastActivity = h.ConnectedAt
		}
		silentFor := now.Sub(lastActivity)
		if silentFor > flowHealthUnreapedAfter {
			// Dead in both directions and still registered: the sweeper is
			// not doing its job. Counted regardless of the attach grace — a
			// stream cannot be both brand-new and silent for 5 minutes.
			res.Unreaped++
			continue
		}
		if now.Sub(h.ConnectedAt) < flowHealthAttachGrace {
			continue // too young to be evidence about anything
		}
		res.Asserted++
		if silentFor > flowHealthStale {
			// Inside the sweeper's grace band: it will be torn down shortly
			// and the row deleted with it. Not a fault, and not proof of
			// health either — but Asserted already counts it, which is
			// honest: we ruled on it and found nothing wrong yet.
			continue
		}
		lease, ok := leases[h.DaemonID]
		if !ok || now.Sub(lease) > staleAfter {
			res.Stalled++
		}
	}
	return res
}

// flowHealthSummary renders the one-line human verdict.
//
// Kept SHORT on purpose: `forge env smoke` echoes the body into its detail
// column and truncates it at 160 characters, so a summary that re-states the
// numeric fields would push itself off the end of the line an operator
// actually reads. It says what the counts mean, not what they are. The
// stale-after window is named only when a probe overrode it — otherwise it is
// a constant and pure noise.
func flowHealthSummary(res flowHealthResult, staleAfter time.Duration) string {
	var s string
	switch res.status() {
	case "unhealthy":
		s = fmt.Sprintf("%d stalled, %d unreaped of %d held stream(s)", res.Stalled, res.Unreaped, res.Held)
	case "undetermined":
		s = "no daemon stream to assert on"
	default:
		s = fmt.Sprintf("%d/%d held stream(s) vouched for", res.Asserted, res.Held)
	}
	if staleAfter != flowHealthStale {
		s += fmt.Sprintf(" (stale-after %s)", staleAfter)
	}
	return s
}

// flowHealthResponse is the STATUS-ONLY public body. No per-entity detail.
type flowHealthResponse struct {
	Status      string `json:"status"` // "healthy" | "unhealthy" | "undetermined"
	Check       string `json:"check"`  // "daemon-flow"
	Attachments int    `json:"attachments"`
	Held        int    `json:"held"`
	Asserted    int    `json:"asserted"`
	Stale       int    `json:"stale"`
	Unreaped    int    `json:"unreaped"`
	Summary     string `json:"summary"`
}

// flowHealthHandler builds the GET /flow-health handler. It snapshots the
// streams this gateway holds, reads the attachment registry, runs the pure
// assertion, and returns 200 (healthy/undetermined) / 503 (unhealthy) with a
// terse aggregate body.
//
// staleAfter is injectable so a probe can request a TIGHTER registry window
// (the `?stale-after=` knob smoke uses to simulate a break in a healthy env);
// absent/invalid falls back to flowHealthStale. Note the knob can only force
// a fault when this gateway actually holds a stream — with nothing held there
// is nothing to assert, by design.
func flowHealthHandler(lister flowHealthAttachmentLister, streams flowHealthStreamSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		staleAfter := flowHealthStale
		if v := r.URL.Query().Get("stale-after"); v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				staleAfter = d
			}
		}

		w.Header().Set("Content-Type", "application/json")

		registry, err := lister.ListAllDaemonAttachments(r.Context())
		if err != nil {
			// Fail closed. This is NOT "undetermined": every reader of the
			// daemon flow (IsDaemonAttached, ListFreshDaemonAttachmentsForUser)
			// goes through the same table, so a registry we cannot read is a
			// flow that is genuinely broken for users — a true unhealthy, not
			// an ambiguity. /ready's DB ping fires alongside it and names the
			// real cause.
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(flowHealthResponse{
				Status:  "unhealthy",
				Check:   "daemon-flow",
				Summary: "could not read daemon_attachment registry",
			})
			return
		}

		var held []services.HeldDaemonStream
		if streams != nil {
			held = streams.HeldDaemonStreams()
		}

		res := daemonFlowHealth(held, registry, staleAfter, time.Now())
		resp := flowHealthResponse{
			Status:      res.status(),
			Check:       "daemon-flow",
			Attachments: res.Registry,
			Held:        res.Held,
			Asserted:    res.Asserted,
			Stale:       res.Stalled,
			Unreaped:    res.Unreaped,
			Summary:     flowHealthSummary(res, staleAfter),
		}
		if resp.Status == "unhealthy" {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}
