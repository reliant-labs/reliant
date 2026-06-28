// Copyright (c) 2025 Reliant Labs

package servergateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
)

// flowhealth.go — the daemon-gateway's APP-FLOW health endpoint.
//
// GET /flow-health asserts the daemon-connected invariant from the gateway's
// OWN vantage point: the connection registry (daemon_attachment) must have NO
// STALE attachments. The gateway bumps last_stream_activity on every stream
// message + heartbeat (well under a minute), so an attachment whose activity
// is older than the staleness window is a DEAD stream the gateway is looping
// on — exactly the failure mode whose only other symptom is
// `GetFileTree → "unavailable: no daemon connected for user"`.
//
// This is the daemon-flow analogue of /readyz: /readyz proves the gateway can
// serve; /flow-health proves the daemon flow is actually live. `forge smoke`
// curls it and folds the 200/503 into its PASS/FAIL report, so a green smoke
// can no longer hide a broken daemon flow.
//
// PUBLIC / NO-LEAK CONTRACT. The response is STATUS-ONLY: an aggregate count
// ("attachments", "stale") and a healthy/unhealthy status — NO daemon_id,
// user_id, or pod IP. The endpoint is anonymous-safe so smoke can curl it
// without creds. The per-daemon archaeology (which daemon, which user) stays
// in the auth'd `control-plane doctor daemon-flow` CLI, not here.

// flowHealthAttachmentLister is the narrow read the flow-health assertion
// needs: the live attachment registry. Satisfied by *db.Repo.
type flowHealthAttachmentLister interface {
	ListAllDaemonAttachments(ctx context.Context) ([]*db.DaemonAttachment, error)
}

// flowHealthStale bounds how old an attachment's last_stream_activity may be
// before the gateway treats its stream as dead. Matches the daemon-flow CLI's
// 2m window: the gateway bumps activity well under a minute, so 2m is
// generous. Kept here (not configurable) because it's a diagnostic threshold,
// not app config.
const flowHealthStale = 2 * time.Minute

// daemonFlowHealth is the pure assertion: given the live attachments and the
// current time, count the total and the STALE ones. Healthy iff zero stale.
// No I/O — split out so the decision logic is unit-testable without a DB.
func daemonFlowHealth(attachments []*db.DaemonAttachment, staleAfter time.Duration, now time.Time) (ok bool, total, stale int) {
	total = len(attachments)
	for _, att := range attachments {
		if now.Sub(att.LastStreamActivity) > staleAfter {
			stale++
		}
	}
	return stale == 0, total, stale
}

// flowHealthResponse is the STATUS-ONLY public body. No per-entity detail.
type flowHealthResponse struct {
	Status      string `json:"status"` // "healthy" | "unhealthy"
	Check       string `json:"check"`  // "daemon-flow"
	Attachments int    `json:"attachments"`
	Stale       int    `json:"stale"`
	Summary     string `json:"summary"`
}

// flowHealthHandler builds the GET /flow-health handler. It reads the live
// attachment registry, runs the pure assertion, and returns 200 (healthy) /
// 503 (unhealthy/error) with a terse aggregate body.
//
// staleAfter is injectable so a probe can request a TIGHTER window (e.g. the
// `?stale-after=` knob smoke uses to simulate a break in a healthy env);
// absent/invalid falls back to flowHealthStale.
func flowHealthHandler(lister flowHealthAttachmentLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		staleAfter := flowHealthStale
		if v := r.URL.Query().Get("stale-after"); v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				staleAfter = d
			}
		}

		w.Header().Set("Content-Type", "application/json")

		attachments, err := lister.ListAllDaemonAttachments(r.Context())
		if err != nil {
			// Couldn't read the registry — we can't assert the invariant, so
			// fail closed (503) rather than claim healthy.
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(flowHealthResponse{
				Status:  "unhealthy",
				Check:   "daemon-flow",
				Summary: "could not read daemon_attachment registry",
			})
			return
		}

		ok, total, stale := daemonFlowHealth(attachments, staleAfter, time.Now())
		resp := flowHealthResponse{
			Status:      "healthy",
			Check:       "daemon-flow",
			Attachments: total,
			Stale:       stale,
			Summary:     fmt.Sprintf("%d attachment(s), %d stale (stale-after %s)", total, stale, staleAfter),
		}
		if !ok {
			resp.Status = "unhealthy"
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}
