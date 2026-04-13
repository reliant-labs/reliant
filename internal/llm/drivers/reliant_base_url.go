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
	if trimmedKey == "" || !isManagedReliantKey(trimmedKey) || !isLoopbackBaseURL(baseURL) {
		return trimmedKey, nil
	}

	masterKey := strings.TrimSpace(os.Getenv("LITELLM_MASTER_KEY"))
	if masterKey == "" {
		masterKey = reliantLocalLiteLLMMasterKey
		logging.Warn("LITELLM_MASTER_KEY not set; using default local LiteLLM master key for managed Reliant token", "base_url", baseURL)
	}

	return masterKey, map[string]string{
		reliantManagedKeyForwardHeader: trimmedKey,
	}
}

func isManagedReliantKey(apiKey string) bool {
	trimmedKey := strings.TrimSpace(apiKey)
	return strings.HasPrefix(trimmedKey, "rly_") || strings.HasPrefix(trimmedKey, "rlnt_")
}

func isLoopbackBaseURL(rawURL string) bool {
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
	return parsedIP != nil && parsedIP.IsLoopback()
}