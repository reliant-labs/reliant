// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/authhooks"
)

func TestSupabaseAuthHookIsAbsentWithoutASecret(t *testing.T) {
	if h := NewSupabaseAuthHook(""); h != nil {
		t.Fatalf("NewSupabaseAuthHook(\"\") = %v, want nil: no secret serves no route", h)
	}
}

func TestSupabaseAuthHookVerifiesTheTriggersBearer(t *testing.T) {
	h := NewSupabaseAuthHook("hook-secret")
	body := `{"v":1,"event":"signed_in","user_id":"u-hook","dedupe":"s-hook"}`
	for auth, want := range map[string]int{"Bearer hook-secret": http.StatusNoContent, "Bearer nope": http.StatusUnauthorized} {
		req := httptest.NewRequest(http.MethodPost, authhooks.Path("supabase"), strings.NewReader(body))
		req.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != want {
			t.Errorf("%s: status %d, want %d", auth, w.Code, want)
		}
	}
}

func TestSupabaseAuthHookRecordsNothingWithoutConsent(t *testing.T) {
	// No analytics client is installed in this test process: every user's
	// consent is unknown, so nothing is recorded.
	if supabaseAuthHookAllows(context.Background(), authhooks.Event{UserID: "u-1"}) {
		t.Error("an event was allowed with no consent to read")
	}
}
