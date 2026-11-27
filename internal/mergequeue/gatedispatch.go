// Copyright (c) 2025 Reliant Labs
package mergequeue

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun"
)

// DispatchGate is the [Gate] over the gate runner (gaterun.Dispatcher, M9):
// the gate runs on a member of the repository's gate pool. A dispatch error
// is the unproven result gaterun.ResultFromError says it is.
type DispatchGate struct {
	Dispatcher gaterun.Dispatcher
}

// Run implements Gate.
func (g DispatchGate) Run(ctx context.Context, req GateRequest) (GateResult, error) {
	if g.Dispatcher == nil {
		return GateResult{}, errors.New("mergequeue: DispatchGate needs a Dispatcher")
	}
	res, err := g.Dispatcher.Dispatch(ctx, gaterun.GateRequest{
		RepoID: req.RepoID, BatchID: req.BatchID, Attempt: req.Attempt,
		CandidateRef: req.CandidateRef, CandidateSHA: req.CandidateSHA, BaseSHA: req.BaseSHA,
		Gate: req.Gate, Shadow: req.Shadow, AllowUnisolated: req.Gate.AllowUnisolated,
	})
	if err != nil {
		if ctx.Err() != nil {
			return GateResult{}, ctx.Err()
		}
		res = gaterun.ResultFromError(gaterun.GateID(req.BatchID, req.Attempt), err)
	}
	out := GateResult{
		Class: GateClass(res.Class), Isolation: string(res.Isolation), Machine: res.Machine,
		ReasonCode: res.ReasonCode, Reason: res.Reason,
		FailingTests: res.FailingTests, InfraMatches: res.InfraMatches,
	}
	if len(res.Stages) > 0 {
		if b, err := json.Marshal(res.Stages); err == nil {
			out.Stages = b
		}
	}
	return out, nil
}

// GateRepos is gaterun.RepoReader over the queue's store: what a gate needs
// of an enrolled repository (pipeline_repos).
type GateRepos struct {
	Store interface {
		GetPipelineRepo(ctx context.Context, id string) (*core.PipelineRepo, error)
	}
}

var _ gaterun.RepoReader = GateRepos{}

// GateRepo implements gaterun.RepoReader. The machine pool's owner is the
// user who enrolled the repository.
func (g GateRepos) GateRepo(ctx context.Context, repoID string) (gaterun.GateRepo, error) {
	r, err := g.Store.GetPipelineRepo(ctx, repoID)
	if err != nil {
		return gaterun.GateRepo{}, err
	}
	if r == nil {
		return gaterun.GateRepo{}, errors.New("mergequeue: no enrolled repository " + repoID)
	}
	return gaterun.GateRepo{Repository: r.Repository, ProjectID: r.ProjectID, OwnerUserID: r.CreatedByUserID, Mode: r.Mode}, nil
}
