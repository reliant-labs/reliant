package readguard

import (
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlightPanicCompletesAndReleasesSlot(t *testing.T) {
	g := New(Deps{Limits: Limits{PerUser: 1}}).(*guard)
	proc := Read[pb.ListChatsResponse]("list")
	_, err := g.do(context.Background(), "user", "key", proc, func(context.Context) (*answer, error) {
		panic("secret handler detail")
	})
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	require.NotContains(t, err.Error(), "secret handler detail")
	resp, err := g.do(context.Background(), "user", "key", proc, func(context.Context) (*answer, error) {
		return &answer{msg: &pb.ListChatsResponse{Total: 1}}, nil
	})
	require.NoError(t, err)
	require.Equal(t, int32(1), resp.msg.(*pb.ListChatsResponse).Total)
	g.mu.Lock()
	defer g.mu.Unlock()
	require.Empty(t, g.users)
}

// Outer interceptors may mutate a result immediately. Every waiter must send
// its own copy of the shared message and write its own response headers, so
// one caller's interceptor changing them is never seen by another (and never
// races: run under -race to exercise that boundary). An error needs no copy:
// a connect v2 *connect.Error, and svcerr's wrappers, are immutable.
func TestEveryWaiterOwnsResponseAndHeaders(t *testing.T) {
	svc := newChatLists()
	mutate := func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			return next(ctx, spec, mutatingStream{ServerStream: stream, ctx: ctx})
		}
	}
	client, g := serve(t, svc, Deps{}, mutate)
	const count = 30
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, header, err := listChatsWithHeader(context.Background(), client, "user", "p1")
			if !assert.NoError(t, err) {
				return
			}
			assert.Equal(t, int32(2), resp.Total, "the read's total, bumped once by this caller's own interceptor")
			assert.Equal(t, []string{"owned"}, header.Values("X-Caller"))
			assert.Equal(t, []string{"1"}, header.Values("X-Read"))
		}()
	}
	require.Eventually(t, func() bool { return callers(g, "user") == count }, 30*time.Second, time.Millisecond)
	close(svc.release)
	wg.Wait()
	require.Equal(t, int32(1), svc.reads.Load())
}

// mutatingStream is an outer interceptor's stream that changes the response
// message and the response headers as it sends.
type mutatingStream struct {
	connect.ServerStream
	ctx context.Context
}

func (s mutatingStream) Send(msg any) error {
	if resp, ok := msg.(*pb.ListChatsResponse); ok {
		resp.Total++
	}
	if info, ok := connect.CallInfoForServerContext(s.ctx); ok {
		info.ResponseHeader().Add("X-Caller", "owned")
	}
	return s.ServerStream.Send(msg)
}
