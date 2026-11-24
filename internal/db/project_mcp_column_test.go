package db

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/store"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

func TestProjectMCPConfigsColumnRead(t *testing.T) {
	t.Parallel()

	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	mcp := `{"global":{"mcpServers":{"a":{"command":"x"}}}}`
	skills := `[{"name":"big"}]`
	if err := store.UpsertProjectConfigRecord(ctx, repo.ORM(), &ProjectConfigRecord{
		ProjectID: "test-project", DaemonID: "daemon-1", MCPConfigs: &mcp, ProjectSkillsJSON: &skills,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := store.GetProjectMCPConfigsJSON(ctx, repo.ORM(), "test-project")
	if err != nil || got == nil || *got != mcp {
		t.Fatalf("GetProjectMCPConfigsJSON = %v, %v; want %q", got, err, mcp)
	}
	if _, err := store.GetProjectMCPConfigsJSON(ctx, repo.ORM(), "no-such-project"); !svcerr.IsNotFound(err) {
		t.Fatalf("no record: err = %v; want svcerr.NotFound", err)
	}
}
