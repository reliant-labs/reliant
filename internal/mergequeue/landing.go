// Copyright (c) 2025 Reliant Labs
package mergequeue

import (
	"context"
	"errors"
	"slices"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
	"github.com/reliant-labs/reliant/internal/mergequeue/lander"
)

// LanderAdapter is the scheduler's [Lander] over the M11 lander.
type LanderAdapter struct {
	Lander *lander.Lander
}

// Land implements Lander. The lander records its outcome on the batch.
func (a LanderAdapter) Land(ctx context.Context, req LandRequest) error {
	if a.Lander == nil {
		return errors.New("mergequeue: LanderAdapter needs a Lander")
	}
	_, err := a.Lander.Land(ctx, lander.Request(req))
	return err
}

// GateVerifier is lander.Verifier over the queue's [Gate]: after an
// integrity failure, it gates main's own tree at its tip.
type GateVerifier struct {
	Gate    Gate
	Configs Configs
}

var _ lander.Verifier = GateVerifier{}

// Verify implements lander.Verifier.
func (v GateVerifier) Verify(ctx context.Context, req lander.VerifyRequest) (lander.VerifyResult, error) {
	configs := v.Configs
	if configs == nil {
		configs = StoredConfig{}
	}
	cfg, err := configs.Resolved(ctx, req.Repo)
	if err != nil {
		return lander.VerifyResult{}, err
	}
	// Main's own tip has no batch over a base: no BaseSHA, so the gate
	// fetches the tip alone.
	res, err := v.Gate.Run(ctx, GateRequest{
		RepoID: req.Repo.ID, BatchID: req.Batch.ID, Attempt: req.Batch.Attempt,
		CandidateRef: req.Repo.BaseBranch, CandidateSHA: req.SHA, Gate: cfg.Gate,
	})
	if err != nil {
		return lander.VerifyResult{}, err
	}
	out := lander.VerifyResult{Class: lander.VerifyClass(res.Class), Machine: res.Machine, Reason: res.Reason}
	if iso := core.QueueIsolationBackend(res.Isolation); slices.Contains(core.AllQueueIsolationBackends, iso) {
		out.Isolation = iso
	}
	return out, nil
}

// Credentials is how the queue reaches GitHub for one repository:
// queuetoken's Clients (the client) and Source (the token git uses).
type Credentials interface {
	Client(ctx context.Context, repo *core.PipelineRepo) (ghport.Client, lander.Identity, error)
}

// TokenSource is the token git fetches and pushes with.
type TokenSource func(ctx context.Context, repo *core.PipelineRepo) (string, error)

// AccessOf is the [GitHubAccess] over the lander's credentials and a token
// source: the same identity reads, builds and lands.
func AccessOf(c Credentials, token TokenSource) GitHubAccess {
	return func(ctx context.Context, repo *core.PipelineRepo) (ghport.Client, string, error) {
		client, _, err := c.Client(ctx, repo)
		if err != nil {
			return nil, "", err
		}
		tok, err := token(ctx, repo)
		if err != nil {
			return nil, "", err
		}
		return client, tok, nil
	}
}
