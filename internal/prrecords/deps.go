// forge:deps — consumer-declared interfaces for this package's Deps.
package prrecords

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/ghcontents"
)

// Store is where records are kept. store.PrRecordExtStore satisfies it.
type Store interface {
	ApplyPRRecordChange(ctx context.Context, c core.PRRecordChange) (bool, error)
}

// PipelineStore is the PR pipeline's state the projector keeps for enrolled
// repositories: their enrolment and config, and each PR's pr_pipeline row.
// *db.Repo satisfies it.
type PipelineStore interface {
	GetPipelineRepoByRepository(ctx context.Context, provider, repositoryID string) (*core.PipelineRepo, error)
	GetPipelineRepo(ctx context.Context, id string) (*core.PipelineRepo, error)
	RecordPipelineRepoConfig(ctx context.Context, id string, version int64, sha string, config []byte, changes []core.PipelineConfigChange) (*core.PipelineRepo, error)
	SetPipelineRepoConfigError(ctx context.Context, id string, version int64, sha, reason string) (*core.PipelineRepo, error)
	GetPRRecordByNumber(ctx context.Context, provider, repositoryID string, number int) (*core.PRRecord, error)
	GetPRRecordByMergeCommit(ctx context.Context, provider, repositoryID, sha string) (*core.PRRecord, error)
	GetPRPipeline(ctx context.Context, recordID string) (*core.PRPipeline, error)
	EnsurePRPipeline(ctx context.Context, p core.PRPipelineNew) (*core.PRPipeline, bool, error)
	ResetPRPipelineForHead(ctx context.Context, recordID string, version int64, head string, next core.PRPipelineNext) (*core.PRPipeline, error)
	TransitionPRPipeline(ctx context.Context, recordID string, version int64, to core.PRPipeline) (*core.PRPipeline, error)
}

// Contents reads one repository file at a commit. ghcontents.Service
// satisfies it.
type Contents interface {
	Read(ctx context.Context, req ghcontents.Request) ([]byte, error)
}
