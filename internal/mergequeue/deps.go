// Copyright (c) 2025 Reliant Labs
package mergequeue

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// Store is the queue's state: every method is *db.Repo's of the same name
// (internal/db/pipeline_*.go), whose docs say what each locks and returns.
type Store interface {
	GetPipelineRepo(ctx context.Context, id string) (*core.PipelineRepo, error)
	SetPipelineRepoQueueState(ctx context.Context, id string, version int64, state core.PipelineQueueState, pausedBy string) (*core.PipelineRepo, error)

	GetQueuePRRecord(ctx context.Context, id string) (*core.PRRecord, error)
	ListQueueHeadChecks(ctx context.Context, recordID, head string) ([]core.PRRecordCheck, error)
	LatestPRHeadVerdict(ctx context.Context, recordID, head string) (*core.PRHeadVerdictRecord, error)
	InsertPRHeadVerdict(ctx context.Context, v core.PRHeadVerdictRecord) (*core.PRHeadVerdictRecord, error)
	ActivePipelineDecisionsFor(ctx context.Context, kind core.PipelineDecisionSubjectKind, subjectID, head string, now time.Time) ([]*core.PipelineDecision, error)
	ActivePipelineHold(ctx context.Context, recordID string, now time.Time) (*core.PipelineDecision, error)
	GetPRPipeline(ctx context.Context, recordID string) (*core.PRPipeline, error)
	TransitionPRPipeline(ctx context.Context, recordID string, version int64, to core.PRPipeline) (*core.PRPipeline, error)

	EnqueueQueueEntry(ctx context.Context, e core.QueueEntryNew) (*core.QueueEntry, bool, error)
	ListAdmissibleQueueEntries(ctx context.Context, repoID string, now time.Time, limit int) ([]*core.QueueEntry, error)
	ListStaleQueueEntries(ctx context.Context, repoID string) ([]core.StaleQueueEntry, error)
	EjectQueueEntry(ctx context.Context, id string, reason core.QueueEjectReason) (*core.QueueEntry, error)
	RequeueQueueEntries(ctx context.Context, batchID string) (int64, error)
	ListQueueBatchEntries(ctx context.Context, batchID string) ([]*core.QueueEntry, error)

	CreateQueueBatch(ctx context.Context, b core.QueueBatchNew) (*core.QueueBatch, error)
	GetQueueBatch(ctx context.Context, id string) (*core.QueueBatch, error)
	LatestQueueBatches(ctx context.Context, repoID string, limit int) ([]*core.QueueBatch, error)
	AttachQueueEntries(ctx context.Context, batchID string, entryIDs []string) error
	SetQueueBatchState(ctx context.Context, id string, from, to core.QueueBatchState, u core.QueueBatchUpdate) (*core.QueueBatch, error)
	InsertMainHealth(ctx context.Context, h core.MainHealthRecord) (*core.MainHealthRecord, error)
}

// Builder runs the queue's code operations. The queue holds no repository
// bytes (owner, 2026-11-27): building a batch's candidate and listing a
// head's changed paths are jobs on a daemon of the repository's gate pool,
// which reports back only ids, paths and verdicts (buildjob). An error means
// the job reached no answer; a retry with the same request is safe.
type Builder interface {
	Build(ctx context.Context, repo *core.PipelineRepo, req buildjob.BuildRequest) (buildjob.BuildResult, error)
	ChangedPaths(ctx context.Context, repo *core.PipelineRepo, req buildjob.PathsRequest) (buildjob.PathsResult, error)
}

// Refs reads a branch's tip commit through the GitHub API: metadata, never
// repository contents.
type Refs interface {
	BranchTip(ctx context.Context, repo *core.PipelineRepo, branch string) (string, error)
}

// ClientRefs is [Refs] over a per-repository GitHub client.
type ClientRefs struct {
	Clients interface {
		Client(ctx context.Context, repo *core.PipelineRepo) (ghport.Client, error)
	}
}

// BranchTip implements Refs.
func (c ClientRefs) BranchTip(ctx context.Context, repo *core.PipelineRepo, branch string) (string, error) {
	gh, err := ghport.ParseRepo(repo.Repository)
	if err != nil {
		return "", err
	}
	client, err := c.Clients.Client(ctx, repo)
	if err != nil {
		return "", err
	}
	ref, err := client.GetRef(ctx, gh, ghport.BranchRef(branch))
	if err != nil {
		return "", err
	}
	return ref.SHA, nil
}

// NoBuilder is the [Builder] of a server with no gate pool daemon to run
// code operations on: every job is refused, so nothing is built and an
// entry needing its changed paths is not admitted.
type NoBuilder struct{}

// ErrNoBuilder is every [NoBuilder] job.
var ErrNoBuilder = errors.New("mergequeue: no gate pool daemon runs this repository's code operations")

// Build implements Builder.
func (NoBuilder) Build(context.Context, *core.PipelineRepo, buildjob.BuildRequest) (buildjob.BuildResult, error) {
	return buildjob.BuildResult{}, ErrNoBuilder
}

// ChangedPaths implements Builder.
func (NoBuilder) ChangedPaths(context.Context, *core.PipelineRepo, buildjob.PathsRequest) (buildjob.PathsResult, error) {
	return buildjob.PathsResult{}, ErrNoBuilder
}

// Configs resolves a repository's pipeline.yaml, as read from its base
// branch and recorded in pipeline_repos.config_json.
type Configs interface {
	Resolved(ctx context.Context, repo *core.PipelineRepo) (config.Resolved, error)
}

// StoredConfig is the [Configs] that reads pipeline_repos.config_json: the
// pipeline.yaml document as JSON (a JSON document is YAML, so config.Load
// reads it), or the defaults when none has been recorded yet.
type StoredConfig struct{}

// Resolved implements Configs.
func (StoredConfig) Resolved(_ context.Context, repo *core.PipelineRepo) (config.Resolved, error) {
	if len(repo.ConfigJSON) == 0 || string(repo.ConfigJSON) == "null" {
		return config.Defaults(), nil
	}
	return config.Load(repo.ConfigJSON)
}

// GateClass is a gate's verdict (§3.4): the exit contract's 0, 1 and 2.
type GateClass string

const (
	GatePass     GateClass = "pass"
	GateFail     GateClass = "fail"
	GateUnproven GateClass = "unproven"
)

// GateRequest is one gate of one batch's candidate: the queue's half of the
// MVP contract's gaterun.GateRequest.
type GateRequest struct {
	RepoID, BatchID string
	// Attempt counts gates of this batch, from 1: a retry on another member
	// is the next.
	Attempt      int
	CandidateRef string
	CandidateSHA string
	// BaseSHA is the batch's base (queue_batches.base_sha): what the gate
	// diffs the candidate against. "" for a gate of main's own tip.
	BaseSHA string
	Gate    config.ResolvedGate
	// Shadow marks a gate whose batch never lands.
	Shadow bool
	// AvoidMachines are members that already left this batch unproven: a
	// retry asks for another one.
	AvoidMachines []string
	// PreferMachine is the member that built the candidate, to try first:
	// its git cache already holds it. "" prefers none.
	PreferMachine string
}

// GateResult is a gate's verdict: the queue's half of the MVP contract's
// gaterun.GateResult.
type GateResult struct {
	Class GateClass
	// Isolation is the backend it ran in: sysbox and vm are isolated; runc
	// (shadow only) is not.
	Isolation string
	// Machine is the member that ran it.
	Machine    string
	ReasonCode string
	Reason     string
	// FailingTests and InfraMatches are the gate's own report.
	FailingTests []string
	InfraMatches []string
	// Stages is the per-stage summary, recorded as queue_batches.stages_json
	// (an object).
	Stages json.RawMessage
}

// Isolated reports whether the gate ran inside the isolation boundary §3.3
// requires.
func (r GateResult) Isolated() bool { return r.Isolation == "sysbox" || r.Isolation == "vm" }

// Landable is the hard rule: a batch lands only on a pass produced inside an
// isolation boundary. A runc gate never lands, whatever it says.
func (r GateResult) Landable() bool { return r.Class == GatePass && r.Isolated() }

// Gate runs a batch's gate. An error means no verdict was reached: the queue
// records it as unproven.
type Gate interface {
	Run(ctx context.Context, req GateRequest) (GateResult, error)
}

// LandRequest is a passed batch for the lander, in landing order.
type LandRequest struct {
	Repo    *core.PipelineRepo
	Batch   *core.QueueBatch
	Entries []*core.QueueEntry
	// Heads are the heads to merge, by entry id: the reviewed head, or a
	// regenerated entry's new head.
	Heads map[string]string
	// Trees are T_1..T_n, checked after each squash merge.
	Trees []string
}

// Lander lands a passed batch on main (M11). It is called only in mode on,
// and only for a landable gate result; [NoLander] is the shadow one.
type Lander interface {
	Land(ctx context.Context, req LandRequest) error
}

// NoLander lands nothing: the lander of a queue that only shadows.
type NoLander struct{}

// Land implements Lander.
func (NoLander) Land(context.Context, LandRequest) error { return nil }
