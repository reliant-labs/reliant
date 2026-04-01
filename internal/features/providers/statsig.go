// Copyright (c) 2025 Reliant Labs
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/features/types"
	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	statsigServerEndpoint = "https://statsigapi.net/v1/check_gate"
	// Using official Statsig API endpoint for client keys (api.statsig.com is also valid)
	statsigClientEndpoint = "https://statsigapi.net/v1/check_gate"
	defaultTimeout        = 10 * time.Second
	cacheTTL              = 30 * time.Second
)

// StatsigProvider implements feature flag evaluation using Statsig
type StatsigProvider struct {
	apiKey      string // Can be either server secret key or client key
	isClientKey bool   // Whether we're using a client key
	environment string
	httpClient  *http.Client
	priority    int
	cache       *featureCache
}

// featureCache provides simple caching for feature evaluations
type featureCache struct {
	entries map[string]*cacheEntry
	mu      sync.RWMutex
}

type cacheEntry struct {
	value      interface{}
	expiration time.Time
}

// NewStatsigProvider creates a new Statsig provider
func NewStatsigProvider(priority int) *StatsigProvider {
	return &StatsigProvider{
		priority: priority,
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
		cache: &featureCache{
			entries: make(map[string]*cacheEntry),
		},
	}
}

func (p *StatsigProvider) Name() string {
	return "statsig"
}

func (p *StatsigProvider) Priority() int {
	return p.priority
}

func (p *StatsigProvider) Initialize(ctx context.Context, config map[string]interface{}) error {
	// Check for client key first (desktop app)
	if key, ok := config["client_key"].(string); ok && key != "" {
		p.apiKey = key
		p.isClientKey = true
	} else if key, ok := config["server_secret_key"].(string); ok && key != "" {
		// Fall back to server key
		p.apiKey = key
		p.isClientKey = false
	} else {
		// Last resort: check environment
		p.apiKey = os.Getenv("STATSIG_SERVER_SECRET_KEY")
		p.isClientKey = false
	}

	// Detect if it's a client key by prefix
	if strings.HasPrefix(p.apiKey, "client-") {
		p.isClientKey = true
	}

	if p.apiKey == "" {
		return fmt.Errorf("statsig API key not configured")
	}

	// Get environment
	if env, ok := config["environment"].(string); ok {
		p.environment = env
	} else {
		p.environment = os.Getenv("STATSIG_ENVIRONMENT")
		if p.environment == "" {
			p.environment = "production"
		}
	}

	// Set timeout if configured
	if timeout, ok := config["timeout"].(string); ok {
		if duration, err := time.ParseDuration(timeout); err == nil {
			p.httpClient.Timeout = duration
		}
	}

	return nil
}

func (p *StatsigProvider) EvaluateBool(ctx context.Context, key string, evalCtx *types.EvaluationContext, defaultValue bool) (bool, error) {
	// Check cache first
	if cached := p.getFromCache(key, evalCtx); cached != nil {
		if val, ok := cached.(bool); ok {
			return val, nil
		}
	}

	// Build Statsig request
	user := p.buildStatsigUser(evalCtx)

	requestBody := map[string]interface{}{
		"gateName": key,
		"user":     user,
		"statsigEnvironment": map[string]string{
			"tier": p.environment,
		},
	}

	body, err := json.Marshal(requestBody)
	if err != nil {
		return defaultValue, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Use the appropriate endpoint based on key type
	endpoint := statsigServerEndpoint
	if p.isClientKey {
		endpoint = statsigClientEndpoint
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return defaultValue, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	// Use lowercase header per Statsig HTTP API specification
	req.Header.Set("statsig-api-key", p.apiKey)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return defaultValue, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Read response body for better error debugging
		body, _ := io.ReadAll(resp.Body)
		logging.Warn("[Statsig] API error", "status", resp.StatusCode, "endpoint", endpoint, "body", string(body))
		return defaultValue, fmt.Errorf("statsig API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Value bool `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return defaultValue, fmt.Errorf("failed to decode response: %w", err)
	}

	// Cache the result
	p.putInCache(key, evalCtx, result.Value)

	return result.Value, nil
}

func (p *StatsigProvider) EvaluateString(ctx context.Context, key string, evalCtx *types.EvaluationContext, defaultValue string) (string, error) {
	// Statsig primarily uses gates (booleans), so we'll use a gate to control string values
	// This is a simplified implementation - in production you might use Statsig's dynamic configs
	enabled, err := p.EvaluateBool(ctx, key+"_enabled", evalCtx, false)
	if err != nil || !enabled {
		return defaultValue, err
	}

	// For string values, we could use dynamic configs or just return a configured value
	// For now, return a simple mapping
	if evalCtx != nil && evalCtx.Custom != nil {
		if val, ok := evalCtx.Custom[key].(string); ok {
			return val, nil
		}
	}

	return defaultValue, nil
}

func (p *StatsigProvider) EvaluateInt(ctx context.Context, key string, evalCtx *types.EvaluationContext, defaultValue int) (int, error) {
	// Similar to string evaluation
	enabled, err := p.EvaluateBool(ctx, key+"_enabled", evalCtx, false)
	if err != nil || !enabled {
		return defaultValue, err
	}

	if evalCtx != nil && evalCtx.Custom != nil {
		if val, ok := evalCtx.Custom[key].(int); ok {
			return val, nil
		}
		if val, ok := evalCtx.Custom[key].(float64); ok {
			return int(val), nil
		}
	}

	return defaultValue, nil
}

func (p *StatsigProvider) EvaluateFloat(ctx context.Context, key string, evalCtx *types.EvaluationContext, defaultValue float64) (float64, error) {
	enabled, err := p.EvaluateBool(ctx, key+"_enabled", evalCtx, false)
	if err != nil || !enabled {
		return defaultValue, err
	}

	if evalCtx != nil && evalCtx.Custom != nil {
		if val, ok := evalCtx.Custom[key].(float64); ok {
			return val, nil
		}
		if val, ok := evalCtx.Custom[key].(int); ok {
			return float64(val), nil
		}
	}

	return defaultValue, nil
}

func (p *StatsigProvider) EvaluateJSON(ctx context.Context, key string, evalCtx *types.EvaluationContext, defaultValue interface{}) (interface{}, error) {
	enabled, err := p.EvaluateBool(ctx, key+"_enabled", evalCtx, false)
	if err != nil || !enabled {
		return defaultValue, err
	}

	if evalCtx != nil && evalCtx.Custom != nil {
		if val, ok := evalCtx.Custom[key]; ok {
			return val, nil
		}
	}

	return defaultValue, nil
}

func (p *StatsigProvider) HealthCheck(ctx context.Context) error {
	// Simple health check - try to evaluate a dummy gate
	_, err := p.EvaluateBool(ctx, "__health_check__", nil, false)
	if err != nil {
		// Ignore the specific error, just check connectivity
		return nil
	}
	return nil
}

func (p *StatsigProvider) Shutdown(ctx context.Context) error {
	// Nothing to clean up for now
	return nil
}

// Helper methods

func (p *StatsigProvider) buildStatsigUser(evalCtx *types.EvaluationContext) map[string]interface{} {
	user := make(map[string]interface{})

	// Use provided context or get global context
	ctx := evalCtx
	// Note: If ctx is nil, we proceed with empty user context.
	// We can't import features package here due to circular dependency.

	if ctx != nil {
		if ctx.UserID != "" {
			user["userID"] = ctx.UserID
		}
		if ctx.SessionID != "" {
			user["sessionID"] = ctx.SessionID
		}

		// Add custom fields
		custom := make(map[string]interface{})
		if ctx.Environment != "" {
			custom["environment"] = ctx.Environment
		}
		// Add version if provided
		if ctx.Version != "" {
			custom["version"] = ctx.Version
		}

		// Merge additional custom fields
		for k, v := range ctx.Custom {
			custom[k] = v
		}

		if len(custom) > 0 {
			user["custom"] = custom
		}
	}

	// Always include a user ID (anonymous if not provided)
	if _, ok := user["userID"]; !ok {
		user["userID"] = "anonymous"
	}

	return user
}

func (p *StatsigProvider) getCacheKey(key string, evalCtx *types.EvaluationContext) string {
	if evalCtx == nil {
		return key
	}

	// Create a unique cache key based on the evaluation context
	cacheKey := fmt.Sprintf("%s:%s:%s:%s", key, evalCtx.UserID, evalCtx.SessionID, evalCtx.Environment)
	return cacheKey
}

func (p *StatsigProvider) getFromCache(key string, evalCtx *types.EvaluationContext) interface{} {
	cacheKey := p.getCacheKey(key, evalCtx)

	p.cache.mu.RLock()
	defer p.cache.mu.RUnlock()

	if entry, ok := p.cache.entries[cacheKey]; ok {
		if time.Now().Before(entry.expiration) {
			return entry.value
		}
	}

	return nil
}

func (p *StatsigProvider) putInCache(key string, evalCtx *types.EvaluationContext, value interface{}) {
	cacheKey := p.getCacheKey(key, evalCtx)

	p.cache.mu.Lock()
	defer p.cache.mu.Unlock()

	p.cache.entries[cacheKey] = &cacheEntry{
		value:      value,
		expiration: time.Now().Add(cacheTTL),
	}
}
