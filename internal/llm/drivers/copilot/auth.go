// Copyright (c) 2025 Reliant Labs
package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/logging"
)

// GitHub device-code OAuth constants. These mirror the ground-truth capture in
// .dev/copilot/login.curl exactly — the client_id, scopes, endpoints and grant
// type are what the GitHub Copilot CLI uses, and GitHub validates them.
const (
	// GitHubClientID is the public OAuth client id used by the Copilot CLI.
	// #nosec G101 -- public OAuth client identifier, not a secret
	GitHubClientID = "Ov23ctDVkRmgkPke0Mmm"

	// GitHubDeviceScopes are the OAuth scopes requested during the device flow.
	GitHubDeviceScopes = "read:user,read:org,repo,gist"

	// GitHubDeviceCodeEndpoint starts the device-authorization flow.
	// #nosec G101 -- OAuth endpoint URL, not a credential
	GitHubDeviceCodeEndpoint = "https://github.com/login/device/code"

	// GitHubAccessTokenEndpoint is polled to exchange an authorized device code
	// for a GitHub OAuth access token.
	// #nosec G101 -- OAuth endpoint URL, not a credential
	GitHubAccessTokenEndpoint = "https://github.com/login/oauth/access_token"

	// GitHubDeviceGrantType is the OAuth grant type for the device flow.
	GitHubDeviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

	// deviceUserAgent matches the capture; GitHub is lenient here but we keep it
	// aligned with the Copilot CLI.
	deviceUserAgent = "copilot/1.0.69 (darwin v24.16.0) term/Apple_Terminal"
)

// DeviceAuth is the result of starting the device-authorization flow. The caller
// (a backend RPC handler in a later stage) presents UserCode + VerificationURI
// to the user, then polls with the DeviceCode.
type DeviceAuth struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// GitHubTokens is the normalized result of a completed device flow.
type GitHubTokens struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
}

// deviceCodeResponse is the raw github.com/login/device/code response.
type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// accessTokenResponse is the raw github.com/login/oauth/access_token response.
// While the flow is pending it carries an `error` (authorization_pending,
// slow_down, expired_token, access_denied); on success it carries the token.
type accessTokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	Interval         int    `json:"interval"`
}

// ErrAuthorizationPending is returned by PollDeviceAuthOnce while the user has
// not yet approved the device code.
var ErrAuthorizationPending = fmt.Errorf("authorization pending")

// ErrSlowDown is returned by PollDeviceAuthOnce when GitHub asks us to back off.
var ErrSlowDown = fmt.Errorf("slow down")

// ErrDeviceCodeExpired is returned by PollDeviceAuthOnce when the device code
// has expired and the login flow must be restarted.
var ErrDeviceCodeExpired = fmt.Errorf("device code expired: restart the login flow")

// ErrAccessDenied is returned by PollDeviceAuthOnce when the user declined the
// authorization request on GitHub.
var ErrAccessDenied = fmt.Errorf("authorization denied by user")

// secretKeySubstrings identifies response field names that carry credentials
// and must never be logged verbatim. Matching is case-insensitive substring, so
// this catches token, access_token, copilot-session-token, refresh_token,
// api_key, etc. while leaving expiry/endpoint/sku/flag fields intact.
var secretKeySubstrings = []string{"token", "secret", "password", "credential", "_key", "apikey", "api_key"}

// looksSecret reports whether a response field name should be redacted.
func looksSecret(key string) bool {
	lk := strings.ToLower(key)
	for _, s := range secretKeySubstrings {
		if strings.Contains(lk, s) {
			return true
		}
	}
	return false
}

// redactSecret renders a secret string as its length plus a short prefix so we
// can tell tokens apart in logs without ever emitting the full value.
func redactSecret(s string) string {
	prefix := s
	if len(prefix) > 6 {
		prefix = prefix[:6]
	}
	return fmt.Sprintf("len=%d prefix=%q…", len(s), prefix)
}

// redactValue recursively redacts secret-looking fields in an arbitrary JSON
// value. Non-secret scalars/objects/arrays pass through unchanged so nested
// structures (endpoints maps, available_models arrays) stay legible.
func redactValue(key string, v any) any {
	if looksSecret(key) {
		if sv, ok := v.(string); ok {
			return redactSecret(sv)
		}
		return "<redacted>"
	}
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = redactValue(k, vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = redactValue(key, vv)
		}
		return out
	default:
		return v
	}
}

// summarizeJSONResponse parses body as a generic object and returns a sorted key
// list plus a compact, secret-redacted JSON rendering of the whole structure,
// suitable for a single diagnostic log line. This is how we discover fields the
// typed structs currently discard (e.g. an individual-tier session token,
// account id, or available_models list).
func summarizeJSONResponse(body []byte) (keys []string, fields string) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Sprintf("<unparseable JSON: %v>", err)
	}
	keys = make([]string, 0, len(m))
	redacted := make(map[string]any, len(m))
	for k, v := range m {
		keys = append(keys, k)
		redacted[k] = redactValue(k, v)
	}
	sort.Strings(keys)
	if b, err := json.Marshal(redacted); err == nil {
		fields = string(b)
	} else {
		fields = fmt.Sprintf("%v", redacted)
	}
	return keys, fields
}

func oauthHTTPClient() *http.Client {
	c := llm.ResilientHTTPClient()
	c.Timeout = 30 * time.Second
	return c
}

// StartDeviceAuth begins the GitHub device-authorization flow. It POSTs the
// client id + scopes to the device-code endpoint and returns the user code,
// verification URI and polling interval.
func StartDeviceAuth(ctx context.Context) (*DeviceAuth, error) {
	form := url.Values{}
	form.Set("client_id", GitHubClientID)
	form.Set("scope", GitHubDeviceScopes)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, GitHubDeviceCodeEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create device-code request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", deviceUserAgent)

	resp, err := oauthHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("device-code request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read device-code response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device-code request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var dc deviceCodeResponse
	if err := json.Unmarshal(body, &dc); err != nil {
		return nil, fmt.Errorf("failed to parse device-code response: %w", err)
	}
	if dc.DeviceCode == "" || dc.UserCode == "" {
		return nil, fmt.Errorf("device-code response missing device_code/user_code")
	}
	if dc.Interval <= 0 {
		dc.Interval = 5 // GitHub default poll interval
	}

	logging.Info("[Copilot][auth] device/code response",
		"status", resp.StatusCode,
		"verification_uri", dc.VerificationURI,
		"has_user_code", dc.UserCode != "",
		"interval", dc.Interval,
		"expires_in", dc.ExpiresIn)

	return &DeviceAuth{
		DeviceCode:      dc.DeviceCode,
		UserCode:        dc.UserCode,
		VerificationURI: dc.VerificationURI,
		ExpiresIn:       dc.ExpiresIn,
		Interval:        dc.Interval,
	}, nil
}

// PollDeviceAuthOnce performs a single poll of the access-token endpoint.
// It returns:
//   - (tokens, nil) once the user has authorized the device,
//   - (nil, ErrAuthorizationPending) while still waiting,
//   - (nil, ErrSlowDown) when GitHub asks us to back off (add to the interval),
//   - (nil, other error) on a terminal failure (access_denied, expired_token).
//
// Backend RPC handlers that want a blocking helper should use PollDeviceAuth.
func PollDeviceAuthOnce(ctx context.Context, deviceCode string) (*GitHubTokens, error) {
	form := url.Values{}
	form.Set("client_id", GitHubClientID)
	form.Set("device_code", deviceCode)
	form.Set("grant_type", GitHubDeviceGrantType)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, GitHubAccessTokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create access-token request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", deviceUserAgent)

	resp, err := oauthHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("access-token request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read access-token response: %w", err)
	}

	var at accessTokenResponse
	if err := json.Unmarshal(body, &at); err != nil {
		return nil, fmt.Errorf("failed to parse access-token response (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if at.Error != "" {
		switch at.Error {
		case "authorization_pending":
			return nil, ErrAuthorizationPending
		case "slow_down":
			return nil, ErrSlowDown
		case "expired_token":
			return nil, ErrDeviceCodeExpired
		case "access_denied":
			return nil, ErrAccessDenied
		default:
			if at.ErrorDescription != "" {
				return nil, fmt.Errorf("device authorization failed: %s (%s)", at.Error, at.ErrorDescription)
			}
			return nil, fmt.Errorf("device authorization failed: %s", at.Error)
		}
	}

	if at.AccessToken == "" {
		return nil, fmt.Errorf("access-token response missing access_token")
	}

	keys, redacted := summarizeJSONResponse(body)
	logging.Info("[Copilot][auth] oauth/access_token response (authorized)",
		"status", resp.StatusCode,
		"keys", keys,
		"fields", redacted)

	return &GitHubTokens{
		AccessToken: at.AccessToken,
		TokenType:   at.TokenType,
		Scope:       at.Scope,
	}, nil
}

// PollDeviceAuth blocks until the device flow completes, the context is
// cancelled, or a terminal error occurs. It honors GitHub's polling interval and
// backs off on slow_down. interval is the seconds value returned by
// StartDeviceAuth; a non-positive value defaults to 5s.
func PollDeviceAuth(ctx context.Context, deviceCode string, interval int) (*GitHubTokens, error) {
	if interval <= 0 {
		interval = 5
	}
	delay := time.Duration(interval) * time.Second

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}

		tokens, err := PollDeviceAuthOnce(ctx, deviceCode)
		switch {
		case err == nil:
			return tokens, nil
		case err == ErrAuthorizationPending:
			// keep waiting at the current interval
		case err == ErrSlowDown:
			// GitHub asks us to add (at least) 5s to the interval
			delay += 5 * time.Second
		default:
			return nil, err
		}
	}
}

// resolveGitHubToken finds the GitHub OAuth token to authenticate with, in
// priority order: the credential passed by the resolver (BearerToken/ApiKey),
// the GITHUB_TOKEN env var, then the standard GitHub CLI / Copilot config files.
func resolveGitHubToken(opts llm.DriverOptions) (string, error) {
	if t := strings.TrimSpace(opts.BearerToken); t != "" {
		return t, nil
	}
	if t := strings.TrimSpace(opts.ApiKey); t != "" {
		return t, nil
	}
	if t := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); t != "" {
		return t, nil
	}
	if t, err := loadGitHubTokenFromDisk(); err == nil && t != "" {
		return t, nil
	}
	return "", fmt.Errorf("github token is required for Copilot: connect GitHub Copilot from Settings")
}

// loadGitHubTokenFromDisk reads the GitHub OAuth token from the standard GitHub
// CLI / Copilot config locations. Best-effort developer convenience only.
func loadGitHubTokenFromDisk() (string, error) {
	var configDir string
	switch {
	case os.Getenv("XDG_CONFIG_HOME") != "":
		configDir = os.Getenv("XDG_CONFIG_HOME")
	case runtime.GOOS == "windows":
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			configDir = localAppData
		} else {
			configDir = filepath.Join(os.Getenv("HOME"), "AppData", "Local")
		}
	default:
		configDir = filepath.Join(os.Getenv("HOME"), ".config")
	}

	filePaths := []string{
		filepath.Join(configDir, "github-copilot", "hosts.json"),
		filepath.Join(configDir, "github-copilot", "apps.json"),
	}

	for _, filePath := range filePaths {
		data, err := os.ReadFile(filePath) // #nosec G304 -- fixed config paths
		if err != nil {
			continue
		}
		var config map[string]map[string]interface{}
		if err := json.Unmarshal(data, &config); err != nil {
			continue
		}
		for key, value := range config {
			if strings.Contains(key, "github.com") {
				if oauthToken, ok := value["oauth_token"].(string); ok && oauthToken != "" {
					return oauthToken, nil
				}
			}
		}
	}

	logging.Debug("GitHub token not found in standard Copilot config locations")
	return "", fmt.Errorf("github token not found in standard locations")
}
