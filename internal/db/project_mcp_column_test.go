package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestProjectMCPConfigsColumnRead(t *testing.T) {
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
	if _, err := repo.GetProjectMCPConfigsJSON(ctx, "no-such-project"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("no record: err = %v; want sql.ErrNoRows", err)
	}
}
