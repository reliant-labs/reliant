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
	finish := d.trackRunExecution("one", "chat", cancel)
	sibling, cancelSibling := context.WithCancel(context.Background())
	defer cancelSibling()
	finishSibling := d.trackRunExecution("two", "other", cancelSibling)
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
	require.ErrorContains(t, <-returned, "late activity dispatch is not fenced")
	// No cross-machine clock cutoff: neither a late dispatch nor a legitimate
	// resumed conversation may be mistaken for proof of cancellation.
	late := d.trackRunExecution("late", "chat", func() {})
	late()
	resumed := d.trackRunExecution("resume", "chat", func() {})
	resumed()
}
func TestRunCancelEmptySnapshotDoesNotAcknowledgeLateDispatch(t *testing.T) {
	d := &daemonClient{}
	reply, err := d.cancelOwnedRun(context.Background(), []byte(`{"chat_id":"chat"}`))
	require.Nil(t, reply)
	require.ErrorContains(t, err, "late activity dispatch is not fenced")
	// The delayed activity does not exist in the original snapshot. There is
	// intentionally no timestamp assertion: independent worker/daemon clocks
	// cannot establish execution ownership or justify a stopped:true response.
	cancelled := false
	finish := d.trackRunExecution("delayed", "chat", func() { cancelled = true })
	defer finish()
	require.False(t, cancelled)
}

func TestRunCancelTimeoutIsNotSuccess(t *testing.T) {
	d := &daemonClient{}
	finish := d.trackRunExecution("one", "chat", func() {})
	defer finish()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err := d.cancelOwnedRun(ctx, []byte(`{"chat_id":"chat"}`))
	require.ErrorContains(t, err, "unconfirmed")
}
