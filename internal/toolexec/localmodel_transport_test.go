// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// fakeDaemonMgr stands in for the gateway's ToolsDaemonService AND for the
// daemon behind it: OpenLocalModelRelay performs the request against a real
// httptest server and feeds ordered chunks back, exactly as the daemon handler
// is specified to.
type fakeDaemonMgr struct {
	recordingDaemonMgr
	upstream string // base URL of the "local model server"

	mu        sync.Mutex
	requests  []*reliantv1.LocalModelHTTPRequest
	cancelled map[string]chan struct{}
	inventory *reliantv1.LocalModelInventory
	offline   bool
}

func (m *fakeDaemonMgr) OpenLocalModelRelay(userID, daemonID string, req *reliantv1.LocalModelHTTPRequest, deliver func(*reliantv1.LocalModelHTTPChunk)) (func(), error) {
	m.mu.Lock()
	if m.offline {
		m.mu.Unlock()
		return nil, fmt.Errorf("daemon %s is not connected for user %s", daemonID, userID)
	}
	m.requests = append(m.requests, req)
	if m.cancelled == nil {
		m.cancelled = map[string]chan struct{}{}
	}
	cancelCh := make(chan struct{})
	m.cancelled[req.GetRequestId()] = cancelCh
	m.mu.Unlock()

	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { <-cancelCh; cancel() }()

		httpReq, _ := http.NewRequestWithContext(ctx, req.GetMethod(), m.upstream+req.GetPath(), bytes.NewReader(req.GetBody()))
		for k, v := range req.GetHeaders() {
			httpReq.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			deliver(&reliantv1.LocalModelHTTPChunk{RequestId: req.GetRequestId(), Sequence: 0, Done: true, Error: err.Error()})
			return
		}
		defer resp.Body.Close()
		headers := map[string]string{}
		for k := range resp.Header {
			headers[k] = resp.Header.Get(k)
		}
		seq := uint64(0)
		deliver(&reliantv1.LocalModelHTTPChunk{RequestId: req.GetRequestId(), Sequence: seq, Status: int32(resp.StatusCode), Headers: headers})
		seq++
		buf := make([]byte, 4096)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				deliver(&reliantv1.LocalModelHTTPChunk{RequestId: req.GetRequestId(), Sequence: seq, Data: append([]byte(nil), buf[:n]...)})
				seq++
			}
			if rerr != nil {
				done := &reliantv1.LocalModelHTTPChunk{RequestId: req.GetRequestId(), Sequence: seq, Done: true}
				if rerr != io.EOF && ctx.Err() == nil {
					done.Error = rerr.Error()
				}
				deliver(done)
				return
			}
		}
	}()
	return func() {}, nil
}

func (m *fakeDaemonMgr) CancelLocalModelRelay(_, _, requestID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch, ok := m.cancelled[requestID]; ok {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

func (m *fakeDaemonMgr) wasCancelled(requestID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch, ok := m.cancelled[requestID]
	if !ok {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func (m *fakeDaemonMgr) RefreshLocalModels(ctx context.Context, _, _ string) (*reliantv1.LocalModelInventory, error) {
	if m.inventory == nil {
		return nil, errors.New("daemon never answered")
	}
	return m.inventory, nil
}

func (m *fakeDaemonMgr) firstRequest(t *testing.T) *reliantv1.LocalModelHTTPRequest {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	require.NotEmpty(t, m.requests)
	return m.requests[0]
}

// sseServer streams n chat-completion chunks, one every gap, then [DONE]. It
// closes `stopped` when the request context ends mid-stream.
func sseServer(t *testing.T, n int, gap time.Duration, stopped chan struct{}) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for i := 0; i < n; i++ {
			select {
			case <-r.Context().Done():
				if stopped != nil {
					close(stopped)
				}
				return
			default:
			}
			fmt.Fprintf(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"qwen3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"tok%d \"}}]}\n\n", i)
			fl.Flush()
			time.Sleep(gap)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func relayClient(t *testing.T, upstream string, mgr *fakeDaemonMgr) (*openai.Client, *NATSDaemonRouter) {
	t.Helper()
	nc, router := startBridgeAndRouter(t, mgr)
	_ = nc
	mgr.upstream = upstream
	rt := NewLocalModelTransport(router, "user-1", "daemon-1", "ollama")
	client := openai.NewClient(
		option.WithBaseURL("http://local-model.invalid/"),
		option.WithAPIKey("unused"),
		option.WithHTTPClient(&http.Client{Transport: rt}),
		option.WithMaxRetries(0),
	)
	return &client, router
}

func chatParams() openai.ChatCompletionNewParams {
	return openai.ChatCompletionNewParams{
		Model:    "qwen3",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	}
}

func TestLocalModelTransport_StreamsSSEEndToEnd(t *testing.T) {
	mgr := &fakeDaemonMgr{}
	srv := sseServer(t, 40, time.Millisecond, nil)
	client, _ := relayClient(t, srv.URL, mgr)

	stream := client.Chat.Completions.NewStreaming(context.Background(), chatParams())
	var got strings.Builder
	chunks := 0
	for stream.Next() {
		for _, c := range stream.Current().Choices {
			got.WriteString(c.Delta.Content)
		}
		chunks++
	}
	require.NoError(t, stream.Err())
	assert.Equal(t, 40, chunks)
	assert.True(t, strings.HasPrefix(got.String(), "tok0 tok1 "), got.String())

	req := mgr.firstRequest(t)
	assert.Equal(t, "ollama", req.GetEndpointId())
	assert.Equal(t, http.MethodPost, req.GetMethod())
	assert.Equal(t, "/chat/completions", req.GetPath())
	assert.NotContains(t, req.GetHeaders(), "Authorization", "client credentials must not cross to the daemon")
	assert.Contains(t, string(req.GetBody()), `"qwen3"`)
}

func TestLocalModelTransport_NonStreamingAndQuery(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"object":"list","data":[{"id":"qwen3","object":"model","q":%q}]}`, r.URL.RawQuery)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mgr := &fakeDaemonMgr{upstream: srv.URL}
	_, router := startBridgeAndRouter(t, mgr)
	rt := NewLocalModelTransport(router, "user-1", "daemon-1", "ollama")

	httpReq, _ := http.NewRequest(http.MethodGet, "http://local-model.invalid/models?limit=5", nil)
	resp, err := rt.RoundTrip(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, string(body), `"q":"limit=5"`)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
}

func TestLocalModelTransport_LargeBodyChunkedBothWays(t *testing.T) {
	big := bytes.Repeat([]byte("abcdefghij"), 100*1024) // 1MB, far over the 16KB test max_payload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in, _ := io.ReadAll(r.Body)
		w.Write(in) // echo
	}))
	t.Cleanup(srv.Close)
	mgr := &fakeDaemonMgr{upstream: srv.URL}
	_, router := startBridgeAndRouter(t, mgr)
	rt := NewLocalModelTransport(router, "user-1", "daemon-1", "ollama")

	httpReq, _ := http.NewRequest(http.MethodPost, "http://local-model.invalid/echo", bytes.NewReader(big))
	resp, err := rt.RoundTrip(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(big, got), "1MB must round-trip byte-identical through request chunking and chunk splitting")
}

func TestLocalModelTransport_MidStreamCancelAbortsDaemonRequest(t *testing.T) {
	stopped := make(chan struct{})
	srv := sseServer(t, 10000, 2*time.Millisecond, stopped)
	mgr := &fakeDaemonMgr{}
	client, _ := relayClient(t, srv.URL, mgr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := client.Chat.Completions.NewStreaming(ctx, chatParams())
	seen := 0
	for stream.Next() {
		seen++
		if seen == 5 {
			cancel()
		}
	}
	require.Error(t, stream.Err(), "a cancelled stream must report an error")
	assert.True(t, errors.Is(stream.Err(), context.Canceled), "got %v", stream.Err())
	assert.Less(t, seen, 10000)

	reqID := mgr.firstRequest(t).GetRequestId()
	require.Eventually(t, func() bool { return mgr.wasCancelled(reqID) }, 3*time.Second, 10*time.Millisecond,
		"the cancel must reach the daemon")
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream server never saw its request context end")
	}
}

func TestLocalModelTransport_DaemonOfflineIsTypedAndNamesDaemon(t *testing.T) {
	// No bridge subscription for daemon-ghost at all: NATS answers
	// no-responders, which must become a typed error.
	nc := startPayloadTestNATS(t)
	router := NewNATSDaemonRouter(nc)
	rt := NewLocalModelTransport(router, "user-1", "daemon-ghost", "ollama")

	httpReq, _ := http.NewRequest(http.MethodGet, "http://local-model.invalid/models", nil)
	start := time.Now()
	_, err := rt.RoundTrip(httpReq)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 3*time.Second, "offline must fail fast")
	assert.ErrorIs(t, err, ErrLocalModelDaemonUnavailable)
	var typed *LocalModelDaemonUnavailableError
	require.True(t, errors.As(err, &typed))
	assert.Equal(t, "daemon-ghost", typed.DaemonID)
	assert.Contains(t, err.Error(), "daemon-ghost")
}

func TestLocalModelTransport_ConnectedGatewayButDaemonGoneIsTyped(t *testing.T) {
	mgr := &fakeDaemonMgr{offline: true}
	_, router := startBridgeAndRouter(t, mgr)
	rt := NewLocalModelTransport(router, "user-1", "daemon-1", "ollama")

	httpReq, _ := http.NewRequest(http.MethodGet, "http://local-model.invalid/models", nil)
	_, err := rt.RoundTrip(httpReq)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLocalModelDaemonUnavailable)
	assert.Contains(t, err.Error(), "daemon-1")
}

func TestLocalModelTransport_RelayFailureBeforeResponseIsRoundTripError(t *testing.T) {
	// Upstream refuses connections: the daemon reports one done chunk with
	// error set and status 0.
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	mgr := &fakeDaemonMgr{upstream: url}
	_, router := startBridgeAndRouter(t, mgr)
	rt := NewLocalModelTransport(router, "user-1", "daemon-1", "ollama")

	httpReq, _ := http.NewRequest(http.MethodGet, "http://local-model.invalid/models", nil)
	_, err := rt.RoundTrip(httpReq)
	require.Error(t, err)
	var relayErr *LocalModelRelayError
	require.True(t, errors.As(err, &relayErr), "got %T %v", err, err)
	assert.Equal(t, "daemon-1", relayErr.DaemonID)
}

func TestLocalModelTransport_MidStreamDaemonErrorSurfacesAsReadError(t *testing.T) {
	// A server that sends a head and some body then drops the connection.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100000")
		w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		hj, _ := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		conn.Close()
	}))
	t.Cleanup(srv.Close)
	mgr := &fakeDaemonMgr{upstream: srv.URL}
	_, router := startBridgeAndRouter(t, mgr)
	rt := NewLocalModelTransport(router, "user-1", "daemon-1", "ollama")

	httpReq, _ := http.NewRequest(http.MethodGet, "http://local-model.invalid/x", nil)
	resp, err := rt.RoundTrip(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	assert.Equal(t, "partial", string(body))
	require.Error(t, err)
	var relayErr *LocalModelRelayError
	assert.True(t, errors.As(err, &relayErr), "got %T %v", err, err)
}

func TestLocalModelTransport_AuthorizationIsTheSubject(t *testing.T) {
	// user-2's router must not be able to reach user-1's daemon: the bridge
	// only subscribed user-1's subjects.
	mgr := &fakeDaemonMgr{}
	nc, _ := startBridgeAndRouter(t, mgr)
	routerOther := NewNATSDaemonRouter(nc)
	rt := NewLocalModelTransport(routerOther, "user-2", "daemon-1", "ollama")

	httpReq, _ := http.NewRequest(http.MethodGet, "http://local-model.invalid/models", nil)
	_, err := rt.RoundTrip(httpReq)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLocalModelDaemonUnavailable)
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	assert.Empty(t, mgr.requests, "the daemon must never see another user's request")
}

func TestLocalModelStream_IdleTimeout(t *testing.T) {
	// Daemon accepts and then says nothing.
	mgr := &silentDaemonMgr{}
	_, router := startBridgeAndRouter(t, mgr)

	stream, err := router.OpenLocalModelHTTP(context.Background(), "user-1", "daemon-1",
		&reliantv1.LocalModelHTTPRequest{EndpointId: "ollama", Method: "GET", Path: "/models", IdleTimeoutMs: 50})
	require.NoError(t, err)
	defer stream.Close()
	start := time.Now()
	_, err = stream.Next()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLocalModelIdleTimeout)
	assert.Contains(t, err.Error(), "daemon-1")
	assert.Less(t, time.Since(start), 8*time.Second)
}

type silentDaemonMgr struct{ fakeDaemonMgr }

func (m *silentDaemonMgr) OpenLocalModelRelay(_, _ string, _ *reliantv1.LocalModelHTTPRequest, _ func(*reliantv1.LocalModelHTTPChunk)) (func(), error) {
	return func() {}, nil
}

func TestRefreshLocalModels_ReturnsDaemonInventory(t *testing.T) {
	want := &reliantv1.LocalModelInventory{
		ProbedAt: "2026-10-04T00:00:00Z",
		Endpoints: []*reliantv1.LocalModelEndpoint{{
			Id: "ollama", Kind: "ollama", BaseUrl: "http://localhost:11434/v1",
			Models: []*reliantv1.LocalModelInfo{{Name: "qwen3:latest", ContextWindow: 32768, SupportsTools: true, SupportsChat: true}},
		}},
	}
	mgr := &fakeDaemonMgr{inventory: want}
	_, router := startBridgeAndRouter(t, mgr)

	got, err := router.RefreshLocalModels(context.Background(), "user-1", "daemon-1")
	require.NoError(t, err)
	assert.Equal(t, "ollama", got.GetEndpoints()[0].GetId())
	assert.Equal(t, int64(32768), got.GetEndpoints()[0].GetModels()[0].GetContextWindow())
}

func TestRefreshLocalModels_OfflineIsTyped(t *testing.T) {
	nc := startPayloadTestNATS(t)
	router := NewNATSDaemonRouter(nc)
	_, err := router.RefreshLocalModels(context.Background(), "user-1", "daemon-ghost")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLocalModelDaemonUnavailable)
	assert.Contains(t, err.Error(), "daemon-ghost")
}

func TestRefreshLocalModels_DaemonErrorIsReported(t *testing.T) {
	mgr := &fakeDaemonMgr{} // inventory nil => manager errors
	_, router := startBridgeAndRouter(t, mgr)
	_, err := router.RefreshLocalModels(context.Background(), "user-1", "daemon-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon never answered")
}
