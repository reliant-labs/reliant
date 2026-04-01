package codex

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/logging"
)

// CodexTokens contains normalized Codex OAuth tokens.
type CodexTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
	IDToken      string `json:"id_token"`
}

const (
	// CodexOAuthTokenEndpoint is the OAuth token endpoint used for authorization-code
	// and refresh-token exchanges.
	// #nosec G101 -- OAuth endpoint URL, not a credential
	CodexOAuthTokenEndpoint = "https://auth.openai.com/oauth/token"

	// CodexClientID is the OAuth client ID used by Codex clients.
	// #nosec G101 -- public OAuth client identifier
	CodexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	// TokenRefreshBuffer is the time before expiry considered effectively expired.
	TokenRefreshBuffer = 5 * time.Minute
)

// IsTokenExpired checks if the JWT access token has expired.
func IsTokenExpired(accessToken string) bool {
	exp, err := extractExpiryFromJWT(accessToken)
	if err != nil {
		// If we can't parse the token, consider it expired
		return true
	}

	// Add a buffer to account for clock skew and near-expiry behavior.
	return time.Now().Add(TokenRefreshBuffer).After(exp)
}

// GetTokenExpiry returns the expiry time of the JWT access token.
func GetTokenExpiry(accessToken string) (time.Time, error) {
	return extractExpiryFromJWT(accessToken)
}

// jwtExpiryClaims is a minimal struct to extract just the exp claim.
type jwtExpiryClaims struct {
	Exp int64 `json:"exp"`
}

// extractExpiryFromJWT extracts the expiry time from a JWT token.
func extractExpiryFromJWT(token string) (time.Time, error) {
	parts := splitJWT(token)
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("invalid JWT format: expected 3 parts, got %d", len(parts))
	}

	// Decode the payload (second part)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	var claims jwtExpiryClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, fmt.Errorf("failed to parse JWT claims: %w", err)
	}

	if claims.Exp == 0 {
		return time.Time{}, fmt.Errorf("JWT does not contain exp claim")
	}

	return time.Unix(claims.Exp, 0), nil
}

// splitJWT splits a JWT token into its parts.
func splitJWT(token string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			parts = append(parts, token[start:i])
			start = i + 1
		}
	}
	parts = append(parts, token[start:])
	return parts
}

// CodexOAuthTokenRequest captures OAuth token exchange inputs.
type CodexOAuthTokenRequest struct {
	GrantType    string
	Code         string
	CodeVerifier string
	RedirectURI  string
	RefreshToken string
}

// TokenRefreshResponse represents a successful response from the OAuth token endpoint.
type TokenRefreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// TokenRefreshError represents an error response from the OAuth token endpoint.
type TokenRefreshError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// ExchangeCodexAuthorizationCode exchanges an OAuth authorization code + PKCE verifier
// for Codex tokens.
func ExchangeCodexAuthorizationCode(code, codeVerifier, redirectURI string) (*CodexTokens, error) {
	return exchangeCodexTokens(
		CodexOAuthTokenEndpoint,
		func() *http.Client { c := llm.ResilientHTTPClient(); c.Timeout = 30 * time.Second; return c }(),
		CodexOAuthTokenRequest{
			GrantType:    "authorization_code",
			Code:         code,
			CodeVerifier: codeVerifier,
			RedirectURI:  redirectURI,
		},
	)
}

// RefreshCodexTokens exchanges a refresh token for new access and refresh tokens.
func RefreshCodexTokens(refreshToken string) (*CodexTokens, error) {
	return exchangeCodexTokens(
		CodexOAuthTokenEndpoint,
		func() *http.Client { c := llm.ResilientHTTPClient(); c.Timeout = 30 * time.Second; return c }(),
		CodexOAuthTokenRequest{
			GrantType:    "refresh_token",
			RefreshToken: refreshToken,
		},
	)
}

func exchangeCodexTokens(tokenEndpoint string, client *http.Client, req CodexOAuthTokenRequest) (*CodexTokens, error) {
	if client == nil {
		return nil, fmt.Errorf("http client is required")
	}

	grantType := strings.TrimSpace(req.GrantType)
	if grantType == "" {
		return nil, fmt.Errorf("grant_type is required")
	}

	formData := url.Values{}
	formData.Set("grant_type", grantType)
	formData.Set("client_id", CodexClientID)

	switch grantType {
	case "authorization_code":
		if strings.TrimSpace(req.Code) == "" {
			return nil, fmt.Errorf("authorization code is required")
		}
		if strings.TrimSpace(req.CodeVerifier) == "" {
			return nil, fmt.Errorf("code verifier is required")
		}
		if strings.TrimSpace(req.RedirectURI) == "" {
			return nil, fmt.Errorf("redirect URI is required")
		}
		formData.Set("code", strings.TrimSpace(req.Code))
		formData.Set("code_verifier", strings.TrimSpace(req.CodeVerifier))
		formData.Set("redirect_uri", strings.TrimSpace(req.RedirectURI))
	case "refresh_token":
		if strings.TrimSpace(req.RefreshToken) == "" {
			return nil, fmt.Errorf("refresh token is empty")
		}
		formData.Set("refresh_token", strings.TrimSpace(req.RefreshToken))
	default:
		return nil, fmt.Errorf("unsupported grant_type %q", grantType)
	}

	httpReq, err := http.NewRequest("POST", tokenEndpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create token exchange request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

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
		return nil, mapTokenExchangeError(grantType, resp.StatusCode, body)
	}

	var tokenResp TokenRefreshResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token exchange response: %w", err)
	}

	return normalizeCodexTokens(tokenResp, req.RefreshToken)
}

func normalizeCodexTokens(tokenResp TokenRefreshResponse, existingRefreshToken string) (*CodexTokens, error) {
	accessToken := strings.TrimSpace(tokenResp.AccessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("token exchange response missing access token")
	}

	refreshToken := strings.TrimSpace(tokenResp.RefreshToken)
	if refreshToken == "" {
		refreshToken = strings.TrimSpace(existingRefreshToken)
	}

	accountID, err := extractAccountIDFromAccessToken(accessToken)
	if err != nil {
		logging.Warn("Failed to extract account ID from token", "error", err)
	}

	return &CodexTokens{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		IDToken:      strings.TrimSpace(tokenResp.IDToken),
		AccountID:    accountID,
	}, nil
}

func mapTokenExchangeError(grantType string, statusCode int, body []byte) error {
	var errResp TokenRefreshError
	if err := json.Unmarshal(body, &errResp); err == nil && strings.TrimSpace(errResp.Error) != "" {
		errCode := strings.TrimSpace(errResp.Error)
		errDesc := strings.TrimSpace(errResp.ErrorDescription)

		switch errCode {
		case "invalid_grant":
			if grantType == "authorization_code" {
				return fmt.Errorf("authorization code is invalid or expired")
			}
			return fmt.Errorf("codex session expired: please reconnect Codex")
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

// extractAccountIDFromAccessToken extracts the chatgpt_account_id from a JWT access token.
func extractAccountIDFromAccessToken(token string) (string, error) {
	parts := splitJWT(token)
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid JWT format")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("failed to parse JWT claims: %w", err)
	}

	if auth, ok := claims["https://api.openai.com/auth"].(map[string]interface{}); ok {
		if accountID, ok := auth["chatgpt_account_id"].(string); ok {
			return accountID, nil
		}
	}

	return "", fmt.Errorf("account ID not found in token")
}
