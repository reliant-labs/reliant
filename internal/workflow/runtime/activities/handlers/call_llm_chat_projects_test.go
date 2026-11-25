// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/threads"
)

// requestShapeDriver records the prompts and each tool's schema, as the
// drivers send them.
type requestShapeDriver struct {
	toolCaptureMockDriver
	prompts []string
	schemas map[string]string
}

func (d *requestShapeDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, available []tools.Tool) <-chan llm.DriverEvent {
	d.prompts = append([]string(nil), prompts...)
	d.schemas = map[string]string{}
	for _, tool := range available {
		raw, _ := json.Marshal(tool.ParamSchema())
		d.schemas[tool.Name()] = string(raw)
	}
	return d.toolCaptureMockDriver.StreamResponse(ctx, prompts, messages, available)
}

// One call_llm turn of a chat, with the file and planning tools; wired reads
// the chat's attached projects.
func runChatProjectsTurn(t *testing.T, h *IdempotencyTestHelper, chatID string, wired bool) *requestShapeDriver {
	t.Helper()
	driver := &requestShapeDriver{}
	act := NewCallLLMActivity(CallLLMDeps{Mailbox: store.NewAgentMessageExtStore(h.Repo().ORM()), ToolResults: toolCallsOf(h.Repo()), Store: h.Repo(),
		Threads: threads.New(threads.Deps{Repo: h.Repo(), Messages: store.NewMessageExtStore(h.Repo().ORM()), Senders: store.NewThreadExtStore(h.Repo().ORM())}),
		Spawns:  store.NewToolCallExtStore(h.Repo().ORM()), ToolsFactory: tools.NewToolsFactory(&tools.ToolsOptions{Repo: h.Repo()}),
		ConfigProvider: &staticConfigProvider{},
		DriverResolver: func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
			return driver, nil
		},
	})
	if wired {
		act = act.WithChatProjects(h.Repo())
	}
	input := ActivityInput{
		Runtime: RuntimeContext{ChatID: chatID, Thread: chatID},
		Node: &reliantv1.Node{Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
			Model:       &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Literal{Literal: &reliantv1.ModelSelector{Id: "mock-model"}}},
			ToolsConfig: &reliantv1.ToolsConfig{PreloadedTools: celStringListLiteral([]string{tools.ToolView, tools.ShellToolName, tools.ToolCreatePlan})},
		}}},
	}
	var output CallLLMOutput
	require.NoError(t, h.ExecuteActivity(act.Execute, input, &output))
	require.NotEmpty(t, driver.schemas)
	return driver
}

func TestCallLLM_ChatProjectsShapeTheToolsAndPromptOnlyWithMoreThanOne(t *testing.T) {
	t.Parallel()
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	user := uuid.NewString()
	project := h.CreateTestProjectWithPath(ctx, uuid.NewString(), user, t.TempDir())
	chat := h.CreateTestChat(ctx, uuid.NewString(), project.ID, user)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	// One project: the request is exactly the one an unwired activity sends.
	before := runChatProjectsTurn(t, h, chat.ID, false)
	single := runChatProjectsTurn(t, h, chat.ID, true)
	assert.Equal(t, before.prompts, single.prompts, "a single-project chat's prompt is unchanged (it is cached)")
	assert.Equal(t, before.schemas, single.schemas, "and so are its tool schemas")
	assert.NotContains(t, single.schemas[tools.ToolView], `"project"`)

	apiDir := t.TempDir()
	api := &db.Project{ID: uuid.NewString(), UserID: user, Name: "api", Path: apiDir}
	require.NoError(t, h.Repo().CreateProject(ctx, api))
	installTestCheckout(t, h.Repo(), api.ID, user, apiDir)
	machine := testMachine
	require.NoError(t, h.Repo().AttachChatProject(ctx, &core.ChatProject{ChatID: chat.ID, ProjectID: api.ID, DaemonID: &machine, CreatedAt: time.Now()}))

	multi := runChatProjectsTurn(t, h, chat.ID, true)
	for _, name := range []string{tools.ToolView, tools.ShellToolName} {
		var schema struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		require.NoError(t, json.Unmarshal([]byte(multi.schemas[name]), &schema), name)
		require.Contains(t, schema.Properties, tools.ProjectParam, name)
		assert.Equal(t, []string{"Test Project", "api"}, schema.Properties[tools.ProjectParam].Enum, name)
		assert.NotContains(t, schema.Required, tools.ProjectParam, name)
	}
	assert.Equal(t, single.schemas[tools.ToolCreatePlan], multi.schemas[tools.ToolCreatePlan], "a tool without repo is unchanged")
	assert.Equal(t, single.prompts[0], multi.prompts[0], "the cached first block is unchanged")

	note := strings.Join(multi.prompts, "\n")
	assert.Contains(t, note, "This chat works across several projects")
	assert.Contains(t, note, "- Test Project (the primary)")
	assert.Contains(t, note, "- api: "+apiDir)
}
