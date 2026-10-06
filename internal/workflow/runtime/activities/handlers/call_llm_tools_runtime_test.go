// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	dbcore "github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/toolexec"
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
	driverResolver := func(ctx context.Context, userID string, prefs models.Preferences, opts ...llm.DriverOption) (llm.Driver, error) {
		return mockDriver, nil
	}

	activityInstance := NewCallLLMActivity(
		h.Repo(),
		nil,
		tools.NewToolsFactory(&tools.ToolsOptions{Repo: h.Repo()}),
		&staticConfigProvider{},
		driverResolver,
		nil,
	)

	tests := []struct {
		name                 string
		toolFilter           []string
		noToolsConfig        bool
		expectContainsTool   string
		expectExactlyOneTool string
		expectNoTools        bool
	}{
		{
			name:               "default/preset tools available",
			toolFilter:         []string{"tag:coding:default"},
			expectContainsTool: "view",
		},
		{
			name: "empty tools override does not wipe tools",
			// Runtime receives preset/default tool filter after upstream input merge.
			toolFilter:         []string{"tag:coding:default"},
			expectContainsTool: "view",
		},
		{
			name:                 "non-empty tools override is honored",
			toolFilter:           []string{"view"},
			expectExactlyOneTool: "view",
		},
		{
			name:                 "component library explicit tool is available",
			toolFilter:           []string{"component_library"},
			expectExactlyOneTool: "component_library",
		},
		{
			name:          "no tools_config disables tools",
			noToolsConfig: true,
			expectNoTools: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			callLLMArgs := &reliantv1.CallLLMArgs{
				Model: &reliantv1.CelModelSelector{
					Value: &reliantv1.CelModelSelector_Literal{
						Literal: &reliantv1.ModelSelector{Id: "mock-model"},
					},
				},
			}
			if !tc.noToolsConfig {
				callLLMArgs.ToolsConfig = &reliantv1.ToolsConfig{
					PreloadedTools: celStringListLiteral(tc.toolFilter),
				}
			}
			input := ActivityInput{
				Runtime: RuntimeContext{
					ChatID: chat.ID,
					Thread: chat.ID,
				},
				Node: &reliantv1.Node{
					Type: "call_llm",
					Args: &reliantv1.Node_CallLlm{
						CallLlm: callLLMArgs,
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
				// The declared tool is the WHOLE set. load_tool used to ride
				// along with every tool-enabled agent, so this once expected it
				// here too; it is now offered only when a node declares
				// loadable_tools for it to reach, and these cases declare none.
				// A discovery tool with nothing to discover is schema the model
				// must read and can never use.
				assert.ElementsMatch(t,
					[]string{tc.expectExactlyOneTool},
					mockDriver.capturedTools)
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
	driverResolver := func(ctx context.Context, userID string, prefs models.Preferences, opts ...llm.DriverOption) (llm.Driver, error) {
		return mockDriver, nil
	}

	activityInstance := NewCallLLMActivity(
		h.Repo(),
		nil,
		tools.NewToolsFactory(&tools.ToolsOptions{Repo: h.Repo()}),
		&staticConfigProvider{},
		driverResolver,
		nil,
	)

	// With ToolsConfig, filter and spawn are separate fields.
	// filter contains tag-based tools, spawn contains spawn entries.
	resolvedFilter := []string{"tag:coding:default"}
	resolvedSpawn := []string{"spawn:builtin://agent(general,researcher)"}

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
					ToolsConfig: &reliantv1.ToolsConfig{
						PreloadedTools: celStringListLiteral(resolvedFilter),
						Spawn:          celStringListLiteral(resolvedSpawn),
					},
				},
			},
		},
	}

	var output CallLLMOutput
	err := h.ExecuteActivity(activityInstance.Execute, input, &output)
	require.NoError(t, err)

	require.NotEmpty(t, mockDriver.capturedTools, "expected resolved runtime tools to be non-empty")
	assert.Contains(t, mockDriver.capturedTools, "view")
	assert.Contains(t, mockDriver.capturedTools, "spawn")
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
	driverResolver := func(ctx context.Context, userID string, prefs models.Preferences, opts ...llm.DriverOption) (llm.Driver, error) {
		return mockDriver, nil
	}

	activityInstance := NewCallLLMActivity(
		h.Repo(),
		nil,
		tools.NewToolsFactory(&tools.ToolsOptions{Repo: h.Repo()}),
		&staticConfigProvider{},
		driverResolver,
		nil,
	)

	resolvedToolFilter := []string{"tag:coding:default", "spawn:builtin://agent(general,researcher)"}
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
					ToolsConfig: &reliantv1.ToolsConfig{
						PreloadedTools: celStringListLiteral(resolvedToolFilter),
					},
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
	driverResolver := func(ctx context.Context, userID string, prefs models.Preferences, opts ...llm.DriverOption) (llm.Driver, error) {
		return mockDriver, nil
	}

	mcpManager := mcp.NewManager(mcp.RoleDaemon)
	defer func() {
		_ = mcpManager.Close()
	}()
	resolver := &recordingProjectResolver{}
	mcpManager.SetProjectConfigResolver(resolver.ResolveProjectConfig)

	activityInstance := NewCallLLMActivity(
		h.Repo(),
		nil,
		tools.NewToolsFactory(&tools.ToolsOptions{Repo: h.Repo()}),
		resolver,
		driverResolver,
		toolexec.NewLocalMCPContextBinder(mcpManager),
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
					ToolsConfig: &reliantv1.ToolsConfig{
						// A server that can never be registered. chrome-devtools is a
						// BUILTIN, connected on any machine with Chrome on disk
						// (GitHub's ubuntu runners have it), so naming its tool made
						// this test pass or fail depending on the host.
						PreloadedTools: celStringListLiteral([]string{"mcp__never-registered-server__some_tool"}),
					},
				},
			},
		},
	}

	var output CallLLMOutput
	err := h.ExecuteActivity(activityInstance.Execute, input, &output)
	require.NoError(t, err)
	require.Equal(t, worktreePath, resolver.lastProjectPath)
	// The subject here is the path MCP enumeration is scoped to, asserted
	// above. The tool list is incidental and empty: the only name declared is
	// an MCP tool from a server this chat has not connected, so it expands to
	// nothing — and nothing rides along any more, since load_tool is offered
	// only where a node declared loadable_tools for it to reach.
	assert.Empty(t, mockDriver.capturedTools)
}

// spawn_send rides along only where there is a counterpart to message: a
// sub-agent replying to the parent that spawned it, or an orchestrator actually
// configured to spawn children. A plain root agent has neither, and handing it a
// mailbox tool it can never use costs schema on every single request.
func TestCallLLMActivity_SpawnToolsOfferedOnlyWhenReachable(t *testing.T) {
	// spawn_stop rides the same grant but on the STRICTLY TIGHTER condition:
	// it refuses anything that is not the caller's own direct child, so an
	// agent that cannot spawn has nothing it could ever legally name. The
	// depth-1 row is what separates the two — a sub-agent that cannot spawn has
	// a parent to report to and no children to stop.
	tests := []struct {
		name          string
		spawnDepth    int
		spawnEntries  []string
		wantSpawnSend bool
		wantSpawnStop bool
	}{
		{
			name:          "plain root agent has nobody to message",
			spawnDepth:    0,
			wantSpawnSend: false,
			wantSpawnStop: false,
		},
		{
			name:          "orchestrator configured to spawn can message and stop its children",
			spawnDepth:    0,
			spawnEntries:  []string{"spawn:builtin://agent(general)"},
			wantSpawnSend: true,
			wantSpawnStop: true,
		},
		{
			// maxSpawnDepth is 1, so a sub-agent cannot spawn at all: its
			// spawn entries are dropped before reachability is decided. It
			// therefore has a parent to report to and, by construction, no
			// children of its own to stop. The entries are declared here
			// anyway, so this row pins the DEPTH cap rather than an absent
			// config — if the cap ever rises, spawn_stop follows
			// canSpawnChildren and this expectation flips with it.
			name:          "sub-agent cannot spawn at max depth, so has no children to stop",
			spawnDepth:    1,
			spawnEntries:  []string{"spawn:builtin://agent(general)"},
			wantSpawnSend: true,
			wantSpawnStop: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewIdempotencyTestHelper(t)
			defer h.Cleanup()

			ctx := context.Background()
			project := h.CreateTestProject(ctx, "project-mailbox", "user-mailbox")
			chat := h.CreateTestChat(ctx, "chat-mailbox", project.ID, project.UserID)
			h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

			mockDriver := &toolCaptureMockDriver{}
			driverResolver := func(ctx context.Context, userID string, prefs models.Preferences, opts ...llm.DriverOption) (llm.Driver, error) {
				return mockDriver, nil
			}

			activityInstance := NewCallLLMActivity(
				h.Repo(),
				nil,
				tools.NewToolsFactory(&tools.ToolsOptions{Repo: h.Repo()}),
				&staticConfigProvider{},
				driverResolver,
				nil,
			)

			toolsConfig := &reliantv1.ToolsConfig{
				PreloadedTools: celStringListLiteral([]string{"view"}),
			}
			if len(tc.spawnEntries) > 0 {
				toolsConfig.Spawn = celStringListLiteral(tc.spawnEntries)
			}

			input := ActivityInput{
				Runtime: RuntimeContext{
					ChatID:     chat.ID,
					Thread:     chat.ID,
					SpawnDepth: tc.spawnDepth,
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
							ToolsConfig: toolsConfig,
						},
					},
				},
			}

			var output CallLLMOutput
			err := h.ExecuteActivity(activityInstance.Execute, input, &output)
			require.NoError(t, err)

			if tc.wantSpawnSend {
				assert.Contains(t, mockDriver.capturedTools, tools.ToolSpawnSend)
			} else {
				assert.NotContains(t, mockDriver.capturedTools, tools.ToolSpawnSend)
			}
			if tc.wantSpawnStop {
				assert.Contains(t, mockDriver.capturedTools, tools.ToolSpawnStop)
			} else {
				assert.NotContains(t, mockDriver.capturedTools, tools.ToolSpawnStop)
			}
		})
	}
}

// An agent that has actually spawned gets the tools for managing what it
// spawned, without having to load them by hand. spawn_status used to be
// deferred: an orchestrator had to load_tool it after every fan-out before it
// could check on a single child (seen on every roofers-2026-10-05 run).
//
// "Has spawned" is read from durable state — a spawn tool_calls row that
// started a child — not from an in-process flag, so it survives a worker
// restart and holds for an agent whose spawn config has since gone away.
// Before the first spawn, spawn_status stays off the request: it costs schema
// on every turn of an agent that may never fan out.
func TestCallLLMActivity_SpawnManagementToolsOfferedOnceThreadHasSpawned(t *testing.T) {
	tests := []struct {
		name          string
		spawnEntries  []string
		seedSpawn     bool
		seedInherited bool
		want          []string
		wantAbsent    []string
	}{
		{
			name:         "orchestrator before its first spawn is not handed spawn_status yet",
			spawnEntries: []string{"spawn:builtin://agent(general)"},
			wantAbsent:   []string{tools.ToolSpawnStatus},
		},
		{
			name:         "orchestrator that has spawned gets status, send and stop",
			spawnEntries: []string{"spawn:builtin://agent(general)"},
			seedSpawn:    true,
			want:         []string{tools.ToolSpawnStatus, tools.ToolSpawnSend, tools.ToolSpawnStop},
		},
		{
			// The spawn grant can disappear between turns (a workflow or
			// preset change); the children it already started have not.
			name:      "agent with children but no spawn config still manages them",
			seedSpawn: true,
			want:      []string{tools.ToolSpawnStatus, tools.ToolSpawnSend, tools.ToolSpawnStop},
		},
		{
			// A branch may look at the sub-agents it inherited, never
			// control them: the original conversation still owns them.
			name:          "branch that inherited sub-agents gets spawn_status only",
			seedInherited: true,
			want:          []string{tools.ToolSpawnStatus},
			wantAbsent:    []string{tools.ToolSpawnSend, tools.ToolSpawnStop},
		},
	}

	seedSpawnedChild := func(t *testing.T, repo db.Repository, ctx context.Context, chatID, parentThread string) {
		t.Helper()
		now := time.Now()
		childThread := "child-" + uuid.New().String()
		_, err := repo.CreateThread(ctx, &db.Thread{
			ID: childThread, ChatID: chatID, ParentThreadID: &parentThread,
			Origin: db.ThreadOriginSpawn, Status: db.ThreadStatusRunning, CreatedAt: now,
		})
		require.NoError(t, err)
		require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
			ID: childThread, ChatID: chatID, WorkflowName: "builtin://agent",
			Thread: childThread, Status: db.Active(), CreatedAt: now,
		}))
		require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
			ID: "toolu_" + uuid.New().String(), ChatID: chatID, ThreadID: &parentThread,
			ToolName: "spawn", Status: dbcore.ToolCallStatusBackgrounded, ChildWorkflowID: &childThread,
			RequestedAt: now, CreatedAt: now, UpdatedAt: now,
		}))
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewIdempotencyTestHelper(t)
			defer h.Cleanup()

			ctx := context.Background()
			repo := h.Repo()
			project := h.CreateTestProject(ctx, "project-spawned", "user-spawned")
			var chat *db.Chat
			if tc.seedInherited {
				// The original spawns, then is branched after that spawn.
				source := h.CreateTestChat(ctx, "chat-source", project.ID, project.UserID)
				seedSpawnedChild(t, repo, ctx, source.ID, source.ID)
				h.CreateTestUserMessage(ctx, source.ID, source.ID)
				forkPoint, err := repo.GetLatestMessageInThread(ctx, source.ID)
				require.NoError(t, err)

				chat = &db.Chat{ID: "chat-branch", ProjectID: project.ID, UserID: project.UserID}
				require.NoError(t, repo.CreateChat(ctx, chat))
				sourceThread := source.ID
				_, err = repo.CreateThread(ctx, &db.Thread{
					ID: chat.ID, ChatID: chat.ID, ParentThreadID: &sourceThread,
					ForkAtMessageID: &forkPoint.ID, Origin: db.ThreadOriginFork,
				})
				require.NoError(t, err)
				parentCW := forkPoint.ContextWindowID
				_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{
					ID: chat.ID + ":" + chat.ID + ":0", ThreadID: chat.ID,
					ParentContextWindowID: &parentCW, ForkAtMessageID: &forkPoint.ID,
				})
				require.NoError(t, err)
			} else {
				chat = h.CreateTestChat(ctx, "chat-spawned", project.ID, project.UserID)
			}
			h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

			if tc.seedSpawn {
				seedSpawnedChild(t, repo, ctx, chat.ID, chat.ID)
			}

			mockDriver := &toolCaptureMockDriver{}
			driverResolver := func(ctx context.Context, userID string, prefs models.Preferences, opts ...llm.DriverOption) (llm.Driver, error) {
				return mockDriver, nil
			}
			activityInstance := NewCallLLMActivity(
				h.Repo(),
				nil,
				tools.NewToolsFactory(&tools.ToolsOptions{Repo: h.Repo()}),
				&staticConfigProvider{},
				driverResolver,
				nil,
			)

			toolsConfig := &reliantv1.ToolsConfig{
				PreloadedTools: celStringListLiteral([]string{"view"}),
			}
			if len(tc.spawnEntries) > 0 {
				toolsConfig.Spawn = celStringListLiteral(tc.spawnEntries)
			}
			input := ActivityInput{
				Runtime: RuntimeContext{ChatID: chat.ID, Thread: chat.ID},
				Node: &reliantv1.Node{
					Type: "call_llm",
					Args: &reliantv1.Node_CallLlm{
						CallLlm: &reliantv1.CallLLMArgs{
							Model: &reliantv1.CelModelSelector{
								Value: &reliantv1.CelModelSelector_Literal{
									Literal: &reliantv1.ModelSelector{Id: "mock-model"},
								},
							},
							ToolsConfig: toolsConfig,
						},
					},
				},
			}

			var output CallLLMOutput
			require.NoError(t, h.ExecuteActivity(activityInstance.Execute, input, &output))
			for _, name := range tc.want {
				assert.Contains(t, mockDriver.capturedTools, name)
			}
			for _, name := range tc.wantAbsent {
				assert.NotContains(t, mockDriver.capturedTools, name)
			}
		})
	}
}
