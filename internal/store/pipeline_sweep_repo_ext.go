package store

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// sweepLimit bounds one repository's rows a daily PR sweep reads: far above
// any repository's open PRs, so a runaway is visible rather than unbounded.
const sweepLimit = 2000

// ListOpenPRPipelines lists the pipeline rows of a repository's PRs that are
// still open to the pipeline (every state but landed and closed), oldest
// update first: what the daily PR sweep looks over. It reads
// pr_pipeline_repo_idx.
func ListOpenPRPipelines(ctx context.Context, db orm.Context, repoID string) ([]*core.PRPipeline, error) {
	var out []*core.PRPipeline
	if err := db.Bun().NewRaw(`SELECT `+prPipelineColumns+` FROM pr_pipeline
		WHERE repo_id = ? AND state NOT IN (?, ?)
		ORDER BY updated_at, record_id LIMIT ?`,
		repoID, core.PRPipelineLanded, core.PRPipelineClosed, sweepLimit).Scan(ctx, &out); err != nil {
		return nil, fmt.Errorf("list open pr pipelines: %w", err)
	}
	return out, nil
}
