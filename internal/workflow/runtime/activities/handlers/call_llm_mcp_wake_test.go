package handlers

import (
	"context"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/toolexec"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/stretchr/testify/require"
)

// daemonCommandRecorder stands in for the daemon-backed MCP runtime: every
// method that would send a daemon command records it, with the daemon the
// call was bound to.
type daemonCommandRecorder struct {
	mcp.Runtime
	commands  *[]string
	selectors *[]*toolexec.DaemonSelector
	selector  *toolexec.DaemonSelector
}

func (r daemonCommandRecorder) EnsureProjectServersLoaded(_ context.Context, _ string) *mcp.ProjectServerLoadResult {
	*r.commands = append(*r.commands, "mcp.ensure_loaded")
	*r.selectors = append(*r.selectors, r.selector)
	return &mcp.ProjectServerLoadResult{}
}

func (r daemonCommandRecorder) ListProjectTools(string) (map[string][]mcp.Tool, error) {
	*r.commands = append(*r.commands, "mcp.server_status")
	return map[string][]mcp.Tool{}, nil
}

func (r daemonCommandRecorder) ListAllTools() (map[string][]mcp.Tool, error) {
	*r.commands = append(*r.commands, "mcp.server_status")
	return map[string][]mcp.Tool{}, nil
}

type recordingBinder struct {
	commands  []string
	selectors []*toolexec.DaemonSelector
}

func (b *recordingBinder) Bind(toolCtx *rctx.ToolContext) *rctx.ToolContext {
	return toolCtx.WithMCP(daemonCommandRecorder{
		commands:  &b.commands,
		selectors: &b.selectors,
		selector:  toolexec.DaemonSelectorFromContext(toolCtx.Context),
	})
}

func runCallLLMWithFilter(t *testing.T, preloaded []string, rtxSelector *activitytypes.DaemonSelector) *recordingBinder {
	t.Helper()
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateTestProjectWithPath(ctx, "project-mcp-wake", "user-mcp-wake", "/tmp/project-wake")
	chat := h.CreateTestChat(ctx, "chat-mcp-wake", project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	mockDriver := &toolCaptureMockDriver{}
	driverResolver := func(ctx context.Context, userID string, prefs models.Preferences, opts ...llm.DriverOption) (llm.Driver, error) {
		return mockDriver, nil
	}
	binder := &recordingBinder{}
	activity := NewCallLLMActivity(
		h.Repo(), nil,
		tools.NewToolsFactory(&tools.ToolsOptions{Repo: h.Repo()}),
		&recordingProjectResolver{}, driverResolver, binder,
	)

	input := ActivityInput{
		Runtime: RuntimeContext{ChatID: chat.ID, Thread: chat.ID, DaemonSelector: rtxSelector},
		Node: &reliantv1.Node{
			Type: "call_llm",
			Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
				Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Literal{
					Literal: &reliantv1.ModelSelector{Id: "mock-model"},
				}},
				ToolsConfig: &reliantv1.ToolsConfig{PreloadedTools: celStringListLiteral(preloaded)},
			}},
		},
	}
	var output CallLLMOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, input, &output))
	return binder
}

func TestCallLLM_NodeWithoutMCPToolsSendsNoDaemonCommands(t *testing.T) {
	binder := runCallLLMWithFilter(t, []string{"view", "tag:file"}, nil)
	require.Empty(t, binder.commands, "a node that cannot reach mcp__ tools must not touch the daemon")
}

func TestCallLLM_NodeReachingMCPToolsEnsuresServersOnRunDaemon(t *testing.T) {
	binder := runCallLLMWithFilter(t, []string{"mcp__chrome-devtools__new_page"},
		&activitytypes.DaemonSelector{ID: "daemon-B"})
	require.Contains(t, binder.commands, "mcp.ensure_loaded")
	require.NotEmpty(t, binder.selectors)
	require.NotNil(t, binder.selectors[0], "discovery must target the run's daemon, not the default")
	require.Equal(t, "daemon-B", binder.selectors[0].ID)
}
