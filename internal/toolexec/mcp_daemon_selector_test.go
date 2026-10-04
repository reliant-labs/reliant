package toolexec

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/require"
)

// selectorRecordingRouter records which daemon each generic command was sent to.
type selectorRecordingRouter struct {
	routerStub
	defaultCommands []string
	pinnedCommands  map[string][]string
}

const mcpCallResponse = `{"result":{"content":[{"type":"text","text":"ok"}]}}`

func (r *selectorRecordingRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, _ []byte, _ int32) ([]byte, error) {
	r.defaultCommands = append(r.defaultCommands, commandType)
	return []byte(mcpCallResponse), nil
}

func (r *selectorRecordingRouter) SendDaemonCommandToDaemon(_ context.Context, _ string, daemonID, commandType string, _ []byte, _ int32) ([]byte, error) {
	if r.pinnedCommands == nil {
		r.pinnedCommands = map[string][]string{}
	}
	r.pinnedCommands[daemonID] = append(r.pinnedCommands[daemonID], commandType)
	return []byte(mcpCallResponse), nil
}

func bindMCPRuntime(t *testing.T, router DaemonRouter, selector *DaemonSelector) *rctx.ToolContext {
	t.Helper()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "user-1")
	ctx = WithDaemonSelector(ctx, selector)
	bound := NewDaemonMCPContextBinder(router).Bind(rctx.NewToolContext(ctx, "chat", "thread", nil, nil))
	require.NotNil(t, bound.MCP)
	return bound
}

func TestDaemonMCPRuntime_HonoursRunDaemonSelector(t *testing.T) {
	router := &selectorRecordingRouter{}
	bound := bindMCPRuntime(t, router, &DaemonSelector{ID: "daemon-B"})

	_, err := bound.MCP.CallTool("session", "server", "tool", nil)
	require.NoError(t, err)

	require.Equal(t, []string{"mcp.call_tool"}, router.pinnedCommands["daemon-B"])
	require.Empty(t, router.defaultCommands, "an MCP call must not fall back to the default daemon")
}

func TestDaemonMCPRuntime_NilSelectorUsesDefaultLikeBuiltins(t *testing.T) {
	router := &selectorRecordingRouter{}
	bound := bindMCPRuntime(t, router, nil)

	_, err := bound.MCP.CallTool("session", "server", "tool", nil)
	require.NoError(t, err)

	require.Equal(t, []string{"mcp.call_tool"}, router.defaultCommands)
	require.Empty(t, router.pinnedCommands)
}

type contextCapturingBinder struct{ selector *DaemonSelector }

func (b *contextCapturingBinder) Bind(toolCtx *rctx.ToolContext) *rctx.ToolContext {
	b.selector = DaemonSelectorFromContext(toolCtx.Context)
	return toolCtx
}

func TestRemoteExecutor_PropagatesDaemonSelectorToServerTools(t *testing.T) {
	binder := &contextCapturingBinder{}
	local := NewLocalToolExecutor(nil)
	local.SetMCPContextBinder(binder)
	exec := NewRemoteExecutor(&routerStub{})
	exec.SetServerExecutor(local)

	_, err := exec.ExecuteTool(context.Background(), &ToolRequest{
		ToolName: "mcp__server__tool", ToolInput: `{}`, ToolCallID: "c1",
		UserID: "user-1", ChatID: "chat", ProjectID: "proj",
		DaemonSelector: &DaemonSelector{ID: "daemon-B"},
	})
	require.NoError(t, err)
	require.NotNil(t, binder.selector)
	require.Equal(t, "daemon-B", binder.selector.ID)
}
