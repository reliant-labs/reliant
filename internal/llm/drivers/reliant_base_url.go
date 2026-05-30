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
