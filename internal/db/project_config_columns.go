package db

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/store"
)

// GetProjectConfigPushedAt returns when the daemon pushed the project's
// current config record (pushed_at), without reading any of its payload. It
// returns sql.ErrNoRows, unwrapped, when the project has no record, like
// GetProjectConfigRecord. See store.GetProjectConfigPushedAt.
func (r *Repo) GetProjectConfigPushedAt(ctx context.Context, projectID string) (time.Time, error) {
	if projectID == "" {
		return time.Time{}, fmt.Errorf("project ID cannot be empty")
	}
	return store.GetProjectConfigPushedAt(ctx, r.ORM(), projectID)
}

// GetProjectScenariosJSON reads one column of the project config record: the
// synced .reliant scenarios. It returns sql.ErrNoRows, unwrapped, when the
// project has no record.
func (r *Repo) GetProjectScenariosJSON(ctx context.Context, projectID string) (*string, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	return store.GetProjectScenariosJSON(ctx, r.ORM(), projectID)
}

// GetProjectConfigVersion returns an opaque token that changes whenever the
// project's config record is rewritten, without reading any of its payload.
// It returns sql.ErrNoRows, unwrapped, when the project has no record, like
// GetProjectConfigRecord. See store.GetProjectConfigVersion for what the
// token is made of and why.
func (r *Repo) GetProjectConfigVersion(ctx context.Context, projectID string) (string, error) {
	if projectID == "" {
		return "", fmt.Errorf("project ID cannot be empty")
	}
	return store.GetProjectConfigVersion(ctx, r.ORM(), projectID)
}
