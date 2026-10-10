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

// Wire frames of an Anthropic stream, verbatim in shape. A thinking block under
// the redact-thinking beta (every claude-code model on the 2.1.204
// fingerprint, which includes claude-sonnet-5-5) opens with content_block_start
// and then streams NOTHING but pings until its signature arrives — the
// thinking text is withheld, not streamed.
const (
	frameMessageStart      = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":248000}}}\n\n"
	frameThinkingOpen      = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n"
	frameThinkingDelta     = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"considering the init script\"}}\n\n"
	frameSignature         = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"EqQBCkYIBxgCKkA\"}}\n\n"
	frameBlockStop         = "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"
	frameTextOpen          = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"
	frameTextDelta         = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"Done.\"}}\n\n"
	frameTextStop          = "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n"
	frameMessageDelta      = "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":38500}}\n\n"
	frameMessageStop       = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	frameAnthropicPingWire = "event: ping\ndata: {\"type\": \"ping\"}\n\n"
)

// stallScale maps the production minutes onto test time: one "minute" here is
// 150ms, so the 5-minute content-stall deadline is 750ms and the incident's
// six-minute thinking phase is 900ms. The ratio is what the tests pin.
const stallScale = 150 * time.Millisecond

// scriptedStream serves steps in order — a frame verbatim, or a stretch of
// pings every pingEvery and nothing else — then ends the body cleanly.
func scriptedStream(t *testing.T, pingEvery time.Duration, steps ...streamStep) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		flusher.Flush()
		for _, step := range steps {
			if step.frame != "" {
				_, _ = io.WriteString(w, step.frame)
				flusher.Flush()
				continue
			}
			until := time.Now().Add(step.pingsFor)
			for time.Now().Before(until) {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(pingEvery):
				}
				_, _ = io.WriteString(w, frameAnthropicPingWire)
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type streamStep struct {
	frame    string
	pingsFor time.Duration
}

func frame(f string) streamStep        { return streamStep{frame: f} }
func pings(d time.Duration) streamStep { return streamStep{pingsFor: d} }

func readAllWithin(t *testing.T, resp *http.Response, limit time.Duration) ([]byte, error) {
	t.Helper()
	type result struct {
		body []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		body, err := io.ReadAll(resp.Body)
		done <- result{body, err}
	}()
	select {
	case r := <-done:
		return r.body, r.err
	case <-time.After(limit):
		t.Fatalf("stream did not finish within %s", limit)
		return nil, nil
	}
}

// TestStreamingHTTPClient_RedactedThinkingOutlivesContentStall is the
// regression for chat 622675c2's implementer sub-agent (thread 89e64991).
//
// claude-5.5-sonnet at effort high on a ~248k-token context thought for 30-38k
// tokens per turn at ~125 tok/s — four to five minutes during which the redacted
// thinking block streamed only pings. Every turn that thought past five minutes
// was cut as a "content stall", all five retries repeated the same thinking and
// the same cut, and the whole chat paused. The provider was healthy throughout:
// the orchestrator's Opus calls on the same subscription succeeded in 2-26s.
//
// Pings INSIDE an open content block are the model generating output it does not
// stream. They must not be judged by the awaiting-content deadline.
func TestStreamingHTTPClient_RedactedThinkingOutlivesContentStall(t *testing.T) {
	const idle = 90 * stallScale // byte-idle never reached: pings keep the socket busy
	const contentStall = 5 * stallScale

	srv := scriptedStream(t, stallScale/10,
		frame(frameMessageStart),
		frame(frameThinkingOpen),
		pings(6*stallScale), // six "minutes" of redacted thinking
		frame(frameSignature),
		frame(frameBlockStop),
		frame(frameTextOpen),
		frame(frameTextDelta),
		frame(frameTextStop),
		frame(frameMessageDelta),
		frame(frameMessageStop),
	)

	client := newStreamingHTTPClientWithStall(idle, contentStall)
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := readAllWithin(t, resp, 20*contentStall)
	require.NoError(t, err, "a model thinking inside an open block is working, not stalled")
	assert.Contains(t, string(body), "message_stop", "the whole turn must arrive")
}

// TestStreamingHTTPClient_ThinkingDeltasForSixMinutesNeverStall pins the
// visible-thinking case (summarized display, as opus-5.5 requests): every
// thinking_delta is content, so six "minutes" of them never trip a 5-minute
// content deadline no matter how long the block stays open.
func TestStreamingHTTPClient_ThinkingDeltasForSixMinutesNeverStall(t *testing.T) {
	const idle = 90 * stallScale
	const contentStall = 5 * stallScale

	steps := []streamStep{frame(frameMessageStart), frame(frameThinkingOpen)}
	for elapsed := time.Duration(0); elapsed < 6*stallScale; elapsed += stallScale / 2 {
		steps = append(steps, pings(stallScale/2), frame(frameThinkingDelta))
	}
	steps = append(steps, frame(frameSignature), frame(frameBlockStop), frame(frameMessageDelta), frame(frameMessageStop))
	srv := scriptedStream(t, stallScale/10, steps...)

	client := newStreamingHTTPClientWithStall(idle, contentStall)
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := readAllWithin(t, resp, 20*contentStall)
	require.NoError(t, err, "streamed thinking deltas are content")
	assert.Contains(t, string(body), "message_stop")
}

// TestStreamingHTTPClient_PingsWithNoBlockOpenStillStall pins the guard this
// work must not weaken: the credit-exhaustion hang answers 200 and then pings
// with no content block ever opened (its turns recorded zero prompt tokens, so
// not even message_start arrived). That stream must still be cut at the
// awaiting-content deadline.
func TestStreamingHTTPClient_PingsWithNoBlockOpenStillStall(t *testing.T) {
	const idle = 90 * stallScale
	const contentStall = 5 * stallScale

	srv := scriptedStream(t, stallScale/10, pings(60*stallScale))

	client := newStreamingHTTPClientWithStall(idle, contentStall)
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	_, err = readAllWithin(t, resp, 20*contentStall)
	require.Error(t, err, "a ping-only stream is a dead stream")
	assert.ErrorIs(t, err, ErrStreamContentStalled)

	var stall *StreamStallError
	require.ErrorAs(t, err, &stall)
	assert.Equal(t, StallAwaitingContent, stall.Phase)
	assert.Equal(t, contentStall, stall.Timeout)
}

// TestStreamingHTTPClient_WedgedOpenBlockIsStillBounded pins the other half:
// widening the deadline inside a block must not make a block that never ends
// unbounded. A block open on keepalives past the mid-block deadline is cut,
// and says so.
func TestStreamingHTTPClient_WedgedOpenBlockIsStillBounded(t *testing.T) {
	const idle = 90 * stallScale
	const contentStall = 2 * stallScale
	const midBlock = 5 * stallScale

	srv := scriptedStream(t, stallScale/10,
		frame(frameMessageStart),
		frame(frameThinkingOpen),
		pings(60*stallScale),
	)

	client := newStreamingHTTPClientWithStalls(idle, contentStall, midBlock)
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	start := time.Now()
	_, err = readAllWithin(t, resp, 20*midBlock)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStreamContentStalled)
	assert.GreaterOrEqual(t, time.Since(start), midBlock-stallScale,
		"cut at the AWAITING deadline — an open block was judged as no block")

	var stall *StreamStallError
	require.ErrorAs(t, err, &stall)
	assert.Equal(t, StallMidBlock, stall.Phase)
	assert.Equal(t, midBlock, stall.Timeout)
	assert.Contains(t, stall.Error(), "timeout", "autoClassify keys on this word to retry")
}

// A closed block hands the stream back to the awaiting deadline: pings after
// content_block_stop are judged as pings with nothing open.
func TestStreamingHTTPClient_ClosingABlockRestoresTheAwaitingDeadline(t *testing.T) {
	const idle = 90 * stallScale
	const contentStall = 3 * stallScale
	const midBlock = 60 * stallScale

	srv := scriptedStream(t, stallScale/10,
		frame(frameMessageStart),
		frame(frameThinkingOpen),
		frame(frameSignature),
		frame(frameBlockStop),
		pings(60*stallScale),
	)

	client := newStreamingHTTPClientWithStalls(idle, contentStall, midBlock)
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	_, err = readAllWithin(t, resp, 20*contentStall)
	var stall *StreamStallError
	require.ErrorAs(t, err, &stall)
	assert.Equal(t, StallAwaitingContent, stall.Phase)
}

// TestStreamMidBlockStallTimeout_ScalesWithOutputBudget pins the adaptive
// bound: the output budget at the floor generation rate, never tighter than
// the awaiting deadline, never looser than the cap.
func TestStreamMidBlockStallTimeout_ScalesWithOutputBudget(t *testing.T) {
	t.Setenv(StreamMidBlockStallTimeoutEnv, "")
	const awaiting = 5 * time.Minute
	for _, tc := range []struct {
		budget int64
		want   time.Duration
	}{
		{budget: 0, want: DefaultStreamMidBlockStallTimeout},       // unknown: the cap
		{budget: 4096, want: awaiting},                             // a title request: never below awaiting
		{budget: 32000, want: 800 * time.Second},                   // haiku-4.5's captured max_tokens
		{budget: 64000, want: 1600 * time.Second},                  // sonnet-5 / opus-4.8 / fable
		{budget: 128000, want: DefaultStreamMidBlockStallTimeout},  // sonnet-5.5 / opus-5.5: capped
		{budget: 1 << 62, want: DefaultStreamMidBlockStallTimeout}, // absurd: no overflow
	} {
		assert.Equal(t, tc.want, midBlockStallTimeout(tc.budget, awaiting), "budget %d", tc.budget)
	}

	// The incident: 38,269 thinking tokens took 300.7s. Whatever budget the
	// request carried, a block that long must fit with room to spare.
	assert.Greater(t, StreamMidBlockStallTimeout(128000), 3*301*time.Second)

	t.Run("env pins it", func(t *testing.T) {
		t.Setenv(StreamMidBlockStallTimeoutEnv, "45m")
		assert.Equal(t, 45*time.Minute, StreamMidBlockStallTimeout(4096))
	})
	t.Run("bad env is ignored", func(t *testing.T) {
		t.Setenv(StreamMidBlockStallTimeoutEnv, "soon")
		assert.Equal(t, DefaultStreamMidBlockStallTimeout, StreamMidBlockStallTimeout(0))
	})
}

// The driver names its output budget on the request context; the transport
// must size that response's mid-block deadline from it.
func TestIdleTimeoutTransport_SizesMidBlockDeadlineFromRequestBudget(t *testing.T) {
	t.Setenv(StreamMidBlockStallTimeoutEnv, "")
	transport := &idleTimeoutTransport{
		base: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		}),
		timeout:             time.Minute,
		contentStallTimeout: 5 * time.Minute,
	}

	liveness := NewStreamLiveness()
	ctx := WithStreamLiveness(WithStreamOutputBudget(t.Context(), 32000), liveness)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://provider.invalid/v1/messages", nil)
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	reader, ok := resp.Body.(*IdleTimeoutReader)
	require.True(t, ok)
	defer reader.Close()

	assert.Equal(t, 800*time.Second, reader.stall.midBlock)
	assert.Equal(t, 5*time.Minute, reader.stall.awaiting)
	assert.Same(t, liveness, reader.stall.liveness)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Liveness is reported for keepalives inside an open block — the progress the
// driver cannot see — and for nothing else: not for pings with no block open
// (the credit-exhaustion hang must not look alive) and not for content, which
// reaches the driver as events anyway.
func TestIdleTimeoutReader_TouchesLivenessOnlyForKeepalivesMidBlock(t *testing.T) {
	liveness := NewStreamLiveness()
	body, provider := io.Pipe()
	reader := newIdleTimeoutReaderWithPolicy(body, time.Minute, streamStallPolicy{
		awaiting: time.Minute, midBlock: time.Hour, liveness: liveness,
	})
	defer reader.Close()
	defer provider.Close()
	feed := func(chunk string) {
		go func() { _, _ = io.WriteString(provider, chunk) }()
		_, err := io.ReadFull(reader, make([]byte, len(chunk)))
		require.NoError(t, err)
	}

	feed(frameAnthropicPingWire)
	_, touched := liveness.Since()
	assert.False(t, touched, "a ping with no block open is not liveness")

	feed(frameMessageStart + frameThinkingOpen)
	_, touched = liveness.Since()
	assert.False(t, touched, "content is not reported; the driver sees it")

	feed(frameAnthropicPingWire)
	since, touched := liveness.Since()
	assert.True(t, touched, "a ping inside an open block is the model working")
	assert.Less(t, since, time.Second)
}

func TestSSEProgressScanner_TracksOpenBlocks(t *testing.T) {
	var s sseProgressScanner
	assert.False(t, s.midBlock(), "a response starts with no block open")

	s.sawContent([]byte(frameMessageStart))
	assert.False(t, s.midBlock(), "message_start opens no block")

	s.sawContent([]byte(frameThinkingOpen))
	assert.True(t, s.midBlock())

	s.sawContent([]byte(frameAnthropicPingWire))
	assert.True(t, s.midBlock(), "a ping does not close the block")

	// A frame split across reads still lands.
	s.sawContent([]byte("event: content_block_st"))
	s.sawContent([]byte("op\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"))
	assert.False(t, s.midBlock())

	s.sawContent([]byte(frameTextOpen))
	assert.True(t, s.midBlock())
	s.sawContent([]byte(frameMessageStop))
	assert.False(t, s.midBlock(), "message_stop can leave nothing open")
}
