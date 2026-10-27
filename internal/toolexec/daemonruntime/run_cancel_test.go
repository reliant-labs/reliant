package daemonruntime

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func cancelFixture(t *testing.T) *daemonClient {
	t.Helper()
	d := &daemonClient{}
	d.bootCfg.DataDir = t.TempDir()
	return d
}
func TestRunCancelFencesLateDispatchAndSurvivesRestart(t *testing.T) {
	d := cancelFixture(t)
	reply, err := d.cancelOwnedRun(context.Background(), []byte(`{"chat_id":"chat","root_first_run_id":"old"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"stopped":true}`, string(reply))
	_, err = d.trackRunExecution("late", "chat", "old", func() {})
	require.ErrorContains(t, err, "cancelled")
	restarted := &daemonClient{bootCfg: d.bootCfg}
	_, err = restarted.trackRunExecution("late-new-id", "chat", "old", func() {})
	require.ErrorContains(t, err, "cancelled")
	finish, err := restarted.trackRunExecution("new-conversation", "chat", "new", func() {})
	require.NoError(t, err)
	finish()
}
func TestRunCancelWaitsForOwnedCompletionAndLeavesSibling(t *testing.T) {
	d := cancelFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finish, err := d.trackRunExecution("one", "chat", "root", cancel)
	require.NoError(t, err)
	sibling, cancelSibling := context.WithCancel(context.Background())
	defer cancelSibling()
	fs, err := d.trackRunExecution("two", "other", "other", cancelSibling)
	require.NoError(t, err)
	defer fs()
	result := make(chan error, 1)
	go func() {
		_, e := d.cancelOwnedRun(context.Background(), []byte(`{"chat_id":"chat","root_first_run_id":"root"}`))
		result <- e
	}()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("not cancelled")
	}
	select {
	case <-result:
		t.Fatal("ack before handler completed")
	default:
	}
	require.NoError(t, sibling.Err())
	finish()
	require.NoError(t, <-result)
}
func TestRunCancelTimeoutIsNotSuccess(t *testing.T) {
	d := cancelFixture(t)
	finish, err := d.trackRunExecution("one", "chat", "root", func() {})
	require.NoError(t, err)
	defer finish()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err = d.cancelOwnedRun(ctx, []byte(`{"chat_id":"chat","root_first_run_id":"root"}`))
	require.ErrorContains(t, err, "unconfirmed")
}
func TestRunCancelRestartWithActiveMarkerDoesNotClaimTreeGone(t *testing.T) {
	d := cancelFixture(t)
	finish, err := d.trackRunExecution("active", "chat", "root", func() {})
	require.NoError(t, err)
	defer finish()
	restarted := &daemonClient{bootCfg: d.bootCfg}
	_, err = restarted.cancelOwnedRun(context.Background(), []byte(`{"chat_id":"chat","root_first_run_id":"root"}`))
	require.ErrorContains(t, err, "execution markers remain")
}

func TestRunCancelMissingIdentityDoesNotAcknowledge(t *testing.T) {
	d := cancelFixture(t)
	_, err := d.cancelOwnedRun(context.Background(), []byte(`{"chat_id":"chat"}`))
	require.ErrorContains(t, err, "identity required")
}
