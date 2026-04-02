package claude

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsTokenExpired(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{
			name:      "zero time is expired",
			expiresAt: time.Time{},
			want:      true,
		},
		{
			name:      "past time is expired",
			expiresAt: time.Now().Add(-1 * time.Hour),
			want:      true,
		},
		{
			name:      "within buffer is expired",
			expiresAt: time.Now().Add(3 * time.Minute),
			want:      true,
		},
		{
			name:      "future time is not expired",
			expiresAt: time.Now().Add(1 * time.Hour),
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsTokenExpired(tt.expiresAt)
			if got != tt.want {
				t.Errorf("IsTokenExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExchangeClaudeAuthCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var reqBody authCodeJSONBody
		if err := json.Unmarshal(body, &reqBody); err != nil {
			t.Fatalf("failed to parse request body: %v", err)
		}
		if reqBody.GrantType != "authorization_code" {
			t.Errorf("expected grant_type=authorization_code, got %s", reqBody.GrantType)
		}
		if reqBody.ClientID != ClaudeClientID {
			t.Errorf("expected client_id=%s, got %s", ClaudeClientID, reqBody.ClientID)
		}
		if reqBody.Code != "test-code" {
			t.Errorf("expected code=test-code, got %s", reqBody.Code)
		}
		if reqBody.CodeVerifier != "test-verifier" {
			t.Errorf("expected code_verifier=test-verifier, got %s", reqBody.CodeVerifier)
		}
		if reqBody.RedirectURI != "http://localhost:1234/callback" {
			t.Errorf("expected redirect_uri=http://localhost:1234/callback, got %s", reqBody.RedirectURI)
		}

		resp := ClaudeTokenResponse{
			TokenType:    "Bearer",
			AccessToken:  "sk-ant-oat01-test-access-token",
			ExpiresIn:    31536000,
			RefreshToken: "sk-ant-ort01-test-refresh-token",
			Scope:        "user:inference",
		}
		resp.Organization.UUID = "org-uuid-123"
		resp.Organization.Name = "Test Org"
		resp.Account.UUID = "account-uuid-456"
		resp.Account.EmailAddress = "test@example.com"

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	tokens, err := exchangeClaudeAuthCode(server.URL, client, "test-code", "test-verifier", "http://localhost:1234/callback", "test-state")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tokens.AccessToken != "sk-ant-oat01-test-access-token" {
		t.Errorf("expected access_token=sk-ant-oat01-test-access-token, got %s", tokens.AccessToken)
	}
	if tokens.RefreshToken != "sk-ant-ort01-test-refresh-token" {
		t.Errorf("expected refresh_token=sk-ant-ort01-test-refresh-token, got %s", tokens.RefreshToken)
	}
	if tokens.AccountUUID != "account-uuid-456" {
		t.Errorf("expected account_uuid=account-uuid-456, got %s", tokens.AccountUUID)
	}
	if tokens.AccountEmail != "test@example.com" {
		t.Errorf("expected account_email=test@example.com, got %s", tokens.AccountEmail)
	}
	if tokens.OrganizationUUID != "org-uuid-123" {
		t.Errorf("expected organization_uuid=org-uuid-123, got %s", tokens.OrganizationUUID)
	}
	if tokens.OrganizationName != "Test Org" {
		t.Errorf("expected organization_name=Test Org, got %s", tokens.OrganizationName)
	}
	if tokens.Scope != "user:inference" {
		t.Errorf("expected scope=user:inference, got %s", tokens.Scope)
	}
	if tokens.ExpiresAt.Before(time.Now()) {
		t.Errorf("expected expires_at to be in the future")
	}
}

func TestRefreshClaudeTokens_JSONBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var reqBody refreshTokenJSONBody
		if err := json.Unmarshal(body, &reqBody); err != nil {
			t.Fatalf("failed to parse request body: %v", err)
		}
		if reqBody.GrantType != "refresh_token" {
			t.Errorf("expected grant_type=refresh_token, got %s", reqBody.GrantType)
		}
		if reqBody.RefreshToken != "sk-ant-ort01-old-refresh" {
			t.Errorf("expected refresh_token=sk-ant-ort01-old-refresh, got %s", reqBody.RefreshToken)
		}
		if reqBody.ClientID != ClaudeClientID {
			t.Errorf("expected client_id=%s, got %s", ClaudeClientID, reqBody.ClientID)
		}
		if reqBody.Scope != ClaudeRefreshScope {
			t.Errorf("expected scope=%s, got %s", ClaudeRefreshScope, reqBody.Scope)
		}

		resp := ClaudeTokenResponse{
			TokenType:    "Bearer",
			AccessToken:  "sk-ant-oat01-new-access-token",
			ExpiresIn:    31536000,
			RefreshToken: "sk-ant-ort01-new-refresh-token",
			Scope:        "user:inference",
		}
		resp.Account.UUID = "account-uuid"
		resp.Account.EmailAddress = "test@example.com"
		resp.Organization.UUID = "org-uuid"
		resp.Organization.Name = "Test Org"

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	tokens, err := refreshClaudeTokens(server.URL, client, "sk-ant-ort01-old-refresh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens.AccessToken != "sk-ant-oat01-new-access-token" {
		t.Errorf("expected new access token, got %s", tokens.AccessToken)
	}
	if tokens.RefreshToken != "sk-ant-ort01-new-refresh-token" {
		t.Errorf("expected new refresh token, got %s", tokens.RefreshToken)
	}
}

func TestRefreshClaudeTokens_PreservesRefreshTokenWhenNotReturned(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ClaudeTokenResponse{
			TokenType:   "Bearer",
			AccessToken: "sk-ant-oat01-new-access",
			ExpiresIn:   31536000,
			Scope:       "user:inference",
			// No refresh_token in response
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	tokens, err := refreshClaudeTokens(server.URL, client, "sk-ant-ort01-existing-refresh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens.RefreshToken != "sk-ant-ort01-existing-refresh" {
		t.Errorf("expected existing refresh token to be preserved, got %s", tokens.RefreshToken)
	}
}

func TestExchangeClaudeAuthCode_ErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ClaudeTokenError{
			Error:            "invalid_grant",
			ErrorDescription: "The authorization code has expired",
		})
	}))
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	_, err := exchangeClaudeAuthCode(server.URL, client, "expired-code", "verifier", "http://localhost:1234/callback", "test-state")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "authorization code is invalid or expired" {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestExchangeClaudeAuthCode_ValidationErrors(t *testing.T) {
	client := &http.Client{Timeout: 5 * time.Second}

	tests := []struct {
		name         string
		code         string
		codeVerifier string
		redirectURI  string
		wantErr      string
	}{
		{
			name:    "empty code",
			wantErr: "authorization code is required",
		},
		{
			name:    "empty code_verifier",
			code:    "code",
			wantErr: "code verifier is required",
		},
		{
			name:         "empty redirect_uri",
			code:         "code",
			codeVerifier: "verifier",
			wantErr:      "redirect URI is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := exchangeClaudeAuthCode("http://unused", client, tt.code, tt.codeVerifier, tt.redirectURI, "test-state")
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if err.Error() != tt.wantErr {
				t.Errorf("expected error %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestRefreshClaudeTokens_ValidationErrors(t *testing.T) {
	client := &http.Client{Timeout: 5 * time.Second}

	_, err := refreshClaudeTokens("http://unused", client, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "refresh token is empty" {
		t.Errorf("unexpected error: %s", err.Error())
	}
}

func TestExchangeClaudeAuthCode_NilClient(t *testing.T) {
	_, err := exchangeClaudeAuthCode("http://unused", nil, "code", "verifier", "http://localhost/callback", "test-state")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "http client is required" {
		t.Errorf("unexpected error: %s", err.Error())
	}
}

func TestRefreshClaudeTokens_NilClient(t *testing.T) {
	_, err := refreshClaudeTokens("http://unused", nil, "refresh-token")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "http client is required" {
		t.Errorf("unexpected error: %s", err.Error())
	}
}

func TestNormalizeClaudeTokens_MissingAccessToken(t *testing.T) {
	_, err := normalizeClaudeTokens(ClaudeTokenResponse{}, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "token exchange response missing access token" {
		t.Errorf("unexpected error: %s", err.Error())
	}
}

func TestRefreshClaudeTokens_InvalidGrantError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ClaudeTokenError{
			Error:            "invalid_grant",
			ErrorDescription: "The refresh token has been revoked",
		})
	}))
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	_, err := refreshClaudeTokens(server.URL, client, "revoked-token")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// For refresh_token grant type, invalid_grant maps to "session expired" message
	if err.Error() != "claude session expired: please reconnect Claude" {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}
