// Copyright (c) 2025 Reliant Labs
package readguard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
)

// chatLists is a ChatService whose ListChats and ListArchivedChats block until
// released, counting the reads that reached it and the ones whose context was
// cancelled under them.
type chatLists struct {
	reliantv1connect.UnimplementedChatServiceHandler

	release   chan struct{}
	reads     atomic.Int32
	cancelled atomic.Int32
	// running is sent each read's project as it starts.
	running chan string
}

func newChatLists() *chatLists {
	return &chatLists{release: make(chan struct{}), running: make(chan string, 100)}
}

func (s *chatLists) ListChats(ctx context.Context, req *connect.Request[reliantv1.ListChatsRequest]) (*connect.Response[reliantv1.ListChatsResponse], error) {
	s.reads.Add(1)
	s.running <- req.Msg.ProjectId
	select {
	case <-s.release:
	case <-ctx.Done():
		s.cancelled.Add(1)
		return nil, ctx.Err()
	}
	resp := connect.NewResponse(&reliantv1.ListChatsResponse{
		Chats: []*reliantv1.Chat{{Id: "chat-in-" + req.Msg.ProjectId}},
		Total: 1,
	})
	resp.Header().Set("X-Read", "1")
	return resp, nil
}

func (s *chatLists) ListArchivedChats(ctx context.Context, _ *connect.Request[reliantv1.ListArchivedChatsRequest]) (*connect.Response[reliantv1.ListArchivedChatsResponse], error) {
	s.reads.Add(1)
	s.running <- "archived"
	<-s.release
	return connect.NewResponse(&reliantv1.ListArchivedChatsResponse{}), nil
}

func (s *chatLists) GetChat(ctx context.Context, _ *connect.Request[reliantv1.GetChatRequest]) (*connect.Response[reliantv1.GetChatResponse], error) {
	s.reads.Add(1)
	return connect.NewResponse(&reliantv1.GetChatResponse{}), nil
}

// serve mounts svc behind the guard, with the caller's user taken from the
// X-User header the way the API auth interceptor installs it, and returns a
// client.
func serve(t *testing.T, svc *chatLists, cfg Deps) (reliantv1connect.ChatServiceClient, *guard) {
	t.Helper()
	cfg.Procedures = []Procedure{
		Read[reliantv1.ListChatsResponse](reliantv1connect.ChatServiceListChatsProcedure),
		Read[reliantv1.ListArchivedChatsResponse](reliantv1connect.ChatServiceListArchivedChatsProcedure),
	}
	authn := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if user := req.Header().Get("X-User"); user != "" {
				ctx = auth.WithUser(ctx, user, "user", user+"@example.com")
			}
			return next(ctx, req)
		}
	})
	g := New(cfg).(*guard)
	path, handler := reliantv1connect.NewChatServiceHandler(svc, connect.WithInterceptors(authn, g))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return reliantv1connect.NewChatServiceClient(server.Client(), server.URL), g
}

// callers is how many callers wait on user's reads in flight.
func callers(g *guard, user string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	if u := g.users[user]; u != nil {
		for _, f := range u.flights {
			n += f.callers
		}
	}
	return n
}

// queued is how many of user's reads wait for a slot.
func queued(g *guard, user string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if u := g.users[user]; u != nil {
		return u.waiting
	}
	return 0
}

func listChats(ctx context.Context, client reliantv1connect.ChatServiceClient, user, project string) (*connect.Response[reliantv1.ListChatsResponse], error) {
	req := connect.NewRequest(&reliantv1.ListChatsRequest{ProjectId: project})
	req.Header().Set("X-User", user)
	return client.ListChats(ctx, req)
}

// waitRunning waits for n reads to have reached the service.
func waitRunning(t *testing.T, svc *chatLists, n int) {
	t.Helper()
	for range n {
		select {
		case <-svc.running:
		case <-time.After(5 * time.Second):
			t.Fatalf("expected %d reads to reach the service", n)
		}
	}
}

// A burst of one user's identical ListChats — the storm an old client sends —
// is one read: every caller gets that read's answer, each its own copy.
func TestIdenticalReadsShareOneRead(t *testing.T) {
	t.Parallel()
	svc := newChatLists()
	client, g := serve(t, svc, Deps{})

	const burst = 50
	var wg sync.WaitGroup
	responses := make([]*connect.Response[reliantv1.ListChatsResponse], burst)
	errs := make([]error, burst)
	for i := range burst {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses[i], errs[i] = listChats(context.Background(), client, "u1", "p1")
		}()
	}
	waitRunning(t, svc, 1)
	// Every caller has arrived and joined before the read answers.
	require.Eventually(t, func() bool { return callers(g, "u1") == burst }, 5*time.Second, 5*time.Millisecond)
	close(svc.release)
	wg.Wait()

	require.Equal(t, int32(1), svc.reads.Load(), "identical concurrent reads must share one read")
	for i := range burst {
		require.NoError(t, errs[i])
		require.Equal(t, "chat-in-p1", responses[i].Msg.Chats[0].Id)
		require.Equal(t, "1", responses[i].Header().Get("X-Read"), "a shared answer keeps the read's headers")
	}
}

// Different users never share or count against each other, and different
// requests from one user are different reads.
func TestReadsAreSharedPerUserAndRequest(t *testing.T) {
	t.Parallel()
	svc := newChatLists()
	client, _ := serve(t, svc, Deps{})

	var wg sync.WaitGroup
	for _, call := range []struct{ user, project string }{{"u1", "p1"}, {"u2", "p1"}, {"u1", "p2"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := listChats(context.Background(), client, call.user, call.project)
			require.NoError(t, err)
			require.Equal(t, "chat-in-"+call.project, resp.Msg.Chats[0].Id)
		}()
	}
	waitRunning(t, svc, 3)
	close(svc.release)
	wg.Wait()
	require.Equal(t, int32(3), svc.reads.Load())
}

// One user's distinct reads run PerUser at a time. One more waits for a slot
// and runs when one frees; past Queue waiters, or past Wait, a read is refused
// with ResourceExhausted and a Retry-After, and nothing reaches the database.
// Another user is unaffected.
func TestDistinctReadsAreBoundedPerUser(t *testing.T) {
	t.Parallel()
	svc := newChatLists()
	client, g := serve(t, svc, Deps{PerUser: 2, Queue: 1, Wait: 300 * time.Millisecond, RetryAfter: 2 * time.Second})

	type result struct {
		resp *connect.Response[reliantv1.ListChatsResponse]
		err  error
	}
	start := func(user, project string) chan result {
		ch := make(chan result, 1)
		go func() {
			resp, err := listChats(context.Background(), client, user, project)
			ch <- result{resp, err}
		}()
		return ch
	}

	first, second := start("u1", "p1"), start("u1", "p2")
	waitRunning(t, svc, 2)

	// The third waits in the queue; the fourth finds the queue full.
	third := start("u1", "p3")
	require.Eventually(t, func() bool { return queued(g, "u1") == 1 }, 5*time.Second, 5*time.Millisecond)
	_, err := listChats(context.Background(), client, "u1", "p4")
	requireRefused(t, err, "2")

	// The third is refused once Wait passes without a slot.
	r := <-third
	requireRefused(t, r.err, "2")

	// Another user still gets a read of their own.
	other := start("u2", "p1")
	waitRunning(t, svc, 1)

	// A waiter takes the slot a finished read frees.
	fifth := start("u1", "p5")
	require.Eventually(t, func() bool { return queued(g, "u1") == 1 }, 5*time.Second, 5*time.Millisecond)
	close(svc.release)
	for _, ch := range []chan result{first, second, other, fifth} {
		r := <-ch
		require.NoError(t, r.err)
	}
	require.Equal(t, int32(4), svc.reads.Load(), "a refused read must not reach the service")
}

func requireRefused(t *testing.T, err error, retryAfter string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err), "%v", err)
	var ce *connect.Error
	require.True(t, errors.As(err, &ce))
	require.Equal(t, retryAfter, ce.Meta().Get("Retry-After"))
}

// A caller that gives up does not cancel a read other callers still wait on;
// when the last one gives up, the read is cancelled (so a client's abort still
// frees the database, as #1865 made it).
func TestSharedReadIsCancelledWithItsLastCaller(t *testing.T) {
	t.Parallel()
	svc := newChatLists()
	client, g := serve(t, svc, Deps{})

	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	done1, done2 := make(chan error, 1), make(chan error, 1)
	go func() { _, err := listChats(ctx1, client, "u1", "p1"); done1 <- err }()
	waitRunning(t, svc, 1)
	go func() { _, err := listChats(ctx2, client, "u1", "p1"); done2 <- err }()
	require.Eventually(t, func() bool { return callers(g, "u1") == 2 }, 5*time.Second, 5*time.Millisecond)

	cancel1()
	require.Error(t, <-done1)
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, int32(0), svc.cancelled.Load(), "the read must outlive a caller while another waits on it")

	cancel2()
	require.Error(t, <-done2)
	require.Eventually(t, func() bool { return svc.cancelled.Load() == 1 }, 5*time.Second, 5*time.Millisecond,
		"the read must be cancelled once no caller waits on it")
	require.Equal(t, int32(1), svc.reads.Load())
}

// Procedures the guard does not name, and calls with no signed-in user, pass
// straight through.
func TestUnguardedCallsPassThrough(t *testing.T) {
	t.Parallel()
	svc := newChatLists()
	client, _ := serve(t, svc, Deps{PerUser: 1, Queue: 1, Wait: time.Millisecond})

	go func() { _, _ = listChats(context.Background(), client, "u1", "p1") }()
	waitRunning(t, svc, 1)
	for range 3 {
		req := connect.NewRequest(&reliantv1.GetChatRequest{ChatId: "c"})
		req.Header().Set("X-User", "u1")
		_, err := client.GetChat(context.Background(), req)
		require.NoError(t, err, "GetChat is not guarded")
	}
	close(svc.release)
}
