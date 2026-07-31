// Copyright (c) 2025 Reliant Labs
package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	openaiopt "github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hangAfterHeaders serves the exact failure seen in production: response
// headers are written and flushed, then the connection is held open forever
// with no body bytes. Nothing below the HTTP layer notices — the socket is
// healthy, the peer is just silent.
func hangAfterHeaders(t *testing.T) *httptest.Server {
	t.Helper()
	released := make(chan struct{})
	t.Cleanup(func() { close(released) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-released:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestStreamingHTTPClient_CutsStreamThatGoesSilentAfterHeaders is the core
// regression: headers arrive, then nothing. Without the idle timeout this read
// blocks until the far end resets the connection, which in the measured dogfood
// run took 8 to 17 minutes.
func TestStreamingHTTPClient_CutsStreamThatGoesSilentAfterHeaders(t *testing.T) {
	srv := hangAfterHeaders(t)

	client := newStreamingHTTPClient(500 * time.Millisecond)
	resp, err := client.Get(srv.URL)
	require.NoError(t, err, "headers must arrive; the hang is in the body")
	defer resp.Body.Close()

	done := make(chan error, 1)
	go func() {
		_, readErr := io.ReadAll(resp.Body)
		done <- readErr
	}()

	select {
	case readErr := <-done:
		require.Error(t, readErr, "a silent stream must not read cleanly")
		assert.ErrorIs(t, readErr, ErrStreamIdleTimeout,
			"the caller must be told the stream went idle, not that some body was closed")
	case <-time.After(10 * time.Second):
		t.Fatal("body read hung past the idle timeout — the stream guard is not wired")
	}
}

// TestStreamingHTTPClient_SlowButAliveStreamSurvives is the false-positive
// guard. The whole safety argument for lowering the timeout is that ANY byte
// resets the clock, so provider keepalives, ping frames and reasoning-summary
// deltas keep a thinking stream alive. This pins that behaviour: a stream that
// dribbles one frame per half-timeout runs far past the timeout untouched.
func TestStreamingHTTPClient_SlowButAliveStreamSurvives(t *testing.T) {
	const idle = 500 * time.Millisecond
	const frames = 8 // 8 * idle/2 = 4x the idle timeout of total stream time

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		flusher.Flush()
		for i := 0; i < frames; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(idle / 2):
			}
			_, _ = io.WriteString(w, ": keepalive\n")
			flusher.Flush()
		}
	}))
	defer srv.Close()

	client := newStreamingHTTPClient(idle)
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "a slow but live stream must not be killed")
	assert.Len(t, body, frames*len(": keepalive\n"), "every frame must survive")
}

// TestNewOpenAISDKClient_CutsSilentStream proves the guard survives the vendor
// SDK's own plumbing. codex, local, azure, openai and reliant all stream
// through openai-go; a client built by the sanctioned constructor must surface
// the idle timeout out of ssestream, not hang inside it.
func TestNewOpenAISDKClient_CutsSilentStream(t *testing.T) {
	srv := hangAfterHeaders(t)
	t.Setenv(StreamIdleTimeoutEnv, "500ms")

	client := NewOpenAISDKClient(
		openaiopt.WithBaseURL(srv.URL),
		openaiopt.WithAPIKey("test"),
		openaiopt.WithMaxRetries(0),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		stream := client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
			Model:    shared.ChatModel("test-model"),
			Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
		})
		for stream.Next() { //nolint:revive // draining the stream is the point
		}
		done <- stream.Err()
	}()

	select {
	case err := <-done:
		require.Error(t, err, "a silent SSE stream must end in an error, not a clean EOF")
		assert.ErrorIs(t, err, ErrStreamIdleTimeout)
	case <-time.After(15 * time.Second):
		t.Fatal("openai-go stream hung past the idle timeout — the SDK client is not using StreamingHTTPClient")
	}
}

// TestStreamIdleTimeout_ResolvesOverride pins the operational escape hatch: the
// timeout is lowered on inference about upstream keepalive cadence, so it must
// be adjustable without a rebuild, and a bad value must not disable the guard.
func TestStreamIdleTimeout_ResolvesOverride(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		t.Setenv(StreamIdleTimeoutEnv, "")
		assert.Equal(t, DefaultStreamIdleTimeout, StreamIdleTimeout())
	})

	t.Run("honours a valid duration", func(t *testing.T) {
		t.Setenv(StreamIdleTimeoutEnv, "3m")
		assert.Equal(t, 3*time.Minute, StreamIdleTimeout())
	})

	for _, bad := range []string{"nonsense", "0s", "-10s"} {
		t.Run("falls back on "+bad, func(t *testing.T) {
			t.Setenv(StreamIdleTimeoutEnv, bad)
			assert.Equal(t, DefaultStreamIdleTimeout, StreamIdleTimeout())
		})
	}
}

// TestDefaultStreamIdleTimeout_IsArguedFromData pins the value against silent
// drift back to a number nobody measured. 4,039 real streams: p99 total
// duration 137.6s, 97.8% finish end to end inside 90s. A default above two
// minutes tolerates a dead stream for longer than 99% of live ones take in
// total, which is what made the original 5 minutes cost 4,665s in one run.
func TestDefaultStreamIdleTimeout_IsArguedFromData(t *testing.T) {
	assert.LessOrEqual(t, DefaultStreamIdleTimeout, 2*time.Minute,
		"a dead stream must be cut faster than 99%% of live streams take to finish entirely")
	assert.GreaterOrEqual(t, DefaultStreamIdleTimeout, 60*time.Second,
		"must stay well clear of the p99 stream duration so slow generations are never at risk")
}

// TestIdleTimeoutReader_ReportsTheRealCause covers the reader directly: the
// sentinel used to be declared and never returned, so callers saw an opaque
// closed-body error and error classification had nothing to match on.
func TestIdleTimeoutReader_ReportsTheRealCause(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()

	r := NewIdleTimeoutReader(pr, 200*time.Millisecond)
	_, err := r.Read(make([]byte, 8))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStreamIdleTimeout)
}

// TestIdleTimeoutReader_EOFIsNotAnIdleTimeout guards the other direction: a
// stream that ends normally must report EOF, not a spurious timeout.
func TestIdleTimeoutReader_EOFIsNotAnIdleTimeout(t *testing.T) {
	r := NewIdleTimeoutReader(io.NopCloser(strings.NewReader("data: hi\n\n")), 5*time.Second)
	body, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "data: hi\n\n", string(body))
	require.NoError(t, r.Close())
}
