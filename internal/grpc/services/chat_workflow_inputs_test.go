package services

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/stretchr/testify/require"
)

func modelInput(defaultSelector *reliantv1.ModelSelector) *reliantv1.Input {
	cfg := &reliantv1.ModelInputConfig{}
	if defaultSelector != nil {
		cfg.Default = defaultSelector
	}
	return &reliantv1.Input{
		Type:   "model",
		Config: &reliantv1.Input_ModelInput{ModelInput: cfg},
	}
}

func groupInput(inputs map[string]*reliantv1.Input) *reliantv1.Input {
	return &reliantv1.Input{
		Type: "group",
		Config: &reliantv1.Input_GroupInput{GroupInput: &reliantv1.GroupInputConfig{
			Inputs: inputs,
		}},
	}
}

func TestBuildWorkflowInputs_EmptyToolsDoesNotOverridePreset(t *testing.T) {
	service := &ChatService{}
	projectPath := t.TempDir()

	presetLoader := preset.NewLoaderForProject(projectPath)
	agentPreset, err := presetLoader.Load("general")
	require.NoError(t, err)
	require.NotNil(t, agentPreset)

	expectedToolsRaw, ok := agentPreset.Params["tools"]
	require.True(t, ok, "general preset should define tools")
	expectedTools, ok := expectedToolsRaw.([]interface{})
	require.True(t, ok, "tools should be a list")
	require.NotEmpty(t, expectedTools)

	userParams := map[string]*structpb.Value{
		"tools": structpb.NewListValue(&structpb.ListValue{Values: []*structpb.Value{}}),
	}

	initialInputs := service.buildWorkflowInputs(
		context.Background(),
		"user-1",
		projectPath,
		"",
		"builtin://agent",
		map[string]string{"default": "general"},
		userParams,
	)

	toolsRaw, exists := initialInputs["tools"]
	require.True(t, exists)
	tools, ok := toolsRaw.([]interface{})
	require.True(t, ok)
	require.Equal(t, expectedTools, tools)
}

func TestBuildWorkflowInputs_EmptySpawnPresetsDoesNotOverridePreset(t *testing.T) {
	service := &ChatService{}
	projectPath := t.TempDir()

	presetLoader := preset.NewLoaderForProject(projectPath)
	agentPreset, err := presetLoader.Load("general")
	require.NoError(t, err)
	require.NotNil(t, agentPreset)

	expectedSpawnPresetsRaw, ok := agentPreset.Params["spawn_presets"]
	require.True(t, ok, "general preset should define spawn_presets")
	expectedSpawnPresets, ok := expectedSpawnPresetsRaw.([]interface{})
	require.True(t, ok, "spawn_presets should be a list")
	require.NotEmpty(t, expectedSpawnPresets)

	userParams := map[string]*structpb.Value{
		"spawn_presets": structpb.NewListValue(&structpb.ListValue{Values: []*structpb.Value{}}),
	}

	initialInputs := service.buildWorkflowInputs(
		context.Background(),
		"user-1",
		projectPath,
		"",
		"builtin://agent",
		map[string]string{"default": "general"},
		userParams,
	)

	spawnPresetsRaw, exists := initialInputs["spawn_presets"]
	require.True(t, exists)
	spawnPresets, ok := spawnPresetsRaw.([]interface{})
	require.True(t, ok)
	require.Equal(t, expectedSpawnPresets, spawnPresets)
}

func TestBuildWorkflowInputs_NonEmptyToolsStillOverridePreset(t *testing.T) {
	service := &ChatService{}
	projectPath := t.TempDir()

	userParams := map[string]*structpb.Value{
		"tools": structpb.NewListValue(&structpb.ListValue{Values: []*structpb.Value{
			structpb.NewStringValue("mcp__search"),
		}}),
	}

	initialInputs := service.buildWorkflowInputs(
		context.Background(),
		"user-1",
		projectPath,
		"",
		"builtin://agent",
		map[string]string{"default": "general"},
		userParams,
	)

	toolsRaw, exists := initialInputs["tools"]
	require.True(t, exists)
	tools, ok := toolsRaw.([]interface{})
	require.True(t, ok)
	require.Equal(t, []interface{}{"mcp__search"}, tools)
}

func TestBuildStateUpdateForActiveWorkflow_AppliesSelectedPresetAndSkipsEmptyToolOverrides(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	service := &ChatService{database: repo}

	projectPath := t.TempDir()
	now := time.Now().UTC()
	err := repo.CreateProject(ctx, &db.Project{
		ID:         "project-1",
		UserID:     "user-1",
		Name:       "Project",
		Path:       projectPath,
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	})
	require.NoError(t, err)

	chat := &db.Chat{
		ID:              "chat-1",
		UserID:          "user-1",
		ProjectID:       "project-1",
		WorkflowName:    ptr.Of("builtin://agent"),
		SelectedPresets: map[string]string{"default": "general"},
	}

	params := map[string]*structpb.Value{
		"tools": structpb.NewListValue(&structpb.ListValue{Values: []*structpb.Value{}}),
	}

	stateUpdate := service.buildStateUpdateForActiveWorkflow(
		ctx,
		"user-1",
		chat,
		"builtin://agent",
		map[string]string{"default": "general"},
		params,
	)

	toolsRaw, exists := stateUpdate["tools"]
	require.True(t, exists)
	tools, ok := toolsRaw.([]interface{})
	require.True(t, ok)
	require.NotEmpty(t, tools)
}

func TestBuildStateUpdateForActiveWorkflow_UsesNewPresetSelection(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	service := &ChatService{database: repo}

	projectPath := t.TempDir()
	now := time.Now().UTC()
	err := repo.CreateProject(ctx, &db.Project{
		ID:         "project-2",
		UserID:     "user-2",
		Name:       "Project 2",
		Path:       projectPath,
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	})
	require.NoError(t, err)

	chat := &db.Chat{
		ID:              "chat-2",
		UserID:          "user-2",
		ProjectID:       "project-2",
		WorkflowName:    ptr.Of("builtin://agent"),
		SelectedPresets: map[string]string{"default": "general"},
	}

	stateUpdate := service.buildStateUpdateForActiveWorkflow(
		ctx,
		"user-2",
		chat,
		"builtin://agent",
		map[string]string{"default": "general"},
		map[string]*structpb.Value{},
	)

	_, hasTools := stateUpdate["tools"]
	_, hasSpawnPresets := stateUpdate["spawn_presets"]
	require.True(t, hasTools)
	require.True(t, hasSpawnPresets)
}

func TestBuildWorkflowInputs_LoadsUserPresetToolsFromDatabase(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	service := &ChatService{database: repo}
	projectPath := t.TempDir()
	now := time.Now().UTC()
	projectID := "project-user-preset"
	userID := "user-user-preset"

	err := repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     userID,
		Name:       "Project User Preset",
		Path:       projectPath,
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	})
	require.NoError(t, err)

	customTools := []interface{}{"tag:default", "tag:mcp"}
	_, err = repo.CreatePreset(ctx, &db.Preset{
		ID:        uuid.New().String(),
		UserID:    userID,
		ProjectID: ptr.Of(projectID),
		Name:      "General MCP Copy",
		Slug:      "general-mcp-copy",
		Tag:       "agent",
		Params: map[string]interface{}{
			"tools":         customTools,
			"spawn_presets": []interface{}{"general", "researcher"},
		},
	})
	require.NoError(t, err)

	initialInputs := service.buildWorkflowInputs(
		ctx,
		userID,
		projectPath,
		projectID,
		"builtin://agent",
		map[string]string{"default": "general-mcp-copy"},
		map[string]*structpb.Value{},
	)

	require.Equal(t, customTools, initialInputs["tools"])
	require.Equal(t, []interface{}{"general", "researcher"}, initialInputs["spawn_presets"])
}

func TestNormalizeLegacyModelSelectorString(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		wantID        string
		wantProviders []interface{}
		wantNil       bool
	}{
		{
			name:    "plain model id",
			raw:     "gpt-5.4-mini",
			wantID:  "gpt-5.4-mini",
			wantNil: false,
		},
		{
			name:          "legacy model at provider",
			raw:           "gpt-5.4-mini@codex",
			wantID:        "gpt-5.4-mini",
			wantProviders: []interface{}{"codex"},
			wantNil:       false,
		},
		{
			name:          "whitespace trimmed",
			raw:           "  gpt-5.4-mini@openai  ",
			wantID:        "gpt-5.4-mini",
			wantProviders: []interface{}{"openai"},
			wantNil:       false,
		},
		{
			name:    "empty string returns nil",
			raw:     "   ",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, got := normalizeLegacyModelSelectorString(tt.raw)
			if tt.wantNil {
				require.Nil(t, got)
				require.Empty(t, gotID)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, tt.wantID, gotID)
			require.Equal(t, tt.wantID, got["id"])
			if tt.wantProviders == nil {
				_, exists := got["providers"]
				require.False(t, exists)
			} else {
				require.Equal(t, tt.wantProviders, got["providers"])
			}
		})
	}
}

func TestNormalizeModelInputs_ConvertsLegacyModelProviderString(t *testing.T) {
	inputs := map[string]interface{}{
		"agent": map[string]interface{}{
			"model": "gpt-5.4-mini@codex",
		},
	}
	schemas := map[string]*reliantv1.Input{
		"agent": groupInput(map[string]*reliantv1.Input{
			"model": modelInput(nil),
		}),
	}

	normalizeModelInputs(inputs, schemas)

	agent, ok := inputs["agent"].(map[string]interface{})
	require.True(t, ok)
	modelValue, ok := agent["model"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "gpt-5.4-mini", modelValue["id"])
	require.Equal(t, []interface{}{"codex"}, modelValue["providers"])
}

func TestShouldSkipEmptyPresetOverride(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		value    interface{}
		expected bool
	}{
		{
			name:     "empty tools list",
			key:      "tools",
			value:    []interface{}{},
			expected: true,
		},
		{
			name:     "empty spawn presets list",
			key:      "spawn_presets",
			value:    []interface{}{},
			expected: true,
		},
		{
			name:     "non-empty tools list",
			key:      "tools",
			value:    []interface{}{"a"},
			expected: false,
		},
		{
			name:     "other empty list key",
			key:      "something_else",
			value:    []interface{}{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldSkipEmptyPresetOverride(tt.key, tt.value)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestValidateWorkflowParamStructure_AcceptsNestedKeys(t *testing.T) {
	agentValue, err := structpb.NewValue(map[string]interface{}{
		"model": map[string]interface{}{"id": "gpt-4o"},
		"mode":  "planning",
	})
	require.NoError(t, err)

	err = validateWorkflowParamStructure(map[string]*structpb.Value{
		"agent": agentValue,
	})
	require.NoError(t, err)
}

func TestValidateWorkflowParamStructure_RejectsDottedKeys(t *testing.T) {
	tests := []struct {
		name     string
		params   map[string]*structpb.Value
		expected string
	}{
		{
			name: "rejects top-level dotted key",
			params: map[string]*structpb.Value{
				"agent.model": structpb.NewStringValue("gpt-4o"),
			},
			expected: "agent.model",
		},
		{
			name: "rejects nested dotted key",
			params: map[string]*structpb.Value{
				"agent": structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{
					"model.id": structpb.NewStringValue("gpt-4o"),
				}}),
			},
			expected: "agent.model.id",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateWorkflowParamStructure(testCase.params)
			require.Error(t, err)
			require.Contains(t, err.Error(), testCase.expected)
			require.Contains(t, err.Error(), "nested objects")
		})
	}
}

func TestBuildStateUpdateForActiveWorkflow_AcceptsNestedWorkflowParams(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	service := &ChatService{database: repo}

	projectPath := t.TempDir()
	now := time.Now().UTC()
	err := repo.CreateProject(ctx, &db.Project{
		ID:         "project-nested-params",
		UserID:     "user-nested-params",
		Name:       "Project Nested Params",
		Path:       projectPath,
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	})
	require.NoError(t, err)

	chat := &db.Chat{
		ID:           "chat-nested-params",
		UserID:       "user-nested-params",
		ProjectID:    "project-nested-params",
		WorkflowName: ptr.Of("builtin://agent"),
	}

	agentValue := structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{
		"model": structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{
			"id": structpb.NewStringValue("gpt-4o"),
		}}),
		"mode": structpb.NewStringValue("planning"),
	}})

	stateUpdate := service.buildStateUpdateForActiveWorkflow(
		ctx,
		"user-nested-params",
		chat,
		"builtin://agent",
		nil,
		map[string]*structpb.Value{"agent": agentValue},
	)

	agentMap, ok := stateUpdate["agent"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "planning", agentMap["mode"])
	require.NotContains(t, stateUpdate, "agent.model")
}

func TestBuildWorkflowInputs_NormalizesBuiltinNestedLegacyModelSelector(t *testing.T) {
	service := &ChatService{}
	projectPath := t.TempDir()

	initialInputs := service.buildWorkflowInputs(
		context.Background(),
		"user-builtin-model-normalize",
		projectPath,
		"",
		"builtin://agent",
		map[string]string{"default": "general"},
		map[string]*structpb.Value{"model": structpb.NewStringValue("gpt-5.4-mini@codex")},
	)

	modelValue, ok := initialInputs["model"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "gpt-5.4-mini", modelValue["id"])
	require.Equal(t, []interface{}{"codex"}, modelValue["providers"])
}
