package reliant

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3"
)

func TestShouldRetryReliantAPIError(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		body        string
		wantRetry   bool
		wantReason  string
		wantSummary string
	}{
		{
			name:        "retries on 429 rate limit",
			statusCode:  429,
			body:        `{"error":{"message":"Rate limited","type":"rate_limit_error","code":"429"}}`,
			wantRetry:   true,
			wantReason:  "http_429",
			wantSummary: "Rate limited",
		},
		{
			name:        "fails fast on local gcloud reauth error wrapped in 500",
			statusCode:  500,
			body:        `{"error":{"message":"Reauthentication is needed. Please run gcloud auth application-default login to reauthenticate.","type":"api_error","code":"500"}}`,
			wantRetry:   false,
			wantReason:  "terminal_auth_config_error",
			wantSummary: "Reauthentication is needed. Please run gcloud auth application-default login to reauthenticate.",
		},
		{
			name:        "retries on overloaded upstream 500",
			statusCode:  500,
			body:        `{"error":{"message":"Internal server error: provider overloaded, try again later","type":"api_error","code":"500"}}`,
			wantRetry:   true,
			wantReason:  "transient_upstream_500",
			wantSummary: "Internal server error: provider overloaded, try again later",
		},
		{
			name:        "fails fast on opaque non-transient 500",
			statusCode:  500,
			body:        `{"error":{"message":"Model misconfiguration for local provider","type":"api_error","code":"500"}}`,
			wantRetry:   false,
			wantReason:  "non_retryable_500",
			wantSummary: "Model misconfiguration for local provider",
		},
		{
			// 429 + insufficient_quota is the reliant-managed free-tier global
			// budget exhaustion signal from the control-plane LLM proxy.
			// Retrying is futile (budget is monthly hard cap), so we fail
			// fast and let the wrapper attach the upgrade marker.
			name:        "fails fast on reliant-managed quota exhausted",
			statusCode:  429,
			body:        `{"error":{"message":"Free tier quota exceeded — please upgrade your plan.","type":"insufficient_quota","code":"insufficient_quota","upgrade_url":"/billing/plans"}}`,
			wantRetry:   false,
			wantReason:  "reliant_managed_quota_exhausted",
			wantSummary: "Free tier quota exceeded — please upgrade your plan.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apierr := newOpenAIErrorForTest(t, tt.statusCode, tt.body)

			retry, reason := shouldRetryReliantAPIError(apierr)
			if retry != tt.wantRetry {
				t.Fatalf("retry = %v, want %v", retry, tt.wantRetry)
			}
			if reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tt.wantReason)
			}

			summary := summarizeReliantAPIError(apierr)
			if summary != tt.wantSummary {
				t.Fatalf("summary = %q, want %q", summary, tt.wantSummary)
			}
		})
	}
}

func TestReliantRetryExhaustedError(t *testing.T) {
	rateLimitErr := newOpenAIErrorForTest(t, 429, `{"error":{"message":"Rate limited","type":"rate_limit_error","code":"429"}}`)
	if got := reliantRetryExhaustedError(rateLimitErr).Error(); got != "maximum retry attempts reached for rate limit: 8 retries" {
		t.Fatalf("rate limit retry exhaustion = %q", got)
	}

	transient500 := newOpenAIErrorForTest(t, 503, `{"error":{"message":"Service unavailable","type":"api_error","code":"503"}}`)
	if got := reliantRetryExhaustedError(transient500).Error(); got != "maximum retry attempts reached for transient Reliant API error (status 503): 8 retries" {
		t.Fatalf("transient retry exhaustion = %q", got)
	}
}

func TestWrapReliantManagedQuotaError(t *testing.T) {
	apierr := newOpenAIErrorForTest(t, 429, `{"error":{"message":"Free tier quota exceeded — please upgrade your plan.","type":"insufficient_quota","code":"insufficient_quota","upgrade_url":"/billing/plans"}}`)

	err := wrapReliantManagedQuotaError(apierr)
	var quotaErr *ErrReliantManagedQuotaExhausted
	if !errors.As(err, &quotaErr) {
		t.Fatalf("wrapReliantManagedQuotaError = %T, want *ErrReliantManagedQuotaExhausted", err)
	}
	// Without the openai-go raw-JSON populated (private field), the parser
	// falls back to DefaultReliantUpgradeURL. The test contract is that the
	// marker + a non-empty upgrade URL are baked into the message.
	if quotaErr.UpgradeURL == "" {
		t.Fatalf("UpgradeURL = empty, want non-empty")
	}
	if !strings.Contains(err.Error(), ReliantManagedQuotaMarker) {
		t.Fatalf("err.Error() = %q, want substring %q", err.Error(), ReliantManagedQuotaMarker)
	}
	if !strings.Contains(err.Error(), DefaultReliantUpgradeURL) {
		t.Fatalf("err.Error() = %q, want substring %q", err.Error(), DefaultReliantUpgradeURL)
	}

	// Missing message still produces a usable error string.
	apierr2 := newOpenAIErrorForTest(t, 429, `{"error":{"type":"insufficient_quota","code":"insufficient_quota"}}`)
	err2 := wrapReliantManagedQuotaError(apierr2)
	var quotaErr2 *ErrReliantManagedQuotaExhausted
	if !errors.As(err2, &quotaErr2) {
		t.Fatalf("wrapReliantManagedQuotaError = %T, want *ErrReliantManagedQuotaExhausted", err2)
	}
	if !strings.Contains(err2.Error(), ReliantManagedQuotaMarker) {
		t.Fatalf("err2.Error() = %q, want substring %q", err2.Error(), ReliantManagedQuotaMarker)
	}
}

func TestIsReliantManagedQuotaError(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "insufficient_quota in code",
			body: `{"error":{"message":"quota","type":"insufficient_quota","code":"insufficient_quota"}}`,
			want: true,
		},
		{
			name: "plain rate limit is not quota",
			body: `{"error":{"message":"slow down","type":"rate_limit_error","code":"429"}}`,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apierr := newOpenAIErrorForTest(t, 429, tc.body)
			if got := isReliantManagedQuotaError(apierr); got != tc.want {
				t.Fatalf("isReliantManagedQuotaError = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRetryDelayMs_UsesRetryAfterHeader(t *testing.T) {
	apierr := newOpenAIErrorForTest(t, 429, `{"error":{"message":"Rate limited","type":"rate_limit_error","code":"429"}}`)
	apierr.Response.Header.Set("Retry-After", "7")

	if got := retryDelayMs(3, apierr); got != 7000 {
		t.Fatalf("retryDelayMs = %d, want 7000", got)
	}
}

func newOpenAIErrorForTest(t *testing.T, statusCode int, body string) *openai.Error {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, "http://localhost:4000/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	resp := &http.Response{
		StatusCode: statusCode,
		Status:     fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}

	return &openai.Error{
		Message:    extractOpenAIMessageForTest(body),
		Type:       extractOpenAIFieldForTest(body, "type"),
		Code:       extractOpenAIFieldForTest(body, "code"),
		StatusCode: statusCode,
		Request:    req,
		Response:   resp,
	}
}

func extractOpenAIMessageForTest(body string) string {
	marker := `"message":"`
	idx := strings.Index(body, marker)
	if idx == -1 {
		return ""
	}
	start := idx + len(marker)
	end := strings.Index(body[start:], `"`)
	if end == -1 {
		return body[start:]
	}
	return body[start : start+end]
}

func extractOpenAIFieldForTest(body string, field string) string {
	marker := fmt.Sprintf(`"%s":"`, field)
	idx := strings.Index(body, marker)
	if idx == -1 {
		return ""
	}
	start := idx + len(marker)
	end := strings.Index(body[start:], `"`)
	if end == -1 {
		return body[start:]
	}
	return body[start : start+end]
}
