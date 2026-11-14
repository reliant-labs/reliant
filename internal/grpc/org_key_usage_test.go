package grpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

type orgKeyStore struct {
	calls    int
	org      string
	from, to time.Time
}

func (s *orgKeyStore) SumLLMOrgKeyUsage(_ context.Context, orgID string, from, to time.Time) ([]db.OrgKeyModelUsage, error) {
	s.calls++
	s.org, s.from, s.to = orgID, from, to
	return []db.OrgKeyModelUsage{{Provider: "anthropic", Model: "claude-sonnet-4-5", Calls: 3, InputTokens: 100, OutputTokens: 10, CacheReadTokens: 5, CacheWriteTokens: 1}}, nil
}

// Only the internal service reads an org's org-key counts, for one org and a
// bounded range of UTC days.
func TestOrgKeyUsageAuthenticationAndWire(t *testing.T) {
	const secret = "internal-shared-test-key"
	token, err := auth.SignInternalServiceToken(secret)
	require.NoError(t, err)
	body := `{"org_id":"org-a","start_date":"2026-11-01","end_date":"2026-11-14"}`
	for _, tt := range []struct {
		name, method, token, body string
		status                    int
	}{
		{"missing", http.MethodPost, "", body, 401}, {"user", http.MethodPost, "ordinary-user-jwt", body, 401},
		{"method", http.MethodGet, token, body, 405}, {"unknown field", http.MethodPost, token, `{"org_id":"org-a","user_id":"x"}`, 400},
		{"no org", http.MethodPost, token, `{"org_id":"","start_date":"2026-11-01","end_date":"2026-11-14"}`, 400},
		{"reversed", http.MethodPost, token, `{"org_id":"org-a","start_date":"2026-11-14","end_date":"2026-11-01"}`, 400},
		{"too long", http.MethodPost, token, `{"org_id":"org-a","start_date":"2020-01-01","end_date":"2026-11-14"}`, 400},
		{"trailing", http.MethodPost, token, body + ` {}`, 400},
		{"valid", http.MethodPost, token, body, 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &orgKeyStore{}
			req := httptest.NewRequest(tt.method, OrgKeyUsagePath, strings.NewReader(tt.body))
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			w := httptest.NewRecorder()
			NewOrgKeyUsageHandler(store, secret).ServeHTTP(w, req)
			require.Equal(t, tt.status, w.Code, w.Body.String())
			require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			if tt.status != 200 {
				require.Zero(t, store.calls)
				return
			}
			require.Equal(t, "org-a", store.org)
			require.Equal(t, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), store.from)
			require.Equal(t, time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC), store.to)
			var out orgKeyUsageResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
			require.Equal(t, "org-a", out.OrgID)
			require.Equal(t, []orgKeyUsageRow{{Provider: "anthropic", Model: "claude-sonnet-4-5", Calls: 3, InputTokens: 100, OutputTokens: 10, CacheReadTokens: 5, CacheWriteTokens: 1}}, out.Rows)
		})
	}
	// No secret configured: nothing is served, whatever is presented.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, OrgKeyUsagePath, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	NewOrgKeyUsageHandler(&orgKeyStore{}, "").ServeHTTP(w, req)
	require.Equal(t, 401, w.Code)
}
