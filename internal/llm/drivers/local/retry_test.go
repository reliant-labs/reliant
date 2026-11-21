// Copyright (c) 2025 Reliant Labs
package local

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
)

// refusingServer refuses the first `refusals` requests with status (and
// Retry-After when set), then streams reasoningFieldStream.
func refusingServer(t *testing.T, hits *atomic.Int32, refusals int32, status int, retryAfter string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) <= refusals {
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			http.Error(w, `{"error":{"message":"model is loading"}}`, status)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, reasoningFieldStream)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func withRecordingLadder(c *LocalClient, sleeps *[]time.Duration, maxAttempts int) *LocalClient {
	c.ladder = ratelimit.Retryer{
		MaxAttempts: maxAttempts,
		Jitter:      func(time.Duration) time.Duration { return 0 },
		Sleep:       func(_ context.Context, d time.Duration) error { *sleeps = append(*sleeps, d); return nil },
	}
	return c
}

func TestStream_RetriesA429AfterItsRetryAfterAndAnnouncesIt(t *testing.T) {
	var hits atomic.Int32
	srv := refusingServer(t, &hits, 1, http.StatusTooManyRequests, "5")
	var sleeps []time.Duration
	c := withRecordingLadder(newTestClient(srv.URL, nil), &sleeps, 0)

	var waits []llm.RetryWait
	var content string
	for ev := range c.StreamResponse(context.Background(), nil, userTurn("hi"), nil) {
		switch ev.Type {
		case llm.EventRetryWait:
			waits = append(waits, *ev.Retry)
		case llm.EventContentDelta:
			content += ev.Content
		case llm.EventError:
			require.NoError(t, ev.Error)
		}
	}
	assert.Equal(t, "4", content)
	assert.EqualValues(t, 2, hits.Load())
	assert.Equal(t, []time.Duration{5 * time.Second}, sleeps)
	require.Len(t, waits, 1, "the old loop slept without a word")
	assert.Equal(t, http.StatusTooManyRequests, waits[0].StatusCode)
}

func TestA400IsNotRetried(t *testing.T) {
	var hits atomic.Int32
	srv := refusingServer(t, &hits, 1, http.StatusBadRequest, "")
	var sleeps []time.Duration
	c := withRecordingLadder(newTestClient(srv.URL, nil), &sleeps, 0)

	got := collect(c.StreamResponse(context.Background(), nil, userTurn("hi"), nil))
	require.Error(t, got.err)
	_, err := c.SendMessages(context.Background(), nil, userTurn("hi"), nil)
	require.Error(t, err)
	assert.EqualValues(t, 2, hits.Load(), "one request per call, none retried")
	assert.Empty(t, sleeps)
}

// The SDK's own retries are off: with the ladder held to one retry, a server
// that always fails sees exactly two requests per call.
func TestSDKRetriesAreOff(t *testing.T) {
	var hits atomic.Int32
	srv := refusingServer(t, &hits, 100, http.StatusServiceUnavailable, "")
	var sleeps []time.Duration
	c := withRecordingLadder(newTestClient(srv.URL, nil), &sleeps, 1)

	_, err := c.SendMessages(context.Background(), nil, userTurn("hi"), nil)
	require.Error(t, err)
	assert.EqualValues(t, 2, hits.Load())
	assert.Equal(t, []time.Duration{2 * time.Second}, sleeps)
}
