package drivers

import (
	"strings"
	"sync/atomic"
	"testing"

	accesstoken "github.com/reliant-labs/forge/pkg/accesstoken"
)

const (
	// reliantNeutralBaseURL is the fallback when RELIANT_API_BASE_URL is unset:
	// a LOCAL admin-server on the port control-plane's KCL pins for it
	// (deploy/kcl/lib/ports.k ADMIN_SERVER_PORT = 8090).
	//
	// Loopback, and NOT the hosted admin-server — unlike CLIConfig's server
	// and gateway, whose defaults are the hosted endpoints so a `go install`
	// CLI works out of the box. The difference is who reads this: CLIConfig
	// configures the CLI/daemon a USER runs, while this is read
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
	reliantNeutralBaseURL = "http://localhost:8090/v1"
)

// reliantBaseURL is the process's configured Reliant LLM gateway URL
// (RELIANT_API_BASE_URL, the server role's LLM config); nil or empty is the
// neutral fallback.
var reliantBaseURL atomic.Pointer[string]

// ConfigureReliantBaseURL sets the Reliant LLM gateway URL. A server role calls
// it once at boot, from its config, before any LLM call.
func ConfigureReliantBaseURL(url string) {
	url = strings.TrimSpace(url)
	reliantBaseURL.Store(&url)
}

// SetReliantBaseURLForTest sets the gateway URL for the length of a test.
func SetReliantBaseURLForTest(tb testing.TB, url string) {
	tb.Helper()
	prev := reliantBaseURL.Load()
	ConfigureReliantBaseURL(url)
	tb.Cleanup(func() { reliantBaseURL.Store(prev) })
}

func ResolveReliantBaseURL(_ string) string {
	if configured := reliantBaseURL.Load(); configured != nil && *configured != "" {
		return *configured
	}
	return reliantNeutralBaseURL
}

// ResolveReliantAPIKey returns the bearer to send to the Reliant LLM gateway.
//
// The Reliant LLM key is an `rlat_` access token (llm:invoke) minted by
// control-plane, authenticated by control-plane's LLM proxy at
// RELIANT_API_BASE_URL. It is sent as-is: there is no local re-keying.
//
// There is no local re-keying: an earlier loose prefix test re-keyed any
// reliant-prefixed string (daemon and connector credentials included) as a
// "managed" LLM key against a loopback LiteLLM. The exact-shape check is
// IsReliantLLMKey below. The second return value is kept for the callers'
// extra-header plumbing and is always nil.
func ResolveReliantAPIKey(apiKey, _ string) (string, map[string]string) {
	return strings.TrimSpace(apiKey), nil
}

// IsReliantLLMKey reports whether a key has the exact shape of a Reliant LLM
// gateway key — an `rlat_` access token — and nothing looser.
func IsReliantLLMKey(apiKey string) bool {
	return accesstoken.HasFormat(strings.TrimSpace(apiKey))
}
