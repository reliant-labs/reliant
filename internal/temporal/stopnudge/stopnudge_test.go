// Copyright (c) 2025 Reliant Labs
package stopnudge

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
)

// fakeClient answers the four stop requests with err; every other method of
// client.Client is the nil embedded interface (calling one panics, which is
// what a test that reaches one should do).
type fakeClient struct {
	client.Client
	err error
}

func (f *fakeClient) SignalWorkflow(context.Context, string, string, string, interface{}) error {
	return f.err
}
func (f *fakeClient) CancelWorkflow(context.Context, string, string) error { return f.err }
func (f *fakeClient) TerminateWorkflow(context.Context, string, string, string, ...interface{}) error {
	return f.err
}
func (f *fakeClient) ResetWorkflowExecution(context.Context, *workflowservice.ResetWorkflowExecutionRequest) (*workflowservice.ResetWorkflowExecutionResponse, error) {
	return &workflowservice.ResetWorkflowExecutionResponse{}, f.err
}

type recordingPublisher struct {
	mu   sync.Mutex
	sent []string
}

func (p *recordingPublisher) Publish(subject string, data []byte) error {
	var m message
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, subject+" "+m.WorkflowID)
	return nil
}

func TestWrap_NudgesAfterEveryStopAndNothingElse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pub := &recordingPublisher{}
	c := Wrap(&fakeClient{}, pub, "signal.pause", "interrupt_thread")

	require.NoError(t, c.SignalWorkflow(ctx, "wf-pause", "", "signal.pause", nil))
	require.NoError(t, c.SignalWorkflow(ctx, "wf-interrupt", "", "interrupt_thread", nil))
	require.NoError(t, c.SignalWorkflow(ctx, "wf-wake", "", "thread_wake", nil))
	require.NoError(t, c.CancelWorkflow(ctx, "wf-cancel", ""))
	require.NoError(t, c.TerminateWorkflow(ctx, "wf-terminate", "", "reason"))
	_, err := c.ResetWorkflowExecution(ctx, &workflowservice.ResetWorkflowExecutionRequest{
		WorkflowExecution: &commonpb.WorkflowExecution{WorkflowId: "wf-reset"},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{
		Subject + " wf-pause",
		Subject + " wf-interrupt",
		Subject + " wf-cancel",
		Subject + " wf-terminate",
		Subject + " wf-reset",
	}, pub.sent, "a signal that stops nothing (thread_wake) must not nudge")
}

// A request Temporal refused stopped nothing, so it nudges nothing, and the
// caller still gets the error unchanged.
func TestWrap_FailedStopDoesNotNudge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	refused := errors.New("workflow not found")
	pub := &recordingPublisher{}
	c := Wrap(&fakeClient{err: refused}, pub, "signal.pause")

	assert.ErrorIs(t, c.SignalWorkflow(ctx, "wf", "", "signal.pause", nil), refused)
	assert.ErrorIs(t, c.CancelWorkflow(ctx, "wf", ""), refused)
	assert.ErrorIs(t, c.TerminateWorkflow(ctx, "wf", "", "r"), refused)
	assert.Empty(t, pub.sent)
}

// A nudge that cannot be published never fails the stop it follows.
func TestWrap_PublishFailureDoesNotFailTheStop(t *testing.T) {
	t.Parallel()
	c := Wrap(&fakeClient{}, failingPublisher{}, "signal.pause")
	assert.NoError(t, c.SignalWorkflow(context.Background(), "wf", "", "signal.pause", nil))
}

type failingPublisher struct{}

func (failingPublisher) Publish(string, []byte) error { return errors.New("nats: connection closed") }

func TestHub_HandleMessageDropsMalformedMessages(t *testing.T) {
	t.Parallel()
	h := NewHub()
	nudged, unwatch := h.Watch("wf")
	defer unwatch()
	h.HandleMessage([]byte("not json"))
	h.HandleMessage([]byte(`{"workflow_id":""}`))
	select {
	case <-nudged:
		t.Fatal("a malformed message nudged a watcher")
	default:
	}
	h.HandleMessage([]byte(`{"workflow_id":"wf"}`))
	select {
	case <-nudged:
	default:
		t.Fatal("a well-formed message did not nudge its watcher")
	}
}

func TestHub_DeliversToTheWorkflowsWatchersOnly(t *testing.T) {
	t.Parallel()
	h := NewHub()
	a1, unwatchA1 := h.Watch("a")
	a2, unwatchA2 := h.Watch("a")
	b, unwatchB := h.Watch("b")
	defer unwatchB()

	h.Deliver("a")
	h.Deliver("a") // coalesces with the pending one; never blocks
	for _, ch := range []<-chan struct{}{a1, a2} {
		select {
		case <-ch:
		default:
			t.Fatal("a watcher of the nudged workflow got nothing")
		}
	}
	select {
	case <-b:
		t.Fatal("a watcher of another workflow was nudged")
	default:
	}

	unwatchA1()
	unwatchA1() // idempotent
	assert.Equal(t, 1, h.watching("a"))
	unwatchA2()
	assert.Equal(t, 0, h.watching("a"))
	h.mu.Lock()
	_, kept := h.watches["a"]
	h.mu.Unlock()
	assert.False(t, kept, "an unwatched workflow leaves no entry behind")
}

// End to end over a real (in-process) NATS server: the client's stop on one
// connection reaches the hub subscribed on another.
func TestWrapToHub_OverNATS(t *testing.T) {
	t.Parallel()
	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	srv := natsserver.RunServer(&opts)
	t.Cleanup(srv.Shutdown)

	pubConn, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(pubConn.Close)
	subConn, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(subConn.Close)

	hub := NewHub()
	sub, err := subConn.Subscribe(Subject, func(m *nats.Msg) { hub.HandleMessage(m.Data) })
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	require.NoError(t, subConn.Flush())
	nudged, unwatch := hub.Watch("wf-1")
	defer unwatch()

	c := Wrap(&fakeClient{}, pubConn, "signal.pause")
	require.NoError(t, c.SignalWorkflow(context.Background(), "wf-1", "", "signal.pause", nil))

	select {
	case <-nudged:
	case <-time.After(5 * time.Second):
		t.Fatal("the nudge never reached the hub")
	}
}
