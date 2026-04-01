package codex

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExchangeCodexTokensAuthorizationCodeSuccess(t *testing.T) {
	t.Parallel()

	accessToken := createCodexJWTWithAccountID("acct-123")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST request, got %s", r.Method)
		}

		if err := r.ParseForm(); err != nil {
			t.Fatalf("failed to parse form: %v", err)
		}

		if got := r.Form.Get("grant_type"); got != "authorization_code" {
			t.Fatalf("grant_type = %q, want %q", got, "authorization_code")
		}
		if got := r.Form.Get("client_id"); got != CodexClientID {
			t.Fatalf("client_id = %q, want %q", got, CodexClientID)
		}
		if got := r.Form.Get("code"); got != "auth-code" {
			t.Fatalf("code = %q, want %q", got, "auth-code")
		}
		if got := r.Form.Get("code_verifier"); got != "pkce-verifier" {
			t.Fatalf("code_verifier = %q, want %q", got, "pkce-verifier")
		}
		if got := r.Form.Get("redirect_uri"); got != "http://localhost:1455/auth/callback" {
			t.Fatalf("redirect_uri = %q, want %q", got, "http://localhost:1455/auth/callback")
		}

		_ = json.NewEncoder(w).Encode(TokenRefreshResponse{
			AccessToken:  accessToken,
			RefreshToken: "refresh-123",
			IDToken:      "id-123",
		})
	}))
	defer server.Close()

	tokens, err := exchangeCodexTokens(server.URL, server.Client(), CodexOAuthTokenRequest{
		GrantType:    "authorization_code",
		Code:         "auth-code",
		CodeVerifier: "pkce-verifier",
		RedirectURI:  "http://localhost:1455/auth/callback",
	})
	if err != nil {
		t.Fatalf("exchangeCodexTokens() error = %v", err)
	}

	if tokens.AccessToken != accessToken {
		t.Fatalf("AccessToken = %q, want %q", tokens.AccessToken, accessToken)
	}
	if tokens.RefreshToken != "refresh-123" {
		t.Fatalf("RefreshToken = %q, want %q", tokens.RefreshToken, "refresh-123")
	}
	if tokens.IDToken != "id-123" {
		t.Fatalf("IDToken = %q, want %q", tokens.IDToken, "id-123")
	}
	if tokens.AccountID != "acct-123" {
		t.Fatalf("AccountID = %q, want %q", tokens.AccountID, "acct-123")
	}
}

func TestExchangeCodexTokensRefreshSuccessPreservesExistingRefreshToken(t *testing.T) {
	t.Parallel()

	accessToken := createCodexJWTWithAccountID("acct-refresh")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("failed to parse form: %v", err)
		}

		if got := r.Form.Get("grant_type"); got != "refresh_token" {
			t.Fatalf("grant_type = %q, want %q", got, "refresh_token")
		}
		if got := r.Form.Get("refresh_token"); got != "existing-refresh" {
			t.Fatalf("refresh_token = %q, want %q", got, "existing-refresh")
		}
		if got := r.Form.Get("client_id"); got != CodexClientID {
			t.Fatalf("client_id = %q, want %q", got, CodexClientID)
		}

		_ = json.NewEncoder(w).Encode(TokenRefreshResponse{
			AccessToken: accessToken,
			IDToken:     "id-refresh",
		})
	}))
	defer server.Close()

	tokens, err := exchangeCodexTokens(server.URL, server.Client(), CodexOAuthTokenRequest{
		GrantType:    "refresh_token",
		RefreshToken: "existing-refresh",
	})
	if err != nil {
		t.Fatalf("exchangeCodexTokens() error = %v", err)
	}

	if tokens.RefreshToken != "existing-refresh" {
		t.Fatalf("RefreshToken = %q, want %q", tokens.RefreshToken, "existing-refresh")
	}
	if tokens.AccountID != "acct-refresh" {
		t.Fatalf("AccountID = %q, want %q", tokens.AccountID, "acct-refresh")
	}
}

func TestExchangeCodexTokensValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		req     CodexOAuthTokenRequest
		wantErr string
	}{
		{
			name:    "missing grant type",
			req:     CodexOAuthTokenRequest{},
			wantErr: "grant_type is required",
		},
		{
			name: "authorization code missing code",
			req: CodexOAuthTokenRequest{
				GrantType:    "authorization_code",
				CodeVerifier: "verifier",
				RedirectURI:  "http://127.0.0.1/callback",
			},
			wantErr: "authorization code is required",
		},
		{
			name: "authorization code missing verifier",
			req: CodexOAuthTokenRequest{
				GrantType:   "authorization_code",
				Code:        "auth-code",
				RedirectURI: "http://127.0.0.1/callback",
			},
			wantErr: "code verifier is required",
		},
		{
			name: "authorization code missing redirect URI",
			req: CodexOAuthTokenRequest{
				GrantType:    "authorization_code",
				Code:         "auth-code",
				CodeVerifier: "verifier",
			},
			wantErr: "redirect URI is required",
		},
		{
			name: "refresh token empty",
			req: CodexOAuthTokenRequest{
				GrantType: "refresh_token",
			},
			wantErr: "refresh token is empty",
		},
		{
			name: "unsupported grant type",
			req: CodexOAuthTokenRequest{
				GrantType: "client_credentials",
			},
			wantErr: "unsupported grant_type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := exchangeCodexTokens("http://127.0.0.1:1", &http.Client{Timeout: time.Second}, tt.req)
			if err == nil {
				t.Fatalf("expected error but got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestMapTokenExchangeError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		grantType string
		status    int
		body      string
		wantErr   string
	}{
		{
			name:      "invalid grant for authorization code",
			grantType: "authorization_code",
			status:    http.StatusBadRequest,
			body:      `{"error":"invalid_grant","error_description":"expired code"}`,
			wantErr:   "authorization code is invalid or expired",
		},
		{
			name:      "invalid grant for refresh token",
			grantType: "refresh_token",
			status:    http.StatusBadRequest,
			body:      `{"error":"invalid_grant","error_description":"expired refresh"}`,
			wantErr:   "codex session expired: please reconnect Codex",
		},
		{
			name:      "invalid request with description",
			grantType: "authorization_code",
			status:    http.StatusBadRequest,
			body:      `{"error":"invalid_request","error_description":"missing code_verifier"}`,
			wantErr:   "token exchange failed: invalid request (missing code_verifier)",
		},
		{
			name:      "non-json body falls back to status",
			grantType: "refresh_token",
			status:    http.StatusUnauthorized,
			body:      "not-json",
			wantErr:   "token exchange failed with status 401: not-json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := mapTokenExchangeError(tt.grantType, tt.status, []byte(tt.body))
			if err == nil {
				t.Fatalf("expected error but got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestExchangeCodexAuthorizationCodeInputValidation(t *testing.T) {
	t.Parallel()

	_, err := ExchangeCodexAuthorizationCode("", "verifier", "http://127.0.0.1/callback")
	if err == nil {
		t.Fatalf("expected validation error for missing code")
	}
	if !strings.Contains(err.Error(), "authorization code is required") {
		t.Fatalf("error = %q, want missing code validation", err.Error())
	}
}

func TestRefreshCodexTokensInputValidation(t *testing.T) {
	t.Parallel()

	_, err := RefreshCodexTokens("")
	if err == nil {
		t.Fatalf("expected validation error for missing refresh token")
	}
	if !strings.Contains(err.Error(), "refresh token is empty") {
		t.Fatalf("error = %q, want missing refresh token validation", err.Error())
	}
}

func createCodexJWTWithAccountID(accountID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))

	claims := map[string]interface{}{
		"https://api.openai.com/auth": map[string]interface{}{
			"chatgpt_account_id": accountID,
		},
	}
	claimsJSON, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signature := base64.RawURLEncoding.EncodeToString([]byte("signature"))

	return header + "." + payload + "." + signature
}
