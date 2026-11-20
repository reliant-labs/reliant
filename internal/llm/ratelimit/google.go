// Copyright (c) 2025 Reliant Labs
package ratelimit

import (
	"encoding/json"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Google APIs (Gemini API, Vertex AI, Cloud Code Assist)
// ---------------------------------------------------------------------------
//
// Google answers a refusal with a google.rpc.Status, and says how long to wait
// in its details rather than in a Retry-After header. A 429 RESOURCE_EXHAUSTED
// is usually a per-minute limit that lifts in seconds; the details tell it
// apart from a daily quota (wait for tomorrow) and from a quota of zero (the
// model is not on this tier at all), which retrying cannot fix.

// GoogleStatus is a google.rpc.Status error body.
type GoogleStatus struct {
	Code    int              `json:"code"`
	Message string           `json:"message"`
	Status  string           `json:"status"`
	Details []map[string]any `json:"details"`
}

// ParseGoogleStatus reads {"error": {...}}, or the one-element array of it
// that Cloud Code Assist sends. OpenAI's error envelope also carries a code,
// but as a string, which fails the decode.
func ParseGoogleStatus(body []byte) (GoogleStatus, bool) {
	var env struct {
		Error *GoogleStatus `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Error.valid() {
		return *env.Error, true
	}
	var list []struct {
		Error *GoogleStatus `json:"error"`
	}
	if err := json.Unmarshal(body, &list); err == nil && len(list) > 0 && list[0].Error.valid() {
		return *list[0].Error, true
	}
	return GoogleStatus{}, false
}

// valid tells a google.rpc.Status from another vendor's {"error": {...}}
// (Anthropic's, OpenAI's), which names neither a code nor a status.
func (s *GoogleStatus) valid() bool {
	return s != nil && (s.Code != 0 || s.Status != "")
}

// GoogleHTTPStatus is the HTTP status of a google.rpc.Status: code when it has
// one, else the canonical mapping of its status name. An error sent in-band on
// a stream carries only the name, and must classify like the wire status.
func GoogleHTTPStatus(code int, status string) int {
	if code > 0 {
		return code
	}
	switch status {
	case "RESOURCE_EXHAUSTED":
		return 429
	case "UNAVAILABLE":
		return 503
	case "INTERNAL", "UNKNOWN", "DATA_LOSS":
		return 500
	case "DEADLINE_EXCEEDED":
		return 504
	case "ABORTED":
		return 409
	case "INVALID_ARGUMENT", "FAILED_PRECONDITION", "OUT_OF_RANGE":
		return 400
	case "UNAUTHENTICATED":
		return 401
	case "PERMISSION_DENIED":
		return 403
	case "NOT_FOUND":
		return 404
	}
	return 0
}

const (
	googleRetryInfo    = "type.googleapis.com/google.rpc.RetryInfo"
	googleQuotaFailure = "type.googleapis.com/google.rpc.QuotaFailure"
	googleErrorInfo    = "type.googleapis.com/google.rpc.ErrorInfo"
)

// DescribeGoogle adds what a google.rpc.Status's details say to f:
//   - RetryInfo.retryDelay is the provider's wait (f.RetryAfter);
//   - an ErrorInfo with reason QUOTA_EXHAUSTED, or a QuotaFailure naming a
//     per-day quota, is an exhausted usage window (f.UsageLimit), resetting
//     at the ErrorInfo's quotaResetTimeStamp when it gives one;
//   - a QuotaFailure whose quota value is 0 has nothing to wait for (f.Fatal).
//
// Anything else — RESOURCE_EXHAUSTED on a per-minute quota included — is left
// to the status code, so a 429 retries like any other.
func DescribeGoogle(f *Failure, details []map[string]any) {
	var resetDelay time.Duration
	for _, d := range details {
		switch d["@type"] {
		case googleRetryInfo:
			if v, ok := d["retryDelay"].(string); ok {
				if dur, err := time.ParseDuration(v); err == nil && dur > 0 {
					f.RetryAfter = dur
				}
			}
		case googleQuotaFailure:
			violations, _ := d["violations"].([]any)
			for _, raw := range violations {
				v, _ := raw.(map[string]any)
				id, _ := v["quotaId"].(string)
				if strings.Contains(id, "PerDay") {
					f.UsageLimit = true
					f.Limit.Window = "daily"
				}
				if q, ok := v["quotaValue"].(string); ok && q == "0" {
					f.Fatal = true
				}
			}
		case googleErrorInfo:
			reason, _ := d["reason"].(string)
			if reason != "QUOTA_EXHAUSTED" {
				continue
			}
			f.UsageLimit = true
			meta, _ := d["metadata"].(map[string]any)
			if v, ok := meta["quotaResetTimeStamp"].(string); ok {
				if t, ok := ParseResetTime(v); ok {
					f.Limit.ResetAt = t
				}
			}
			if v, ok := meta["quotaResetDelay"].(string); ok {
				if dur, err := time.ParseDuration(v); err == nil && dur > 0 {
					resetDelay = dur
				}
			}
		}
	}
	if f.RetryAfter == 0 && resetDelay > 0 {
		f.RetryAfter = resetDelay
	}
}
