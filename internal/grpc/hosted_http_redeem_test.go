package grpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/hosteduse"
	"github.com/stretchr/testify/require"
)

type redeemStore struct{ calls int }

func (*redeemStore) CreateHostedHTTPActionTicket(context.Context, core.HostedHTTPActionTicket) (bool, error) {
	return false, nil
}
func (s *redeemStore) RedeemHostedHTTPActionTicket(_ context.Context, id, grant, digest string) (*core.HostedHTTPActionTicket, error) {
	s.calls++
	return &core.HostedHTTPActionTicket{ID: id, GrantID: grant, RequestSHA256: digest, OwnerUserID: "owner", ProjectID: "project", WorkflowID: "runtime-per-fire", WorkflowName: "trader-bot", NodeID: "fixed-node", ExpiresAt: time.Now().UTC().Add(time.Minute)}, nil
}

func TestHostedRedeemAuthenticationAndWire(t *testing.T) {
	const secret = "internal-shared-test-key"
	token, err := auth.SignInternalServiceToken(secret)
	require.NoError(t, err)
	body := `{"ticket_id":"ticket","grant_id":"grant","request_sha256":"` + strings.Repeat("a", 64) + `"}`
	cases := []struct {
		name, method, token, body string
		status                    int
	}{
		{"missing", http.MethodPost, "", body, 401}, {"user", http.MethodPost, "ordinary-user-jwt", body, 401},
		{"method", http.MethodGet, token, body, 405}, {"unknown", http.MethodPost, token, `{"owner_user_id":"attacker"}`, 400},
		{"trailing", http.MethodPost, token, body + ` {}`, 400}, {"oversize", http.MethodPost, token, strings.Repeat("x", 5000), 400},
		{"bad-digest", http.MethodPost, token, `{"ticket_id":"ticket","grant_id":"grant","request_sha256":"bad"}`, 403},
		{"valid", http.MethodPost, token, body, 200},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			store := &redeemStore{}
			h := NewHostedHTTPRedeemHandler(store, secret)
			req := httptest.NewRequest(tt.method, "/internal/hosted-http/redeem", strings.NewReader(tt.body))
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			require.Equal(t, tt.status, w.Code)
			require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			if tt.status != 200 {
				require.Zero(t, store.calls)
				return
			}
			var out hosteduse.Metadata
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
			require.Equal(t, "owner", out.OwnerUserID)
			require.Equal(t, "runtime-per-fire", out.RunID)
			require.Equal(t, "runtime-per-fire", out.WorkflowID)
			require.NotContains(t, w.Body.String(), "request_sha256")
			require.Equal(t, 1, store.calls)
		})
	}
	// A correct internal token never enables an unconfigured deployment.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/hosted-http/redeem", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	NewHostedHTTPRedeemHandler(&redeemStore{}, "").ServeHTTP(w, req)
	require.Equal(t, 401, w.Code)
}

func TestHostedRedeemJWTClaimRestrictions(t *testing.T) {
	now := time.Now()
	for _, kind := range []string{"wrong-key", "wrong-role", "wrong-subject", "wrong-issuer", "wrong-audience", "no-exp", "expired", "future", "long-lived", "hs512"} {
		t.Run(kind, func(t *testing.T) {
			claims := jwt.MapClaims{"sub": auth.InternalServiceSubject, "role": auth.InternalServiceRole, "iss": "control-plane", "aud": "authenticated", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix()}
			key := "secret"
			alg := jwt.SigningMethodHS256
			switch kind {
			case "wrong-key":
				key = "other"
			case "wrong-role":
				claims["role"] = "user"
			case "wrong-subject":
				claims["sub"] = "user"
			case "wrong-issuer":
				claims["iss"] = "other"
			case "wrong-audience":
				claims["aud"] = "other"
			case "no-exp":
				delete(claims, "exp")
			case "expired":
				claims["exp"] = now.Add(-time.Minute).Unix()
			case "future":
				claims["iat"] = now.Add(time.Minute).Unix()
			case "long-lived":
				claims["exp"] = now.Add(time.Hour).Unix()
			case "hs512":
				alg = jwt.SigningMethodHS512
			}
			token, err := jwt.NewWithClaims(alg, claims).SignedString([]byte(key))
			require.NoError(t, err)
			require.False(t, validHostedRedeemBearer("Bearer "+token, "secret"))
		})
	}
}
