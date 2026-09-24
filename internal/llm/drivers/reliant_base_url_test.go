package drivers

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

var reliantLLMKey = "rlat_" + strings.Repeat("A1b2", 8)

func TestResolveReliantBaseURL_DefaultWhenUnset(t *testing.T) {
	t.Setenv("RELIANT_API_BASE_URL", "")
	assert.Equal(t, reliantNeutralBaseURL, ResolveReliantBaseURL(reliantLLMKey))
}

// The fallback must be a LOCAL admin-server, never a hosted hostname: a hosted
// default would break internal/builddefaults' OSS-clean contract, and the old
// https://api.reliant.dev/v1 did not resolve at all.
func TestResolveReliantBaseURL_FallbackIsNeutralNotHosted(t *testing.T) {
	t.Setenv("RELIANT_API_BASE_URL", "")
	resolved := ResolveReliantBaseURL(reliantLLMKey)
	u, err := url.Parse(resolved)
	assert.NoError(t, err)
	assert.Equal(t, "localhost", u.Hostname(),
		"fallback %q must be loopback; a hosted default breaks the OSS-clean contract", resolved)
	assert.NotContains(t, resolved, "reliant.dev", "reliant.dev does not resolve")
}

func TestResolveReliantBaseURL_UsesConfiguredURL(t *testing.T) {
	t.Setenv("RELIANT_API_BASE_URL", "https://proxy.example.com/v1")
	assert.Equal(t, "https://proxy.example.com/v1", ResolveReliantBaseURL(reliantLLMKey))
}

// TestResolveReliantAPIKey_SendsTheAccessTokenAsIs: the LLM key is an rlat_
// access token authenticated by control-plane's proxy. Nothing re-keys it,
// on loopback or off it, and no forwarding header is set.
func TestResolveReliantAPIKey_SendsTheAccessTokenAsIs(t *testing.T) {
	t.Setenv("LITELLM_MASTER_KEY", "sk-local-master")
	for _, base := range []string{"http://localhost:4000/v1", "http://litellm:4000/v1", "https://proxy.example.com/v1"} {
		key, headers := ResolveReliantAPIKey("  "+reliantLLMKey+"\n", base)
		assert.Equal(t, reliantLLMKey, key, "base %s", base)
		assert.Nil(t, headers, "base %s", base)
	}
}

// TestIsReliantLLMKey_RejectsEveryOtherFamily is reliant's cross-family
// recognizer test for the LLM path. The recognizer it replaced —
// HasPrefix("rly_") || HasPrefix("rlnt_") — accepted a pasted daemon PAT or
// connector credential as a managed LLM key.
func TestIsReliantLLMKey_RejectsEveryOtherFamily(t *testing.T) {
	assert.True(t, IsReliantLLMKey(reliantLLMKey))
	for name, tok := range map[string]string{
		"daemon/api PAT":        "rlnt_pat_" + strings.Repeat("A1b2C3", 5),
		"connector credential":  "rlnt_conn_" + strings.Repeat("Z9y8X7", 5),
		"retired rlnt_ LLM key": "rlnt_" + strings.Repeat("aB-_", 10) + "xyz",
		"retired rly_ key":      "rly_" + strings.Repeat("0a1b2c3d", 8),
		"port share link":       "dpat_123e4567-e89b-12d3-a456-426614174000",
		"openai key":            "sk-" + strings.Repeat("x", 40),
		"truncated rlat_":       reliantLLMKey[:len(reliantLLMKey)-1],
	} {
		assert.False(t, IsReliantLLMKey(tok), "%s was recognized as a Reliant LLM key", name)
	}
}
