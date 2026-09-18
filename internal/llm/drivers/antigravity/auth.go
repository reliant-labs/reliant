// Copyright (c) 2025 Reliant Labs

// Package antigravity implements the Antigravity provider, which authenticates
// against Google's OAuth endpoints and serves Gemini models through Google's
// internal cloudcode endpoint.
//
// DRIVER ID: "antigravity". Provider name and driver id are deliberately the
// same string here. That is NOT universal in this tree — Claude's OAuth
// credential registers under the driver id "anthropic", because its access
// token is an sk-ant-oat key the anthropic driver auto-detects. Antigravity
// has its own driver, its own token endpoint and its own credential row, so it
// keeps its own id everywhere: the provider marker written by
// SetProviderAPIKey, the models.DriverID the resolver switches on, and the
// package name.
package antigravity

import (
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

// DriverID is the single spelling of this provider's identity. Every switch
// arm, provider marker and DB lookup keys off this value.
const DriverID = "antigravity"

// AntigravityTokens contains normalized Antigravity (Google) OAuth tokens.
//
// Unlike Codex, the access token is an opaque `ya29.` Google token, not a JWT,
// so there is no exp claim to read. Google returns expires_in on every
// exchange, so the expiry is computed once here and STORED — the same shape
// Claude uses.
type AntigravityTokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	IDToken      string    `json:"id_token"`
	Scope        string    `json:"scope"`
}

const (
	// AntigravityAuthorizeEndpoint is Google's OAuth authorize endpoint. The
	// frontend builds the authorize URL; this is exported so both halves agree.
	AntigravityAuthorizeEndpoint = "https://accounts.google.com/o/oauth2/auth"

	// AntigravityTokenEndpoint is the OAuth token endpoint used for both
	// authorization-code and refresh-token exchanges.
	// #nosec G101 -- OAuth endpoint URL, not a credential
	AntigravityTokenEndpoint = "https://oauth2.googleapis.com/token"

	// AntigravityClientID is the OAuth client ID used by Antigravity clients.
	// #nosec G101 -- public OAuth client identifier
	AntigravityClientID = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"

	// AntigravityClientSecret is the client secret shipped inside the
	// Antigravity CLI. Google calls this an "installed application" secret and
	// explicitly does not treat it as confidential — it is extractable from
	// any copy of the client, exactly like the GitHub Copilot client secret
	// already in this tree. Google's token endpoint rejects the exchange
	// without it, so it has to travel with the request.
	// #nosec G101 -- public/extracted client secret for an installed app
	AntigravityClientSecret = "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"

	// AntigravityScope is the space-delimited scope set the Antigravity client
	// requests. The frontend must request exactly this set: Google binds the
	// granted scopes to the refresh token, so a narrower authorize request
	// produces tokens the cloudcode endpoint rejects.
	AntigravityScope = "https://www.googleapis.com/auth/cloud-platform " +
		"https://www.googleapis.com/auth/userinfo.email " +
		"https://www.googleapis.com/auth/userinfo.profile " +
		"https://www.googleapis.com/auth/cclog " +
		"https://www.googleapis.com/auth/experimentsandconfigs " +
		"https://www.googleapis.com/auth/aicode " +
		"openid"

	// TokenRefreshBuffer is the time before expiry considered effectively
	// expired, covering clock skew and in-flight requests.
	TokenRefreshBuffer = 5 * time.Minute

	// defaultExpiry is used when Google omits expires_in. Google's access
	// tokens are an hour; assuming that and refreshing early is safer than
	// assuming a long life and serving a dead token.
	defaultExpiry = time.Hour
)

// IsTokenExpired reports whether the stored expiry has passed, or is close
// enough to count. A zero expiry means "we do not know", which is treated as
// expired so the refresher runs rather than a dead token being sent.
func IsTokenExpired(expiresAt time.Time) bool {
	if expiresAt.IsZero() {
		return true
	}
	return time.Now().Add(TokenRefreshBuffer).After(expiresAt)
}

// AntigravityOAuthTokenRequest captures OAuth token exchange inputs.
type AntigravityOAuthTokenRequest struct {
	GrantType    string
	Code         string
	CodeVerifier string
	RedirectURI  string
	RefreshToken string
}

// antigravityTokenResponse is a successful response from Google's token endpoint.
type antigravityTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// antigravityTokenError is an error response from Google's token endpoint.
type antigravityTokenError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// ExchangeAntigravityAuthorizationCode exchanges an OAuth authorization code +
// PKCE verifier for Antigravity tokens.
func ExchangeAntigravityAuthorizationCode(code, codeVerifier, redirectURI string) (*AntigravityTokens, error) {
	return exchangeAntigravityTokens(
		AntigravityTokenEndpoint,
		tokenExchangeClient(),
		AntigravityOAuthTokenRequest{
			GrantType:    "authorization_code",
			Code:         code,
			CodeVerifier: codeVerifier,
			RedirectURI:  redirectURI,
		},
	)
}

// RefreshAntigravityTokens exchanges a refresh token for a new access token.
//
// Google does NOT rotate the refresh token on an installed-app refresh: the
// response carries a new access_token and no refresh_token. normalize keeps
// the caller's existing refresh token in that case, so the stored lineage
// survives.
func RefreshAntigravityTokens(refreshToken string) (*AntigravityTokens, error) {
	return exchangeAntigravityTokens(
		AntigravityTokenEndpoint,
		tokenExchangeClient(),
		AntigravityOAuthTokenRequest{
			GrantType:    "refresh_token",
			RefreshToken: refreshToken,
		},
	)
}

func tokenExchangeClient() *http.Client {
	c := llm.ResilientHTTPClient()
	c.Timeout = 30 * time.Second
	return c
}

func exchangeAntigravityTokens(tokenEndpoint string, client *http.Client, req AntigravityOAuthTokenRequest) (*AntigravityTokens, error) {
	if client == nil {
		return nil, fmt.Errorf("http client is required")
	}

	grantType := strings.TrimSpace(req.GrantType)
	if grantType == "" {
		return nil, fmt.Errorf("grant_type is required")
	}

	formData := url.Values{}
	formData.Set("grant_type", grantType)
	formData.Set("client_id", AntigravityClientID)
	formData.Set("client_secret", AntigravityClientSecret)

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
	httpReq.Header.Set("Accept", "application/json")

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

	var tokenResp antigravityTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token exchange response: %w", err)
	}

	return normalizeAntigravityTokens(tokenResp, req.RefreshToken)
}

// normalizeAntigravityTokens turns a raw token response into stored state. An
// empty access token is the one unusable outcome: a 200 that carries no
// credential must surface as an error rather than persist a blank row that
// later looks like a connected provider.
func normalizeAntigravityTokens(tokenResp antigravityTokenResponse, existingRefreshToken string) (*AntigravityTokens, error) {
	accessToken := strings.TrimSpace(tokenResp.AccessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("token exchange response missing access token")
	}

	// Google omits refresh_token on refresh responses (the grant is not
	// rotated), so fall back to the one we just consumed rather than clearing
	// the stored lineage.
	refreshToken := strings.TrimSpace(tokenResp.RefreshToken)
	if refreshToken == "" {
		refreshToken = strings.TrimSpace(existingRefreshToken)
	}

	expiresAt := time.Now().Add(defaultExpiry)
	if tokenResp.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	} else {
		logging.Warn("Antigravity token response missing expires_in; assuming one hour")
	}

	return &AntigravityTokens{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		IDToken:      strings.TrimSpace(tokenResp.IDToken),
		Scope:        strings.TrimSpace(tokenResp.Scope),
	}, nil
}

func mapTokenExchangeError(grantType string, statusCode int, body []byte) error {
	var errResp antigravityTokenError
	if err := json.Unmarshal(body, &errResp); err == nil && strings.TrimSpace(errResp.Error) != "" {
		errCode := strings.TrimSpace(errResp.Error)
		errDesc := strings.TrimSpace(errResp.ErrorDescription)

		switch errCode {
		case "invalid_grant":
			if grantType == "authorization_code" {
				return fmt.Errorf("authorization code is invalid or expired")
			}
			return fmt.Errorf("antigravity session expired: please reconnect Antigravity")
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
