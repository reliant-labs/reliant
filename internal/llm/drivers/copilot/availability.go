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
		ID     string `json:"id"`
		Policy *struct {
			State string `json:"state"`
		} `json:"policy"`
	} `json:"data"`
}

type availabilityEntry struct {
	enabled   map[string]bool // api_model id -> enabled
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
	githubToken = strings.TrimSpace(githubToken)
	if githubToken == "" {
		return nil, fmt.Errorf("github token is required to query copilot model availability")
	}

	key := tokenKey(githubToken)

	availabilityMu.Lock()
	if entry, ok := availabilityCache[key]; ok && time.Since(entry.fetchedAt) < availabilityTTL {
		availabilityMu.Unlock()
		return entry.enabled, nil
	}
	availabilityMu.Unlock()

	enabled, err := fetchEnabledModels(ctx, githubToken)
	if err != nil {
		return nil, err
	}

	availabilityMu.Lock()
	availabilityCache[key] = availabilityEntry{enabled: enabled, fetchedAt: time.Now()}
	availabilityMu.Unlock()

	return enabled, nil
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

func fetchEnabledModels(ctx context.Context, githubToken string) (map[string]bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build copilot models request: %w", err)
	}
	req.Header.Set("authorization", "Bearer "+githubToken)
	req.Header.Set("accept", "application/json")
	req.Header.Set("copilot-integration-id", copilotIntegrationID)
	req.Header.Set("editor-version", copilotEditorVersion)
	req.Header.Set("user-agent", copilotUserAgent)
	req.Header.Set("x-github-api-version", copilotAPIVersion)

	resp, err := llm.StreamingHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot models request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read copilot models response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("copilot models request returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return parseEnabledModels(body)
}

// parseEnabledModels maps a GET /models body to api_model -> enabled.
func parseEnabledModels(body []byte) (map[string]bool, error) {
	var parsed copilotModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse copilot models response: %w", err)
	}
	out := make(map[string]bool, len(parsed.Data))
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
	}
	return out, nil
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
