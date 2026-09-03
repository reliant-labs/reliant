// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// liveSecrets are concrete secret-shaped values we must never see in logs.
// (Synthetic — not real credentials — but match the real leaked shapes from
// data/logs.txt: sk_live_, sk_test_, whsec_, sb_publishable_, re_.)
var liveSecrets = []string{
	"sk_live_EXAMPLEonly",
	"sk_test_EXAMPLEonly",
	"whsec_aB1cD2eF3gH4iJ5kL6mN7oP8",
	"sb_publishable_9q8w7e6r5t4y3u2i1o0p",
	"re_M8abc123XYZdef456GHI789",
}

func TestRedactString_ScrubsKnownSecretPrefixes(t *testing.T) {
	t.Parallel()
	for _, secret := range liveSecrets {
		line := "API key is " + secret + " trailing"
		out := redactString(line)
		assert.NotContains(t, out, secret, "secret %q must be redacted", secret)
		assert.Contains(t, out, redactedPlaceholder)
		// Non-secret context is preserved.
		assert.Contains(t, out, "API key is")
		assert.Contains(t, out, "trailing")
	}
}

func TestRedactString_ScrubsEnvAssignments(t *testing.T) {
	t.Parallel()
	cases := []string{
		"STRIPE_SECRET=plain-random-value-no-prefix",
		"DATABASE_PASSWORD: aGVsbG8td29ybGQtc3VwZXItc2VjcmV0",
		"api_key=abcdef0123456789abcdef",
		"AUTH_TOKEN=zzzz-not-a-known-prefix-9999",
	}
	for _, c := range cases {
		out := redactString(c)
		assert.Contains(t, out, redactedPlaceholder, "assignment %q should be redacted", c)
		// The key name survives so the log stays diagnostic.
		key := c[:strings.IndexAny(c, ":=")]
		assert.Contains(t, out, strings.TrimSpace(key))
	}
}

func TestRedactString_LeavesNonSecretsAlone(t *testing.T) {
	t.Parallel()
	// Includes keys that merely contain a signal word as a substring (tokens,
	// total_tokens, author) — these must NOT be redacted, or useful debug logging
	// (token counts, etc.) would be destroyed.
	clean := []string{
		"node=call_llm iteration=3 tokens=1421 status=ok",
		"prompt_tokens=900 completion_tokens=521 total_tokens=1421",
		"author=alice token_count=42 authority=admin",
	}
	for _, c := range clean {
		assert.Equal(t, c, redactString(c), "non-secret line altered: %q", c)
	}
}

// TestRedactValue_NodeOutputWithToolResults is the core regression test for the
// leak: a tool reads a .env file, its contents land in a node-output map under
// tool_results[].content, and that map is logged. After redaction, no secret
// byte may appear in the emitted log line.
func TestRedactValue_NodeOutputWithToolResults(t *testing.T) {
	t.Parallel()
	envFileContents := strings.Join([]string{
		"STRIPE_SECRET_KEY=" + liveSecrets[0],
		"STRIPE_WEBHOOK_SECRET=" + liveSecrets[2],
		"NEXT_PUBLIC_SUPABASE_KEY=" + liveSecrets[3],
		"RESEND_API_KEY=" + liveSecrets[4],
	}, "\n")

	// Shape mirrors real node outputs: keyed by node id, each holding tool_results
	// (a []interface{} of maps with a content field) plus response_text.
	nodeOutputs := map[string]interface{}{
		"read_env": map[string]interface{}{
			"tool_results": []interface{}{
				map[string]interface{}{
					"tool_call_id": "tc1",
					"name":         "read_file",
					"content":      envFileContents,
				},
			},
			"response_text": "I read .env; it contains " + liveSecrets[1],
		},
	}

	redacted := redactValue(nodeOutputs)

	// Emit through the real log.Logger interface, exactly as loop_executor does,
	// then assert no secret survives in the rendered keyvals.
	logger := &presetTestLogger{}
	logger.Info("[InlineLoop] Iteration complete, evaluating outputs",
		"loopID", "loop-1",
		"iteration", 0,
		"nodeOutputs", redacted,
	)

	require.Len(t, logger.infos, 1)
	rendered := renderKeyvals(logger.infos[0].keyvals)
	for _, secret := range liveSecrets {
		assert.NotContains(t, rendered, secret,
			"secret %q leaked into emitted log line: %s", secret, rendered)
	}
	assert.Contains(t, rendered, redactedPlaceholder)

	// Original is untouched — redaction must not alter what the agent/engine sees.
	orig := nodeOutputs["read_env"].(map[string]interface{})
	tr := orig["tool_results"].([]interface{})[0].(map[string]interface{})
	assert.Equal(t, envFileContents, tr["content"], "redaction mutated original node output")
}

// renderKeyvals serializes structured-log keyvals the way a slog/temporal sink
// would, so secret bytes anywhere in the value tree show up if present.
func renderKeyvals(keyvals []interface{}) string {
	var b strings.Builder
	for _, kv := range keyvals {
		fmt.Fprintf(&b, "%+v ", kv)
	}
	return b.String()
}
