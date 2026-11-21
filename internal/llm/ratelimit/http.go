// Copyright (c) 2025 Reliant Labs
package ratelimit

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
)

// maxErrorBody bounds how much of a refused response OpenHTTP reads for the
// driver's error text.
const maxErrorBody = 64 << 10

// Describe turns one refused attempt into the Failure the ladder classifies.
// resp is nil when the request got no response at all (err says why);
// otherwise it is a non-2xx response, already closed, whose body (up to 64KB)
// is body. Drivers start from HTTPFailure and add what their provider says.
type Describe func(resp *http.Response, body []byte, err error) Failure

// OpenHTTP opens a request through the ladder, for drivers that speak raw
// HTTP rather than through an SDK: newReq builds attempt n's request (an
// error from it is final, since rebuilding cannot fix it), do sends it (the
// driver's http.Client.Do: the I/O stays the driver's), and every non-2xx
// answer or transport failure goes to describe and then to Decide. It returns
// the first 2xx response with its body unread.
//
// It covers the request-open only. Once a 2xx is returned, a failure while
// the driver reads the body is the driver's to report, never retried here:
// the events it already emitted cannot be taken back, and CallLLM's own retry
// owns a stream that broke mid-way.
func (r Retryer) OpenHTTP(ctx context.Context, do func(*http.Request) (*http.Response, error), newReq func() (*http.Request, error), describe Describe, emit func(llm.RetryWait) error) (*http.Response, error) {
	var resp *http.Response
	err := r.Run(ctx, func(int) (Failure, bool) {
		req, err := newReq()
		if err != nil {
			return Failure{Err: err, Fatal: true}, false
		}
		res, err := do(req)
		if err != nil {
			return describe(nil, nil, err), false
		}
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			resp = res
			return Failure{}, true
		}
		body, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
		_ = res.Body.Close()
		return describe(res, body, nil), false
	}, emit)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// HTTPFailure is the Failure for a refused response (status and headers) or,
// with resp nil, for a request that got none.
func HTTPFailure(resp *http.Response, err error) Failure {
	f := Failure{Err: err}
	if resp != nil {
		f.Status = resp.StatusCode
		f.Header = resp.Header
	}
	return f
}

// IsTransportError reports whether err is a request that got no HTTP answer
// (dial, TLS, reset, a connection closed early) or no first byte in time (the
// streaming transport's idle, stall and first-token guards), the status-less
// failures that are safe to retry. A driver whose SDK can fail in other ways
// without a status (a decode error, a client-side validation) marks those
// Fatal rather than letting the ladder read them as a dropped connection.
func IsTransportError(err error) bool {
	var urlErr *url.Error
	var netErr net.Error
	return errors.As(err, &urlErr) || errors.As(err, &netErr) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, llm.ErrStreamIdleTimeout) || errors.Is(err, llm.ErrStreamContentStalled) || errors.Is(err, llm.ErrStreamFirstTokenTimeout)
}

// ResetHint reads when an exhausted header-declared bucket refills, for
// Failure.ResetHint: the OpenAI form x-ratelimit-reset-{requests,tokens} (a
// duration) of a bucket whose x-ratelimit-remaining-* is 0, or a bare
// x-ratelimit-reset (OpenRouter sends an epoch in milliseconds; a duration or
// plain seconds are read too) unless x-ratelimit-remaining says requests
// remain. The longest exhausted bucket wins. Zero means no hint.
func ResetHint(h http.Header, now time.Time) time.Duration {
	if len(h) == 0 {
		return 0
	}
	var hint time.Duration
	for _, kind := range []string{"requests", "tokens"} {
		if n, ok := parseInt(h.Get("x-ratelimit-remaining-" + kind)); !ok || n > 0 {
			continue
		}
		if d, ok := parseResetDuration(h.Get("x-ratelimit-reset-" + kind)); ok {
			hint = max(hint, d)
		}
	}
	if v := strings.TrimSpace(h.Get("x-ratelimit-reset")); v != "" {
		if n, ok := parseInt(h.Get("x-ratelimit-remaining")); !ok || n <= 0 {
			if t, ok := ParseResetTime(v); ok {
				hint = max(hint, t.Sub(now))
			} else if d, ok := parseResetDuration(v); ok {
				hint = max(hint, d)
			}
		}
	}
	return max(hint, 0)
}
