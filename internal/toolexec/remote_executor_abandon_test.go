package toolexec

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A tool the server stopped waiting for must be stopped on the daemon too.
//
// The daemon runs each execution under context.WithCancel(context.Background())
// (daemonruntime/runtime.go), decoupled from the transport on purpose so a
// flaky connection cannot kill a healthy command. The side effect is that
// cancelling the server's context reaches nothing: before this, a pause or
// interrupt marked the call cancelled while the user's `bash` ran to
// completion on their machine, burning the time they had just asked to stop.
//
// The daemon can stop it — cancelToolExecution cancels the registered context,
// which propagates to exec.CommandContext and signals the process group. It
// only has to be told, which is what the abandonment push does.
type cancelRecordingRouter struct {
	routerStub

	mu       sync.Mutex
	cancels  []string
	blockFor time.Duration
}

func (r *cancelRecordingRouter) SendToolExecutionCancel(_ context.Context, _, requestID, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancels = append(r.cancels, requestID)
	return nil
}

// SendToolRequestSync blocks until the caller's context dies, standing in for a
// long-running command (the `spawn_status(wait:true)` and `sleep` cases).
func (r *cancelRecordingRouter) SendToolRequestSync(ctx context.Context, _ string, _ *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(r.blockFor):
		return &ToolExecutionResponse{Success: true}, nil
	}
}

func (r *cancelRecordingRouter) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.cancels...)
}

func TestExecuteOnDaemon_AbandonedCallCancelsOnDaemon(t *testing.T) {
	router := &cancelRecordingRouter{blockFor: time.Minute}
	executor := &RemoteExecutor{router: router}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := executor.executeOnDaemon(ctx, &ToolRequest{
		UserID:     "user-1",
		ChatID:     "chat-1",
		ToolName:   "bash",
		ToolCallID: "toolu_abandoned",
	}, time.Now())
	require.NoError(t, err, "an abandoned call reports a result, it does not error out")

	assert.Equal(t, []string{"toolu_abandoned"}, router.recorded(),
		"the daemon must be told to stop the process the server stopped waiting for")
}

// The push is strictly for abandonment. A call that returned normally must not
// send one -- the execution is already over, and a stray cancel racing the next
// dispatch of the same tool call id would stop work nobody asked to stop.
func TestExecuteOnDaemon_CompletedCallDoesNotCancel(t *testing.T) {
	router := &cancelRecordingRouter{blockFor: time.Millisecond}
	executor := &RemoteExecutor{router: router}

	_, err := executor.executeOnDaemon(context.Background(), &ToolRequest{
		UserID:     "user-1",
		ChatID:     "chat-1",
		ToolName:   "bash",
		ToolCallID: "toolu_completed",
	}, time.Now())
	require.NoError(t, err)

	assert.Empty(t, router.recorded(),
		"a tool that finished on its own has nothing to cancel")
}

// A tool that SUCCEEDED must not be cancelled just because a sibling was.
//
// Every tool in a turn runs against one shared activity context
// (execute_tools.go), so cancelling one kills the context all of them observe.
// Gating the abandonment push on ambient `ctx.Err()` therefore fired for tools
// that had already returned successfully — a stray cancel against a tool call id
// a later dispatch may reuse, and, for a BACKGROUNDED tool, a kill of the very
// process group the user asked to keep running past the turn.
func TestExecuteOnDaemon_SiblingCancellationDoesNotCancelCompletedCall(t *testing.T) {
	router := &cancelRecordingRouter{blockFor: time.Millisecond}
	executor := &RemoteExecutor{router: router}

	// The call completes, and only THEN does the shared context die — exactly
	// what a sibling tool's cancellation looks like from here.
	ctx, cancel := context.WithCancel(context.Background())
	res, err := executor.executeOnDaemon(ctx, &ToolRequest{
		UserID:     "user-1",
		ChatID:     "chat-1",
		ToolName:   "bash",
		ToolCallID: "toolu_sibling_ok",
	}, time.Now())
	require.NoError(t, err)
	require.True(t, res.Success)
	cancel()

	assert.Empty(t, router.recorded(),
		"a tool that returned its own result was not abandoned, whatever happened to its siblings")
}
