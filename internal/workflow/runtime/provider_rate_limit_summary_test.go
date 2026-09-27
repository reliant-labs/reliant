package runtime

import (
	"errors"
	"strings"
	"testing"

	"go.temporal.io/sdk/temporal"
)

// usageLimitErr is the string a usage-limited CallLLM attempt produces once
// Temporal has flattened it: the marker CallLLM plants, then the SDK's own
// 429 text with the provider's JSON body.
const usageLimitErr = `activity error (type: CallLLM, scheduledEventID: 5, startedEventID: 6, identity: w@host): ` +
	`failed to stream LLM response: claude-code rate limit: You have reached your usage limit. ` +
	`[RELIANT_PROVIDER_RATE_LIMITED:claude-code|18000] (type: ProviderRateLimited, retryable: false): ` +
	`LLM streaming error: POST "https://api.anthropic.com/v1/messages?beta=true": 429 Too Many Requests ` +
	`{"type":"error","error":{"type":"rate_limit_error","message":"You have reached your usage limit."}}`

// The regression: a Claude subscription out of credit was reported as "Rate
// limited by API provider — wait a few minutes before retrying" when the limit
// resets in hours, and the provider's own sentence and the reset time were
// both dropped. The summary must carry the provider's words and the wait.
func TestExtractLLMErrorSummary_UsageLimitNamesProviderAndWait(t *testing.T) {
	got := extractLLMErrorSummary(usageLimitErr)

	for _, want := range []string{"claude-code", "You have reached your usage limit.", "5h"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q is missing %q", got, want)
		}
	}
	if strings.Contains(got, "few minutes") {
		t.Errorf("summary %q still claims a few-minute wait for an hours-long limit", got)
	}
}

func TestHumanizeRetryError_UsageLimitDoesNotSayFewMinutes(t *testing.T) {
	got := humanizeRetryError("call_llm", errors.New(usageLimitErr))
	if strings.Contains(got, "few minutes") {
		t.Errorf("humanizeRetryError = %q; an hours-long limit must not be described as a few minutes", got)
	}
	if !strings.Contains(got, "5h") {
		t.Errorf("humanizeRetryError = %q; must state the provider's wait", got)
	}
}

func TestProviderRateLimitSummary(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want string
	}{
		{
			name: "no marker",
			msg:  "429 Too Many Requests",
			want: "",
		},
		{
			name: "hours",
			msg:  "claude-code rate limit: usage limit [RELIANT_PROVIDER_RATE_LIMITED:claude-code|18000]",
			want: "claude-code rate limit: usage limit — it resets in about 5h",
		},
		{
			name: "provider sentence keeps its punctuation",
			msg:  "claude-code rate limit: You have reached your usage limit. [RELIANT_PROVIDER_RATE_LIMITED:claude-code|18000]",
			want: "claude-code rate limit: You have reached your usage limit. — it resets in about 5h",
		},
		{
			name: "no provider named",
			msg:  "slow down [RELIANT_PROVIDER_RATE_LIMITED:|120]",
			want: "Rate limited by the AI provider: slow down — it resets in about 2m",
		},
		{
			name: "hours and minutes",
			msg:  "x [RELIANT_PROVIDER_RATE_LIMITED:claude-code|5460]",
			want: "claude-code rate limit: x — it resets in about 1h 31m",
		},
		{
			name: "minutes",
			msg:  "x [RELIANT_PROVIDER_RATE_LIMITED:openai|300]",
			want: "openai rate limit: x — it resets in about 5m",
		},
		{
			name: "no reset time reported",
			msg:  "claude-code rate limit: slow down [RELIANT_PROVIDER_RATE_LIMITED:claude-code|]",
			want: "claude-code rate limit: slow down",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := providerRateLimitSummary(tt.msg); got != tt.want {
				t.Errorf("providerRateLimitSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

// An ordinary 429 with no marker keeps today's generic wording; the fix must
// only change what it can back with the provider's own numbers.
func TestExtractLLMErrorSummary_PlainRateLimitUnchanged(t *testing.T) {
	msg := `failed to stream LLM response: 429 Too Many Requests {"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`
	if got, want := extractLLMErrorSummary(msg), "Rate limited by the AI provider (slow down)"; got != want {
		t.Errorf("extractLLMErrorSummary() = %q, want %q", got, want)
	}
}

// A non-retryable ProviderRateLimited error must not be reported as terminal:
// it is not a defect, it is waiting, and the chat must stay resumable.
func TestIsTerminal_ProviderRateLimitedIsNotTerminal(t *testing.T) {
	err := temporal.NewNonRetryableApplicationError("usage limit", "ProviderRateLimited", nil)
	if isTerminal(err) {
		t.Error("ProviderRateLimited must not be classified terminal")
	}
}

// classifyError runs on every activity return. Its string patterns must not
// overrule an error the activity already classified: the provider's own
// wording is not ours to pattern-match, and "quota exceeded" is exactly what a
// spent usage window may say.
func TestClassifyError_KeepsDeliberateClassification(t *testing.T) {
	for _, msg := range []string{
		"claude-code rate limit: You have reached your usage limit. [RELIANT_PROVIDER_RATE_LIMITED:claude-code|18000]",
		"claude-code rate limit: quota exceeded for this plan [RELIANT_PROVIDER_RATE_LIMITED:claude-code|18000]",
		"claude-code rate limit: invalid usage window [RELIANT_PROVIDER_RATE_LIMITED:claude-code|18000]",
	} {
		in := temporal.NewNonRetryableApplicationError(msg, "ProviderRateLimited", errors.New("429"))
		out := classifyError(in)

		var app *temporal.ApplicationError
		if !errors.As(out, &app) {
			t.Fatalf("classifyError(%q) lost the ApplicationError", msg)
		}
		if app.Type() != "ProviderRateLimited" {
			t.Errorf("classifyError(%q) retyped the error as %q", msg, app.Type())
		}
		if isTerminal(out) {
			t.Errorf("classifyError(%q) made a rate limit terminal", msg)
		}
	}
}

// The early return must not swallow the classifier's real job: an
// unclassified error still gets pattern-matched.
func TestClassifyError_StillClassifiesPlainErrors(t *testing.T) {
	if !isTerminal(classifyError(errors.New("resource not found"))) {
		t.Error("a plain 'not found' error must still be classified terminal")
	}
	if isTerminal(classifyError(errors.New("connection refused"))) {
		t.Error("a plain transient error must still be retryable")
	}
	retryable := temporal.NewApplicationError("resource not found", "SomeRetryable")
	if !isTerminal(classifyError(retryable)) {
		t.Error("a RETRYABLE ApplicationError is not a deliberate terminal decision and must still be pattern-matched")
	}
}
