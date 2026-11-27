// Copyright (c) 2025 Reliant Labs

package app

import (
	"log/slog"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/pipeline/grants"
	"github.com/reliant-labs/reliant/internal/store"
)

// pipelineGrants is the pipeline's capability grants over repo
// (internal/pipeline/grants): Authorize for pr.verdict (and, once the merge
// queue's lander is wired, for land), GrantApproved for the approval
// service, List and Revoke for PipelineService, and the
// reliant/pipeline.request_grant@1 server action on the worker. It reads and
// writes nothing until the pipeline.enabled switch lets a caller reach it.
func pipelineGrants(repo *db.Repo) (*grants.Service, error) {
	return grants.New(grants.Deps{
		Store:  store.NewPipelineGrantExtStore(repo.ORM()),
		Cards:  repo,
		Logger: slog.Default(),
	})
}
