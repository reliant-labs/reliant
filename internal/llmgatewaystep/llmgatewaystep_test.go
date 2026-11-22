// Copyright (c) 2025 Reliant Labs
package llmgatewaystep

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Claude Code gets the gateway and the key in the variables it reads, its
// models by family, the beta switch, and none of the variables that would
// make it authenticate or route another way.
func TestClaudeCodeEnv(t *testing.T) {
	t.Parallel()
	cli, ok := Lookup("claude-code")
	require.True(t, ok)
	env, err := cli.Env("https://gw.example", "rlsk_secret", []string{"claude-sonnet-4-6", "claude-opus-4-1", "claude-haiku-4-5"})
	require.NoError(t, err)

	assert.Equal(t, map[string]string{
		"ANTHROPIC_BASE_URL":                     "https://gw.example",
		"ANTHROPIC_MODEL":                        "claude-sonnet-4-6",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":           "claude-opus-4-1",
		"ANTHROPIC_DEFAULT_SONNET_MODEL":         "claude-sonnet-4-6",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":          "claude-haiku-4-5",
		"ANTHROPIC_SMALL_FAST_MODEL":             "claude-haiku-4-5",
		"CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS": "1",
	}, env.Set)
	assert.Equal(t, map[string]string{"ANTHROPIC_AUTH_TOKEN": "rlsk_secret"}, map[string]string(env.Secret),
		"the key is the one secret, and only in ANTHROPIC_AUTH_TOKEN")
	assert.ElementsMatch(t, []string{
		"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX",
		"ANTHROPIC_VERTEX_PROJECT_ID", "ANTHROPIC_BEDROCK_BASE_URL", "ANTHROPIC_VERTEX_BASE_URL",
		"AWS_BEARER_TOKEN_BEDROCK", "ANTHROPIC_CUSTOM_HEADERS",
	}, env.Unset)
	for name := range env.Set {
		assert.NotContains(t, env.Unset, name)
	}
}

// A family the models do not cover falls back to the main model; no models
// leaves the CLI's own choice of model alone.
func TestClaudeCodeEnvModelFallback(t *testing.T) {
	t.Parallel()
	cli, _ := Lookup(CLIClaudeCode)
	env, err := cli.Env("https://gw", "k", []string{"claude-sonnet-4-6"})
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-6", env.Set["ANTHROPIC_DEFAULT_OPUS_MODEL"])
	assert.Equal(t, "claude-sonnet-4-6", env.Set["ANTHROPIC_SMALL_FAST_MODEL"])

	env, err = cli.Env("https://gw", "k", nil)
	require.NoError(t, err)
	_, has := env.Set["ANTHROPIC_MODEL"]
	assert.False(t, has)

	_, err = cli.Env("", "k", nil)
	assert.Error(t, err, "no gateway url, no environment")
	_, err = cli.Env("https://gw", "", nil)
	assert.Error(t, err, "no key, no environment")
}

// Every variable the mapping sets or unsets is reserved from a node's env.
func TestNamesCoverEveryVariable(t *testing.T) {
	t.Parallel()
	cli, _ := Lookup(CLIClaudeCode)
	env, err := cli.Env("https://gw", "k", []string{"m"})
	require.NoError(t, err)
	names := cli.Names()
	for name := range env.Set {
		assert.Contains(t, names, name)
	}
	for name := range env.Secret {
		assert.Contains(t, names, name)
	}
	for _, name := range env.Unset {
		assert.Contains(t, names, name)
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		cli    string
		budget float64
		models []string
		err    string
	}{
		{cli: "claude-code"},
		{cli: "claude-code", budget: 50, models: []string{"a", "b"}},
		{cli: "", err: "needs a cli"},
		{cli: "codex", err: `"codex" is not one`},
		{cli: "claude-code", budget: -1, err: "budget_usd"},
		{cli: "claude-code", budget: 50.01, err: "budget_usd"},
		{cli: "claude-code", models: []string{"a", " "}, err: "empty"},
		{cli: "claude-code", models: []string{"a", "a"}, err: "twice"},
	} {
		err := Validate(tc.cli, tc.budget, tc.models)
		if tc.err == "" {
			assert.NoError(t, err, "%+v", tc)
			continue
		}
		require.Error(t, err, "%+v", tc)
		assert.Contains(t, err.Error(), tc.err)
	}
}

func TestBudgetAndTTL(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 5.0, Budget(0))
	assert.Equal(t, 2.0, Budget(2))
	assert.Equal(t, 7*time.Minute, TTL(0), "default timeout 5m + 2m")
	assert.Equal(t, 22*time.Minute, TTL(20*time.Minute))
	assert.Equal(t, 12*time.Hour, TTL(24*time.Hour), "capped")
}

func TestMentionsBinary(t *testing.T) {
	t.Parallel()
	cli, _ := Lookup(CLIClaudeCode)
	for cmd, want := range map[string]bool{
		`claude -p "fix it" --output-format json`: true,
		`cd app && claude -p x`:                   true,
		`/usr/local/bin/claude -p x`:              true,
		`npx claude -p x`:                         true,
		`echo claudette`:                          false,
		`./scripts/claude-review.sh`:              false,
		`go test ./...`:                           false,
		"   ":                                     false,
	} {
		assert.Equal(t, want, cli.MentionsBinary(cmd), cmd)
	}
}
