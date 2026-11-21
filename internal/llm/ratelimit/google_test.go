// Copyright (c) 2025 Reliant Labs
package ratelimit

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A per-minute RESOURCE_EXHAUSTED retries after its RetryInfo; a daily quota
// and a quota of zero do not, since no wait inside one request fixes them.
func TestDescribeGoogle(t *testing.T) {
	const perMinute = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exceeded","details":[
		{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerMinutePerProjectPerModel","quotaValue":"60"}]},
		{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"17s"}]}}`
	const perDay = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exceeded","details":[
		{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier","quotaValue":"250"}]},
		{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"43s"}]}}`
	const zeroQuota = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[
		{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateContentInputTokensPerModelPerMinute-FreeTier","quotaValue":"0"}]}]}}`
	const cloudCode = `[{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[
		{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","metadata":{"quotaResetDelay":"3h2m","quotaResetTimeStamp":"2026-10-18T15:02:00Z"}}]}}]`

	r := Retryer{Provider: "Gemini", Now: func() time.Time { return fixedNow }}
	decide := func(body string) (time.Duration, bool, error) {
		s, ok := ParseGoogleStatus([]byte(body))
		require.True(t, ok, body)
		f := Failure{Err: errors.New("refused"), Status: GoogleHTTPStatus(s.Code, s.Status)}
		DescribeGoogle(&f, s.Details)
		wait, retry, err := r.Decide(1, f)
		return wait.Delay, retry, err
	}

	delay, retry, err := decide(perMinute)
	require.NoError(t, err)
	assert.True(t, retry)
	assert.Equal(t, 17*time.Second, delay, "RetryInfo is the provider's wait")

	_, retry, err = decide(perDay)
	assert.False(t, retry)
	var ule *UsageLimitError
	require.ErrorAs(t, err, &ule, "a daily quota ends the ladder as a usage window")
	assert.Equal(t, "daily", ule.Window)

	_, retry, err = decide(zeroQuota)
	assert.False(t, retry, "a quota of zero has nothing to wait for")
	assert.EqualError(t, err, "refused")

	_, retry, err = decide(cloudCode)
	assert.False(t, retry)
	require.ErrorAs(t, err, &ule)
	assert.Equal(t, time.Date(2026, 10, 18, 15, 2, 0, 0, time.UTC), ule.ResetAt)
}

func TestParseGoogleStatus_RejectsOtherVendorsEnvelopes(t *testing.T) {
	_, ok := ParseGoogleStatus([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow"}}`))
	assert.False(t, ok, "an Anthropic error is not a google.rpc.Status")
	_, ok = ParseGoogleStatus([]byte(`{"error":{"message":"x","code":"rate_limit_exceeded"}}`))
	assert.False(t, ok, "an OpenAI error is not a google.rpc.Status")
	_, ok = ParseGoogleStatus([]byte(`not json`))
	assert.False(t, ok)
}

func TestGoogleHTTPStatus_InBandStatusNames(t *testing.T) {
	assert.Equal(t, 429, GoogleHTTPStatus(0, "RESOURCE_EXHAUSTED"))
	assert.Equal(t, 503, GoogleHTTPStatus(0, "UNAVAILABLE"))
	assert.Equal(t, 400, GoogleHTTPStatus(0, "INVALID_ARGUMENT"), "an overflow or a bad request stays a 400 and is never retried")
	assert.Equal(t, 0, GoogleHTTPStatus(0, "SOMETHING_NEW"))
	assert.Equal(t, 500, GoogleHTTPStatus(500, "RESOURCE_EXHAUSTED"), "a wire code wins over the name")
}
