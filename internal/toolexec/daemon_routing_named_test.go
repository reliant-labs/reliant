package toolexec

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// selectingResolver is a connected-daemon resolver that honours an id selector
// and otherwise lists every daemon, local first, as the real one does.
type selectingResolver struct{ daemons []DaemonInfo }

func (r selectingResolver) ResolveDaemons(_ context.Context, _ string, sel *DaemonSelector) ([]DaemonInfo, error) {
	if sel == nil || sel.ID == "" {
		return r.daemons, nil
	}
	for _, d := range r.daemons {
		if d.DaemonID == sel.ID {
			return []DaemonInfo{d}, nil
		}
	}
	return nil, nil
}

func twoDaemonResolver() selectingResolver {
	return selectingResolver{daemons: []DaemonInfo{
		{DaemonID: "daemon-A", Type: "local", Status: "connected"},
		{DaemonID: "daemon-B", Type: "cloud", Status: "connected"},
	}}
}

func subscribeSubject(t *testing.T, nc *nats.Conn, subject string) chan *nats.Msg {
	t.Helper()
	ch := make(chan *nats.Msg, 4)
	sub, err := nc.ChanSubscribe(subject, ch)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	return ch
}

// A cancel for a request that went to daemon B must reach daemon B, even
// though the user's default daemon is the local A. The request is abandoned
// (nobody answers it), which is the case the abandon-cancel exists for.
func TestWorker_CancelAfterRequestToB_TargetsB(t *testing.T) {
	nc := startPayloadTestNATS(t)
	router := NewNATSDaemonRouter(nc, WithResolver(twoDaemonResolver()))

	cancelOnA := subscribeSubject(t, nc, "tools.cancel.user-1.daemon-A")
	cancelOnB := subscribeSubject(t, nc, "tools.cancel.user-1.daemon-B")
	require.NoError(t, nc.Flush())

	ctx, stop := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer stop()
	_, err := router.SendToolRequestSyncWithSelector(ctx, "user-1",
		&ToolExecutionRequest{RequestID: "req-1", ToolCallID: "tc-1", ToolName: "bash"},
		&DaemonSelector{ID: "daemon-B"})
	require.Error(t, err, "nobody answers, so the call is abandoned")

	require.NoError(t, router.SendToolExecutionCancel(context.Background(), "user-1", "tc-1", "abandoned"))
	require.NoError(t, nc.Flush())

	select {
	case <-cancelOnB:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel never reached daemon B")
	}
	select {
	case <-cancelOnA:
		t.Fatal("cancel for a request sent to B was delivered to the default daemon A")
	case <-time.After(150 * time.Millisecond):
	}
}

// The abandon-cancel pushed by RemoteExecutor must land on the daemon the run
// was pinned to, found through the run's selector alone — the router the
// executor uses may be a different instance from the one that sent the request.
func TestWorker_AbandonCancelUsesTheRunsSelector(t *testing.T) {
	nc := startPayloadTestNATS(t)
	router := NewNATSDaemonRouter(nc, WithResolver(twoDaemonResolver()))
	cancelOnA := subscribeSubject(t, nc, "tools.cancel.user-1.daemon-A")
	cancelOnB := subscribeSubject(t, nc, "tools.cancel.user-1.daemon-B")
	require.NoError(t, nc.Flush())

	executor := &RemoteExecutor{router: &selectorAwareAbandonRouter{DaemonRouter: router}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, err := executor.executeOnDaemon(ctx, &ToolRequest{
		UserID: "user-1", ChatID: "chat-1", ToolName: "bash", ToolCallID: "tc-9",
		DaemonSelector: &DaemonSelector{ID: "daemon-B"},
	}, time.Now())
	require.NoError(t, err)
	require.NoError(t, nc.Flush())

	select {
	case <-cancelOnB:
	case <-time.After(2 * time.Second):
		t.Fatal("abandon-cancel never reached daemon B")
	}
	select {
	case <-cancelOnA:
		t.Fatal("abandon-cancel went to the default daemon A")
	case <-time.After(150 * time.Millisecond):
	}
}

// selectorAwareAbandonRouter blocks the sync request until its context dies
// WITHOUT recording the request, so only the selector can route the cancel.
type selectorAwareAbandonRouter struct{ DaemonRouter }

func (r *selectorAwareAbandonRouter) SendToolRequestSyncWithSelector(ctx context.Context, _ string, _ *ToolExecutionRequest, _ *DaemonSelector) (*ToolExecutionResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// An explicit id selector is only honoured for a daemon the user owns, and a
// foreign id looks exactly like an absent one.
func TestResolveDaemonIDForSelector_ForeignDaemonIDIsNotFound(t *testing.T) {
	records := &fakeDaemonRecords{daemons: []*db.Daemon{
		{ID: "d1", UserID: "user-1"},
		{ID: "d-other", UserID: "user-2"},
	}}
	router := recordsRouter(records)

	got, err := ResolveDaemonIDForSelector(context.Background(), router, "user-1", &DaemonSelector{ID: "d1"})
	require.NoError(t, err)
	assert.Equal(t, "d1", got)

	_, foreignErr := ResolveDaemonIDForSelector(context.Background(), router, "user-1", &DaemonSelector{ID: "d-other"})
	require.Error(t, foreignErr)
	_, absentErr := ResolveDaemonIDForSelector(context.Background(), router, "user-1", &DaemonSelector{ID: "d-nonexistent"})
	require.Error(t, absentErr)
	assert.Equal(t, absentErr.Error(), foreignErr.Error(), "a foreign id must be indistinguishable from an absent one")
	assert.False(t, IsDaemonPending(foreignErr), "not-found, not the retryable 'still starting'")
}
