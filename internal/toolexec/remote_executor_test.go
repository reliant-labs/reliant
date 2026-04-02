package toolexec

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type routerStub struct{}

func (r *routerStub) IsDaemonOnline(_ context.Context, userID string) (bool, error) { return true, nil }
func (r *routerStub) SendToolRequest(ctx context.Context, userID string, request *ToolExecutionRequest) error {
	return nil
}
func (r *routerStub) SendToolExecutionCancel(_ context.Context, userID, requestID, reason string) error { return nil }
func (r *routerStub) SendKillProcess(ctx context.Context, userID, processID string) error {
	return nil
}
func (r *routerStub) SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return nil, nil
}
func (r *routerStub) SendToolRequestSync(ctx context.Context, userID string, request *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	return &ToolExecutionResponse{Success: true}, nil
}
func (r *routerStub) SendLoadProjectConfigs(_ context.Context, userID string, projectPath string, requestID string) error {
	return nil
}
func (r *routerStub) SendWatchProjectConfigs(_ context.Context, userID string, projectPath string, includeInitial bool) error {
	return nil
}
func (r *routerStub) SendTerminalInput(_ context.Context, _, _ string, _ []byte) error {
	return nil
}
func (r *routerStub) SendTerminalResize(_ context.Context, _, _ string, _, _ uint32) error {
	return nil
}
func (r *routerStub) SubscribeTerminalOutput(_ context.Context, _, _ string) (<-chan *TerminalOutputEvent, func(), error) {
	ch := make(chan *TerminalOutputEvent)
	return ch, func() { close(ch) }, nil
}
func (r *routerStub) SubscribeProcessOutput(_ context.Context, _, _ string, _ bool) (<-chan *ProcessOutputEvent, func(), error) {
	ch := make(chan *ProcessOutputEvent)
	return ch, func() { close(ch) }, nil
}
func (r *routerStub) Close() error { return nil }

func TestNewRemoteExecutor_AllowsUnconfiguredRouterUntilBound(t *testing.T) {
	exec := NewRemoteExecutor(nil)
	require.NotNil(t, exec)
	assert.Nil(t, exec.router)
}

func TestRemoteExecutor_SetDaemonRouterPanicsWhenNil(t *testing.T) {
	exec := NewRemoteExecutor(&routerStub{})
	custom := &routerStub{}
	exec.SetDaemonRouter(custom)
	require.Same(t, custom, exec.router)

	require.Panics(t, func() {
		exec.SetDaemonRouter(nil)
	})
}