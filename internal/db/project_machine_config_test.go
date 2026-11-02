package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
)

// pushConfig pushes a machine's whole snapshot: its skills, repo memories and
// MCP servers, as the daemon does on every connect.
func (p *twoMachineProject) pushConfig(t *testing.T, daemonID string, at int64, skills []config.StoredSkill, memories string, mcp string) {
	t.Helper()
	sj, err := config.EncodeCatalogJSON(skills)
	require.NoError(t, err)
	_, err = p.repo.SyncProjectConfigSnapshot(context.Background(), &ProjectConfigRecord{
		ProjectID: p.projectID, DaemonID: daemonID,
		UserConfigYAML:    testStringPtr("from " + daemonID),
		ProjectSkillsJSON: sj, RepoMemoriesJSON: testStringPtr(memories), MCPConfigs: testStringPtr(mcp),
		PushedAt: time.UnixMilli(at).UTC(),
	}, false)
	require.NoError(t, err)
}

// The incident's other half: the release workflow "follows the release
// skill", which lives in control-plane/.reliant/skills. A machine that syncs
// the project without that checkout used to replace the record's skills
// whole, so the skill vanished for every machine. Now each machine keeps its
// own, a run reads its machine's, and the record's skills are the union.
func TestSyncProjectConfigSnapshot_AnotherMachineCannotEraseSkills(t *testing.T) {
	p := newTwoMachineProject(t)
	ctx := context.Background()
	release := config.StoredSkill{SkillPath: "control-plane/.reliant/skills/release", Name: "release", Scope: "project", Body: "cut it"}
	root := config.StoredSkill{SkillPath: ".reliant/skills/root", Name: "root", Scope: "project", Body: "root"}

	p.pushConfig(t, p.defaultID, 1000, []config.StoredSkill{release, root},
		`{"control-plane":"cp memory"}`, `{"project":"{\"mcpServers\":{\"default-only\":{\"command\":\"x\"}}}"}`)
	p.pushConfig(t, p.cicdID, 2000, []config.StoredSkill{root},
		`{}`, `{"project":"{\"mcpServers\":{\"cicd-only\":{\"command\":\"y\"}}}"}`)

	record, err := p.repo.GetProjectConfigRecord(ctx, p.projectID)
	require.NoError(t, err)
	skills, err := config.ParseStoredSkills(record.ProjectSkillsJSON)
	require.NoError(t, err)
	require.Len(t, skills, 2, "the record keeps the skill only default provides")
	memories, err := config.ParseRepoMemories(record.RepoMemoriesJSON)
	require.NoError(t, err)
	assert.Equal(t, "cp memory", memories["control-plane"])
	assert.Equal(t, "from "+p.cicdID, *record.UserConfigYAML, "single-valued parts are the last pusher's")

	cicd, synced, err := p.repo.GetProjectMachineConfigRecord(ctx, p.projectID, p.cicdID)
	require.NoError(t, err)
	require.True(t, synced)
	cicdSkills, err := config.ParseStoredSkills(cicd.ProjectSkillsJSON)
	require.NoError(t, err)
	require.Len(t, cicdSkills, 1, "a run on cicd sees cicd's skills")
	assert.Contains(t, *cicd.MCPConfigs, "cicd-only")

	def, synced, err := p.repo.GetProjectMachineConfigRecord(ctx, p.projectID, p.defaultID)
	require.NoError(t, err)
	require.True(t, synced)
	assert.Contains(t, *def.MCPConfigs, "default-only", "a run on default reads default's MCP, not the last pusher's")
	assert.Equal(t, "from "+p.defaultID, *def.UserConfigYAML)

	_, synced, err = p.repo.GetProjectMachineConfigRecord(ctx, p.projectID, uuid.NewString())
	require.NoError(t, err)
	assert.False(t, synced, "a machine that never synced the project")

	// An older snapshot from default (a stale clock) still refreshes the
	// union and its own row, without replacing the newer record.
	p.pushConfig(t, p.defaultID, 1500, []config.StoredSkill{root}, `{}`, `{}`)
	record, err = p.repo.GetProjectConfigRecord(ctx, p.projectID)
	require.NoError(t, err)
	skills, err = config.ParseStoredSkills(record.ProjectSkillsJSON)
	require.NoError(t, err)
	assert.Len(t, skills, 1, "no machine provides release any more")
	assert.Equal(t, "from "+p.cicdID, *record.UserConfigYAML)
}

// Through the config provider: a run's machine reads its own snapshot, and a
// machine that has never synced one reads the project record.
func TestStoredConfigProvider_ReadsTheRunsMachine(t *testing.T) {
	p := newTwoMachineProject(t)
	ctx := context.Background()
	release := config.StoredSkill{SkillPath: "control-plane/.reliant/skills/release", Name: "release", Scope: "project", Body: "cut it"}
	p.pushConfig(t, p.defaultID, 1000, []config.StoredSkill{release}, `{}`, `{}`)
	p.pushConfig(t, p.cicdID, 2000, nil, `{}`, `{}`)

	provider := config.NewCachedStoredConfigProvider(machineRecordStore{p.repo}, p.repo, 0)
	skillsOn := func(daemonID string) int {
		cfg, err := provider.GetProjectConfig(ctx, config.ProjectRef{ProjectID: p.projectID, DaemonID: daemonID})
		require.NoError(t, err)
		return len(cfg.Skills)
	}
	assert.Equal(t, 1, skillsOn(p.defaultID))
	assert.Equal(t, 0, skillsOn(p.cicdID), "cicd has no control-plane checkout, so a run there has no release skill")
	assert.Equal(t, 1, skillsOn(""), "no machine: the union")
	assert.Equal(t, 1, skillsOn(uuid.NewString()), "a machine that never synced: the union")
}

// machineRecordStore adapts *Repo to config.MachineConfigStore the way
// configadapter does, without importing it (it imports this package).
type machineRecordStore struct{ repo *Repo }

func (s machineRecordStore) GetProjectConfigRecord(ctx context.Context, projectID string) (*config.StoredProjectConfigRecord, error) {
	r, err := s.repo.GetProjectConfigRecord(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return &config.StoredProjectConfigRecord{ProjectID: r.ProjectID, DaemonID: r.DaemonID, ProjectSkillsJSON: r.ProjectSkillsJSON}, nil
}

func (s machineRecordStore) GetProjectMachineConfigRecord(ctx context.Context, projectID, daemonID string) (*config.StoredProjectConfigRecord, bool, error) {
	r, synced, err := s.repo.GetProjectMachineConfigRecord(ctx, projectID, daemonID)
	if err != nil || !synced {
		return nil, false, err
	}
	return &config.StoredProjectConfigRecord{ProjectID: r.ProjectID, DaemonID: r.DaemonID, ProjectSkillsJSON: r.ProjectSkillsJSON}, true, nil
}
