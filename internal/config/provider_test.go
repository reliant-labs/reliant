package config

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

type staticStore struct {
	record *StoredProjectConfigRecord
	err    error
}

func (s *staticStore) GetProjectConfigRecord(ctx context.Context, projectID string) (*StoredProjectConfigRecord, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.record, nil
}

func TestStoredConfigProvider_MergesMemoryAndMCPConfigs(t *testing.T) {
	userYAML := "mcpServers:\n  userServer:\n    command: user-cmd\nskills:\n  activationMode: explicit\n"
	projectYAML := "mcpServers:\n  projectServer:\n    command: project-cmd\nskills:\n  supportingFiles:\n    maxFiles: 5\n"
	localYAML := "mcpServers:\n  localServer:\n    command: local-cmd\nskills:\n  retrieval:\n    maxChunks: 7\n"
	globalMemory := "  global rules  "
	projectMemory := "\nproject context\n"
	mcpConfigs := `{"user":"{\"mcpServers\":{\"u\":{\"command\":\"cu\"}}}","project":"{\"mcpServers\":{\"p\":{\"command\":\"cp\"}}}","local":"{\"mcpServers\":{\"l\":{\"command\":\"cl\"}}}"}`

	provider := NewStoredConfigProvider(&staticStore{record: &StoredProjectConfigRecord{
		ProjectID:         "p1",
		UserConfigYAML:    &userYAML,
		ProjectConfigYAML: &projectYAML,
		LocalConfigYAML:   &localYAML,
		GlobalMemoryMD:    &globalMemory,
		ProjectMemoryMD:   &projectMemory,
		MCPConfigs:        &mcpConfigs,
	}})

	cfg, err := provider.GetProjectConfig(context.Background(), ProjectRef{ProjectID: "p1"})
	require.NoError(t, err)
	require.Equal(t, "global rules", cfg.GlobalMemoryMD)
	require.Equal(t, "project context", cfg.ProjectMemoryMD)
	require.Empty(t, cfg.WorkingDir)

	require.Contains(t, cfg.MCPServers, "u")
	require.Contains(t, cfg.MCPServers, "p")
	require.Contains(t, cfg.MCPServers, "l")

	require.Equal(t, "explicit", cfg.Skills.ActivationMode)
	require.Equal(t, 5, cfg.Skills.SupportingFiles.MaxFiles)
	require.Equal(t, 7, cfg.Skills.Retrieval.MaxChunks)
}

func TestStoredConfigProvider_MergesSkillsIntegrationModeByPrecedence(t *testing.T) {
	userYAML := "skills:\n  integrationMode: tool\n"
	projectYAML := "skills:\n  integrationMode: filesystem\n"

	provider := NewStoredConfigProvider(&staticStore{record: &StoredProjectConfigRecord{
		ProjectID:         "p1",
		UserConfigYAML:    &userYAML,
		ProjectConfigYAML: &projectYAML,
	}})

	cfg, err := provider.GetProjectConfig(context.Background(), ProjectRef{ProjectID: "p1"})
	require.NoError(t, err)
	require.Equal(t, "filesystem", cfg.Skills.IntegrationMode)
}

func TestStoredConfigProvider_MissingStoredRecordReturnsDefaultConfig(t *testing.T) {
	provider := NewStoredConfigProvider(&staticStore{err: sql.ErrNoRows})
	cfg, err := provider.GetProjectConfig(context.Background(), ProjectRef{ProjectID: "missing"})
	require.NoError(t, err)
	require.NotNil(t, cfg)
	// Should return an empty default config, not an error
	require.Empty(t, cfg.WorkingDir)
	require.Nil(t, cfg.MCPServers)
	require.Empty(t, cfg.GlobalMemoryMD)
	require.Empty(t, cfg.ProjectMemoryMD)
}

func TestStoredConfigProvider_ChunkOverlapExplicitZeroOverridesHigherPrecedence(t *testing.T) {
	userYAML := "skills:\n  retrieval:\n    chunkOverlap: 18\n"
	projectYAML := "skills:\n  retrieval:\n    chunkOverlap: 6\n"
	localYAML := "skills:\n  retrieval:\n    chunkOverlap: 0\n"

	provider := NewStoredConfigProvider(&staticStore{record: &StoredProjectConfigRecord{
		ProjectID:         "p1",
		UserConfigYAML:    &userYAML,
		ProjectConfigYAML: &projectYAML,
		LocalConfigYAML:   &localYAML,
	}})

	cfg, err := provider.GetProjectConfig(context.Background(), ProjectRef{ProjectID: "p1"})
	require.NoError(t, err)
	require.Equal(t, 0, cfg.Skills.Retrieval.ChunkOverlap)
}

func TestStoredConfigProvider_ChunkOverlapUnsetDoesNotOverride(t *testing.T) {
	userYAML := "skills:\n  retrieval:\n    chunkOverlap: 18\n"
	projectYAML := "skills:\n  retrieval:\n    chunkBytes: 1200\n"

	provider := NewStoredConfigProvider(&staticStore{record: &StoredProjectConfigRecord{
		ProjectID:         "p1",
		UserConfigYAML:    &userYAML,
		ProjectConfigYAML: &projectYAML,
	}})

	cfg, err := provider.GetProjectConfig(context.Background(), ProjectRef{ProjectID: "p1"})
	require.NoError(t, err)
	require.Equal(t, 18, cfg.Skills.Retrieval.ChunkOverlap)
}

func TestStoredConfigProvider_AvailableSkillsExplicitZeroOverridesHigherPrecedence(t *testing.T) {
	userYAML := "skills:\n  availableSkills:\n    maxCount: 64\n    maxPromptBytes: 6000\n"
	localYAML := "skills:\n  availableSkills:\n    maxCount: 0\n    maxPromptBytes: 0\n"

	provider := NewStoredConfigProvider(&staticStore{record: &StoredProjectConfigRecord{
		ProjectID:       "p1",
		UserConfigYAML:  &userYAML,
		LocalConfigYAML: &localYAML,
	}})

	cfg, err := provider.GetProjectConfig(context.Background(), ProjectRef{ProjectID: "p1"})
	require.NoError(t, err)
	require.Equal(t, 0, cfg.Skills.AvailableSkills.MaxCount)
	require.Equal(t, 0, cfg.Skills.AvailableSkills.MaxPromptBytes)
}
