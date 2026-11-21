// Copyright (c) 2025 Reliant Labs
package antigravity

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/models/message"
)

type redirectTransport struct{ to *url.URL }

func (r redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = r.to.Scheme, r.to.Host
	return http.DefaultTransport.RoundTrip(req)
}

// newRedirectedClient sends the driver's requests (to the fixed Cloud Code
// endpoint) to srv instead, with a ladder whose sleeps are recorded.
func newRedirectedClient(t *testing.T, srv *httptest.Server, sleeps *[]time.Duration) *Client {
	t.Helper()
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return &Client{
		options:    llm.DriverOptions{Model: models.Model{APIModel: "gemini-3-pro"}, MaxTokens: 100},
		httpClient: &http.Client{Transport: redirectTransport{to: u}},
		ladder: ratelimit.Retryer{
			Jitter: func(time.Duration) time.Duration { return 0 },
			Sleep:  func(_ context.Context, d time.Duration) error { *sleeps = append(*sleeps, d); return nil },
		},
	}
}

func run(c *Client) ([]llm.RetryWait, *llm.DriverResponse, error) {
	user := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}
	var (
		waits []llm.RetryWait
		final *llm.DriverResponse
		err   error
	)
	for ev := range c.StreamResponse(context.Background(), nil, user, nil) {
		switch ev.Type {
		case llm.EventRetryWait:
			waits = append(waits, *ev.Retry)
		case llm.EventComplete:
			final = ev.Response
		case llm.EventError:
			err = ev.Error
		}
	}
	return waits, final, err
}

// Cloud Code states its wait in a google.rpc.RetryInfo; the old loop read only
// an integer Retry-After header and so slept its own backoff instead.
func TestStream_RetriesA429AfterTheRetryInfoWait(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"slow down","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"6.5s"}]}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"pong"}]},"finishReason":"STOP"}]}}`+"\n\n")
	}))
	defer srv.Close()
	var sleeps []time.Duration
	waits, final, err := run(newRedirectedClient(t, srv, &sleeps))
	require.NoError(t, err)
	require.NotNil(t, final)
	assert.Equal(t, "pong", final.Content)
	assert.EqualValues(t, 2, hits.Load())
	assert.Equal(t, []time.Duration{6500 * time.Millisecond}, sleeps)
	require.Len(t, waits, 1)
}

func TestStream_A400IsNotRetried(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"bad"}}`)
	}))
	defer srv.Close()
	var sleeps []time.Duration
	_, _, err := run(newRedirectedClient(t, srv, &sleeps))
	require.ErrorContains(t, err, "antigravity: API request failed with status 400")
	assert.EqualValues(t, 1, hits.Load())
	assert.Empty(t, sleeps)
}

// An exhausted Cloud Code quota resets in hours: it ends the ladder as a usage
// window instead of eight retries.
func TestStream_QuotaExhaustedEndsAtOnce(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","metadata":{"quotaResetDelay":"4h"}}]}}`)
	}))
	defer srv.Close()
	var sleeps []time.Duration
	_, _, err := run(newRedirectedClient(t, srv, &sleeps))
	var ule *ratelimit.UsageLimitError
	require.ErrorAs(t, err, &ule)
	assert.EqualValues(t, 1, hits.Load())
}
