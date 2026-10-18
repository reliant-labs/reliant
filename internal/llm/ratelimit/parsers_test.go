// Copyright (c) 2025 Reliant Labs
package ratelimit

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fixedNow = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func hdrs(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestParseResetTime(t *testing.T) {
	want := time.Unix(1790712000, 0).UTC()
	for name, in := range map[string]string{
		"epoch seconds": "1790712000",
		"epoch millis":  "1790712000000",
		"rfc3339":       "2026-09-29T20:00:00Z",
		"rfc3339 zone":  "2026-09-29T22:00:00+02:00",
		"float seconds": "1790712000.0",
	} {
		got, ok := ParseResetTime(in)
		require.True(t, ok, name)
		assert.True(t, got.Equal(want), "%s: %v", name, got)
	}
	for _, bad := range []string{"", "soon", "12", "-5"} {
		_, ok := ParseResetTime(bad)
		assert.False(t, ok, bad)
	}
}

func TestParseAnthropic_SubscriptionHeaders(t *testing.T) {
	s, ok := ParseAnthropic(hdrs(
		"anthropic-ratelimit-unified-status", "allowed_warning",
		"anthropic-ratelimit-unified-reset", "1790712000",
		"anthropic-ratelimit-unified-representative-claim", "five_hour",
		"anthropic-ratelimit-unified-5h-utilization", "0.82",
		"anthropic-ratelimit-unified-5h-reset", "1790712000",
		"anthropic-ratelimit-unified-7d-utilization", "31",
		"anthropic-ratelimit-unified-7d-reset", "1791000000",
		"anthropic-ratelimit-unified-overage-disabled-reason", "out_of_credits",
	), fixedNow)
	require.True(t, ok)
	assert.Equal(t, StatusWarning, s.Status)
	assert.Equal(t, "5h", s.RepresentativeWindow)
	assert.Equal(t, "out_of_credits", s.OverageDisabledReason)
	require.Len(t, s.Windows, 2)
	five, _ := s.Window("5h")
	used, _ := five.Used()
	assert.InDelta(t, 0.82, used, 1e-9)
	assert.Equal(t, 5*time.Hour, five.Length)
	assert.Equal(t, time.Unix(1790712000, 0).UTC(), five.ResetsAt)
	seven, _ := s.Window("7d")
	used, _ = seven.Used()
	assert.InDelta(t, 0.31, used, 1e-9, "a value above 1 is a percentage")
	assert.Equal(t, 7*24*time.Hour, seven.Length)
}

func TestParseAnthropic_RejectedWithOnlyTheDocumentedHeaders(t *testing.T) {
	// The shape stream_retry.go has read off live 429s since 2026-09.
	s, ok := ParseAnthropic(hdrs(
		"anthropic-ratelimit-unified-status", "rejected",
		"anthropic-ratelimit-unified-reset", "1790712000",
		"anthropic-ratelimit-unified-representative-claim", "seven_day",
	), fixedNow)
	require.True(t, ok)
	assert.Equal(t, StatusRejected, s.Status)
	w, found := s.Window("7d")
	require.True(t, found, "unified-reset is attached to the representative window")
	assert.Equal(t, time.Unix(1790712000, 0).UTC(), w.ResetsAt)
}

func TestParseAnthropic_APIKeyHeaders(t *testing.T) {
	s, ok := ParseAnthropic(hdrs(
		"anthropic-ratelimit-requests-limit", "1000",
		"anthropic-ratelimit-requests-remaining", "999",
		"anthropic-ratelimit-requests-reset", "2026-10-18T12:01:00Z",
		"anthropic-ratelimit-tokens-limit", "80000",
		"anthropic-ratelimit-tokens-remaining", "0",
		"anthropic-ratelimit-tokens-reset", "2026-10-18T12:00:30Z",
		"anthropic-ratelimit-input-tokens-limit", "40000",
		"anthropic-ratelimit-output-tokens-remaining", "8000",
	), fixedNow)
	require.True(t, ok)
	assert.Equal(t, StatusRejected, s.Status, "an exhausted window with no stated status is a rejection")
	req, _ := s.Window("requests")
	require.NotNil(t, req.Limit)
	assert.EqualValues(t, 1000, *req.Limit)
	used, _ := req.Used()
	assert.InDelta(t, 0.001, used, 1e-9)
	assert.Equal(t, time.Date(2026, 10, 18, 12, 1, 0, 0, time.UTC), req.ResetsAt)
	tokens, _ := s.Window("tokens")
	used, _ = tokens.Used()
	assert.InDelta(t, 1.0, used, 1e-9)
	assert.Len(t, s.Windows, 4)
}

func TestParseAnthropic_NoRateLimitHeaders(t *testing.T) {
	_, ok := ParseAnthropic(hdrs("Content-Type", "text/event-stream"), fixedNow)
	assert.False(t, ok)
	_, ok = ParseAnthropic(nil, fixedNow)
	assert.False(t, ok)
}

func TestParseAnthropic_GarbageValuesDropOnlyThatField(t *testing.T) {
	s, ok := ParseAnthropic(hdrs(
		"anthropic-ratelimit-unified-status", "allowed",
		"anthropic-ratelimit-unified-5h-utilization", "lots",
		"anthropic-ratelimit-unified-7d-utilization", "0.5",
	), fixedNow)
	require.True(t, ok)
	assert.Equal(t, StatusAllowed, s.Status)
	_, has5 := s.Window("5h")
	assert.False(t, has5)
	_, has7 := s.Window("7d")
	assert.True(t, has7)
}

func TestAnthropicLimitInfo(t *testing.T) {
	info, explicit := AnthropicLimitInfo(hdrs(
		"anthropic-ratelimit-unified-status", "rejected",
		"anthropic-ratelimit-unified-reset", "2026-09-29T20:00:00Z", // RFC 3339 parsed too
		"anthropic-ratelimit-unified-representative-claim", "five_hour",
		"anthropic-ratelimit-unified-overage-disabled-reason", "out_of_credits",
	))
	assert.True(t, explicit)
	assert.Equal(t, time.Date(2026, 9, 29, 20, 0, 0, 0, time.UTC), info.ResetAt)
	assert.Equal(t, "five_hour", info.Window)
	assert.Equal(t, "out_of_credits", info.OverageReason)

	_, explicit = AnthropicLimitInfo(hdrs("anthropic-ratelimit-unified-status", "allowed"))
	assert.False(t, explicit)
	_, explicit = AnthropicLimitInfo(nil)
	assert.False(t, explicit)
}

func TestAnthropicBodyIsUsageLimit(t *testing.T) {
	assert.True(t, AnthropicBodyIsUsageLimit("rate_limit_error", "You have reached your usage limit. Your limit will reset at 5pm"))
	assert.True(t, AnthropicBodyIsUsageLimit("usage_limit_error", ""))
	assert.False(t, AnthropicBodyIsUsageLimit("rate_limit_error", "Number of request tokens has exceeded your per-minute rate limit"))
	assert.False(t, AnthropicBodyIsUsageLimit("overloaded_error", "Overloaded"))
}

// Header fixtures mirror codex-rs codex-api/src/rate_limits.rs tests.
func TestParseCodex_DefaultFamily(t *testing.T) {
	s, ok := ParseCodex(hdrs(
		"x-codex-primary-used-percent", "12.5",
		"x-codex-primary-window-minutes", "300",
		"x-codex-primary-reset-at", "1704069000",
		"x-codex-secondary-used-percent", "40",
		"x-codex-secondary-window-minutes", "10080",
		"x-codex-secondary-reset-at", "1704500000",
	), fixedNow)
	require.True(t, ok)
	assert.Equal(t, StatusAllowed, s.Status)
	assert.Equal(t, "codex", s.LimitID)
	require.Len(t, s.Windows, 2)
	p, _ := s.Window("primary")
	used, _ := p.Used()
	assert.InDelta(t, 0.125, used, 1e-9)
	assert.Equal(t, 300*time.Minute, p.Length)
	assert.Equal(t, time.Unix(1704069000, 0).UTC(), p.ResetsAt)
	sec, _ := s.Window("secondary")
	assert.Equal(t, 10080*time.Minute, sec.Length)
}

func TestParseCodex_OlderResetAfterSeconds(t *testing.T) {
	s, ok := ParseCodex(hdrs(
		"x-codex-primary-used-percent", "5",
		"x-codex-primary-reset-after-seconds", "3600",
	), fixedNow)
	require.True(t, ok)
	p, _ := s.Window("primary")
	assert.Equal(t, fixedNow.Add(time.Hour), p.ResetsAt)
}

func TestParseCodex_OtherFamiliesAndReachedType(t *testing.T) {
	s, ok := ParseCodex(hdrs(
		"x-codex-primary-used-percent", "100",
		"x-codex-primary-reset-at", "1704069000",
		"x-codex-bengalfox-primary-used-percent", "80",
		"x-codex-bengalfox-primary-window-minutes", "60",
		"x-codex-rate-limit-reached-type", "usage_limit_reached",
	), fixedNow)
	require.True(t, ok)
	assert.Equal(t, StatusRejected, s.Status)
	_, hasDefault := s.Window("primary")
	assert.True(t, hasDefault)
	w, hasOther := s.Window("codex_bengalfox.primary")
	require.True(t, hasOther)
	assert.Equal(t, time.Hour, w.Length)
}

func TestParseCodex_EmptyWindowDroppedAndNoHeaders(t *testing.T) {
	_, ok := ParseCodex(hdrs("x-codex-primary-used-percent", "0"), fixedNow)
	assert.False(t, ok, "codex-rs drops a window with no data at all")
	_, ok = ParseCodex(hdrs("content-type", "text/event-stream"), fixedNow)
	assert.False(t, ok)
}

func TestParseCodex_FullWindowIsRejected(t *testing.T) {
	s, ok := ParseCodex(hdrs("x-codex-secondary-used-percent", "100", "x-codex-secondary-reset-at", "1704069000"), fixedNow)
	require.True(t, ok)
	assert.Equal(t, StatusRejected, s.Status)
}

func TestParseCodexEvent(t *testing.T) {
	payload := `{"type":"codex.rate_limits","plan_type":"plus","rate_limits":{"primary":{"used_percent":42.0,"window_minutes":300,"reset_at":1704069000},"secondary":{"used_percent":10,"window_minutes":10080,"reset_at":1704500000}},"metered_limit_name":"codex"}`
	s, ok := ParseCodexEvent([]byte(payload), fixedNow)
	require.True(t, ok)
	assert.Equal(t, "plus", s.PlanType)
	assert.Equal(t, "codex", s.LimitID)
	assert.Equal(t, StatusAllowed, s.Status)
	p, _ := s.Window("primary")
	used, _ := p.Used()
	assert.InDelta(t, 0.42, used, 1e-9)
	assert.Equal(t, time.Unix(1704069000, 0).UTC(), p.ResetsAt)

	_, ok = ParseCodexEvent([]byte(`{"type":"response.created"}`), fixedNow)
	assert.False(t, ok)
	_, ok = ParseCodexEvent([]byte(`not json`), fixedNow)
	assert.False(t, ok)
	_, ok = ParseCodexEvent([]byte(`{"type":"codex.rate_limits"}`), fixedNow)
	assert.False(t, ok, "an event with no windows carries nothing to record")
}

// Body shape from codex-rs codex-api/src/api_bridge.rs (UsageErrorBody).
func TestCodexUsageLimit(t *testing.T) {
	t.Run("resets_at wins, window from limit_window_minutes", func(t *testing.T) {
		info, ok := CodexUsageLimit([]byte(`{"error":{"type":"usage_limit_reached","plan_type":"plus","resets_at":1790712000,"limit_window_minutes":300}}`), nil, fixedNow)
		require.True(t, ok)
		assert.Equal(t, time.Unix(1790712000, 0).UTC(), info.ResetAt)
		assert.Equal(t, "five_hour", info.Window)
	})
	t.Run("older resets_in_seconds is relative to now", func(t *testing.T) {
		info, ok := CodexUsageLimit([]byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":7200}}`), nil, fixedNow)
		require.True(t, ok)
		assert.Equal(t, fixedNow.Add(2*time.Hour), info.ResetAt)
	})
	t.Run("the SDK-unwrapped inner object is accepted", func(t *testing.T) {
		info, ok := CodexUsageLimit([]byte(`{"type":"usage_limit_reached","resets_at":1790712000,"limit_window_minutes":10080}`), nil, fixedNow)
		require.True(t, ok)
		assert.Equal(t, "seven_day", info.Window)
	})
	t.Run("reset falls back to the exhausted header window", func(t *testing.T) {
		info, ok := CodexUsageLimit([]byte(`{"error":{"type":"usage_limit_reached"}}`), hdrs(
			"x-codex-primary-used-percent", "100", "x-codex-primary-window-minutes", "300", "x-codex-primary-reset-at", "1790712000",
			"x-codex-secondary-used-percent", "20", "x-codex-secondary-reset-at", "1791999999",
		), fixedNow)
		require.True(t, ok)
		assert.Equal(t, time.Unix(1790712000, 0).UTC(), info.ResetAt, "the window that is actually exhausted, not the later one")
		assert.Equal(t, "five_hour", info.Window)
	})
	t.Run("no reset anywhere is still a usage limit", func(t *testing.T) {
		info, ok := CodexUsageLimit([]byte(`{"error":{"type":"usage_limit_reached"}}`), nil, fixedNow)
		require.True(t, ok)
		assert.True(t, info.ResetAt.IsZero())
	})
	t.Run("other 429 bodies are not", func(t *testing.T) {
		for _, b := range []string{`{"error":{"type":"rate_limit_exceeded"}}`, `{"detail":"x"}`, `nope`, ``} {
			_, ok := CodexUsageLimit([]byte(b), nil, fixedNow)
			assert.False(t, ok, b)
		}
	})
}

func TestCodexQuotaExhaustedAndHeaderLimit(t *testing.T) {
	assert.True(t, CodexQuotaExhausted([]byte(`{"error":{"type":"insufficient_quota"}}`)))
	assert.True(t, CodexQuotaExhausted([]byte(`{"error":{"type":"x","code":"credit_balance_exhausted"}}`)))
	assert.True(t, CodexQuotaExhausted([]byte(`{"error":{"type":"usage_not_included"}}`)))
	assert.False(t, CodexQuotaExhausted([]byte(`{"error":{"type":"usage_limit_reached"}}`)))

	info, ok := CodexHeaderLimit(hdrs("x-codex-secondary-used-percent", "100", "x-codex-secondary-window-minutes", "10080", "x-codex-secondary-reset-at", "1790712000"), fixedNow)
	require.True(t, ok)
	assert.Equal(t, "seven_day", info.Window)
	_, ok = CodexHeaderLimit(hdrs("x-codex-primary-used-percent", "50", "x-codex-primary-reset-at", "1790712000"), fixedNow)
	assert.False(t, ok)
}

func TestParseOpenAI(t *testing.T) {
	s, ok := ParseOpenAI(hdrs(
		"x-ratelimit-limit-requests", "500",
		"x-ratelimit-remaining-requests", "499",
		"x-ratelimit-reset-requests", "120ms",
		"x-ratelimit-limit-tokens", "30000",
		"x-ratelimit-remaining-tokens", "0",
		"x-ratelimit-reset-tokens", "6m0s",
	), fixedNow)
	require.True(t, ok)
	assert.Equal(t, StatusRejected, s.Status)
	tok, _ := s.Window("tokens")
	assert.Equal(t, fixedNow.Add(6*time.Minute), tok.ResetsAt)
	req, _ := s.Window("requests")
	assert.Equal(t, fixedNow.Add(120*time.Millisecond), req.ResetsAt)
	assert.EqualValues(t, 499, *req.Remaining)

	s, ok = ParseOpenAI(hdrs("x-ratelimit-limit-requests", "500", "x-ratelimit-remaining-requests", "10", "x-ratelimit-reset-requests", "30"), fixedNow)
	require.True(t, ok)
	assert.Equal(t, StatusAllowed, s.Status)
	w, _ := s.Window("requests")
	assert.Equal(t, fixedNow.Add(30*time.Second), w.ResetsAt, "plain seconds are accepted too")

	_, ok = ParseOpenAI(hdrs("content-type", "x"), fixedNow)
	assert.False(t, ok)
}
