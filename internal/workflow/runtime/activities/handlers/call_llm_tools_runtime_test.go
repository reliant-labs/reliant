// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/config"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/ptr"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	wfmodel "github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type toolCaptureMockDriver struct {
	capturedTools []string
}

func (m *toolCaptureMockDriver) Name() string {
	return "tool-capture-mock"
}

func (m *toolCaptureMockDriver) Model() models.Model {
	return models.Model{ID: "mock-model", Name: "Mock Model"}
}

func (m *toolCaptureMockDriver) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) (*llm.DriverResponse, error) {
	return &llm.DriverResponse{
		Content:      "mock",
		FinishReason: "end_turn",
		Usage:        llm.TokenUsage{TokenCount: 1},
	}, nil
}

func (m *toolCaptureMockDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, availableTools []tools.Tool) <-chan llm.DriverEvent {
	m.capturedTools = m.capturedTools[:0]
	for _, tool := range availableTools {
		m.capturedTools = append(m.capturedTools, tool.Name())
	}

	ch := make(chan llm.DriverEvent, 1)
	ch <- llm.DriverEvent{
		Type: llm.EventComplete,
		Response: &llm.DriverResponse{
			Content:      "Done",
			FinishReason: "end_turn",
			Usage:        llm.TokenUsage{TokenCount: 12},
		},
	}
	close(ch)
	return ch
}

func (m *toolCaptureMockDriver) ValidateKey(ctx context.Context) error {
	return nil
}

type staticConfigProvider struct{}

func (p *staticConfigProvider) GetProjectConfig(ctx context.Context, ref config.ProjectRef) (*config.Config, error) {
	return &config.Config{}, nil
}

func TestCallLLMActivity_ToolParametersReachMockDriver(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-tools", "user-tools")
	chat := h.CreateTestChat(ctx, "chat-tools", project.ID, project.UserID)

	// Insert a user message so the LLM call has non-empty message history
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	mockDriver := &toolCaptureMockDriver{}
	originalOverride := drivers.Override
	drivers.Override = mockDriver
	defer func() {
		drivers.Override = originalOverride
	}()

	activityInstance := NewCallLLMActivity(
		h.Repo(),
		nil,
		tools.NewToolsFactory(&tools.ToolsOptions{Repo: h.Repo()}),
		&staticConfigProvider{},
	)

	tests := []struct {
		name                 string
		toolFilter           []string
		toolsEnabled         *bool
		expectContainsTool   string
		expectExactlyOneTool string
		expectNoTools        bool
	}{
		{
			name:               "default/preset tools available",
			toolFilter:         []string{"tag:default"},
			expectContainsTool: "view",
		},
		{
			name: "empty tools override does not wipe tools",
			// Runtime receives preset/default tool filter after upstream input merge.
			toolFilter:         []string{"tag:default"},
			expectContainsTool: "view",
		},
		{
			name:                 "non-empty tools override is honored",
			toolFilter:           []string{"view"},
			expectExactlyOneTool: "view",
		},
		{
			name:          "tools=false on call_llm disables tools",
			toolFilter:    []string{"tag:default"},
			toolsEnabled:  ptr.Of(false),
			expectNoTools: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := ActivityInput{
				Runtime: RuntimeContext{
					ChatID: chat.ID,
					Thread: chat.ID,
				},
				Node: &reliantv1.Node{
					Type: "call_llm",
					Args: &reliantv1.Node_CallLlm{
						CallLlm: &reliantv1.CallLLMArgs{
							Model: &reliantv1.CelModelSelector{
								Value: &reliantv1.CelModelSelector_Literal{
									Literal: &reliantv1.ModelSelector{Id: "mock-model"},
								},
							},
							ToolFilter: celStringListLiteral(tc.toolFilter),
							Tools:      celBoolLiteral(tc.toolsEnabled),
						},
					},
				},
			}

			var output CallLLMOutput
			err := h.ExecuteActivity(activityInstance.Execute, input, &output)
			require.NoError(t, err)

			if tc.expectNoTools {
				assert.Empty(t, mockDriver.capturedTools)
				return
			}

			if tc.expectExactlyOneTool != "" {
				require.Len(t, mockDriver.capturedTools, 1)
				assert.Equal(t, tc.expectExactlyOneTool, mockDriver.capturedTools[0])
				return
			}

			require.NotEmpty(t, mockDriver.capturedTools)
			assert.Contains(t, mockDriver.capturedTools, tc.expectContainsTool)
		})
	}
}

func TestCallLLMActivity_CreateChatStylePayloadToolFilterCELEvaluation(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-tools-cel", "user-tools-cel")
	chat := h.CreateTestChat(ctx, "chat-tools-cel", project.ID, project.UserID)

	// Insert a user message so the LLM call has non-empty message history
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	mockDriver := &toolCaptureMockDriver{}
	originalOverride := drivers.Override
	drivers.Override = mockDriver
	defer func() {
		drivers.Override = originalOverride
	}()

	activityInstance := NewCallLLMActivity(
		h.Repo(),
		nil,
		tools.NewToolsFactory(&tools.ToolsOptions{Repo: h.Repo()}),
		&staticConfigProvider{},
	)

	payloadInputs := map[string]interface{}{
		"mode":          "auto",
		"tools":         []string{"tag:default"},
		"spawn_presets": []string{"general", "researcher"},
	}

	toolFilterExpression := "{{inputs.tools + [spawn(workflow.name, inputs.spawn_presets)]}}"
	evaluatedFilterRaw, err := wfcel.EvaluateTemplate(toolFilterExpression, &wfcel.NodeResolutionContext{
		Inputs: payloadInputs,
		Workflow: &wfmodel.WorkflowContext{
			Name: "builtin://agent",
		},
	})
	require.NoError(t, err)

	evaluatedFilter, ok := evaluatedFilterRaw.([]interface{})
	require.True(t, ok, "expected []interface{} tool filter, got %T", evaluatedFilterRaw)

	resolvedToolFilter := interfaceSliceToStringSlice(evaluatedFilter)
	require.Equal(t,
		[]string{"tag:default", "spawn:builtin://agent(general,researcher)"},
		resolvedToolFilter,
	)

	input := ActivityInput{
		Runtime: RuntimeContext{
			ChatID: chat.ID,
			Thread: chat.ID,
		},
		Node: &reliantv1.Node{
			Type: "call_llm",
			Args: &reliantv1.Node_CallLlm{
				CallLlm: &reliantv1.CallLLMArgs{
					Model: &reliantv1.CelModelSelector{
						Value: &reliantv1.CelModelSelector_Literal{
							Literal: &reliantv1.ModelSelector{Id: "mock-model"},
						},
					},
					ToolFilter: celStringListLiteral(resolvedToolFilter),
				},
			},
		},
	}

	var output CallLLMOutput
	err = h.ExecuteActivity(activityInstance.Execute, input, &output)
	require.NoError(t, err)

	require.NotEmpty(t, mockDriver.capturedTools, "expected resolved runtime tools to be non-empty")
	assert.Contains(t, mockDriver.capturedTools, "view")
	assert.Contains(t, mockDriver.capturedTools, "spawn")
}

func interfaceSliceToStringSlice(values []interface{}) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		stringValue, ok := value.(string)
		if !ok {
			continue
		}
		result = append(result, stringValue)
	}
	return result
}

func TestCallLLMActivity_ResolvedToolFilterContainsNoTemplates(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-tools-no-template", "user-tools-no-template")
	chat := h.CreateTestChat(ctx, "chat-tools-no-template", project.ID, project.UserID)

	// Insert a user message so the LLM call has non-empty message history
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	mockDriver := &toolCaptureMockDriver{}
	originalOverride := drivers.Override
	drivers.Override = mockDriver
	defer func() {
		drivers.Override = originalOverride
	}()

	activityInstance := NewCallLLMActivity(
		h.Repo(),
		nil,
		tools.NewToolsFactory(&tools.ToolsOptions{Repo: h.Repo()}),
		&staticConfigProvider{},
	)

	resolvedToolFilter := []string{"tag:default", "spawn:builtin://agent(general,researcher)"}
	for _, filter := range resolvedToolFilter {
		require.NotContains(t, filter, "{{")
		require.NotContains(t, filter, "}}")
	}

	input := ActivityInput{
		Runtime: RuntimeContext{
			ChatID: chat.ID,
			Thread: chat.ID,
		},
		Node: &reliantv1.Node{
			Type: "call_llm",
			Args: &reliantv1.Node_CallLlm{
				CallLlm: &reliantv1.CallLLMArgs{
					Model: &reliantv1.CelModelSelector{
						Value: &reliantv1.CelModelSelector_Literal{
							Literal: &reliantv1.ModelSelector{Id: "mock-model"},
						},
					},
					ToolFilter: celStringListLiteral(resolvedToolFilter),
				},
			},
		},
	}

	var output CallLLMOutput
	err := h.ExecuteActivity(activityInstance.Execute, input, &output)
	require.NoError(t, err)
	require.NotEmpty(t, mockDriver.capturedTools)
}

func celStringListLiteral(values []string) *reliantv1.CelStringList {
	return &reliantv1.CelStringList{
		Value: &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{Values: values}},
	}
}

func celBoolLiteral(value *bool) *reliantv1.CelBool {
	if value == nil {
		return nil
	}
	return &reliantv1.CelBool{
		Value: &reliantv1.CelBool_Literal{Literal: *value},
	}
}

type recordingProjectResolver struct {
	lastProjectPath string
}

func (p *recordingProjectResolver) GetProjectConfig(ctx context.Context, ref config.ProjectRef) (*config.Config, error) {
	_ = ctx
	_ = ref
	return &config.Config{}, nil
}

func (p *recordingProjectResolver) ResolveProjectConfig(ctx context.Context, projectPath string) (*config.Config, error) {
	_ = ctx
	p.lastProjectPath = projectPath
	return &config.Config{}, nil
}

func TestCallLLMActivity_UsesWorkingDirForMCPEnumerationScope(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	projectPath := "/tmp/project-root"
	worktreePath := "/tmp/project-worktree"
	project := h.CreateTestProjectWithPath(ctx, "project-mcp-scope", "user-mcp-scope", projectPath)
	chat := h.CreateTestChat(ctx, "chat-mcp-scope", project.ID, project.UserID)
	h.CreateTestWorktree(ctx, project.ID, chat.ID, worktreePath)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	mockDriver := &toolCaptureMockDriver{}
	originalOverride := drivers.Override
	drivers.Override = mockDriver
	defer func() {
		drivers.Override = originalOverride
	}()

	mcpManager := mcp.NewManager()
	defer func() {
		_ = mcpManager.Close()
	}()
	resolver := &recordingProjectResolver{}
	mcpManager.SetProjectConfigResolver(resolver.ResolveProjectConfig)

	activityInstance := NewCallLLMActivity(
		h.Repo(),
		nil,
		tools.NewToolsFactory(&tools.ToolsOptions{Repo: h.Repo(), MCPManager: mcpManager}),
		resolver,
	)

	input := ActivityInput{
		Runtime: RuntimeContext{
			ChatID: chat.ID,
			Thread: chat.ID,
		},
		Node: &reliantv1.Node{
			Type: "call_llm",
			Args: &reliantv1.Node_CallLlm{
				CallLlm: &reliantv1.CallLLMArgs{
					Model: &reliantv1.CelModelSelector{
						Value: &reliantv1.CelModelSelector_Literal{
							Literal: &reliantv1.ModelSelector{Id: "mock-model"},
						},
					},
					ToolFilter: celStringListLiteral([]string{"mcp__chrome-devtools__new_page"}),
				},
			},
		},
	}

	var output CallLLMOutput
	err := h.ExecuteActivity(activityInstance.Execute, input, &output)
	require.NoError(t, err)
	require.Equal(t, worktreePath, resolver.lastProjectPath)
	assert.Empty(t, mockDriver.capturedTools)
}
