package daemonruntime

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestRunCancelWaitsForOwnedCompletionAndLeavesSibling(t *testing.T) {
	d := &daemonClient{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finish, ok := d.trackRunExecution("one", "chat", time.Now().UnixNano(), cancel)
	require.True(t, ok)
	sibling, cancelSibling := context.WithCancel(context.Background())
	defer cancelSibling()
	finishSibling, ok := d.trackRunExecution("two", "other", time.Now().UnixNano(), cancelSibling)
	require.True(t, ok)
	defer finishSibling()
	returned := make(chan error, 1)
	go func() {
		_, err := d.cancelOwnedRun(context.Background(), []byte(`{"chat_id":"chat"}`))
		returned <- err
	}()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("not cancelled")
	}
	select {
	case <-returned:
		t.Fatal("ack before handler completed")
	default:
	}
	require.NoError(t, sibling.Err())
	finish()
	require.NoError(t, <-returned)
	_, ok = d.trackRunExecution("late", "chat", 0, cancel)
	require.False(t, ok)
	resumed, ok := d.trackRunExecution("resume", "chat", time.Now().Add(time.Second).UnixNano(), cancel)
	require.True(t, ok)
	resumed()
}
func TestRunCancelTimeoutIsNotSuccess(t *testing.T) {
	d := &daemonClient{}
	finish, _ := d.trackRunExecution("one", "chat", 0, func() {})
	defer finish()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err := d.cancelOwnedRun(ctx, []byte(`{"chat_id":"chat"}`))
	require.ErrorContains(t, err, "unconfirmed")
}
