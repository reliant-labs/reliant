package drivers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveReliantBaseURL_DefaultWhenUnset(t *testing.T) {
	t.Setenv("RELIANT_API_BASE_URL", "")

	resolved := ResolveReliantBaseURL("rlnt_managed_key")

	assert.Equal(t, reliantNeutralBaseURL, resolved)
}

// The fallback must be a LOCAL admin-server, never a hosted hostname.
//
// It used to be https://api.reliant.dev/v1, which does not resolve — the
// domain is NXDOMAIN. Every managed-Reliant CallLLM in a process that had not
// been given RELIANT_API_BASE_URL died with "dial tcp: lookup api.reliant.dev:
// no such host" instead of failing in a way that named the missing config.
//
// A hosted default would also break internal/builddefaults' OSS-clean
// contract, which this package is downstream of: an un-injected build must
// point a self-hoster at their own stack, never silently at Reliant's. Hosted
// values come from control-plane's KCL, which sets this variable explicitly on
// every workload that needs it (deploy/kcl/lib/env.k for the cloud envs,
// deploy/kcl/dev/main.k for the local stack).
func TestResolveReliantBaseURL_FallbackIsNeutralNotHosted(t *testing.T) {
	t.Setenv("RELIANT_API_BASE_URL", "")

	resolved := ResolveReliantBaseURL("rlnt_managed_key")

	assert.True(t, isLocalLiteLLMBaseURL(resolved),
		"fallback %q must be loopback; a hosted default breaks the OSS-clean contract", resolved)
	assert.NotContains(t, resolved, "reliant.dev", "reliant.dev does not resolve")
}

func TestResolveReliantBaseURL_UsesConfiguredRemoteURLForManagedKey(t *testing.T) {
	t.Setenv("RELIANT_API_BASE_URL", "https://proxy.example.com/v1")

	resolved := ResolveReliantBaseURL("rlnt_managed_key")

	assert.Equal(t, "https://proxy.example.com/v1", resolved)
}

func TestResolveReliantBaseURL_UsesConfiguredLoopbackURLForManagedKey(t *testing.T) {
	t.Setenv("RELIANT_API_BASE_URL", "http://localhost:4000/v1")

	resolved := ResolveReliantBaseURL("rlnt_managed_key")

	assert.Equal(t, "http://localhost:4000/v1", resolved)
}

func TestResolveReliantAPIKey_UsesLoopbackMasterKeyForManagedKey(t *testing.T) {
	t.Setenv("LITELLM_MASTER_KEY", "sk-local-master")

	resolvedKey, headers := ResolveReliantAPIKey("rlnt_managed_key", "http://localhost:4000/v1")

	assert.Equal(t, "sk-local-master", resolvedKey)
	assert.Equal(t, map[string]string{reliantManagedKeyForwardHeader: "rlnt_managed_key"}, headers)
}

func TestResolveReliantAPIKey_UsesInClusterLiteLLMMasterKeyForManagedKey(t *testing.T) {
	t.Setenv("LITELLM_MASTER_KEY", "sk-local-master")

	resolvedKey, headers := ResolveReliantAPIKey("rlnt_managed_key", "http://litellm:4000/v1")

	assert.Equal(t, "sk-local-master", resolvedKey)
	assert.Equal(t, map[string]string{reliantManagedKeyForwardHeader: "rlnt_managed_key"}, headers)
}

func TestResolveReliantAPIKey_FallsBackToDefaultLocalMasterKey(t *testing.T) {
	t.Setenv("LITELLM_MASTER_KEY", "")

	resolvedKey, headers := ResolveReliantAPIKey("rly_legacy_managed_key", "http://127.0.0.1:4000/v1")

	assert.Equal(t, reliantLocalLiteLLMMasterKey, resolvedKey)
	assert.Equal(t, map[string]string{reliantManagedKeyForwardHeader: "rly_legacy_managed_key"}, headers)
}

func TestResolveReliantAPIKey_PreservesManualKeyOffLoopback(t *testing.T) {
	resolvedKey, headers := ResolveReliantAPIKey("sk-litellm-test", "http://127.0.0.1:4000/v1")

	assert.Equal(t, "sk-litellm-test", resolvedKey)
	assert.Nil(t, headers)
}

func TestResolveReliantAPIKey_PreservesManagedKeyForRemoteBaseURL(t *testing.T) {
	// Any non-loopback origin exercises this branch; example.com is a reserved
	// documentation domain, so the fixture cannot be mistaken for a real
	// endpoint the way the old api.reliant.dev value was.
	resolvedKey, headers := ResolveReliantAPIKey("rlnt_managed_key", "https://proxy.example.com/v1")

	assert.Equal(t, "rlnt_managed_key", resolvedKey)
	assert.Nil(t, headers)
}

func TestIsLocalLiteLLMBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		rawURL   string
		expected bool
	}{
		{name: "localhost", rawURL: "http://localhost:4000/v1", expected: true},
		{name: "ipv4 loopback", rawURL: "http://127.0.0.1:4000/v1", expected: true},
		{name: "ipv6 loopback", rawURL: "http://[::1]:4000/v1", expected: true},
		{name: "in-cluster service", rawURL: "http://litellm:4000/v1", expected: true},
		{name: "in-cluster svc domain", rawURL: "http://litellm.reliant-dev-reliant-provider.svc:4000/v1", expected: true},
		{name: "in-cluster cluster-local svc domain", rawURL: "http://litellm.reliant-dev-reliant-provider.svc.cluster.local:4000/v1", expected: true},
		{name: "remote host", rawURL: "https://proxy.example.com/v1", expected: false},
		{name: "remote litellm subdomain", rawURL: "https://litellm.example.com/v1", expected: false},
		{name: "invalid", rawURL: "://bad", expected: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.expected, isLocalLiteLLMBaseURL(test.rawURL))
		})
	}
}
