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
	userYAML := "mcpServers:\n  userServer:\n    command: user-cmd\n"
	projectYAML := "mcpServers:\n  projectServer:\n    command: project-cmd\n"
	localYAML := "mcpServers:\n  localServer:\n    command: local-cmd\n"
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
}

func TestStoredConfigProvider_ParsesRepoMemories(t *testing.T) {
	repoMem := `{"api":"api context","forge":"forge context"}`
	provider := NewStoredConfigProvider(&staticStore{record: &StoredProjectConfigRecord{
		ProjectID:        "p1",
		RepoMemoriesJSON: &repoMem,
	}})

	cfg, err := provider.GetProjectConfig(context.Background(), ProjectRef{ProjectID: "p1"})
	require.NoError(t, err)
	require.Len(t, cfg.RepoMemories, 2)
	require.Equal(t, "api context", cfg.RepoMemories["api"])
	require.Equal(t, "forge context", cfg.RepoMemories["forge"])
}

func TestStoredConfigProvider_NilRepoMemories(t *testing.T) {
	provider := NewStoredConfigProvider(&staticStore{record: &StoredProjectConfigRecord{
		ProjectID: "p1",
	}})

	cfg, err := provider.GetProjectConfig(context.Background(), ProjectRef{ProjectID: "p1"})
	require.NoError(t, err)
	require.Nil(t, cfg.RepoMemories)
}

func TestParseRepoMemories(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		result, err := ParseRepoMemories(nil)
		require.NoError(t, err)
		require.Nil(t, result)
	})

	t.Run("empty string", func(t *testing.T) {
		s := ""
		result, err := ParseRepoMemories(&s)
		require.NoError(t, err)
		require.Nil(t, result)
	})

	t.Run("valid", func(t *testing.T) {
		s := `{"api":"api rules","web":"web rules"}`
		result, err := ParseRepoMemories(&s)
		require.NoError(t, err)
		require.Equal(t, map[string]string{"api": "api rules", "web": "web rules"}, result)
	})

	t.Run("invalid json", func(t *testing.T) {
		s := "not json"
		_, err := ParseRepoMemories(&s)
		require.Error(t, err)
	})
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
