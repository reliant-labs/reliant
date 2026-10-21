// Copyright (c) 2025 Reliant Labs

// Package providerusage reads what a subscription provider says about a stored
// OAuth credential: who it belongs to, which plan it is on, and how much of
// each usage window is consumed.
//
// Three endpoints are called with the credential's own bearer token (evidence
// in research/provider-load-balancing/DESIGN.md §4.4). All three were verified
// live on 2026-10-19 with the owner's own Claude Max and ChatGPT pro-lite
// credentials: each answered HTTP 200 and testdata/ holds the redacted bodies:
//
//   - GET https://api.anthropic.com/api/oauth/usage    (Claude, windows)
//   - GET https://api.anthropic.com/api/oauth/profile  (Claude, plan + org)
//   - GET https://chatgpt.com/backend-api/wham/usage   (Codex, windows + plan)
//
// claude.ai/api/organizations/<org>/usage is deliberately absent: it
// authenticates with a browser cookie session we do not hold.
//
// Every fetcher comes in two halves: an XxxRaw that performs the one GET and
// returns the body, and a pure ParseXxx that turns the body into a
// ratelimit.Snapshot (or profile). The convenience XxxUsage composes them. The
// split lets the dev probe capture a real body as a fixture and lets the
// parsers be tested against committed fixtures without a network.
//
// Windows come back in the vocabulary internal/llm/ratelimit already uses for
// response headers ("5h", "7d", "primary", "secondary"; a model-scoped weekly
// row is "7d.<model>", an additional Codex limit "<metered_feature>.primary"),
// so a header-derived and a usage-API-derived Snapshot for the same credential
// are comparable. A Snapshot carries no identity; the caller attaches it.
//
// Parsers are tolerant by design: these endpoints are undocumented, so an
// unknown row is skipped and a malformed value drops that one field rather than
// the response. Only a body that is not the expected shape at all is an error.
//
//forge:exclude-contract: stateless functions over an injected one-method HTTP doer; consumers (accountlimits, provideraccount) declare their own narrow interface
package providerusage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
)

// Provider ids, the driver ids the rest of the system uses.
const (
	ProviderClaude = "claude"
	ProviderCodex  = "codex"
)

const (
	claudeUsageURL   = "https://api.anthropic.com/api/oauth/usage"
	claudeProfileURL = "https://api.anthropic.com/api/oauth/profile"
	codexUsageURL    = "https://chatgpt.com/backend-api/wham/usage"

	// claudeBeta is the beta flag the OAuth endpoints require.
	claudeBeta = "oauth-2025-04-20"

	// claudeUserAgent is the claude-cli fingerprint inference already sends
	// (drivers/anthropic claudeCodeProfileFor, baseline release). Keep the
	// release aligned with it: a usage poll from a different client identity
	// than the inference traffic on the same token is needless noise.
	claudeUserAgent = "claude-cli/2.1.204 (external, cli)"

	// codexUserAgent matches drivers/codex CodexUserAgent for the same reason.
	codexUserAgent = "codex-tui/0.162.1 (Mac OS 14.3.0; arm64) Apple_Terminal/453 (codex-tui; 0.162.1)"

	// maxBody bounds how much of a response is read.
	maxBody = 1 << 20

	// errBodySnippet bounds the body excerpt kept on a StatusError.
	errBodySnippet = 200

	defaultTimeout = 15 * time.Second
)

// usageAPIEnabled is the per-provider switch for the usage-API poller. It is
// the knob DESIGN §4.4 reserves for "the endpoint answers 401/403/404 to a plain
// OAuth token": flip the provider to false and the system runs on response
// headers alone.
//
// Both are true on live evidence (2026-10-19, owner's own accounts, bearer
// access token, no refresh): GET /api/oauth/usage and /api/oauth/profile
// answered 200 for a Claude Max 20x credential, and GET /backend-api/wham/usage
// answered 200 for a ChatGPT pro-lite credential. Re-run
// `reliant-dev provider-usage probe` and flip the entry if one starts to answer
// 401/403/404.
var usageAPIEnabled = map[string]bool{
	ProviderClaude: true,
	ProviderCodex:  true,
}

// UsageAPIEnabled reports whether the poller may call provider's usage API.
// Providers without a usage API are always false.
func UsageAPIEnabled(provider string) bool { return usageAPIEnabled[provider] }

// Doer is the slice of *http.Client these functions use. nil means a client
// with a 15 s timeout.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

var defaultClient = observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: defaultTimeout})

var (
	// ErrThrottled matches (errors.Is) a *ThrottledError: the endpoint answered 429.
	ErrThrottled = errors.New("providerusage: usage endpoint throttled")
	// ErrUnauthorized matches a *StatusError for 401 or 403: the credential was
	// refused (expired, wrong scope, or the endpoint does not take OAuth).
	ErrUnauthorized = errors.New("providerusage: credential refused")
	// ErrUnexpectedResponse matches a 2xx body that is not the expected shape.
	ErrUnexpectedResponse = errors.New("providerusage: unexpected response")
)

// ThrottledError is a 429 from a usage endpoint. The usage endpoints are
// publicly reported as aggressively rate limited, so this is an expected
// outcome: back the poll off, never bench the account.
type ThrottledError struct {
	Provider string
	Endpoint string
	// RetryAfter is the server's Retry-After; zero when absent or "0" (Claude
	// has been observed sending 0 while still refusing, so treat zero as unknown).
	RetryAfter time.Duration
}

func (e *ThrottledError) Error() string {
	s := fmt.Sprintf("providerusage: %s %s throttled (HTTP 429)", e.Provider, e.Endpoint)
	if e.RetryAfter > 0 {
		s += fmt.Sprintf(", retry after %s", e.RetryAfter)
	}
	return s
}

// Is makes errors.Is(err, ErrThrottled) true.
func (e *ThrottledError) Is(target error) bool { return target == ErrThrottled }

// StatusError is any other non-2xx answer.
type StatusError struct {
	Provider   string
	Endpoint   string
	StatusCode int
	// Body is a short excerpt of the response body, for diagnosis.
	Body string
}

func (e *StatusError) Error() string {
	s := fmt.Sprintf("providerusage: %s %s: HTTP %d", e.Provider, e.Endpoint, e.StatusCode)
	if e.Body != "" {
		s += ": " + e.Body
	}
	return s
}

// Is makes errors.Is(err, ErrUnauthorized) true for 401 and 403.
func (e *StatusError) Is(target error) bool {
	return target == ErrUnauthorized && (e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden)
}

func unexpected(provider, endpoint, why string) error {
	return fmt.Errorf("%w: %s %s: %s", ErrUnexpectedResponse, provider, endpoint, why)
}

// get performs one GET and returns the body of a 2xx answer. The access token
// is only ever placed in the Authorization header; no error carries it.
func get(ctx context.Context, d Doer, provider, endpoint, url string, headers map[string]string) ([]byte, error) {
	if d == nil {
		d = defaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("providerusage: %s %s: %w", provider, endpoint, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := d.Do(req)
	if err != nil {
		return nil, fmt.Errorf("providerusage: %s %s: %w", provider, endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("providerusage: %s %s: read body: %w", provider, endpoint, err)
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, &ThrottledError{Provider: provider, Endpoint: endpoint, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > errBodySnippet {
			snippet = snippet[:errBodySnippet]
		}
		return nil, &StatusError{Provider: provider, Endpoint: endpoint, StatusCode: resp.StatusCode, Body: snippet}
	}
	return body, nil
}

// parseRetryAfter reads Retry-After as delta-seconds or an HTTP date.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil {
		if n <= 0 {
			return 0
		}
		return time.Duration(n * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}
