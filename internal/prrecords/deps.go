// forge:deps — consumer-declared interfaces for this package's Deps.
package prrecords

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/db/core"
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
	SetPipelineRepoConfigPending(ctx context.Context, id string, version int64, p core.PipelineConfigPending) (*core.PipelineRepo, error)
	GetPRRecordByNumber(ctx context.Context, provider, repositoryID string, number int) (*core.PRRecord, error)
	GetPRPipeline(ctx context.Context, recordID string) (*core.PRPipeline, error)
	EnsurePRPipeline(ctx context.Context, p core.PRPipelineNew) (*core.PRPipeline, bool, error)
	ResetPRPipelineForHead(ctx context.Context, recordID string, version int64, head string, next core.PRPipelineNext) (*core.PRPipeline, error)
	TransitionPRPipeline(ctx context.Context, recordID string, version int64, to core.PRPipeline) (*core.PRPipeline, error)
}

// Contents reads one repository file at a commit. No server role reads it
// itself: a server holds no repository bytes (owner, 2026-11-27), so
// pipeline.yaml is read by a gate pool daemon and reported back (jobrun's
// ConfigReader), for the [ConfigRefresher]. A read waits on a daemon, so
// it never runs in webhook delivery.
type Contents interface {
	Read(ctx context.Context, req ContentsRequest) ([]byte, error)
}

// ContentsRequest names one file at one commit.
type ContentsRequest struct {
	// UserID is the Reliant user the read acts for; OrgID, when set, the
	// org it acts in.
	UserID, OrgID string
	Owner, Repo   string
	// Path is repository-relative, with no leading slash.
	Path string
	// Ref is a commit sha.
	Ref string
}

// ErrContentsNotFound is a [Contents] read of a file that is not at the ref.
var ErrContentsNotFound = errors.New("prrecords: no such file at that ref")
