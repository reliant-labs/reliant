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

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/egresslimit"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// egressExecutor is an executor whose server side runs the real fetch tool
// (self-hosted dial policy, so an httptest loopback server is reachable)
// behind a real limiter.
func egressExecutor(t *testing.T, l egresslimit.Limits) *RemoteExecutor {
	t.Helper()
	limiter, err := egresslimit.New(egresslimit.Deps{Limits: l})
	require.NoError(t, err)
	e := NewRemoteExecutor(&routerStub{})
	e.SetServerExecutor(NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{})))
	e.SetEgressLimiter(limiter)
	return e
}

func fetchReq(user, target string) *ToolRequest {
	return &ToolRequest{
		ToolName:   tools.ToolFetch,
		ToolInput:  fmt.Sprintf(`{"url":%q,"format":"text"}`, target),
		ToolCallID: "call-" + user,
		UserID:     user, ChatID: "chat-1", ProjectID: "proj-1", Thread: "thread-1",
	}
}

func roomyLimits(l egresslimit.Limits) egresslimit.Limits {
	if l.UserPerMinute == 0 {
		l.UserPerMinute, l.UserBurst = 1_000_000, 1_000_000
	}
	for _, p := range []*int{&l.UserMaxConcurrent, &l.HostMaxConcurrent, &l.TurnMaxParallel} {
		if *p == 0 {
			*p = 1_000_000
		}
	}
	return l
}

func TestEgress_BurstBeyondLimitIsANonFatalToolResult(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	e := egressExecutor(t, roomyLimits(egresslimit.Limits{UserPerMinute: 60, UserBurst: 2}))
	for i := 0; i < 2; i++ {
		res, err := e.executeOnServer(context.Background(), fetchReq("u1", srv.URL), time.Now())
		require.NoError(t, err)
		require.False(t, res.IsError, res.Content)
	}

	res, err := e.executeOnServer(context.Background(), fetchReq("u1", srv.URL), time.Now())
	require.NoError(t, err, "a refusal is a tool result, never an executor error")
	require.True(t, res.IsError)
	require.False(t, res.Success)
	require.Equal(t, ErrorCodeEgressRateLimited, res.ErrorCode)
	require.Contains(t, res.Content, "rate limited")
	require.Contains(t, res.Content, "retry after")
	require.EqualValues(t, 2, hits.Load(), "the refused call never dialed")
}

func TestEgress_TwoUsersAreIndependent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") }))
	defer srv.Close()

	e := egressExecutor(t, roomyLimits(egresslimit.Limits{UserPerMinute: 60, UserBurst: 1}))
	res, _ := e.executeOnServer(context.Background(), fetchReq("alice", srv.URL), time.Now())
	require.False(t, res.IsError, res.Content)
	res, _ = e.executeOnServer(context.Background(), fetchReq("alice", srv.URL), time.Now())
	require.Equal(t, ErrorCodeEgressRateLimited, res.ErrorCode)

	res, _ = e.executeOnServer(context.Background(), fetchReq("bob", srv.URL), time.Now())
	require.False(t, res.IsError, "bob is unaffected by alice's burst: %s", res.Content)
}

func TestEgress_PerHostCapHoldsAcrossUsersAndFreesOnCompletion(t *testing.T) {
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

	e := egressExecutor(t, roomyLimits(egresslimit.Limits{HostMaxConcurrent: 2}))
	var wg sync.WaitGroup
	for _, u := range []string{"alice", "bob"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := e.executeOnServer(context.Background(), fetchReq(u, srv.URL), time.Now())
			if err != nil || res.IsError {
				t.Errorf("%s: %v %+v", u, err, res)
			}
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("the two in-cap fetches never reached the server")
		}
	}

	res, err := e.executeOnServer(context.Background(), fetchReq("carol", srv.URL), time.Now())
	require.NoError(t, err)
	require.Equal(t, ErrorCodeEgressRateLimited, res.ErrorCode, "a third user to the same host is refused: %s", res.Content)
	u, _ := url.Parse(srv.URL)
	require.Contains(t, res.Content, u.Hostname())

	closeGate()
	wg.Wait()
	res, err = e.executeOnServer(context.Background(), fetchReq("carol", srv.URL), time.Now())
	require.NoError(t, err)
	require.False(t, res.IsError, "finished calls free the host: %s", res.Content)
}

type recordingLimiter struct {
	mu   sync.Mutex
	reqs []egresslimit.Request
}

func (r *recordingLimiter) Acquire(_ context.Context, req egresslimit.Request) (func(), error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
	// Refuse, so no tool under test dials anything.
	return nil, &egresslimit.LimitedError{Scope: egresslimit.ScopeUserRate, RetryAfter: time.Second}
}

// Only egress tools are admitted, and each is keyed by who, where and which turn.
func TestEgress_OnlyEgressToolsAreAdmitted(t *testing.T) {
	rec := &recordingLimiter{}
	e := NewRemoteExecutor(&routerStub{})
	e.SetServerExecutor(NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{})))
	e.SetEgressLimiter(rec)

	run := func(name, input string) {
		_, err := e.executeOnServer(context.Background(), &ToolRequest{
			ToolName: name, ToolInput: input, ToolCallID: "c", UserID: "u1",
			ChatID: "chat-9", ProjectID: "p", Thread: "th-3",
		}, time.Now())
		require.NoError(t, err)
	}
	run(tools.ToolFetch, `{"url":"https://Example.com:8443/x","format":"text"}`)
	run(tools.ToolWebSearch, `{"query":"x"}`)
	run(tools.ToolSourcegraph, `{"query":"x"}`)
	run("http__request", `{"url":"https://api.example.org/v1"}`)
	run("github__issue_get", `{}`)
	run(tools.ToolView, `{"file_path":"/nope"}`)
	run("not_a_tool", `{}`)

	got := map[string]egresslimit.Request{}
	for _, r := range rec.reqs {
		got[r.Tool] = r
	}
	require.Len(t, got, 5, "view and unknown tools are not egress")
	require.Equal(t, egresslimit.Request{Tool: "fetch", UserID: "u1", Host: "example.com", TurnKey: "chat-9/th-3"}, got["fetch"])
	require.Equal(t, "html.duckduckgo.com", got["websearch"].Host)
	require.Equal(t, "sourcegraph.com", got["sourcegraph"].Host)
	require.Equal(t, "api.example.org", got["http__request"].Host)
	require.Equal(t, "api.github.com", got["github__issue_get"].Host)
}

func TestEgressDestination(t *testing.T) {
	for _, c := range []struct {
		tool, input, host string
		egress            bool
	}{
		{"fetch", `{"url":"https://a.example.com/p"}`, "a.example.com", true},
		{"fetch", `not json`, "", true},
		{"fetch", `{"url":"::bad"}`, "", true},
		{"websearch", `{}`, "html.duckduckgo.com", true},
		{"http__request", `{"url":"http://H.Example.com./x"}`, "h.example.com", true},
		{"github__repo_get", `{}`, "api.github.com", true},
		{"slack__message_post", `{}`, "slack.com", true},
		{"sentry__issue_list", `{}`, "sentry.io", true},
		{"twilio__message_send", `{}`, "api.twilio.com", true},
		{"view", `{}`, "", false},
		{"bash", `{}`, "", false},
		{"mcp__x__y", `{}`, "", false},
	} {
		host, ok := egressDestination(c.tool, c.input)
		require.Equal(t, c.egress, ok, c.tool)
		require.Equal(t, c.host, host, c.tool)
	}
}

func TestEgress_NoLimiterAdmitsEverything(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") }))
	defer srv.Close()
	e := NewRemoteExecutor(&routerStub{})
	e.SetServerExecutor(NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{})))
	res, err := e.executeOnServer(context.Background(), fetchReq("u1", srv.URL), time.Now())
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
}
