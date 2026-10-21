// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// toolsRecordingDriver keeps the tools array of every request it is handed.
type toolsRecordingDriver struct {
	mu       sync.Mutex
	requests [][]tools.Tool
}

func (d *toolsRecordingDriver) Name() string { return "tools-recorder" }
func (d *toolsRecordingDriver) Model() models.Model {
	return models.Model{ID: "mock-model", Name: "Mock Model"}
}
func (d *toolsRecordingDriver) ValidateKey(context.Context) error { return nil }
func (d *toolsRecordingDriver) SendMessages(context.Context, []string, []message.Message, []tools.Tool) (*llm.DriverResponse, error) {
	return &llm.DriverResponse{Content: "ok", FinishReason: "end_turn"}, nil
}

func (d *toolsRecordingDriver) StreamResponse(_ context.Context, _ []string, _ []message.Message, ts []tools.Tool) <-chan llm.DriverEvent {
	d.mu.Lock()
	d.requests = append(d.requests, append([]tools.Tool(nil), ts...))
	d.mu.Unlock()

	ch := make(chan llm.DriverEvent, 1)
	ch <- llm.DriverEvent{
		Type:     llm.EventComplete,
		Response: &llm.DriverResponse{Content: "ok", FinishReason: "end_turn", Usage: llm.TokenUsage{TokenCount: 10}},
	}
	close(ch)
	return ch
}

// scriptedDaemonRuntime is a daemon-backed MCP runtime whose listing the test
// switches between answering and "daemon is suspended".
type scriptedDaemonRuntime struct {
	mcp.Runtime
	state *scriptedDaemon
}

type scriptedDaemon struct {
	mu       sync.Mutex
	identity string
	servers  map[string][]mcp.Tool
	down     bool
}

func (s *scriptedDaemon) set(servers map[string][]mcp.Tool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.servers, s.down = servers, false
}

func (s *scriptedDaemon) suspend() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = true
}

func (r scriptedDaemonRuntime) MCPListIdentity() string { return r.state.identity }

func (r scriptedDaemonRuntime) EnsureProjectServersLoaded(context.Context, string) *mcp.ProjectServerLoadResult {
	return &mcp.ProjectServerLoadResult{}
}

func (r scriptedDaemonRuntime) ListProjectTools(string) (map[string][]mcp.Tool, error) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if r.state.down {
		return nil, errors.New("daemon is suspended; not waking it")
	}
	return r.state.servers, nil
}

func (r scriptedDaemonRuntime) ListAllTools() (map[string][]mcp.Tool, error) {
	return r.ListProjectTools("")
}

type scriptedDaemonBinder struct{ state *scriptedDaemon }

func (b scriptedDaemonBinder) Bind(toolCtx *rctx.ToolContext) *rctx.ToolContext {
	return toolCtx.WithMCP(scriptedDaemonRuntime{state: b.state})
}

func mcpServers(spec map[string][]string) map[string][]mcp.Tool {
	out := map[string][]mcp.Tool{}
	for server, names := range spec {
		for _, n := range names {
			out[server] = append(out[server], mcp.Tool{
				Name:        n,
				Description: "does " + n,
				InputSchema: map[string]interface{}{"type": "object"},
			})
		}
	}
	return out
}

func mcpToolsCallLLMInput(chatID string, preloaded []string) ActivityInput {
	in := callLLMInput(chatID, chatID, "mock-model")
	in.Node.GetCallLlm().ToolsConfig = &reliantv1.ToolsConfig{PreloadedTools: celStringListLiteral(preloaded)}
	return in
}

func requestToolNames(req []tools.Tool) []string {
	return toolNames(req)
}

// The tools array leads the provider's prompt-cache prefix, so an MCP tool that
// disappears for one request because the machine was suspended, and returns on
// the next, re-writes the whole conversation twice. A turn made while the
// daemon cannot list its MCP servers must carry the tools array of the turn
// before it, byte for byte; only a successful listing may change it.
func TestCallLLM_ToolsArrayIsStableAcrossAnUnreachableDaemon(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	userID := "user-" + uuid.NewString()
	project := h.CreateTestProjectWithPath(ctx, "project-"+uuid.NewString(), userID, "/tmp/project-mcp-stable")
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessageWithText(ctx, chat.ID, chat.ID, "use the tools")

	daemon := &scriptedDaemon{identity: "user=" + userID}
	daemon.set(mcpServers(map[string][]string{"github": {"create_issue", "list_prs"}, "chrome": {"new_page"}}))

	driver := &toolsRecordingDriver{}
	resolver := drivers.DriverResolver(func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return driver, nil
	})
	activity := NewCallLLMActivity(h.Repo(), nil, tools.NewToolsFactory(&tools.ToolsOptions{Repo: h.Repo()}),
		&recordingProjectResolver{}, resolver, scriptedDaemonBinder{state: daemon})

	input := mcpToolsCallLLMInput(chat.ID, []string{"view", "mcp__github__*", "mcp__chrome__*"})
	runTurn := func() {
		t.Helper()
		var out CallLLMOutput
		require.NoError(t, h.ExecuteActivity(activity.Execute, input, &out))
		saveTextOnlyAssistantMessage(t, h, chat.ID, chat.ID, "done")
		h.CreateTestUserMessageWithText(ctx, chat.ID, chat.ID, "continue")
	}

	runTurn() // 1: daemon up
	daemon.suspend()
	runTurn() // 2: daemon suspended, same chat
	daemon.set(mcpServers(map[string][]string{"github": {"create_issue", "list_prs"}, "chrome": {"new_page"}}))
	runTurn() // 3: daemon back, same listing
	daemon.set(mcpServers(map[string][]string{"github": {"create_issue"}, "chrome": {"new_page"}}))
	runTurn() // 4: daemon back with list_prs genuinely gone
	daemon.suspend()
	runTurn() // 5: suspended again, must keep the SHRUNKEN list

	require.Len(t, driver.requests, 5)
	hashes := make([]uint64, len(driver.requests))
	for i, req := range driver.requests {
		hashes[i] = hashTools(req)
	}

	require.Contains(t, requestToolNames(driver.requests[0]), "mcp__github__list_prs", "turn 1 must have the MCP tools or the test proves nothing")
	require.Equal(t, requestToolNames(driver.requests[0]), requestToolNames(driver.requests[1]), "an unreachable daemon dropped MCP tools from the request")
	require.Equal(t, hashes[0], hashes[1], "toolsHash moved across an unreachable-daemon turn")
	require.Equal(t, hashes[0], hashes[2])

	require.NotContains(t, requestToolNames(driver.requests[3]), "mcp__github__list_prs", "a successful listing that no longer has a tool must remove it")
	require.NotEqual(t, hashes[2], hashes[3])
	require.Equal(t, requestToolNames(driver.requests[3]), requestToolNames(driver.requests[4]))
	require.Equal(t, hashes[3], hashes[4], "the last successful listing, not an older one, is what a failure serves")
}
