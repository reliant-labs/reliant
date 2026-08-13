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
)

func att(daemonID string, lastActivity time.Time) *db.DaemonAttachment {
	return &db.DaemonAttachment{
		DaemonID:           daemonID,
		UserID:             "user-" + daemonID,
		Source:             db.DaemonAttachmentSourceInbound,
		AttachedAt:         lastActivity,
		LastStreamActivity: lastActivity,
	}
}

func TestDaemonFlowHealth_Pure(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-10 * time.Second)
	stale := now.Add(-5 * time.Minute)

	tests := []struct {
		name        string
		attachments []*db.DaemonAttachment
		wantOK      bool
		wantStale   int
	}{
		{"no attachments is healthy", nil, true, 0},
		{"all fresh is healthy", []*db.DaemonAttachment{att("a", fresh), att("b", fresh)}, true, 0},
		{"one stale is unhealthy", []*db.DaemonAttachment{att("a", fresh), att("b", stale)}, false, 1},
		{"all stale is unhealthy", []*db.DaemonAttachment{att("a", stale), att("b", stale)}, false, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, total, stale := daemonFlowHealth(tc.attachments, flowHealthStale, now)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			if total != len(tc.attachments) {
				t.Errorf("total = %d, want %d", total, len(tc.attachments))
			}
			if stale != tc.wantStale {
				t.Errorf("stale = %d, want %d", stale, tc.wantStale)
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

func TestFlowHealthHandler_Healthy200(t *testing.T) {
	now := time.Now()
	h := flowHealthHandler(fakeLister{attachments: []*db.DaemonAttachment{att("a", now), att("b", now)}})
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodGet, "/flow-health", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var resp flowHealthResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "healthy" || resp.Attachments != 2 || resp.Stale != 0 {
		t.Errorf("unexpected body: %+v", resp)
	}
	// No-leak: no daemon_id / user_id in the public body.
	if strings.Contains(rr.Body.String(), "daemon_id") || strings.Contains(rr.Body.String(), "user_id") || strings.Contains(rr.Body.String(), "user-a") {
		t.Errorf("flow-health leaked per-entity detail: %s", rr.Body.String())
	}
}

func TestFlowHealthHandler_Stale503(t *testing.T) {
	now := time.Now()
	h := flowHealthHandler(fakeLister{attachments: []*db.DaemonAttachment{att("a", now.Add(-10*time.Minute))}})
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodGet, "/flow-health", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
	var resp flowHealthResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Status != "unhealthy" || resp.Stale != 1 {
		t.Errorf("expected unhealthy + 1 stale, got %+v", resp)
	}
}

// TestFlowHealthHandler_StaleAfterKnob proves the ?stale-after= knob: a
// healthy registry (fresh attachment) can be forced UNHEALTHY with a tight
// window — the mechanism smoke uses to simulate a flow break without breaking
// the real flow.
func TestFlowHealthHandler_StaleAfterKnob(t *testing.T) {
	now := time.Now()
	h := flowHealthHandler(fakeLister{attachments: []*db.DaemonAttachment{att("a", now.Add(-1*time.Second))}})
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodGet, "/flow-health?stale-after=1ns", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 with stale-after=1ns, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestFlowHealthHandler_ReadError503(t *testing.T) {
	h := flowHealthHandler(fakeLister{err: errors.New("db down")})
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodGet, "/flow-health", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on read error, got %d", rr.Code)
	}
}
