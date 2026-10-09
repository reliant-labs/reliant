// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bodyLedger records every response body the SDK opens and whether it was
// closed, by wrapping the real streaming transport.
type bodyLedger struct {
	base   http.RoundTripper
	mu     sync.Mutex
	bodies []*trackedBody
}

type trackedBody struct {
	io.ReadCloser
	closed atomic.Bool
}

func (b *trackedBody) Close() error {
	b.closed.Store(true)
	return b.ReadCloser.Close()
}

func (l *bodyLedger) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := l.base.RoundTrip(req)
	if err != nil || resp.Body == nil {
		return resp, err
	}
	tb := &trackedBody{ReadCloser: resp.Body}
	resp.Body = tb
	l.mu.Lock()
	l.bodies = append(l.bodies, tb)
	l.mu.Unlock()
	return resp, nil
}

func (l *bodyLedger) unclosed() (opened, open int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, b := range l.bodies {
		if !b.closed.Load() {
			open++
		}
	}
	return len(l.bodies), open
}

func ledgeredClient(url string) (*baseClient, *bodyLedger) {
	ledger := &bodyLedger{base: llm.StreamingHTTPClient().Transport}
	c := llm.NewAnthropicSDKClient(
		option.WithBaseURL(url),
		option.WithAPIKey("k"),
		option.WithHTTPClient(&http.Client{Transport: ledger}),
	)
	return &baseClient{client: c}, ledger
}

// TestStreamClosesEveryResponseBody: anthropic-sdk-go's Stream does not close
// its body when the stream ends, and this driver used to leave it to nobody.
// Every finished Anthropic stream therefore left its IdleTimeoutReader's
// watchers parked forever — 708 of them in prod 22 minutes after a restart,
// pinning ~1MB of serialized request each, until the worker was OOMKilled.
func TestStreamClosesEveryResponseBody(t *testing.T) {
	t.Run("stream that completes normally", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(okSSE))
		}))
		defer srv.Close()

		b, ledger := ledgeredClient(srv.URL)
		events := collect(t, b.streamResponseInternal(context.Background(), params()))
		require.Equal(t, llm.EventComplete, events[len(events)-1].Type)

		opened, open := ledger.unclosed()
		assert.Equal(t, 1, opened)
		assert.Zero(t, open, "the response body of a finished stream must be closed by the driver")
	})

	t.Run("stream retried after a 429", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if hits.Add(1) == 1 {
				w.Header().Set("Retry-After-Ms", "1")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`))
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(okSSE))
		}))
		defer srv.Close()

		b, ledger := ledgeredClient(srv.URL)
		collect(t, b.streamResponseInternal(context.Background(), params()))

		opened, open := ledger.unclosed()
		assert.Equal(t, 2, opened)
		assert.Zero(t, open, "neither the rejected attempt's body nor the successful one may be left open")
	})

	t.Run("stream that errors mid-way", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message_start\n" +
				`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
				"event: error\n" +
				`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}` + "\n\n"))
		}))
		defer srv.Close()

		b, ledger := ledgeredClient(srv.URL)
		events := collect(t, b.streamResponseInternal(context.Background(), params()))
		var sawErr bool
		for _, ev := range events {
			sawErr = sawErr || ev.Type == llm.EventError
		}
		require.True(t, sawErr)

		_, open := ledger.unclosed()
		assert.Zero(t, open, "a stream that ends in an error event must still be closed")
	})
}
