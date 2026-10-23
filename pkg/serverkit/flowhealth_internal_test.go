package serverkit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFlowHealthHandler_AllPass200(t *testing.T) {
	h := flowHealthHandler([]FlowCheck{
		{Name: "daemon-flow", Check: func(context.Context) FlowResult {
			return FlowResult{OK: true, Summary: "2 daemons, 0 unattached"}
		}},
	})
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodGet, "/flow-health", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp flowHealthResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "healthy" || len(resp.Checks) != 1 || !resp.Checks[0].OK {
		t.Errorf("unexpected body: %+v", resp)
	}
	if resp.Checks[0].Summary != "2 daemons, 0 unattached" {
		t.Errorf("expected terse aggregate summary, got %q", resp.Checks[0].Summary)
	}
}

func TestFlowHealthHandler_AnyFail503(t *testing.T) {
	h := flowHealthHandler([]FlowCheck{
		{Name: "ok-check", Check: func(context.Context) FlowResult { return FlowResult{OK: true, Summary: "fine"} }},
		{Name: "daemon-flow", Check: func(context.Context) FlowResult {
			return FlowResult{OK: false, Summary: "2 daemons, 1 unattached"}
		}},
	})
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodGet, "/flow-health", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
	var resp flowHealthResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "unhealthy" {
		t.Errorf("expected unhealthy status, got %q", resp.Status)
	}
	// Status-only / no-leak: the body must NOT carry per-entity detail. We
	// assert the summary stays at the aggregate level (no user/daemon IDs).
	body := rr.Body.String()
	if strings.Contains(body, "user_id") || strings.Contains(body, "daemon_id") {
		t.Errorf("flow-health body leaked per-entity detail: %s", body)
	}
}

func TestServer_AddFlowCheck_IgnoresInvalid(t *testing.T) {
	var s Server
	s.AddFlowCheck(FlowCheck{Name: "", Check: func(context.Context) FlowResult { return FlowResult{OK: true} }})
	s.AddFlowCheck(FlowCheck{Name: "no-func", Check: nil})
	s.AddFlowCheck(FlowCheck{Name: "good", Check: func(context.Context) FlowResult { return FlowResult{OK: true} }})
	if len(s.FlowChecks) != 1 || s.FlowChecks[0].Name != "good" {
		t.Errorf("expected only the valid check registered, got %+v", s.FlowChecks)
	}
}
