// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// wholeConfigRowCounter counts reads of the whole project config record. The
// row carries every skill body the daemon indexed and was 18 MB in prod; a
// chat start read it once per workflow resolution and once per preset, which
// is where StartChat's 2.3s p50 went.
type wholeConfigRowCounter struct {
	db.Repository

	mu    sync.Mutex
	reads int
}

func (c *wholeConfigRowCounter) GetProjectConfigRecord(ctx context.Context, projectID string) (*db.ProjectConfigRecord, error) {
	c.mu.Lock()
	c.reads++
	c.mu.Unlock()
	return c.Repository.GetProjectConfigRecord(ctx, projectID)
}

// A chat start resolves its workflow, validates the workflow tree and its
// presets, builds and validates its inputs. None of that may read the whole
// project config record — and a project preset, which lives in that record,
// must still be found and applied through the single-column read.
func TestLaunchChatStartReadsOnlyTheConfigColumnsItNeeds(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)

	presets := `[{"name":"plan-first","yaml_content":"name: plan-first\ntag: agent\nparams:\n  mode: plan\n"}]`
	skills := `[{"name":"a-skill","body":"` + strings.Repeat("x", 64<<10) + `"}]`
	require.NoError(t, repo.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ProjectID: projectID, DaemonID: "daemon-1",
		ProjectPresetsJSON: &presets, ProjectSkillsJSON: &skills,
	}))

	counter := &wholeConfigRowCounter{Repository: repo}
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, counter, starter)

	_, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
		Presets: map[string]string{"default": "plan-first"},
		Params:  mockModelParams(t), Messages: userSeed("go"),
	})
	require.NoError(t, err)

	assert.Zero(t, counter.reads,
		"a chat start must read the config columns it needs, never the whole row")
	assert.Equal(t, "plan", starter.lastRootInput(t).Inputs["mode"],
		"the project's preset must still be applied")
}
