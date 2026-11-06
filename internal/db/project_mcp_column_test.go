package db

import (
	"context"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

func TestProjectMCPConfigsColumnRead(t *testing.T) {
	t.Parallel()

	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	mcp := `{"global":{"mcpServers":{"a":{"command":"x"}}}}`
	skills := `[{"name":"big"}]`
	if err := repo.UpsertProjectConfigRecord(ctx, &ProjectConfigRecord{
		ProjectID: "test-project", DaemonID: "daemon-1", MCPConfigs: &mcp, ProjectSkillsJSON: &skills,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := repo.GetProjectMCPConfigsJSON(ctx, "test-project")
	if err != nil || got == nil || *got != mcp {
		t.Fatalf("GetProjectMCPConfigsJSON = %v, %v; want %q", got, err, mcp)
	}
	if _, err := repo.GetProjectMCPConfigsJSON(ctx, "no-such-project"); !svcerr.IsNotFound(err) {
		t.Fatalf("no record: err = %v; want svcerr.NotFound", err)
	}
}
