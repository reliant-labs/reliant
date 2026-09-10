// Copyright (c) 2025 Reliant Labs
package llm

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pingForever serves the credit-exhaustion failure: 200 + headers, then ping
// frames at `every` forever, and never a single content event.
func pingForever(t *testing.T, every time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		flusher.Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(every):
			}
			_, _ = io.WriteString(w, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
			flusher.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestStreamingHTTPClient_CutsPingOnlyStream is the regression for the
// credit-exhaustion hang. Anthropic accepted the request, returned 200, then
// emitted ping frames for 28-43 minutes while withholding every content event.
// Because every ping is a byte, the byte-idle timer was reset forever and the
// activity returned success with zero tokens after ~35 minutes.
//
// The pings here arrive far faster than the byte-idle timeout, so ONLY the
// content-stall guard can end this stream — which is the whole point.
func TestStreamingHTTPClient_CutsPingOnlyStream(t *testing.T) {
	const idle = 5 * time.Second // deliberately never reached
	const stall = 750 * time.Millisecond

	srv := pingForever(t, idle/50)

	client := newStreamingHTTPClientWithStall(idle, stall)
	resp, err := client.Get(srv.URL)
	require.NoError(t, err, "headers arrive fine; the hang is content that never comes")
	defer resp.Body.Close()

	done := make(chan error, 1)
	go func() {
		_, readErr := io.ReadAll(resp.Body)
		done <- readErr
	}()

	select {
	case readErr := <-done:
		require.Error(t, readErr, "a ping-only stream is a dead stream and must not read cleanly")
		assert.ErrorIs(t, readErr, ErrStreamContentStalled,
			"must be diagnosed as a content stall, not a silent socket")
	case <-time.After(10 * time.Second):
		t.Fatal("ping-only stream outlived the content-stall timeout — keepalives still count as progress")
	}
}

// TestStreamingHTTPClient_ContentAfterPingsSurvives is the false-positive
// guard that matters most, and the reason the content-stall deadline is a
// separate, much looser timer. A model processing a very large prompt sends
// only pings before its first token; that is legitimate work and must not be
// cut. Here the pings run well past the BYTE-idle timeout and content then
// flows normally.
func TestStreamingHTTPClient_ContentAfterPingsSurvives(t *testing.T) {
	const idle = 400 * time.Millisecond
	const stall = 10 * time.Second // generous: this stream is alive

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		flusher.Flush()

		// Pings alone for 3x the byte-idle timeout — "still thinking".
		deadline := time.Now().Add(3 * idle)
		for time.Now().Before(deadline) {
			time.Sleep(idle / 4)
			_, _ = io.WriteString(w, ": ping\n\n")
			flusher.Flush()
		}
		// Then real content.
		for i := 0; i < 3; i++ {
			time.Sleep(idle / 4)
			_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"text\":\"hi\"}\n\n")
			flusher.Flush()
		}
	}))
	defer srv.Close()

	client := newStreamingHTTPClientWithStall(idle, stall)
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "keepalives must still hold a legitimately slow stream open")
	assert.Contains(t, string(body), "content_block_delta")
}

// TestStreamingHTTPClient_SilentSocketStillReportsIdle pins that adding the
// content guard did not weaken the original one: a stream with NO bytes at all
// must still fail fast on the tight byte-idle deadline, not wait out the much
// longer content-stall deadline.
func TestStreamingHTTPClient_SilentSocketStillReportsIdle(t *testing.T) {
	srv := hangAfterHeaders(t)

	client := newStreamingHTTPClientWithStall(400*time.Millisecond, 30*time.Second)
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	done := make(chan error, 1)
	go func() {
		_, readErr := io.ReadAll(resp.Body)
		done <- readErr
	}()

	select {
	case readErr := <-done:
		assert.ErrorIs(t, readErr, ErrStreamIdleTimeout,
			"a silent socket is an idle timeout, not a content stall")
	case <-time.After(10 * time.Second):
		t.Fatal("silent stream waited for the content-stall deadline — the byte-idle guard regressed")
	}
}

// TestStreamContentStallTimeout_ResolvesOverride mirrors the byte-idle
// override test: operators must be able to widen this without a rebuild, and a
// bad value must not disable the guard.
func TestStreamContentStallTimeout_ResolvesOverride(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		t.Setenv(StreamContentStallTimeoutEnv, "")
		assert.Equal(t, DefaultStreamContentStallTimeout, StreamContentStallTimeout())
	})

	t.Run("honours a valid duration", func(t *testing.T) {
		t.Setenv(StreamContentStallTimeoutEnv, "12m")
		assert.Equal(t, 12*time.Minute, StreamContentStallTimeout())
	})

	for _, bad := range []string{"nonsense", "0s", "-1m"} {
		t.Run("falls back on "+bad, func(t *testing.T) {
			t.Setenv(StreamContentStallTimeoutEnv, bad)
			assert.Equal(t, DefaultStreamContentStallTimeout, StreamContentStallTimeout())
		})
	}
}

// TestStreamContentStallTimeout_IsLooserThanIdle pins the invariant that makes
// the two-timer design coherent. If these ever cross, the content guard would
// fire before the byte guard and start cutting slow-but-live streams — the
// exact false positive the split exists to avoid.
func TestStreamContentStallTimeout_IsLooserThanIdle(t *testing.T) {
	assert.Greater(t, DefaultStreamContentStallTimeout, DefaultStreamIdleTimeout,
		"the content deadline must be the looser of the two")
	assert.GreaterOrEqual(t, DefaultStreamContentStallTimeout, 2*137*time.Second,
		"must stay clear of 2x the p99 total stream duration so long prompt processing is never cut")
}

// TestSSEProgressScanner_Classification pins the wire-format rules directly,
// including the two cases that need state across Reads.
func TestSSEProgressScanner_Classification(t *testing.T) {
	t.Run("comment keepalive is not content", func(t *testing.T) {
		var s sseProgressScanner
		assert.False(t, s.sawContent([]byte(": ping\n\n")))
	})

	t.Run("anthropic ping event is not content", func(t *testing.T) {
		var s sseProgressScanner
		assert.False(t, s.sawContent([]byte("event: ping\ndata: {\"type\":\"ping\"}\n\n")))
	})

	t.Run("real event is content", func(t *testing.T) {
		var s sseProgressScanner
		assert.True(t, s.sawContent([]byte("event: content_block_delta\ndata: {\"text\":\"hi\"}\n\n")))
	})

	t.Run("data after a ping frame ends is content", func(t *testing.T) {
		var s sseProgressScanner
		require.False(t, s.sawContent([]byte("event: ping\ndata: {}\n\n")))
		// The blank line closed the ping frame, so a bare data line now
		// belongs to a real frame and must count.
		assert.True(t, s.sawContent([]byte("data: {\"text\":\"hi\"}\n\n")))
	})

	t.Run("ping split across reads stays a ping", func(t *testing.T) {
		var s sseProgressScanner
		assert.False(t, s.sawContent([]byte("event: pi")))
		assert.False(t, s.sawContent([]byte("ng\ndata: {\"type\":\"ping\"}\n\n")))
	})

	t.Run("non-SSE body counts as content", func(t *testing.T) {
		var s sseProgressScanner
		assert.True(t, s.sawContent([]byte("{\"id\":\"msg_1\",\"content\":[]}\n")))
	})

	t.Run("oversized unterminated line counts as content", func(t *testing.T) {
		var s sseProgressScanner
		assert.True(t, s.sawContent(make([]byte, maxPartialLineBuffer+1)))
	})
}
