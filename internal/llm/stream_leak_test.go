// Copyright (c) 2025 Reliant Labs
package llm

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The worker OOM of 2026-10-08: 708 goroutines parked in
// IdleTimeoutReader.watch 22 minutes after a restart, each pinning its
// response body and, through the HTTP/2 stream, the ~1MB serialized request
// behind it. The watchers ended only on Close, and anthropic-sdk-go never
// closes a stream that finished normally. These tests pin the contract that
// makes that leak impossible: the reader releases itself.

// watcherGoroutines counts goroutines currently inside IdleTimeoutReader.watch.
func watcherGoroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), "llm.(*IdleTimeoutReader).watch")
		}
		buf = make([]byte, 2*len(buf))
	}
}

// settledWatcherGoroutines returns the watcher count once it has stopped
// changing, so a test's baseline excludes watchers still exiting from earlier
// tests.
func settledWatcherGoroutines() int {
	prev := watcherGoroutines()
	for i := 0; i < 100; i++ {
		time.Sleep(10 * time.Millisecond)
		cur := watcherGoroutines()
		if cur == prev {
			return cur
		}
		prev = cur
	}
	return prev
}

// requireWatchersBackTo fails unless the watcher count drops to want well
// inside every deadline the reader could have been waiting on, so a watcher
// that exits only because a timer eventually fires still fails the test.
func requireWatchersBackTo(t *testing.T, want int, why string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		got := watcherGoroutines()
		if got <= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %d IdleTimeoutReader watcher goroutine(s) still running, want %d — each one pins its response body and request for good",
				why, got-want, 0)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// errAfter yields its data, then fails with err.
type errAfter struct {
	r   io.Reader
	err error
}

func (e *errAfter) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if errors.Is(err, io.EOF) {
		return n, e.err
	}
	return n, err
}

func (e *errAfter) Close() error { return nil }

func TestIdleTimeoutReader_ReleasesItsWatcherWithoutClose(t *testing.T) {
	// Deadlines far beyond the poll window: only the terminal Read may end
	// the watcher here, never a timer.
	const idle, stall = time.Hour, time.Hour

	t.Run("stream ends at EOF and is never closed", func(t *testing.T) {
		base := watcherGoroutines()
		r := newIdleTimeoutReader(io.NopCloser(strings.NewReader("event: x\ndata: {}\n\n")), idle, stall)
		body, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, "event: x\ndata: {}\n\n", string(body))
		requireWatchersBackTo(t, base, "EOF without Close")
	})

	t.Run("stream fails mid-read and is never closed", func(t *testing.T) {
		base := watcherGoroutines()
		boom := errors.New("stream error: INTERNAL_ERROR")
		r := newIdleTimeoutReader(&errAfter{r: strings.NewReader("data: partial"), err: boom}, idle, stall)
		_, err := io.ReadAll(r)
		require.ErrorIs(t, err, boom, "a real transport error must pass through unchanged")
		requireWatchersBackTo(t, base, "read error without Close")
	})

	t.Run("Close after a terminal read is still safe", func(t *testing.T) {
		base := watcherGoroutines()
		r := newIdleTimeoutReader(io.NopCloser(strings.NewReader("data: hi\n\n")), idle, stall)
		_, err := io.ReadAll(r)
		require.NoError(t, err)
		require.NoError(t, r.Close())
		require.NoError(t, r.Close(), "a second Close must not panic on the released watcher")
		requireWatchersBackTo(t, base, "EOF then Close")
	})
}

// TestIdleTimeoutReader_AbandonedStreamIsStillBounded covers the one path that
// has neither a terminal Read nor a Close: a consumer that simply stops
// reading. No Read means no reset, so the idle deadline fires and the watcher
// ends with it.
func TestIdleTimeoutReader_AbandonedStreamIsStillBounded(t *testing.T) {
	base := watcherGoroutines()
	pr, pw := io.Pipe()
	defer pw.Close()

	r := newIdleTimeoutReader(pr, 100*time.Millisecond, time.Hour)
	_ = r // never read, never closed

	requireWatchersBackTo(t, base, "abandoned stream past its idle deadline")
	_, err := r.Read(make([]byte, 1))
	assert.ErrorIs(t, err, ErrStreamIdleTimeout)
}

// TestIdleTimeoutReader_OneGoroutinePerStream pins the cost of a live stream:
// one goroutine, not one per deadline. The two-watcher design doubled the
// goroutine count of every leak.
func TestIdleTimeoutReader_OneGoroutinePerStream(t *testing.T) {
	// Count from a settled baseline: a watcher released by the previous test
	// may still be unwinding, and counting it would make base one too high.
	base := settledWatcherGoroutines()
	pr, pw := io.Pipe()
	defer pw.Close()

	r := newIdleTimeoutReader(pr, time.Hour, time.Hour)
	defer r.Close()
	// The watcher is started with `go`, so it may not be scheduled yet; wait
	// for it, then require that exactly one exists for this reader.
	require.Eventually(t, func() bool { return watcherGoroutines() >= base+1 },
		time.Second, 5*time.Millisecond)
	assert.Equal(t, base+1, watcherGoroutines())
}

// TestStreamingHTTPClient_StreamReadToEOFWithoutCloseLeaksNothing is the
// production shape end to end: the SSE body is read to EOF through the real
// streaming client and nobody closes it, exactly as anthropic-sdk-go's
// ssestream leaves every finished stream.
func TestStreamingHTTPClient_StreamReadToEOFWithoutCloseLeaksNothing(t *testing.T) {
	// A request body large enough to be worth retaining, as the conversation
	// sent with every LLM call is.
	reqBody := bytes.Repeat([]byte("x"), 256<<10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 5; i++ {
			_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\"}\n\n")
		}
	}))
	defer srv.Close()

	client := newStreamingHTTPClientWithStall(time.Hour, time.Hour)
	base := watcherGoroutines()
	const streams = 20
	for i := 0; i < streams; i++ {
		resp, err := client.Post(srv.URL, "application/json", bytes.NewReader(reqBody))
		require.NoError(t, err)
		_, err = io.ReadAll(resp.Body)
		require.NoError(t, err)
		// Deliberately no resp.Body.Close().
	}
	requireWatchersBackTo(t, base, "streams read to EOF and never closed")
}
