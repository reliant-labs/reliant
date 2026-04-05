package handlers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/features"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/skills"
	skillcatalog "github.com/reliant-labs/reliant/internal/skills/catalog"
	skillscore "github.com/reliant-labs/reliant/internal/skills/core"
	skillmaterialize "github.com/reliant-labs/reliant/internal/skills/materialize"
	skillprompt "github.com/reliant-labs/reliant/internal/skills/prompt"
	"github.com/stretchr/testify/require"
)

func TestGetSystemPrompts_IncludesAvailableSkillsAndActiveSkill(t *testing.T) {
	a := &CallLLMActivity{}
	active := &skills.Skill{Name: "explain-code", Description: "Explain code", Body: "Do X"}
	activeSkillPromptSection := skillprompt.BuildSelectedSkillSection(skillmaterialize.ActiveSkill{
		Definition: skillcatalog.Definition{Name: active.Name, Description: active.Description, Scope: active.Scope},
		Body:       active.Body,
	})

	prompts := a.getSystemPrompts(
		nil,
		"/tmp/project",
		"",
		&config.Config{},
		nil,
		"\n\n<available_skills>\n<skill>\n<name>\nexplain-code\n</name>\n<description>\nExplain code\n</description>\n<location>\n/tmp/project/.reliant/skills/explain-code/SKILL.md\n</location>\n</skill>\n</available_skills>",
		active,
		activeSkillPromptSection,
		[]string{"Requested skill missing", "Skipped /tmp/a/SKILL.md (project): bad frontmatter"},
	)

	require.NotEmpty(t, prompts)
	require.Contains(t, prompts[0], "<available_skills>")
	require.Contains(t, prompts[0], "<active_skill>")
	require.Contains(t, prompts[0], "<skills_notice>")
	require.Contains(t, prompts[0], "Skipped /tmp/a/SKILL.md")
}

func TestGetSystemPrompts_IncludesTrustBoundaryForUntrustedActiveSkill(t *testing.T) {
	a := &CallLLMActivity{}
	active := &skills.Skill{Name: "external-skill", Description: "External", Body: "Do external", Scope: skills.ScopeClaude}
	activeSkillPromptSection := skillprompt.BuildSelectedSkillSection(skillmaterialize.ActiveSkill{
		Definition: skillcatalog.Definition{Name: active.Name, Description: active.Description, Scope: skillscore.Scope(active.Scope)},
		Body:       active.Body,
		Trusted:    false,
	})

	prompts := a.getSystemPrompts(
		nil,
		"/tmp/project",
		"",
		&config.Config{},
		nil,
		"",
		active,
		activeSkillPromptSection,
		nil,
	)

	require.NotEmpty(t, prompts)
	require.Contains(t, prompts[0], "<skills_trust_boundary>")
	require.Contains(t, prompts[0], "untrusted reference content")
}

func TestGetSystemPrompts_AppliesAvailableSkillsRenderLimits(t *testing.T) {
	a := &CallLLMActivity{}
	cfg := &config.Config{}
	cfg.Skills.AvailableSkills.MaxCount = 1
	cfg.Skills.AvailableSkills.MaxPromptBytes = 400

	availableSkillsPromptSection := skillprompt.BuildAvailableSkillsSection([]skillcatalog.Definition{
		{Name: "a-skill", Description: "first", Path: "/tmp/project/.reliant/skills/a-skill/SKILL.md", Scope: skillscore.ScopeProject},
		{Name: "b-skill", Description: "second", Path: "/tmp/project/.reliant/skills/b-skill/SKILL.md", Scope: skillscore.ScopeProject},
	}, skillprompt.AvailableSkillsRenderLimits{MaxSkills: cfg.Skills.AvailableSkills.MaxCount, MaxBytes: cfg.Skills.AvailableSkills.MaxPromptBytes}, skillprompt.AvailableSkillsRenderOptions{})

	prompts := a.getSystemPrompts(
		nil,
		"/tmp/project",
		"",
		cfg,
		nil,
		availableSkillsPromptSection,
		nil,
		"",
		nil,
	)

	require.NotEmpty(t, prompts)
	require.Contains(t, prompts[0], "a-skill")
	require.Contains(t, prompts[0], "additional skills omitted")
	require.NotContains(t, prompts[0], "b-skill")
}

func TestRecentUserMessageTexts_ReturnsChronologicalTail(t *testing.T) {
	history := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "first"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "assistant"}}},
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "second"}}},
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "third"}}},
	}

	texts := recentUserMessageTexts(history, 2)
	require.Equal(t, []string{"second", "third"}, texts)
}

func TestStableSkillNoticeID_DeterministicPerUserTurn(t *testing.T) {
	notice := skills.Notice{Level: skills.NoticeLevelInfo, Message: "skill() docx • supporting files: 3 loaded"}

	idA1 := stableSkillNoticeID("chat-1", "msg-1", notice)
	idA2 := stableSkillNoticeID("chat-1", "msg-1", notice)
	idB := stableSkillNoticeID("chat-1", "msg-2", notice)

	require.Equal(t, idA1, idA2)
	require.NotEqual(t, idA1, idB)
}

type skillsFallbackMockDriver struct{}

func (m *skillsFallbackMockDriver) Name() string {
	return "skills-fallback-mock"
}

func (m *skillsFallbackMockDriver) Model() models.Model {
	return models.Model{ID: "mock-model", Name: "Mock Model"}
}

func (m *skillsFallbackMockDriver) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) (*llm.DriverResponse, error) {
	return &llm.DriverResponse{
		Content:      "mock",
		FinishReason: "end_turn",
		Usage:        llm.TokenUsage{TokenCount: 1},
	}, nil
}

func (m *skillsFallbackMockDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, availableTools []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent, 1)
	ch <- llm.DriverEvent{
		Type: llm.EventComplete,
		Response: &llm.DriverResponse{
			Content:      "Done",
			FinishReason: "end_turn",
			Usage:        llm.TokenUsage{TokenCount: 7},
		},
	}
	close(ch)
	return ch
}

func (m *skillsFallbackMockDriver) ValidateKey(ctx context.Context) error {
	return nil
}

func withSkillsFeatureEnabled(t *testing.T, h *IdempotencyTestHelper, enabled bool) {
	t.Helper()

	value := "false"
	if enabled {
		value = "true"
	}
	t.Setenv(features.SkillsEnabledEnvVar, value)

	ctx := context.Background()
	if err := h.Repo().SetString(ctx, "test-user", nil, features.SkillsEnabledSetting, value); err != nil {
		t.Fatalf("failed to configure %s: %v", features.SkillsEnabledSetting, err)
	}
}

func captureSkillInvocationsForChat(t *testing.T, h *IdempotencyTestHelper, chatID string) []db.SkillInvocationUpdate {
	t.Helper()

	ctx := context.Background()
	rows, err := h.sqlDB.QueryContext(ctx, `SELECT update_type, data FROM chat_updates WHERE chat_id = ? ORDER BY sequence_number DESC LIMIT 100`, chatID)
	if err != nil {
		t.Fatalf("failed to query chat updates: %v", err)
	}
	defer rows.Close()

	invocations := make([]db.SkillInvocationUpdate, 0)
	for rows.Next() {
		var updateType int
		var raw string
		if err := rows.Scan(&updateType, &raw); err != nil {
			t.Fatalf("failed to scan row: %v", err)
		}
		if reliantv1.ChatUpdateType(updateType) != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_SKILL_INVOCATION {
			continue
		}
		var payload db.SkillInvocationUpdate
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatalf("failed to unmarshal chat update payload: %v", err)
		}
		invocations = append(invocations, payload)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("row iteration failed: %v", err)
	}

	return invocations
}

type explicitConfigProvider struct {
	cfg config.Config
}

func (p *explicitConfigProvider) GetProjectConfig(_ context.Context, _ config.ProjectRef) (*config.Config, error) {
	cfg := p.cfg
	return &cfg, nil
}

func executeCallLLMForChatWithProvider(t *testing.T, h *IdempotencyTestHelper, chat *db.Chat, provider config.ConfigProvider) {
	t.Helper()

	mockDriver := &skillsFallbackMockDriver{}
	resolver := func(ctx context.Context, userID string, prefs models.Preferences, opts ...llm.DriverOption) (llm.Driver, error) {
		return mockDriver, nil
	}

	activityInstance := NewCallLLMActivity(
		h.Repo(),
		nil,
		tools.NewToolsFactory(&tools.ToolsOptions{Repo: h.Repo()}),
		provider,
		resolver,
		nil,
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
				},
			},
		},
	}

	var output CallLLMOutput
	if err := h.ExecuteActivity(activityInstance.Execute, input, &output); err != nil {
		t.Fatalf("call_llm execution failed: %v", err)
	}
}

func executeCallLLMForChat(t *testing.T, h *IdempotencyTestHelper, chat *db.Chat) {
	t.Helper()
	executeCallLLMForChatWithProvider(t, h, chat, &staticConfigProvider{})
}

func TestCallLLMActivity_UsesProjectSkillsDiscoveryEvenWhenWorktreeAttached(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	withSkillsFeatureEnabled(t, h, true)

	ctx := context.Background()
	projectPath := t.TempDir()
	project := h.CreateTestProjectWithPath(ctx, "project-skills-scope", "user-skills-scope", projectPath)
	chat := h.CreateTestChat(ctx, "chat-skills-scope", project.ID, project.UserID)

	worktreePath := t.TempDir()
	_ = h.CreateTestWorktree(ctx, project.ID, chat.ID, worktreePath)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	write(filepath.Join(projectPath, ".reliant", "skills", "playwright", "SKILL.md"), `---
name: playwright
description: Playwright browser testing and automation workflows
---
Use the trusted reliant playwright workflow.`)
	write(filepath.Join(worktreePath, ".claude", "skills", "playwright-cli", "SKILL.md"), `---
name: playwright-cli
description: Run Playwright tests with cli automation and browser fixtures
---
Use this external provider skill that should now be ignored by discovery.`)

	h.CreateTestUserMessageWithText(ctx, chat.ID, chat.ID, "/playwright")

	executeCallLLMForChat(t, h, chat)

	invocations := captureSkillInvocationsForChat(t, h, chat.ID)
	require.NotEmpty(t, invocations)

	latest := invocations[0]
	require.Equal(t, db.SkillInvocationTriggerExplicit, latest.Trigger)
	require.Equal(t, db.SkillInvocationStatusActivated, latest.Status)
	require.Equal(t, "playwright", latest.SkillName)
	require.Equal(t, "playwright", latest.RequestedName)
	require.NotContains(t, strings.Join(latest.Warnings, "\n"), "playwright-cli")
	require.NotContains(t, strings.Join(latest.Warnings, "\n"), "untrusted scope")
}

func TestCallLLMActivity_AutoSelectionEmitsSkillInvocation(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	withSkillsFeatureEnabled(t, h, true)

	ctx := context.Background()
	projectPath := t.TempDir()
	project := h.CreateTestProjectWithPath(ctx, "project-skills-auto-no-notice", "user-skills-auto-no-notice", projectPath)
	chat := h.CreateTestChat(ctx, "chat-skills-auto-no-notice", project.ID, project.UserID)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	write(filepath.Join(projectPath, ".reliant", "skills", "sql-debug", "SKILL.md"), `---
name: sql-debug
description: Analyze SQL performance and schema bottlenecks
---
Use SQL diagnostics.`)

	h.CreateTestUserMessageWithText(ctx, chat.ID, chat.ID, "Need help debugging a SQL query")

	executeCallLLMForChat(t, h, chat)

	invocations := captureSkillInvocationsForChat(t, h, chat.ID)
	require.NotEmpty(t, invocations)

	latest := invocations[0]
	require.Equal(t, db.SkillInvocationTriggerAuto, latest.Trigger)
	require.Equal(t, db.SkillInvocationStatusActivated, latest.Status)
	require.Equal(t, "sql-debug", latest.SkillName)
}

func TestCallLLMActivity_SkillsFeatureDisabled_SkipsSkillsDiscoveryAndNotices(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	projectPath := t.TempDir()
	project := h.CreateTestProjectWithPath(ctx, "project-skills-disabled", "test-user", projectPath)
	chat := h.CreateTestChat(ctx, "chat-skills-disabled", project.ID, project.UserID)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	write(filepath.Join(projectPath, ".reliant", "skills", "playwright", "SKILL.md"), `---
name: playwright
description: Playwright browser testing and automation workflows
---
Use the trusted reliant playwright workflow.`)

	h.CreateTestUserMessageWithText(ctx, chat.ID, chat.ID, "Need help with playwright browser tests")

	withSkillsFeatureEnabled(t, h, false)
	executeCallLLMForChat(t, h, chat)

	invocations := captureSkillInvocationsForChat(t, h, chat.ID)
	require.Empty(t, invocations)
}

func TestGetAvailableToolsWithSpawn_SkillsFeatureDisabled_HidesInstallSkill(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-tools-skills-disabled", "test-user")
	chat := h.CreateTestChat(ctx, "chat-tools-skills-disabled", project.ID, project.UserID)

	activityInstance := NewCallLLMActivity(
		h.Repo(),
		nil,
		tools.NewToolsFactory(&tools.ToolsOptions{Repo: h.Repo()}),
		&staticConfigProvider{},
		nil,
		nil,
	)

	t.Setenv(features.SkillsEnabledEnvVar, "false")
	result := activityInstance.getAvailableToolsWithSpawn(ctx, chat, project.Path, []string{"tag:default"}, chat.ID)
	toolNames := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		toolNames = append(toolNames, tool.Name())
	}
	sort.Strings(toolNames)
	require.NotContains(t, toolNames, "install_skill")

	t.Setenv(features.SkillsEnabledEnvVar, "true")
	result = activityInstance.getAvailableToolsWithSpawn(ctx, chat, project.Path, []string{"tag:default"}, chat.ID)
	toolNames = toolNames[:0]
	for _, tool := range result.Tools {
		toolNames = append(toolNames, tool.Name())
	}
	sort.Strings(toolNames)
	require.Contains(t, toolNames, "install_skill")
}

func TestCallLLMActivity_SkillsActivationModeExplicitFromProjectConfig_DisablesAutoSelection(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	withSkillsFeatureEnabled(t, h, true)

	ctx := context.Background()
	projectPath := t.TempDir()
	project := h.CreateTestProjectWithPath(ctx, "project-skills-explicit-mode", "user-skills-explicit-mode", projectPath)
	chat := h.CreateTestChat(ctx, "chat-skills-explicit-mode", project.ID, project.UserID)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	write(filepath.Join(projectPath, ".reliant", "skills", "sql-debug", "SKILL.md"), `---
name: sql-debug
description: Analyze SQL performance and schema bottlenecks
---
Use SQL diagnostics.`)

	h.CreateTestUserMessageWithText(ctx, chat.ID, chat.ID, "Need help debugging a SQL query")

	executeCallLLMForChatWithProvider(t, h, chat, &explicitConfigProvider{cfg: config.Config{
		Skills: config.SkillsConfig{ActivationMode: "explicit"},
	}})

	invocations := captureSkillInvocationsForChat(t, h, chat.ID)
	require.Empty(t, invocations)
}

func TestCallLLMActivity_SkillsIntegrationModeTool_UsesToolPathWithoutFailures(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	withSkillsFeatureEnabled(t, h, true)

	ctx := context.Background()
	projectPath := t.TempDir()
	project := h.CreateTestProjectWithPath(ctx, "project-skills-tool-mode", "user-skills-tool-mode", projectPath)
	chat := h.CreateTestChat(ctx, "chat-skills-tool-mode", project.ID, project.UserID)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	skillDir := filepath.Join(projectPath, ".reliant", "skills", "debug-sql")
	write(filepath.Join(skillDir, "SKILL.md"), `---
name: debug-sql
description: Debug SQL queries and explain plans
---
Use SQL diagnostics.`)
	write(filepath.Join(skillDir, "guide.md"), "sql examples and snippets")

	h.CreateTestUserMessageWithText(ctx, chat.ID, chat.ID, "/debug-sql")

	executeCallLLMForChatWithProvider(t, h, chat, &explicitConfigProvider{cfg: config.Config{
		Skills: config.SkillsConfig{IntegrationMode: "tool"},
	}})

	invocations := captureSkillInvocationsForChat(t, h, chat.ID)
	require.NotEmpty(t, invocations)

	latest := invocations[0]
	require.Equal(t, db.SkillInvocationTriggerExplicit, latest.Trigger)
	require.Equal(t, db.SkillInvocationStatusActivated, latest.Status)
	require.Equal(t, "debug-sql", latest.SkillName)
	require.NotContains(t, strings.ToLower(latest.Message), "failed")
}

func TestCallLLMActivity_SkillInvocationIDStableAcrossTurns(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	withSkillsFeatureEnabled(t, h, true)

	ctx := context.Background()
	projectPath := t.TempDir()
	project := h.CreateTestProjectWithPath(ctx, "project-skills-stable-id", "user-skills-stable-id", projectPath)
	chat := h.CreateTestChat(ctx, "chat-skills-stable-id", project.ID, project.UserID)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	write(filepath.Join(projectPath, ".reliant", "skills", "debug-sql", "SKILL.md"), `---
name: debug-sql
description: Debug SQL queries and explain plans
---
Use SQL diagnostics.`)

	h.CreateTestUserMessageWithText(ctx, chat.ID, chat.ID, "/debug-sql")
	executeCallLLMForChatWithProvider(t, h, chat, &explicitConfigProvider{cfg: config.Config{Skills: config.SkillsConfig{ActivationMode: "explicit"}}})

	h.CreateTestUserMessageWithText(ctx, chat.ID, chat.ID, "/debug-sql")
	executeCallLLMForChatWithProvider(t, h, chat, &explicitConfigProvider{cfg: config.Config{Skills: config.SkillsConfig{ActivationMode: "explicit"}}})

	invocations := captureSkillInvocationsForChat(t, h, chat.ID)
	require.NotEmpty(t, invocations)
	require.GreaterOrEqual(t, len(invocations), 2)
	require.Equal(t, invocations[0].ID, invocations[1].ID)
}

func TestCallLLMActivity_AllowedToolsPolicyFiltersToolExposure(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	withSkillsFeatureEnabled(t, h, true)

	ctx := context.Background()
	projectPath := t.TempDir()
	project := h.CreateTestProjectWithPath(ctx, "project-skills-allowed-tools", "user-skills-allowed-tools", projectPath)
	chat := h.CreateTestChat(ctx, "chat-skills-allowed-tools", project.ID, project.UserID)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	skillDir := filepath.Join(projectPath, ".reliant", "skills", "only-view")
	write(filepath.Join(skillDir, "SKILL.md"), `---
name: only-view
description: Restrict tool access to view
allowed-tools: view
---
Use read-only investigation flow.`)

	activityInstance := NewCallLLMActivity(
		h.Repo(),
		nil,
		tools.NewToolsFactory(&tools.ToolsOptions{Repo: h.Repo()}),
		&explicitConfigProvider{cfg: config.Config{Skills: config.SkillsConfig{ActivationMode: "explicit"}}},
		nil,
		nil,
	)

	toolResult := activityInstance.getAvailableToolsWithSpawn(ctx, chat, projectPath, []string{"view", "edit"}, chat.ID)
	require.Len(t, toolResult.Tools, 2)

	resolvedSkills := skills.DefaultRuntime().ResolveTurn(ctx, skills.ResolveTurnInput{
		ProjectPath:        projectPath,
		LatestUserMessage:  "/only-view",
		ActivationMode:     "explicit",
		AvailableToolNames: []string{"view", "edit"},
	})
	require.NotNil(t, resolvedSkills.ActiveSkill)
	require.Equal(t, []string{"view"}, resolvedSkills.AllowedToolNames)
	require.NotEmpty(t, resolvedSkills.WarningHints)
}
