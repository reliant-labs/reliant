package drivers

import (
	"os"
	"strings"

	accesstoken "github.com/reliant-labs/forge/pkg/accesstoken"
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
	reliantNeutralBaseURL = "http://localhost:8090/v1"
)

func ResolveReliantBaseURL(_ string) string {
	configuredBaseURL := strings.TrimSpace(os.Getenv("RELIANT_API_BASE_URL"))
	if configuredBaseURL == "" {
		return reliantNeutralBaseURL
	}

	return configuredBaseURL
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
