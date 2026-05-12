package drivers

import (
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	reliantDefaultBaseURL          = "https://api.reliant.dev/v1"
	reliantLocalLiteLLMMasterKey   = "sk-reliant-litellm-dev"
	reliantManagedKeyForwardHeader = "X-Reliant-Managed-Key"
)

func ResolveReliantBaseURL(_ string) string {
	configuredBaseURL := strings.TrimSpace(os.Getenv("RELIANT_API_BASE_URL"))
	if configuredBaseURL == "" {
		return reliantDefaultBaseURL
	}

	return configuredBaseURL
}

func ResolveReliantAPIKey(apiKey, baseURL string) (string, map[string]string) {
	trimmedKey := strings.TrimSpace(apiKey)
	if trimmedKey == "" {
		return trimmedKey, nil
	}

	// JWT tokens are forwarded as Bearer auth to the proxy.
	// In local dev (LiteLLM), swap to master key + forward the JWT in a header.
	if isJWT(trimmedKey) {
		if !isLocalLiteLLMBaseURL(baseURL) {
			// Production: use JWT directly as Bearer token
			return trimmedKey, nil
		}
		// Local dev: use LiteLLM master key, forward JWT in header
		masterKey := strings.TrimSpace(os.Getenv("LITELLM_MASTER_KEY"))
		if masterKey == "" {
			masterKey = reliantLocalLiteLLMMasterKey
			logging.Warn("LITELLM_MASTER_KEY not set; using default local LiteLLM master key for JWT auth", "base_url", baseURL)
		}
		return masterKey, map[string]string{
			"X-Reliant-JWT": trimmedKey,
		}
	}

	// Legacy managed keys (rlnt_/rly_): keep existing behavior for backward compat
	if isManagedReliantKey(trimmedKey) && isLocalLiteLLMBaseURL(baseURL) {
		masterKey := strings.TrimSpace(os.Getenv("LITELLM_MASTER_KEY"))
		if masterKey == "" {
			masterKey = reliantLocalLiteLLMMasterKey
			logging.Warn("LITELLM_MASTER_KEY not set; using default local LiteLLM master key for managed Reliant token", "base_url", baseURL)
		}
		return masterKey, map[string]string{
			reliantManagedKeyForwardHeader: trimmedKey,
		}
	}

	return trimmedKey, nil
}

func isManagedReliantKey(apiKey string) bool {
	trimmedKey := strings.TrimSpace(apiKey)
	return strings.HasPrefix(trimmedKey, "rly_") || strings.HasPrefix(trimmedKey, "rlnt_")
}

// isJWT returns true if the token looks like a JWT (three dot-separated base64 segments).
func isJWT(token string) bool {
	parts := strings.Split(token, ".")
	return len(parts) == 3 && len(parts[0]) > 10
}

func isLocalLiteLLMBaseURL(rawURL string) bool {
	parsedURL, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}

	hostname := strings.TrimSpace(parsedURL.Hostname())
	if hostname == "" {
		return false
	}

	if hostname == "localhost" {
		return true
	}

	parsedIP := net.ParseIP(hostname)
	if parsedIP != nil {
		return parsedIP.IsLoopback()
	}

	normalizedHostname := strings.TrimSuffix(strings.ToLower(hostname), ".")
	if normalizedHostname == "litellm" {
		return true
	}

	labels := strings.Split(normalizedHostname, ".")
	return len(labels) >= 3 && labels[0] == "litellm" && labels[2] == "svc"
}
