// Copyright (c) 2025 Reliant Labs

package keyvalidate

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

const pastedKey = "sk-ant-api03-keyvalidate-test-secret-9f8e7d"

// stubDriver answers ValidateKey with err; nothing else of llm.Driver is called.
type stubDriver struct {
	llm.Driver
	err error
}

func (d stubDriver) ValidateKey(context.Context) error { return d.err }

// builder is a buildDriver that records the key the driver was built with and returns d, or
// buildErr.
func builder(gotKey *string, d llm.Driver, buildErr error) buildDriver {
	return func(_ models.Model, _ models.Family, opts ...llm.DriverOption) (llm.Driver, error) {
		var o llm.DriverOptions
		for _, opt := range opts {
			opt(&o)
		}
		*gotKey = o.ApiKey
		if buildErr != nil {
			return nil, buildErr
		}
		return d, nil
	}
}

// quoting is a provider or driver diagnostic that carries the key whole and in part.
func quoting(class string) error {
	return errors.New(class + ` GET "https://api.example.test/v1/models?key=` + pastedKey +
		`": echoed bearer ` + pastedKey + ` (prefix ` + pastedKey[:14] + `…, tail …` + pastedKey[len(pastedKey)-10:] + `)`)
}

// Every failure is one of the fixed messages: the provider's text, which can quote the key, never
// reaches the message the rpcs return to their caller.
func TestValidate_ReturnsOnlyFixedMessages(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		pasted    string
		driverErr error
		buildErr  error
		want      string
	}{
		{name: "an unclassified provider error", pasted: pastedKey, driverErr: quoting("upstream: dial tcp: i/o timeout"), want: MsgCheckFailed},
		{name: "a driver that cannot be built", pasted: pastedKey, buildErr: quoting("init:"), want: MsgInitFailed},
		{name: "an auth refusal", pasted: pastedKey, driverErr: quoting("401 Unauthorized:"), want: MsgInvalid},
		{name: "an unfunded account", pasted: pastedKey, driverErr: quoting(`400 {"type":"invalid_request_error","message":"Your credit balance is too low"}`), want: MsgNoCredit},
		{name: "exhausted quota as a 429", pasted: pastedKey, driverErr: quoting(`429 {"error":{"code":"insufficient_quota"}}`), want: MsgNoCredit},
		{name: "a rate limit", pasted: pastedKey, driverErr: quoting("429 Too Many Requests: rate limit reached"), want: MsgRateLimited},
		{name: "a data policy", pasted: pastedKey, driverErr: quoting("404 No endpoints found matching your data policy"), want: MsgDataPolicy},
		{name: "a pasted key with surrounding whitespace", pasted: "  " + pastedKey + "\n", driverErr: quoting("upstream:"), want: MsgCheckFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var gotKey string
			ok, msg := validate(t.Context(), "anthropic", tc.pasted, builder(&gotKey, stubDriver{err: tc.driverErr}, tc.buildErr))
			require.False(t, ok)
			assert.Equal(t, tc.want, msg)
			assert.NotEmpty(t, Reason(msg), "every failure message has a reason code")
			assert.Equal(t, tc.pasted, gotKey, "the driver is still built with the key it checks")
		})
	}

	var gotKey string
	ok, msg := validate(t.Context(), "anthropic", pastedKey, builder(&gotKey, stubDriver{}, nil))
	assert.True(t, ok)
	assert.Equal(t, MsgValid, msg)
	assert.Empty(t, Reason(MsgValid))
	assert.Empty(t, Reason("some provider text"), "only the fixed messages have a reason")
}

// webKeyErrorCode is the web's key-error classifier (frontends/web/src/components/OnboardingFlow/
// onboardingErrors.ts describeKeyError), rule for rule and in its order: what a client of any
// version shows for a message.
func webKeyErrorCode(msg, provider string) string {
	e := strings.ToLower(msg)
	has := func(subs ...string) bool {
		for _, sub := range subs {
			if strings.Contains(e, sub) {
				return true
			}
		}
		return false
	}
	switch {
	case provider == "openrouter" && has("no endpoints found matching your data policy"):
		return "key_data_policy"
	case has("402", "payment required", "insufficient credits", "insufficient_quota", "credit balance is too low", "quota", "billing"):
		return "key_no_credits"
	case has("unauthorized", "401", "invalid"):
		return "key_invalid"
	case has("rate limit", "429"):
		return "key_rate_limited"
	}
	return "key_validation_failed"
}

// Each fixed message keeps the words the web's classifier reads for its class, and matches no
// earlier class: the web tells "no credit" from "invalid" from "rate limited" as it did when the
// message was the provider's text.
func TestFixedMessagesStayClassifiable(t *testing.T) {
	t.Parallel()
	for msg, want := range map[string]string{
		MsgNoCredit:    "key_no_credits",
		MsgInvalid:     "key_invalid",
		MsgRateLimited: "key_rate_limited",
		MsgCheckFailed: "key_validation_failed",
		MsgInitFailed:  "key_validation_failed",
		MsgNoModels:    "key_validation_failed",
	} {
		assert.Equal(t, want, webKeyErrorCode(msg, "anthropic"), "%q", msg)
	}
	assert.Equal(t, "key_data_policy", webKeyErrorCode(MsgDataPolicy, "openrouter"))
}

// The log lines Validate writes carry the provider's diagnostic with the key redacted: neither the
// key nor any run of redactRun of its characters survives. Not parallel: it replaces slog's default.
func TestValidate_LogsNeverCarryTheKey(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	var gotKey string
	validate(t.Context(), "anthropic", pastedKey, builder(&gotKey, stubDriver{err: quoting("upstream: dial tcp: i/o timeout")}, nil))
	validate(t.Context(), "anthropic", pastedKey, builder(&gotKey, nil, quoting("init:")))
	validate(t.Context(), "anthropic", pastedKey, builder(&gotKey, stubDriver{err: quoting("401 Unauthorized:")}, nil))

	logged := buf.String()
	require.Contains(t, logged, "i/o timeout", "the diagnostic is logged")
	require.Contains(t, logged, "init:")
	assert.Contains(t, logged, redacted)
	for i := 0; i+redactRun <= len(pastedKey); i++ {
		require.NotContains(t, logged, pastedKey[i:i+redactRun], "a run of the key at %d is in the log:\n%s", i, logged)
	}
}

func TestRedact(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "a [redacted] b [redacted]", redact("a k1-secret-long b k2-secret-long", "k1-secret-long", " k2-secret-long\n", ""))
	assert.Equal(t, "nothing to hide", redact("nothing to hide", "", "  "), "an empty key redacts nothing")
	out := redact("prefix sk-ant-api03-ab… only", pastedKey)
	assert.NotContains(t, out, "sk-ant-api03", "a quoted prefix of the key is redacted too")
	assert.True(t, strings.Contains(out, redacted))
}
