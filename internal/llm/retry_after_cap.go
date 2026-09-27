// Copyright (c) 2025 Reliant Labs
package llm

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	openaisdk "github.com/openai/openai-go/v3"

	"github.com/reliant-labs/reliant/internal/logging"
)

// MaxInPlaceRetryAfter is the longest Retry-After an LLM SDK is allowed to
// sleep through inside a single request.
//
// The Stainless-generated SDKs (anthropic-sdk-go, openai-go) retry 408/409/429
// and 5xx in place and honour Retry-After VERBATIM, with no upper bound. That is
// right for a rate limit that clears in seconds and badly wrong for one that
// clears in hours. A Claude subscription that has spent its usage window answers
// 429 with a Retry-After pointing at the window reset, so the SDK parked the
// request for hours before a response body existed. Nothing could see it:
//
//   - the byte-idle and content-stall guards wrap response BODIES, and there
//     was none;
//   - the only thing left was CallLLM's 10-minute progress backstop, which
//     cancelled the context — and the SDK then returned ctx.Err() in place of
//     the 429, discarding the provider's own explanation.
//
// So the chat retried the same doomed request every 10 minutes with no visible
// error. Measured on 2026-09-24: two chats stalled in lockstep from 10:24 to
// 15:18, 32 progress timeouts, zero guard fires, zero error text.
//
// A Retry-After longer than this is not a transient to wait out inside one
// HTTP call. It is an answer, and the caller needs it: the workflow's own retry
// and self-pause ladder is where waiting belongs, and it can tell the user why.
//
// 60 seconds keeps every ordinary rate-limit retry (seconds) in place while
// staying far below the 10-minute progress timeout, so the backstop can never
// again be the thing that ends a rate-limited request.
const MaxInPlaceRetryAfter = 60 * time.Second

// capRetryAfter tells the SDK not to retry a response whose Retry-After exceeds
// MaxInPlaceRetryAfter, so the 429 (and its body) reach the caller immediately.
//
// It uses x-should-retry, the override the Stainless SDKs consult BEFORE the
// status code, rather than rewriting Retry-After. Rewriting the delay down would
// only turn one long sleep into MaxRetries fast, pointless retries against a
// limit that will not have reset; declining the retry is the honest answer.
// The original Retry-After is left in place so callers can still read it.
func capRetryAfter(resp *http.Response) {
	if resp == nil {
		return
	}
	delay, ok := parseRetryAfter(resp.Header)
	if !ok || delay <= MaxInPlaceRetryAfter {
		return
	}
	resp.Header.Set("x-should-retry", "false")

	// Logged because this is the line that proves which failure happened. The
	// incident this guards against left no trace at all: no status, no body,
	// just a context cancelled ten minutes later.
	host := ""
	if resp.Request != nil && resp.Request.URL != nil {
		host = resp.Request.URL.Host
	}
	logging.Warn("[Transport] Provider asked to retry after a long delay; surfacing the error instead of sleeping in the SDK",
		"status", resp.StatusCode,
		"retry_after", delay.Round(time.Second),
		"max_in_place", MaxInPlaceRetryAfter,
		"host", host,
		"request_id", resp.Header.Get("request-id"))
}

// ProviderRateLimit describes a rate limit the provider said will not clear
// within MaxInPlaceRetryAfter — the case capRetryAfter surfaces instead of
// sleeping on.
type ProviderRateLimit struct {
	// StatusCode is the provider's HTTP status (429 in practice).
	StatusCode int
	// Message is the provider's own explanation from the error body, or "" if
	// the body carried none.
	Message string
	// RetryAfter is how long until the provider says the limit resets.
	RetryAfter time.Duration
}

// AsProviderRateLimit reports whether err is a provider rate limit with a
// reset too far off to wait out in place.
//
// It reads the SDK's typed error, so it must be called where that type still
// exists — inside the activity, before Temporal flattens the error to a string.
// Both Stainless SDKs return an error carrying the raw *http.Response, which is
// where Retry-After lives; the error bodies of both follow the
// {"error":{"message":...}} shape.
func AsProviderRateLimit(err error) (ProviderRateLimit, bool) {
	var resp *http.Response
	var body string
	var anthropicErr *anthropicsdk.Error
	var openaiErr *openaisdk.Error
	switch {
	case errors.As(err, &anthropicErr):
		resp, body = anthropicErr.Response, anthropicErr.RawJSON()
	case errors.As(err, &openaiErr):
		resp, body = openaiErr.Response, openaiErr.RawJSON()
	default:
		return ProviderRateLimit{}, false
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		return ProviderRateLimit{}, false
	}
	delay, ok := parseRetryAfter(resp.Header)
	if !ok || delay <= MaxInPlaceRetryAfter {
		return ProviderRateLimit{}, false
	}
	return ProviderRateLimit{
		StatusCode: resp.StatusCode,
		Message:    providerErrorMessage(body),
		RetryAfter: delay,
	}, true
}

// providerErrorMessage pulls error.message out of a provider error body.
func providerErrorMessage(body string) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &parsed) != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Error.Message)
}

// parseRetryAfter reads the retry delay the way the SDKs do, in their order of
// preference: Retry-After-Ms (milliseconds), then Retry-After as either seconds
// or an HTTP-date.
func parseRetryAfter(h http.Header) (time.Duration, bool) {
	if v := strings.TrimSpace(h.Get("Retry-After-Ms")); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil {
			return time.Duration(ms * float64(time.Millisecond)), true
		}
	}
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		return time.Duration(secs * float64(time.Second)), true
	}
	if at, err := http.ParseTime(v); err == nil {
		return time.Until(at), true
	}
	return 0, false
}
