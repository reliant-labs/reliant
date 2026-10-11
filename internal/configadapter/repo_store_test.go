package configadapter

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
)

// recordReadCounter counts reads of the whole project config record.
type recordReadCounter struct {
	*db.Repo
	reads atomic.Int64
}

func (c *recordReadCounter) GetProjectConfigRecord(ctx context.Context, projectID string) (*db.ProjectConfigRecord, error) {
	c.reads.Add(1)
	return c.Repo.GetProjectConfigRecord(ctx, projectID)
}

// The worker's wiring (serverworker): a cached provider over the real
// repository. N CallLLM turns read and parse the multi-MB record once, and a
// daemon push — written by whichever server holds the daemon's connection —
// is picked up on the very next turn through the version column.
func TestCachedProviderOverRepo_ReadsTheRecordOncePerPush(t *testing.T) {
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	push := func(skillName string) {
		t.Helper()
		skills := `[{"skill_path":"` + skillName + `","name":"` + skillName + `","body":"` + strings.Repeat("x", 256<<10) + `"}]`
		require.NoError(t, repo.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
			ProjectID: "test-project", DaemonID: "daemon-1", ProjectSkillsJSON: &skills,
		}))
	}
	counter := &recordReadCounter{Repo: repo}
	provider := config.NewCachedStoredConfigProvider(NewRepoConfigStore(counter), counter, 0)
	ref := config.ProjectRef{ProjectID: "test-project"}

	push("first")
	for range 20 {
		cfg, err := provider.GetProjectConfig(ctx, ref)
		require.NoError(t, err)
		require.Len(t, cfg.Skills, 1)
		require.Equal(t, "first", cfg.Skills[0].SkillPath)
	}
	require.EqualValues(t, 1, counter.reads.Load())

	push("second")
	cfg, err := provider.GetProjectConfig(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, "second", cfg.Skills[0].SkillPath)
	require.EqualValues(t, 2, counter.reads.Load())
}
