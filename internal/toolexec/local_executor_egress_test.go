// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/egresslimit"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/observability"
)

// A daemon bounds its own egress: the LocalToolExecutor a daemon runs tools
// through takes a limiter, and a call over a limit comes back as the same
// fail-soft EGRESS_RATE_LIMITED tool result the server's limiter produces.

func localEgressExecutor(t *testing.T, l egresslimit.Limits) *LocalToolExecutor {
	t.Helper()
	limiter, err := egresslimit.New(egresslimit.Deps{Limits: roomyLimits(l)})
	require.NoError(t, err)
	e := NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{AllowPrivateEgress: true}))
	e.SetEgressLimiter(limiter)
	return e
}

func localFetch(e *LocalToolExecutor, target string) *ExecutionResult {
	return e.ExecuteTool(context.Background(), tools.ToolFetch,
		fmt.Sprintf(`{"url":%q,"format":"text"}`, target), "call-1", 0, map[string]any{"chat_id": "chat-1"})
}

func TestLocalEgress_BurstBeyondTheBucketIsAFailSoftToolResult(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	e := localEgressExecutor(t, egresslimit.Limits{UserPerMinute: 60, UserBurst: 2})
	for i := 0; i < 2; i++ {
		res := localFetch(e, srv.URL)
		require.False(t, res.IsError, res.Content)
	}

	res := localFetch(e, srv.URL)
	require.True(t, res.IsError)
	require.False(t, res.Success)
	require.Equal(t, ErrorCodeEgressRateLimited, res.ErrorCode)
	require.Contains(t, res.Content, "rate limited")
	require.Regexp(t, `retry after \d+s`, res.Content)
	require.Equal(t, res.Content, res.ErrorMessage)
	require.EqualValues(t, 2, hits.Load(), "the refused call never dialed")
}

func TestLocalEgress_ConcurrencyAndPerHostCapsFreeOnCompletion(t *testing.T) {
	for name, limits := range map[string]egresslimit.Limits{
		"per daemon": {UserMaxConcurrent: 1},
		"per host":   {HostMaxConcurrent: 1},
	} {
		t.Run(name, func(t *testing.T) {
			entered := make(chan struct{}, 4)
			gate := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				entered <- struct{}{}
				<-gate
				fmt.Fprint(w, "ok")
			}))
			defer srv.Close()
			closeGate := sync.OnceFunc(func() { close(gate) })
			defer closeGate()

			e := localEgressExecutor(t, limits)
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				if res := localFetch(e, srv.URL); res.IsError {
					t.Errorf("the in-cap fetch failed: %+v", res)
				}
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("the in-cap fetch never reached the server")
			}

			res := localFetch(e, srv.URL)
			require.Equal(t, ErrorCodeEgressRateLimited, res.ErrorCode, res.Content)
			if name == "per host" {
				u, _ := url.Parse(srv.URL)
				require.Contains(t, res.Content, u.Hostname())
			}

			closeGate()
			wg.Wait()
			res = localFetch(e, srv.URL)
			require.False(t, res.IsError, "a finished call frees its slot: %s", res.Content)
		})
	}
}

func TestLocalEgress_RefusalIsCountedInTheEgressMetric(t *testing.T) {
	e := localEgressExecutor(t, egresslimit.Limits{UserPerMinute: 60, UserBurst: 1})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") }))
	defer srv.Close()

	counter := observability.EgressLimitedTotal.WithLabelValues(tools.ToolFetch, egresslimit.ScopeUserRate)
	before := testutil.ToFloat64(counter)
	require.False(t, localFetch(e, srv.URL).IsError)
	require.True(t, localFetch(e, srv.URL).IsError)
	require.Equal(t, before+1, testutil.ToFloat64(counter))
}

func TestLocalEgress_ToolNotFoundIsStillToolNotFound(t *testing.T) {
	// prefer-daemon falls back to the server on TOOL_NOT_FOUND; a limiter in
	// front of the lookup would turn that into a refusal.
	rec := &recordingLimiter{}
	e := NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{}))
	e.SetEgressLimiter(rec)
	res := e.ExecuteTool(context.Background(), "github__not_a_real_action", `{}`, "c", 0, map[string]any{})
	require.Equal(t, "TOOL_NOT_FOUND", res.ErrorCode)
}

func TestLocalEgress_OnlyEgressToolsAreAdmitted(t *testing.T) {
	rec := &recordingLimiter{}
	e := NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{AllowPrivateEgress: true}))
	e.SetEgressLimiter(rec)

	run := func(name, input string) {
		e.ExecuteTool(context.Background(), name, input, "c", 0, map[string]any{"chat_id": "chat-9", "thread": "th-3"})
	}
	run(tools.ToolFetch, `{"url":"https://Example.com:8443/x","format":"text"}`)
	run(tools.ToolView, `{"file_path":"/nope"}`)
	run("not_a_tool", `{}`)

	require.Len(t, rec.reqs, 1, "view and unknown tools are not egress")
	got := rec.reqs[0]
	require.Equal(t, tools.ToolFetch, got.Tool)
	require.Equal(t, "example.com", got.Host)
	require.Equal(t, DaemonEgressSubject, got.UserID, "the daemon's calls share one bucket, whoever the user is")
	require.Empty(t, got.TurnKey, "a daemon has no per-turn bound: the call would wait, not be refused")
}

func TestLocalEgress_NoLimiterAdmitsEverything(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") }))
	defer srv.Close()
	e := NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{AllowPrivateEgress: true}))
	for i := 0; i < 5; i++ {
		require.False(t, localFetch(e, srv.URL).IsError)
	}
}
