package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ClaudeTokens contains normalized Claude OAuth tokens with account metadata.
type ClaudeTokens struct {
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	ExpiresAt        time.Time `json:"expires_at"`
	AccountUUID      string    `json:"account_uuid"`
	AccountEmail     string    `json:"account_email"`
	OrganizationUUID string    `json:"organization_uuid"`
	OrganizationName string    `json:"organization_name"`
	Scope            string    `json:"scope"`
}

const (
	// ClaudeTokenEndpoint is the OAuth token endpoint for both auth code exchange and refresh.
	// #nosec G101 -- OAuth endpoint URL, not a credential
	ClaudeTokenEndpoint = "https://platform.claude.com/v1/oauth/token"

	// ClaudeClientID is the OAuth client ID for Claude.
	// #nosec G101 -- public OAuth client identifier
	ClaudeClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

	// ClaudeRefreshScope is the scope requested during token refresh.
	ClaudeRefreshScope = "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"

	// TokenRefreshBuffer is the time before expiry considered effectively expired.
	TokenRefreshBuffer = 5 * time.Minute
)

// IsTokenExpired checks if the token has expired based on stored expires_at.
func IsTokenExpired(expiresAt time.Time) bool {
	if expiresAt.IsZero() {
		return true
	}
	return time.Now().Add(TokenRefreshBuffer).After(expiresAt)
}

// ClaudeTokenResponse represents a successful response from Claude's OAuth token endpoint.
type ClaudeTokenResponse struct {
	TokenType    string `json:"token_type"`
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Organization struct {
		UUID string `json:"uuid"`
		Name string `json:"name"`
	} `json:"organization"`
	Account struct {
		UUID         string `json:"uuid"`
		EmailAddress string `json:"email_address"`
	} `json:"account"`
}

// ClaudeTokenError represents an error response from Claude's OAuth token endpoint.
type ClaudeTokenError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// authCodeJSONBody is the JSON request body for authorization code exchange.
type authCodeJSONBody struct {
	GrantType    string `json:"grant_type"`
	Code         string `json:"code"`
	RedirectURI  string `json:"redirect_uri"`
	ClientID     string `json:"client_id"`
	CodeVerifier string `json:"code_verifier"`
	State        string `json:"state"`
}

// refreshTokenJSONBody is the JSON request body for refresh token exchanges.
type refreshTokenJSONBody struct {
	GrantType    string `json:"grant_type"`
	RefreshToken string `json:"refresh_token"`
	ClientID     string `json:"client_id"`
	Scope        string `json:"scope"`
}

// ExchangeClaudeAuthorizationCode exchanges an OAuth authorization code + PKCE verifier
// for Claude tokens. Uses JSON POST to platform.claude.com/v1/oauth/token.
func ExchangeClaudeAuthorizationCode(code, codeVerifier, redirectURI, state string) (*ClaudeTokens, error) {
	return exchangeClaudeAuthCode(
		ClaudeTokenEndpoint,
		func() *http.Client { c := llm.ResilientHTTPClient(); c.Timeout = 30 * time.Second; return c }(),
		code, codeVerifier, redirectURI, state,
	)
}

// RefreshClaudeTokens exchanges a refresh token for new access and refresh tokens.
// Uses JSON POST to platform.claude.com/v1/oauth/token with the full scope.
func RefreshClaudeTokens(refreshToken string) (*ClaudeTokens, error) {
	return refreshClaudeTokens(
		ClaudeTokenEndpoint,
		func() *http.Client { c := llm.ResilientHTTPClient(); c.Timeout = 30 * time.Second; return c }(),
		refreshToken,
	)
}

func exchangeClaudeAuthCode(tokenEndpoint string, client *http.Client, code, codeVerifier, redirectURI, state string) (*ClaudeTokens, error) {
	if client == nil {
		return nil, fmt.Errorf("http client is required")
	}
	if strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("authorization code is required")
	}
	if strings.TrimSpace(codeVerifier) == "" {
		return nil, fmt.Errorf("code verifier is required")
	}
	if strings.TrimSpace(redirectURI) == "" {
		return nil, fmt.Errorf("redirect URI is required")
	}

	body := authCodeJSONBody{
		GrantType:    "authorization_code",
		Code:         strings.TrimSpace(code),
		RedirectURI:  strings.TrimSpace(redirectURI),
		ClientID:     ClaudeClientID,
		CodeVerifier: strings.TrimSpace(codeVerifier),
		State:        strings.TrimSpace(state),
	}

	jsonBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal auth code request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", tokenEndpoint, bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create token exchange request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/plain, */*")
	httpReq.Header.Set("User-Agent", "axios/1.8.4")

	return doTokenExchange(client, httpReq, "authorization_code", "")
}

func refreshClaudeTokens(tokenEndpoint string, client *http.Client, refreshToken string) (*ClaudeTokens, error) {
	if client == nil {
		return nil, fmt.Errorf("http client is required")
	}
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("refresh token is empty")
	}

	body := refreshTokenJSONBody{
		GrantType:    "refresh_token",
		RefreshToken: strings.TrimSpace(refreshToken),
		ClientID:     ClaudeClientID,
		Scope:        ClaudeRefreshScope,
	}

	jsonBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal refresh request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", tokenEndpoint, bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create token refresh request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/plain, */*")
	httpReq.Header.Set("User-Agent", "axios/1.8.4")

	return doTokenExchange(client, httpReq, "refresh_token", refreshToken)
}

func doTokenExchange(client *http.Client, httpReq *http.Request, grantType, existingRefreshToken string) (*ClaudeTokens, error) {
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to execute token exchange request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read token exchange response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, mapClaudeTokenExchangeError(grantType, resp.StatusCode, body)
	}

	var tokenResp ClaudeTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token exchange response: %w", err)
	}

	return normalizeClaudeTokens(tokenResp, existingRefreshToken)
}

func normalizeClaudeTokens(tokenResp ClaudeTokenResponse, existingRefreshToken string) (*ClaudeTokens, error) {
	accessToken := strings.TrimSpace(tokenResp.AccessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("token exchange response missing access token")
	}

	refreshToken := strings.TrimSpace(tokenResp.RefreshToken)
	if refreshToken == "" {
		refreshToken = strings.TrimSpace(existingRefreshToken)
	}

	// Compute expiry from expires_in
	var expiresAt time.Time
	if tokenResp.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	} else {
		logging.Warn("Claude token response missing expires_in, using 1 year default")
		expiresAt = time.Now().Add(365 * 24 * time.Hour)
	}

	return &ClaudeTokens{
		AccessToken:      accessToken,
		RefreshToken:     refreshToken,
		ExpiresAt:        expiresAt,
		AccountUUID:      strings.TrimSpace(tokenResp.Account.UUID),
		AccountEmail:     strings.TrimSpace(tokenResp.Account.EmailAddress),
		OrganizationUUID: strings.TrimSpace(tokenResp.Organization.UUID),
		OrganizationName: strings.TrimSpace(tokenResp.Organization.Name),
		Scope:            strings.TrimSpace(tokenResp.Scope),
	}, nil
}

func mapClaudeTokenExchangeError(grantType string, statusCode int, body []byte) error {
	var errResp ClaudeTokenError
	if err := json.Unmarshal(body, &errResp); err == nil && strings.TrimSpace(errResp.Error) != "" {
		errCode := strings.TrimSpace(errResp.Error)
		errDesc := strings.TrimSpace(errResp.ErrorDescription)

		switch errCode {
		case "invalid_grant":
			if grantType == "authorization_code" {
				return fmt.Errorf("authorization code is invalid or expired")
			}
			return fmt.Errorf("claude session expired: please reconnect Claude")
		case "invalid_request":
			if errDesc == "" {
				return fmt.Errorf("token exchange failed: invalid request")
			}
			return fmt.Errorf("token exchange failed: invalid request (%s)", errDesc)
		case "invalid_client":
			if errDesc == "" {
				return fmt.Errorf("token exchange failed: invalid client configuration")
			}
			return fmt.Errorf("token exchange failed: invalid client configuration (%s)", errDesc)
		default:
			if errDesc == "" {
				return fmt.Errorf("token exchange failed: %s", errCode)
			}
			return fmt.Errorf("token exchange failed: %s (%s)", errCode, errDesc)
		}
	}

	bodyText := strings.TrimSpace(string(body))
	if bodyText == "" {
		bodyText = http.StatusText(statusCode)
	}
	return fmt.Errorf("token exchange failed with status %d: %s", statusCode, bodyText)
}
