// Copyright (c) 2025 Reliant Labs
package copilot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/logging"
)

// modelsEndpoint returns the per-account catalog GitHub Copilot serves for the
// authenticated user, including each model's policy state.
const modelsEndpoint = individualBaseURL + "/models"

// availabilityTTL bounds how long a per-account /models result is cached. The
// picker calls this on every ListModels, so we avoid hammering the endpoint
// while still reflecting a user toggling a model on/off in GitHub within a few
// minutes.
const availabilityTTL = 5 * time.Minute

// copilotModelsResponse is the subset of GET /models we consume. A model is
// available to the account iff its policy.state is "enabled" or absent (models
// with no policy block are unrestricted); "disabled" means the user must enable
// it in GitHub Copilot settings before requests succeed.
type copilotModelsResponse struct {
	Data []struct {
		ID                 string   `json:"id"`
		SupportedEndpoints []string `json:"supported_endpoints"`
		Capabilities       struct {
			Limits struct {
				// MaxPromptTokens is the largest prompt Copilot accepts for
				// the model. It is reported alongside max_context_window_tokens
				// (input + output) and max_output_tokens, and is usually their
				// difference — but not always: gpt-5-mini is 264,000 / 64,000
				// with a 128,000 prompt cap. Only this field states the cap.
				MaxPromptTokens int `json:"max_prompt_tokens"`
			} `json:"limits"`
		} `json:"capabilities"`
		Policy *struct {
			State string `json:"state"`
		} `json:"policy"`
	} `json:"data"`
}

// availabilityFailureTTL bounds how long a failed /models fetch is remembered, so
// an unreachable endpoint costs one timed-out request per window rather than one
// per LLM call. While it holds, resolution fails open (every model servable).
const availabilityFailureTTL = 30 * time.Second

// availabilityFetchTimeout caps the cache-miss fetch on the resolution path.
const availabilityFetchTimeout = 4 * time.Second

type availabilityEntry struct {
	enabled   map[string]bool     // api_model id -> enabled
	limits    map[string]int      // api_model id -> max prompt tokens, when reported
	endpoints map[string][]string // api_model id -> supported_endpoints, when reported
	err       error               // non-nil: a remembered failed fetch
	fetchedAt time.Time
}

var (
	availabilityMu    sync.Mutex
	availabilityCache = map[string]availabilityEntry{}
)

// tokenKey derives a stable, non-reversible cache key from the GitHub token so
// the raw token is not used as a map key.
func tokenKey(githubToken string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(githubToken)))
	return hex.EncodeToString(sum[:8])
}

// EnabledModels returns the set of Copilot api_model ids the account may use,
// keyed by api_model id with a bool value. Results are cached per token for
// availabilityTTL. A model is enabled iff its policy.state is not "disabled".
func EnabledModels(ctx context.Context, githubToken string) (map[string]bool, error) {
	entry, err := accountModels(ctx, githubToken)
	if err != nil {
		return nil, err
	}
	return entry.enabled, nil
}

// accountModels returns the cached per-account /models view, fetching on a
// miss. A failed fetch is cached for availabilityFailureTTL and returned as an
// error each time it is read, so callers decide whether to fail open.
func accountModels(ctx context.Context, githubToken string) (availabilityEntry, error) {
	githubToken = strings.TrimSpace(githubToken)
	if githubToken == "" {
		return availabilityEntry{}, fmt.Errorf("github token is required to query copilot model availability")
	}

	key := tokenKey(githubToken)

	availabilityMu.Lock()
	if entry, ok := availabilityCache[key]; ok {
		ttl := availabilityTTL
		if entry.err != nil {
			ttl = availabilityFailureTTL
		}
		if time.Since(entry.fetchedAt) < ttl {
			availabilityMu.Unlock()
			return entry, entry.err
		}
	}
	availabilityMu.Unlock()

	fetchCtx, cancel := context.WithTimeout(ctx, availabilityFetchTimeout)
	defer cancel()
	enabled, limits, endpoints, err := fetchEnabledModels(fetchCtx, githubToken)
	entry := availabilityEntry{enabled: enabled, limits: limits, endpoints: endpoints, err: err, fetchedAt: time.Now()}

	availabilityMu.Lock()
	availabilityCache[key] = entry
	availabilityMu.Unlock()

	return entry, err
}

// IsModelEnabled reports whether the given Copilot api_model is enabled for the
// account. Unknown models (not present in GET /models) are treated as enabled so
// a stale/renamed catalog never hides a model Reliant maps; callers that want
// strict behavior can use EnabledModels directly.
func IsModelEnabled(ctx context.Context, githubToken, apiModel string) (bool, error) {
	enabled, err := EnabledModels(ctx, githubToken)
	if err != nil {
		return false, err
	}
	state, known := enabled[strings.TrimSpace(apiModel)]
	if !known {
		return true, nil
	}
	return state, nil
}

func fetchEnabledModels(ctx context.Context, githubToken string) (map[string]bool, map[string]int, map[string][]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsEndpoint, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to build copilot models request: %w", err)
	}
	req.Header.Set("authorization", "Bearer "+githubToken)
	req.Header.Set("accept", "application/json")
	req.Header.Set("copilot-integration-id", copilotIntegrationID)
	req.Header.Set("editor-version", copilotEditorVersion)
	req.Header.Set("user-agent", copilotUserAgent)
	req.Header.Set("x-github-api-version", copilotAPIVersion)

	resp, err := llm.StreamingHTTPClient().Do(req)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("copilot models request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to read copilot models response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, nil, modelsStatusError(resp.StatusCode, body)
	}

	return parseModels(body)
}

// modelsStatusError classifies a non-200 GET /models answer. 400, 401 and 403
// are GitHub refusing the token every Copilot request would carry — a
// malformed token is a 400 "Authorization header is badly formatted", a
// revoked one a 401, an account without a Copilot seat a 403 — so they are a
// registry.CredentialRejectedError and Copilot becomes unavailable. Anything
// else is an outage, and callers fail open.
func modelsStatusError(status int, body []byte) error {
	detail := strings.TrimSpace(string(body))
	if rejected := registry.RejectedCredential(status, detail); rejected != nil {
		return fmt.Errorf("copilot models request: %w", rejected)
	}
	return fmt.Errorf("copilot models request returned status %d: %s", status, detail)
}

// parseEnabledModels maps a GET /models body to api_model -> enabled.
func parseEnabledModels(body []byte) (map[string]bool, error) {
	enabled, _, _, err := parseModels(body)
	return enabled, err
}

// parseModels maps a GET /models body to api_model -> enabled and, where the
// account reports one, api_model -> max prompt tokens.
func parseModels(body []byte) (map[string]bool, map[string]int, map[string][]string, error) {
	var parsed copilotModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to parse copilot models response: %w", err)
	}
	out := make(map[string]bool, len(parsed.Data))
	limits := make(map[string]int, len(parsed.Data))
	endpoints := make(map[string][]string, len(parsed.Data))
	for _, m := range parsed.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		// Enabled unless the policy explicitly says "disabled".
		enabled := true
		if m.Policy != nil && strings.EqualFold(strings.TrimSpace(m.Policy.State), "disabled") {
			enabled = false
		}
		out[id] = enabled
		if p := m.Capabilities.Limits.MaxPromptTokens; p > 0 {
			limits[id] = p
		}
		if len(m.SupportedEndpoints) > 0 {
			endpoints[id] = m.SupportedEndpoints
		}
	}
	return out, limits, endpoints, nil
}

// EvictAvailabilityCache drops any cached availability for the token. Call after
// a Copilot (re)connect so a freshly-authorized account is re-queried on the
// next picker load rather than waiting out the TTL.
func EvictAvailabilityCache(githubToken string) {
	key := tokenKey(githubToken)
	availabilityMu.Lock()
	delete(availabilityCache, key)
	availabilityMu.Unlock()
	logging.Debug("copilot availability cache evicted")
}

// cachedEndpoints returns the account's supported_endpoints for an api_model from
// the availability cache WITHOUT fetching: resolution has already warmed it
// (accountAvailability), and client construction must not add a network call.
// nil means unknown (cold cache, failed fetch, or model not listed).
func cachedEndpoints(githubToken, apiModel string) []string {
	availabilityMu.Lock()
	defer availabilityMu.Unlock()
	entry, ok := availabilityCache[tokenKey(githubToken)]
	if !ok || entry.err != nil {
		return nil
	}
	return entry.endpoints[strings.TrimSpace(apiModel)]
}
