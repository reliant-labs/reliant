// Copyright (c) 2025 Reliant Labs

package keyvalidate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

const pastedKey = "sk-ant-api03-Zq7xW-pasted-secret-9f8e7d"

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

// echo is a provider diagnostic that quotes key whole, as a prefix and as a suffix.
func echo(class, key string) error {
	r := []rune(key)
	return errors.New(class + " key=" + key + " prefix=" + string(r[:len(r)/2]) + "… suffix=…" + string(r[len(r)/2:]) + " detail=i/o timeout")
}

// The log lines Validate writes carry the reason, the HTTP status and the error's types, and never
// the diagnostic's text: no run of 4 of the key's characters reaches the log, for an ASCII key or
// a multibyte one, echoed whole, as a prefix or as a suffix. Not parallel: it replaces slog's
// default.
func TestValidate_LogsNeverCarryTheKey(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	for _, key := range []string{pastedKey, "sk-ключ-élan-密钥钥匙-🔑🔑-ünïcødé-tail", "  sk-ключ-trailing-space-密钥\n"} {
		var gotKey string
		buf.Reset()
		validate(t.Context(), "anthropic", key, builder(&gotKey, stubDriver{err: echo("503 Service Unavailable upstream:", key)}, nil))
		validate(t.Context(), "anthropic", key, builder(&gotKey, nil, echo("init:", key)))
		validate(t.Context(), "anthropic", key, builder(&gotKey, stubDriver{err: echo("429 rate limit", key)}, nil))
		logged := buf.String()
		require.True(t, utf8.ValidString(logged))
		assert.Contains(t, logged, "reason="+ReasonCheckFailed)
		assert.Contains(t, logged, "reason="+ReasonInitFailed)
		assert.Contains(t, logged, "reason="+ReasonRateLimited)
		assert.Contains(t, logged, "http_status=503")
		assert.Contains(t, logged, "http_status=429")
		assert.Contains(t, logged, "error_types=*errors.errorString")
		assert.NotContains(t, logged, "i/o timeout", "the diagnostic's text is never logged")
		r := []rune(strings.TrimSpace(key))
		for i := 0; i+4 <= len(r); i++ {
			require.NotContains(t, logged, string(r[i:i+4]), "a run of the key at rune %d is in the log:\n%s", i, logged)
		}
	}
}

func TestErrorFacts(t *testing.T) {
	t.Parallel()
	wrapped := fmt.Errorf("check: %w", errors.New("POST /v1/messages: 401 Unauthorized"))
	assert.Equal(t, []any{"http_status", "401", "error_types", "*fmt.wrapError > *errors.errorString"}, errorFacts(wrapped))
	assert.Equal(t, []any{"http_status", "", "error_types", "*errors.errorString"}, errorFacts(errors.New("key sk-4010-x")),
		"a status inside a longer token is not one")
}
