// Copyright (c) 2025 Reliant Labs
package mergequeue

import (
	"context"
	"encoding/json"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
	"github.com/reliant-labs/reliant/internal/mergequeue/gitport"
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

// Workdir is where the queue builds one repository's candidates: a git
// directory it owns, the remote it fetches from and pushes to (credentials in
// the git environment, never the URL), and the GitHub client it reads with.
type Workdir struct {
	Git    gitport.Port
	Remote string
	GitHub ghport.Client
}

// Workdirs opens a repository's [Workdir]. A worker keeps one per repository
// it leads; the scheduler asks for it at the start of every tick.
type Workdirs interface {
	Open(ctx context.Context, repo *core.PipelineRepo) (Workdir, error)
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
