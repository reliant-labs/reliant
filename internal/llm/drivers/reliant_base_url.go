package drivers

import (
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	// reliantNeutralBaseURL is the fallback when RELIANT_API_BASE_URL is unset:
	// a LOCAL admin-server on the port control-plane's KCL pins for it
	// (deploy/kcl/lib/ports.k ADMIN_SERVER_PORT = 8090).
	//
	// Loopback, and NOT the hosted admin-server — unlike
	// internal/builddefaults, whose defaults are now the hosted endpoints so a
	// `go install` CLI works out of the box. The difference is who reads this:
	// builddefaults configures the CLI/daemon a USER runs, while this is read
	// by the api-server and temporal worker, which are SERVER workloads that
	// KCL always configures explicitly (RELIANT_API_BASE_URL in
	// deploy/kcl/lib/env.k for the cloud envs, deploy/kcl/dev/main.k for the
	// local stack). A server process reaching this fallback is misconfigured,
	// and pointing it at Reliant's production LLM proxy would hide that.
	//
	// This was https://api.reliant.dev/v1, a domain that does not resolve, so
	// a process missing the variable failed with "dial tcp: lookup
	// api.reliant.dev: no such host" rather than anything that named the real
	// problem. Loopback at least fails against something the operator controls.
	reliantNeutralBaseURL          = "http://localhost:8090/v1"
	reliantLocalLiteLLMMasterKey   = "sk-reliant-litellm-dev"
	reliantManagedKeyForwardHeader = "X-Reliant-Managed-Key"
)

func ResolveReliantBaseURL(_ string) string {
	configuredBaseURL := strings.TrimSpace(os.Getenv("RELIANT_API_BASE_URL"))
	if configuredBaseURL == "" {
		return reliantNeutralBaseURL
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
