// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// preemptRouter stands in for a daemon running a preemptable wait. Told to
// stop with the preemption reason, the tool answers with its own result, and
// that answer is the reply — unless the router is set to never answer, which is
// what an offline or out-of-date daemon looks like from here.
type preemptRouter struct {
	routerStub

	mu          sync.Mutex
	cancels     []string // reasons, in order
	stopped     chan struct{}
	stopOnce    sync.Once
	neverAnswer bool
	pushErr     error
}

func newPreemptRouter() *preemptRouter {
	return &preemptRouter{stopped: make(chan struct{})}
}

func (r *preemptRouter) SendToolExecutionCancel(_ context.Context, _, _, reason string) error {
	r.mu.Lock()
	r.cancels = append(r.cancels, reason)
	r.mu.Unlock()
	if r.pushErr != nil {
		return r.pushErr
	}
	if reason == PreemptedCancelReason && !r.neverAnswer {
		r.stopOnce.Do(func() { close(r.stopped) })
	}
	return nil
}

func (r *preemptRouter) SendToolRequestSync(ctx context.Context, _ string, _ *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	select {
	case <-r.stopped:
		return &ToolExecutionResponse{Success: true, Content: "Stopped waiting on process p1 after 1s. It is STILL RUNNING."}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(10 * time.Second):
		return &ToolExecutionResponse{Success: true, Content: "never preempted"}, nil
	}
}

func (r *preemptRouter) reasons() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.cancels...)
}

func preemptedAfter(delay time.Duration) context.Context {
	preempt := make(chan struct{})
	time.AfterFunc(delay, func() { close(preempt) })
	return tools.WithPreemption(context.Background(), preempt)
}

func waitRequest() *ToolRequest {
	return &ToolRequest{UserID: "user-1", ChatID: "chat-1", ToolName: tools.ToolShellWait, ToolCallID: "toolu_wait"}
}

// A preempted daemon call is TOLD to stop and then still waited on, so the
// tool's own answer comes back. Cancelling the wait instead — how every other
// stop works — would abandon the call and drop that answer.
func TestExecuteOnDaemon_PreemptionKeepsTheToolsOwnAnswer(t *testing.T) {
	router := newPreemptRouter()
	executor := &RemoteExecutor{router: router}

	start := time.Now()
	res, err := executor.executeOnDaemon(preemptedAfter(20*time.Millisecond), waitRequest(), start)
	require.NoError(t, err)

	assert.True(t, res.Success, "the tool answered; that is a result, not a failure: %+v", res)
	assert.Contains(t, res.Content, "STILL RUNNING")
	assert.Equal(t, []string{PreemptedCancelReason}, router.reasons(),
		"exactly one stop, and it must say preempted — an 'abandoned' push means the answer was thrown away")
	assert.Less(t, time.Since(start), 5*time.Second)
}

// A daemon that never answers (offline, or too old to know the reason) must
// not hold the agent: past the grace period the wait is abandoned.
func TestExecuteOnDaemon_PreemptedCallThatNeverAnswersIsAbandoned(t *testing.T) {
	router := newPreemptRouter()
	router.neverAnswer = true
	executor := &RemoteExecutor{router: router, preemptGrace: 50 * time.Millisecond}

	start := time.Now()
	res, err := executor.executeOnDaemon(preemptedAfter(20*time.Millisecond), waitRequest(), start)
	require.NoError(t, err)

	assert.False(t, res.Success, "an abandoned wait has no answer to report")
	assert.Less(t, time.Since(start), 5*time.Second, "the grace period, not the tool's own budget, bounds this")
	assert.Equal(t, []string{PreemptedCancelReason, "tool execution abandoned"}, router.reasons())
}

// A stop that cannot be delivered at all abandons the wait at once: there is
// no answer coming to wait for.
func TestExecuteOnDaemon_UndeliverablePreemptionAbandonsAtOnce(t *testing.T) {
	router := newPreemptRouter()
	router.pushErr = errors.New("daemon offline")
	executor := &RemoteExecutor{router: router}

	start := time.Now()
	res, err := executor.executeOnDaemon(preemptedAfter(20*time.Millisecond), waitRequest(), start)
	require.NoError(t, err)

	assert.False(t, res.Success)
	assert.Less(t, time.Since(start), defaultPreemptReplyGrace, "must not wait out a grace period for a stop that never left")
}

// A call with no preemption attached is untouched: no stop is pushed, and it
// runs to its own answer.
func TestExecuteOnDaemon_NoPreemptionNoStop(t *testing.T) {
	router := newPreemptRouter()
	executor := &RemoteExecutor{router: router}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := executor.executeOnDaemon(ctx, waitRequest(), time.Now())
	require.NoError(t, err)

	assert.NotContains(t, router.reasons(), PreemptedCancelReason)
}

// In this process, preemption is delivered as cancellation: a preemptable tool
// answers its context ending with a real result.
func TestCancelOnPreemption(t *testing.T) {
	t.Run("cancels when preempted", func(t *testing.T) {
		ctx, release := cancelOnPreemption(preemptedAfter(10 * time.Millisecond))
		defer release()
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("preemption did not cancel the call")
		}
	})

	t.Run("leaves an unpreemptable call alone", func(t *testing.T) {
		base := context.Background()
		ctx, release := cancelOnPreemption(base)
		defer release()
		assert.Equal(t, base, ctx)
	})
}
