// Copyright (c) 2025 Reliant Labs
package gaterun

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// Dispatcher runs a batch's gate on a member of the repository's gate pool
// and returns its verdict. [NewDispatcher] implements it; the queue's
// scheduler (M7) takes it as a dependency.
//
// An error means no verdict was reached; it is unproven, and
// [ResultFromError] turns it into the GateResult to record. A result is
// landable only when [GateResult.Landable] says so.
//
//forge:contract
type Dispatcher interface {
	Dispatch(ctx context.Context, req GateRequest) (GateResult, error)
}

// GateRequest is one gate of one batch candidate (the queue ↔ gate seam of
// the MVP contracts).
type GateRequest struct {
	// RepoID is the enrolled repository (pipeline_repos.id). The Dispatcher
	// reads its name, project, owner and mode from it.
	RepoID string
	// BatchID and Attempt name the gate: one batch is gated again (a rerun,
	// a retry on another member) under a new Attempt (0 is 1). Together they
	// are the gate id, the idempotency key of every daemon command.
	BatchID string
	Attempt int
	// CandidateRef is the branch the candidate builder pushed,
	// "reliant-queue/<batch>", and CandidateSHA the commit it must hold.
	// A ref that no longer points at the sha is unproven.
	CandidateRef string
	CandidateSHA string
	// BaseSHA is the batch's base, the main commit the candidate is built
	// on (queue_batches.base_sha). The member fetches it beside the
	// candidate and the gate's environment names it (GATE_BASE, CI_BASE),
	// so affected-only lanes diff against it. "" fetches the candidate
	// alone.
	BaseSHA string
	// Gate is the repository's resolved gate (pipeline.yaml from the base
	// branch): its stages, and the pool, timeout, env and infra patterns
	// the gate also needs.
	Gate config.ResolvedGate
	// Timeout bounds the gate's wall time inside the boundary; 0 is
	// Gate.Timeout.
	Timeout time.Duration
	// Env is added over Gate.Env (RELIANT_QUARANTINE, say). Never a secret:
	// anyone who can inspect the gate's container can read it.
	Env map[string]string
	// Shadow marks a shadow-mode gate: it computes and records, and its
	// batch never lands. Only a shadow gate of a repository whose stored
	// mode is shadow may run on the not-isolated runc backend.
	Shadow bool
	// AllowUnisolated is the repository's opt-in (pipeline.yaml
	// gate.allow_unisolated, owner decision 2026-11-26) to gate a landing
	// batch on runc until isolated gate machines exist. The caller marks
	// such a batch's verdict unisolated_accepted, which is the only way a
	// runc batch may land.
	AllowUnisolated bool
}
