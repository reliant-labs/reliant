// Copyright (c) 2025 Reliant Labs

package servergateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/grpc/services"
)

// att builds a registry row (daemon_attachment) whose reachability lease was
// last renewed at lastActivity.
func att(daemonID string, lastActivity time.Time) *db.DaemonAttachment {
	return &db.DaemonAttachment{
		DaemonID:           daemonID,
		UserID:             "user-" + daemonID,
		Source:             db.DaemonAttachmentSourceInbound,
		AttachedAt:         lastActivity,
		LastStreamActivity: lastActivity,
	}
}

// held builds a snapshot of a stream this gateway holds. connectedAt is
// deliberately separate from lastActivity: a stream can be hours old and
// silent, or seconds old and chatty, and the assertion treats those
// differently.
func held(daemonID string, connectedAt, lastActivity time.Time) services.HeldDaemonStream {
	return services.HeldDaemonStream{
		DaemonID:     daemonID,
		ConnectedAt:  connectedAt,
		LastActivity: lastActivity,
	}
}

func TestDaemonFlowHealth_Pure(t *testing.T) {
	t.Parallel()
	now := time.Now()
	long := now.Add(-2 * time.Hour)         // connected long enough to be evidence
	fresh := now.Add(-10 * time.Second)     // lease renewed / stream exchanging
	staleLease := now.Add(-5 * time.Minute) // lease older than flowHealthStale

	tests := []struct {
		name       string
		held       []services.HeldDaemonStream
		registry   []*db.DaemonAttachment
		wantStatus string
		wantStale  int
		wantUnreap int
	}{
		{
			// Nothing connected anywhere. Not a fault: an environment where
			// nobody happens to be running a daemon is not broken.
			name: "empty everything is undetermined", wantStatus: "undetermined",
		},
		{
			name:       "held stream with a fresh lease is healthy",
			held:       []services.HeldDaemonStream{held("a", long, fresh)},
			registry:   []*db.DaemonAttachment{att("a", fresh)},
			wantStatus: "healthy",
		},
		{
			// THE fault: the gateway is still exchanging messages with this
			// daemon (fresh in-memory activity, which our own sends bump)
			// while nothing has arrived from it to renew the lease. Readers
			// see "no daemon connected" for a daemon we claim to serve.
			name:       "held stream whose lease went stale is unhealthy",
			held:       []services.HeldDaemonStream{held("a", long, fresh)},
			registry:   []*db.DaemonAttachment{att("a", staleLease)},
			wantStatus: "unhealthy", wantStale: 1,
		},
		{
			// Same fault, harsher shape: the row is gone entirely (dropped
			// connect event, or reaped by the TTL after a long stall).
			name:       "held stream with no registry row at all is unhealthy",
			held:       []services.HeldDaemonStream{held("a", long, fresh)},
			wantStatus: "unhealthy", wantStale: 1,
		},
		{
			// The 2026-08-24 dev registry, verbatim: one live daemon plus
			// 0c9cff04 (29 days) and 81dc53c1 (51 days, a workspace pod
			// deleted long ago). Rows nobody cleaned up are garbage, not
			// evidence — this used to pin the endpoint at 503 forever.
			name: "long-abandoned orphan rows never make it unhealthy",
			held: []services.HeldDaemonStream{held("live", long, fresh)},
			registry: []*db.DaemonAttachment{
				att("live", fresh),
				att("0c9cff04", now.Add(-29*24*time.Hour)),
				att("81dc53c1", now.Add(-51*24*time.Hour)),
			},
			wantStatus: "healthy",
		},
		{
			// Orphans with nothing held: still nothing to assert.
			name: "orphan rows alone are undetermined, not unhealthy",
			registry: []*db.DaemonAttachment{
				att("0c9cff04", now.Add(-29*24*time.Hour)),
				att("81dc53c1", now.Add(-51*24*time.Hour)),
			},
			wantStatus: "undetermined",
		},
		{
			// Silent in both directions but inside the stale-connection
			// sweeper's own worst-case reap latency (~150s). It will be torn
			// down shortly; flapping red on the way there is noise.
			name:       "silence inside the sweeper grace band is not a fault",
			held:       []services.HeldDaemonStream{held("a", long, now.Add(-3*time.Minute))},
			registry:   []*db.DaemonAttachment{att("a", now.Add(-3*time.Minute))},
			wantStatus: "healthy",
		},
		{
			// Silent past every window the sweeper has: the sweeper itself
			// is not doing its job.
			name:       "held stream silent past the sweeper ceiling is unhealthy",
			held:       []services.HeldDaemonStream{held("a", long, now.Add(-10*time.Minute))},
			registry:   []*db.DaemonAttachment{att("a", now.Add(-10*time.Minute))},
			wantStatus: "unhealthy", wantUnreap: 1,
		},
		{
			// Mid-attach: the row write precedes registration, but a probe
			// landing in the gap must not manufacture a fault.
			name:       "a stream younger than the attach grace is not evidence",
			held:       []services.HeldDaemonStream{held("a", now.Add(-2*time.Second), now)},
			wantStatus: "undetermined",
		},
		{
			name: "one healthy stream and one stalled stream is unhealthy",
			held: []services.HeldDaemonStream{held("a", long, fresh), held("b", long, fresh)},
			registry: []*db.DaemonAttachment{
				att("a", fresh),
				att("b", staleLease),
			},
			wantStatus: "unhealthy", wantStale: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := daemonFlowHealth(tc.held, tc.registry, flowHealthStale, now)
			if got := res.status(); got != tc.wantStatus {
				t.Errorf("status = %q, want %q (%+v)", got, tc.wantStatus, res)
			}
			if res.Stalled != tc.wantStale {
				t.Errorf("stalled = %d, want %d", res.Stalled, tc.wantStale)
			}
			if res.Unreaped != tc.wantUnreap {
				t.Errorf("unreaped = %d, want %d", res.Unreaped, tc.wantUnreap)
			}
			if res.Registry != len(tc.registry) || res.Held != len(tc.held) {
				t.Errorf("registry/held = %d/%d, want %d/%d", res.Registry, res.Held, len(tc.registry), len(tc.held))
			}
		})
	}
}

type fakeLister struct {
	attachments []*db.DaemonAttachment
	err         error
}

func (f fakeLister) ListAllDaemonAttachments(context.Context) ([]*db.DaemonAttachment, error) {
	return f.attachments, f.err
}

type fakeStreams struct {
	streams []services.HeldDaemonStream
}

func (f fakeStreams) HeldDaemonStreams() []services.HeldDaemonStream { return f.streams }

func decodeFlowHealth(t *testing.T, rr *httptest.ResponseRecorder) flowHealthResponse {
	t.Helper()
	var resp flowHealthResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, rr.Body.String())
	}
	return resp
}

func serveFlowHealth(t *testing.T, lister flowHealthAttachmentLister, streams flowHealthStreamSource, query string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	flowHealthHandler(lister, streams)(rr, httptest.NewRequest(http.MethodGet, "/flow-health"+query, nil))
	return rr
}

func TestFlowHealthHandler_Healthy200(t *testing.T) {
	t.Parallel()
	now := time.Now()
	long := now.Add(-time.Hour)
	rr := serveFlowHealth(t,
		fakeLister{attachments: []*db.DaemonAttachment{att("a", now), att("b", now)}},
		fakeStreams{streams: []services.HeldDaemonStream{held("a", long, now), held("b", long, now)}},
		"")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	resp := decodeFlowHealth(t, rr)
	if resp.Status != "healthy" || resp.Attachments != 2 || resp.Held != 2 || resp.Asserted != 2 || resp.Stale != 0 {
		t.Errorf("unexpected body: %+v", resp)
	}
	// No-leak: no daemon_id / user_id in the public body.
	if strings.Contains(rr.Body.String(), "daemon_id") || strings.Contains(rr.Body.String(), "user_id") || strings.Contains(rr.Body.String(), "user-a") {
		t.Errorf("flow-health leaked per-entity detail: %s", rr.Body.String())
	}
}

// TestFlowHealthHandler_OrphanRowsStayGreen is the regression test for the
// bug this endpoint shipped with. Dev's registry carried a live daemon plus
// two rows whose gateways were long gone (29 and 51 days). The check asserted
// over EVERY row in the table, so those two orphans held the whole
// environment at 503 permanently and dragged `forge env smoke` red with it.
func TestFlowHealthHandler_OrphanRowsStayGreen(t *testing.T) {
	t.Parallel()
	now := time.Now()
	long := now.Add(-time.Hour)
	rr := serveFlowHealth(t,
		fakeLister{attachments: []*db.DaemonAttachment{
			att("42906ac5", now.Add(-5*time.Second)),   // live, lease renewed
			att("0c9cff04", now.Add(-29*24*time.Hour)), // laptop daemon, gateway gone
			att("81dc53c1", now.Add(-51*24*time.Hour)), // deleted workspace pod
		}},
		fakeStreams{streams: []services.HeldDaemonStream{held("42906ac5", long, now.Add(-5*time.Second))}},
		"")

	if rr.Code != http.StatusOK {
		t.Fatalf("orphan rows must not fail the check: got %d (%s)", rr.Code, rr.Body.String())
	}
	resp := decodeFlowHealth(t, rr)
	if resp.Status != "healthy" || resp.Stale != 0 || resp.Unreaped != 0 {
		t.Errorf("expected healthy with no faults, got %+v", resp)
	}
	if resp.Attachments != 3 || resp.Held != 1 {
		t.Errorf("expected the orphans reported as context (3 rows, 1 held), got %+v", resp)
	}
}

// TestFlowHealthHandler_StaleLiveAttachment503 is the fault the check exists
// for: a daemon this gateway is actively serving whose lease has stopped
// being renewed — inbound traffic has died while we keep talking into it.
func TestFlowHealthHandler_StaleLiveAttachment503(t *testing.T) {
	t.Parallel()
	now := time.Now()
	rr := serveFlowHealth(t,
		fakeLister{attachments: []*db.DaemonAttachment{att("a", now.Add(-10*time.Minute))}},
		fakeStreams{streams: []services.HeldDaemonStream{held("a", now.Add(-time.Hour), now)}},
		"")

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d (%s)", rr.Code, rr.Body.String())
	}
	resp := decodeFlowHealth(t, rr)
	if resp.Status != "unhealthy" || resp.Stale != 1 {
		t.Errorf("expected unhealthy + 1 stalled, got %+v", resp)
	}
}

// TestFlowHealthHandler_EmptyRegistry200 pins the empty-table answer:
// UNDETERMINED, 200. An environment with no daemon attached is not broken —
// it is unexercised, and there is nothing for the gateway to be wrong about.
// The alternative ("at least one daemon must be attached") is precisely the
// unachievable assertion that made this endpoint permanently red.
func TestFlowHealthHandler_EmptyRegistry200(t *testing.T) {
	t.Parallel()
	rr := serveFlowHealth(t, fakeLister{}, fakeStreams{}, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 on an empty registry, got %d (%s)", rr.Code, rr.Body.String())
	}
	resp := decodeFlowHealth(t, rr)
	if resp.Status != "undetermined" || resp.Asserted != 0 {
		t.Errorf("expected undetermined with nothing asserted, got %+v", resp)
	}
}

// TestFlowHealthHandler_StaleAfterKnob proves the ?stale-after= knob: a
// healthy gateway (held stream, fresh lease) can be forced UNHEALTHY with a
// tight window — the mechanism smoke uses to simulate a flow break without
// breaking the real flow. The knob tightens the REGISTRY window only; if it
// also tightened the in-memory window the stream would look dead to the
// sweeper instead and the knob would cancel itself out.
func TestFlowHealthHandler_StaleAfterKnob(t *testing.T) {
	t.Parallel()
	now := time.Now()
	rr := serveFlowHealth(t,
		fakeLister{attachments: []*db.DaemonAttachment{att("a", now.Add(-1*time.Second))}},
		fakeStreams{streams: []services.HeldDaemonStream{held("a", now.Add(-time.Hour), now)}},
		"?stale-after=1ns")

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 with stale-after=1ns, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// TestFlowHealthHandler_ReadError503 keeps the read failure fail-CLOSED. It
// is not "undetermined": every reader of the daemon flow goes through the
// same table, so a registry we cannot read is a flow that is genuinely broken
// for users.
func TestFlowHealthHandler_ReadError503(t *testing.T) {
	t.Parallel()
	rr := serveFlowHealth(t, fakeLister{err: errors.New("db down")}, fakeStreams{}, "")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on read error, got %d", rr.Code)
	}
}

// TestFlowHealthHandler_NilStreamSource keeps the handler total: a nil source
// asserts nothing rather than panicking on a health probe.
func TestFlowHealthHandler_NilStreamSource(t *testing.T) {
	t.Parallel()
	rr := serveFlowHealth(t, fakeLister{attachments: []*db.DaemonAttachment{att("a", time.Now())}}, nil, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if resp := decodeFlowHealth(t, rr); resp.Status != "undetermined" {
		t.Errorf("expected undetermined, got %+v", resp)
	}
}

// TestFlowHealthBodyFitsSmokeDetail keeps the public body inside the 160-char
// window `forge env smoke` renders in its detail column (flowBodySummary
// truncates past that). A body that overflows loses its tail — the part an
// operator reads to find out WHY — so the length is part of the contract.
func TestFlowHealthBodyFitsSmokeDetail(t *testing.T) {
	t.Parallel()
	now := time.Now()
	long := now.Add(-time.Hour)
	cases := map[string]*httptest.ResponseRecorder{
		"healthy": serveFlowHealth(t,
			fakeLister{attachments: []*db.DaemonAttachment{att("a", now), att("b", now.Add(-29*24*time.Hour)), att("c", now.Add(-51*24*time.Hour))}},
			fakeStreams{streams: []services.HeldDaemonStream{held("a", long, now)}}, ""),
		"unhealthy": serveFlowHealth(t,
			fakeLister{attachments: []*db.DaemonAttachment{att("a", now.Add(-10*time.Minute))}},
			fakeStreams{streams: []services.HeldDaemonStream{held("a", long, now)}}, ""),
		"undetermined": serveFlowHealth(t, fakeLister{}, fakeStreams{}, ""),
	}
	for name, rr := range cases {
		body := strings.TrimSpace(rr.Body.String())
		if len(body) > 160 {
			t.Errorf("%s body is %d chars, over smoke's 160-char detail window: %s", name, len(body), body)
		}
	}
}
