package core

import (
	"encoding/json"
	"errors"
	"time"
)

// The pipeline's rows, as the store reads and writes them
// (internal/store/pipeline_*_repo_ext.go, the internal/db facade over them).
// The vocabularies are the closed sets beside this file.

// Errors the pipeline store returns. Each is returned wrapped; test with
// errors.Is. None aborts the caller's transaction: every one is decided from
// a statement that succeeded (no row matched, or ON CONFLICT DO NOTHING).
var (
	// ErrPipelineVersionConflict is a compare-and-set that lost: the row is
	// no longer at the version (or in the state) the caller read, or it is
	// gone. Re-read it and decide again.
	ErrPipelineVersionConflict = errors.New("pipeline: the row changed since it was read")
	// ErrPipelineVerdictExists is a second verdict for one (record, head,
	// round): verdicts are append-only, so a new one needs a new round.
	ErrPipelineVerdictExists = errors.New("pipeline: a verdict for this head and round already exists")
	// ErrPipelineAttemptConflict is an attempt number another caller kept
	// taking first: the attempt is being started concurrently.
	ErrPipelineAttemptConflict = errors.New("pipeline: the attempt was started concurrently")
	// ErrPipelineRepoEnrolledElsewhere is an enrolment of a repository
	// another project has enrolled. Moving it is refused (G1).
	ErrPipelineRepoEnrolledElsewhere = errors.New("pipeline: the repository is enrolled by another project")
	// ErrQueueBatchTransition is a batch state change §3.2 does not allow
	// (QueueBatchState.CanTransitionTo).
	ErrQueueBatchTransition = errors.New("queue: illegal batch state transition")
	// ErrQueueEntryOtherHead is an enqueue of a PR that is already in the
	// queue at another head: eject the old entry first.
	ErrQueueEntryOtherHead = errors.New("queue: the PR is queued at another head")
	// ErrQueueEntryNotAttachable is an entry that cannot join the batch: it
	// is not waiting (nor batched in the batch's parent), it is another
	// repository's, or the batch is no longer building.
	ErrQueueEntryNotAttachable = errors.New("queue: entry cannot be attached to the batch")
)

// PipelineRepo is one enrolled repository (pipeline_repos).
type PipelineRepo struct {
	ID, OrgID, ProjectID string
	Provider, AccountKey string
	RepositoryID         string
	Repository           string
	BaseBranch           string
	Mode                 PipelineRepoMode
	// ConfigSHA and ConfigJSON are the validated .reliant/pipeline.yaml at
	// that sha of BaseBranch; empty until it has been read. ConfigJSON is
	// the file as JSON: JSON is YAML, so config.Parse (or config.Load)
	// reads it back.
	ConfigSHA  string
	ConfigJSON json.RawMessage
	// ConfigError is why the file read from BaseBranch at ConfigErrorSHA
	// was not recorded (missing, unreadable, invalid); the config above
	// stayed in force. Empty once a valid file is recorded.
	ConfigError, ConfigErrorSHA string
	// ConfigPending is the base-branch commit whose pipeline.yaml a push
	// left to read, and its read's progress; the zero value when none is.
	ConfigPending           PipelineConfigPending `bun:"embed:config_pending_"`
	QueueState              PipelineQueueState
	QueuePausedByDecisionID string
	CreatedByUserID         string
	CreatedAt, UpdatedAt    time.Time
	// Version is the compare-and-set counter: every Set* names the version
	// it read.
	Version int64
}

// PipelineConfigPending is a pipeline.yaml read a base-branch push left to
// do (pipeline_repos.config_pending_*): the projector records it in webhook
// delivery, and a worker loop reads the file on a gate pool daemon.
type PipelineConfigPending struct {
	// SHA is the commit to read the file at; "" when nothing is pending.
	SHA string
	// At is when the push was projected.
	At *time.Time
	// SenderID and SenderLogin are the push's sender, whom a gate stage
	// change the file carries is attributed to when no merged PR is.
	SenderID, SenderLogin string
	// Attempts counts the reads started.
	Attempts int
	// RetryAt is when it is next due.
	RetryAt *time.Time
}

// PipelineRepoEnrolment is what enrolling a repository writes. A new row
// starts with mode off and a running queue.
type PipelineRepoEnrolment struct {
	// ID names a new row; empty generates one. Ignored on re-enrolment.
	ID, OrgID, ProjectID string
	Provider, AccountKey string
	RepositoryID         string
	Repository           string
	// BaseBranch defaults to main.
	BaseBranch      string
	CreatedByUserID string
}

// PipelineConfigChange is a gate stage a pipeline.yaml change on the base
// branch turned off or back on: one pipeline_setting_changes row with
// changed_via config, recorded with the config that carried it. The
// repository, its org and project, and the config sha are the config's.
type PipelineConfigChange struct {
	Setting            PipelineSetting
	OldValue, NewValue string
	// ConfigRecordID is the PR that landed the change; empty when it is
	// not known (a direct push).
	ConfigRecordID string
	// The actor at the provider: the merging PR's author, else whoever
	// pushed. All three are required.
	ChangedByProvider, ChangedByProviderUserID, ChangedByLogin string
}

// PRPipeline is one PR's place in the pipeline's state machine
// (pr_pipeline). The fields after Version are the mutable ones a transition
// writes; the rest are fixed when the row is created.
type PRPipeline struct {
	RecordID, OrgID, ProjectID, RepoID string
	CreatedAt, UpdatedAt               time.Time
	Version                            int64

	State             PRPipelineState
	StateHeadSHA      string
	Priority          int
	PrioritySource    PRPrioritySource
	ReviewRound       int
	FixAttempts       int
	FixSignature      string
	OwnerRunID        string
	NextAction        string
	NextActionAt      *time.Time
	NeedsPersonReason string
	NeedsPersonSince  *time.Time
}

// PRPipelineNew is a PR's first row. Priority 0 with an empty source is the
// default (50, default).
type PRPipelineNew struct {
	RecordID, OrgID, ProjectID, RepoID string
	// State is draft or awaiting_review for a new PR.
	State          PRPipelineState
	StateHeadSHA   string
	Priority       int
	PrioritySource PRPrioritySource
	NextAction     string
	NextActionAt   *time.Time
}

// PRPipelineNext is what a PR waits for next, and when it is due. The zero
// value waits for nothing.
type PRPipelineNext struct {
	Action string
	At     *time.Time
}

// PRHeadVerdictRecord is one review verdict on one head (pr_head_verdicts).
type PRHeadVerdictRecord struct {
	// ID names a new row; empty generates one.
	ID, OrgID, ProjectID, RecordID string
	HeadSHA                        string
	Round                          int
	Verdict                        PRHeadVerdict
	Rule                           PRVerdictRule
	RiskTier                       PRRiskTier
	// FindingsJSON defaults to [].
	FindingsJSON      json.RawMessage
	RequiredDecisions []PipelineDecisionKind
	RunID, NodeID     string
	ReviewerPrincipal string
	// TriggerID and DaemonID are the activation whose run wrote a model
	// verdict and the machine it ran on; "" for a structural verdict and
	// for every row written before they were recorded.
	TriggerID, DaemonID string
	CreatedAt           time.Time
}

// PipelineAttempt is one review, fix, gate or respond attempt
// (pipeline_attempts).
type PipelineAttempt struct {
	ID, OrgID, ProjectID string
	SubjectKind          PipelineAttemptSubjectKind
	SubjectID            string
	HeadSHA              string
	Stage                PipelineAttemptStage
	Signature            string
	Attempt              int
	RunID                string
	// Outcome is empty while the attempt runs.
	Outcome    PipelineAttemptOutcome
	DetailJSON json.RawMessage
	StartedAt  time.Time
	FinishedAt *time.Time
}

// PipelineDecision is one owner decision (pipeline_decisions), as read.
type PipelineDecision struct {
	ID, OrgID, ProjectID string
	SubjectKind          PipelineDecisionSubjectKind
	SubjectID            string
	HeadSHA              string
	Kind                 PipelineDecisionKind
	State                PipelineDecisionState
	ValueJSON            json.RawMessage
	Reason               string
	RequestedByRunID     string
	RequestedByPrincipal string
	DecidedByUserID      string
	DecidedVia           PipelineDecidedVia
	PolicyChangeID       string
	CreatedAt            time.Time
	DecidedAt, ExpiresAt *time.Time
	RevokedAt            *time.Time
	RevokedByUserID      string
}

// QueueEntry is one approved PR head in the merge queue (queue_entries).
type QueueEntry struct {
	ID, OrgID, ProjectID, RepoID, RecordID string
	HeadSHA                                string
	Priority                               int
	EnqueuedAt                             time.Time
	BatchID                                string
	State                                  QueueEntryState
	EjectReason                            QueueEjectReason
	LandedSHA                              string
	LandedAt                               *time.Time
	UpdatedAt                              time.Time
}

// QueueEntryNew is an enqueue.
type QueueEntryNew struct {
	// ID names a new row; empty generates one.
	ID, OrgID, ProjectID, RepoID, RecordID string
	HeadSHA                                string
	Priority                               int
}

// QueueBatch is one candidate a gate judges (queue_batches).
type QueueBatch struct {
	ID, OrgID, ProjectID, RepoID string
	BaseSHA                      string
	CandidateSHA, CandidateRef   string
	ParentBatchID                string
	Kind                         QueueBatchKind
	ExpectedTreesJSON            json.RawMessage
	LandedSHA                    string
	IsolationBackend             QueueIsolationBackend
	GateMachineID                string
	State                        QueueBatchState
	GateRunner                   QueueGateRunner
	GateRunID                    string
	CheckNames                   []string
	Attempt                      int
	VerdictJSON                  json.RawMessage
	StagesJSON                   json.RawMessage
	StartedAt                    time.Time
	FinishedAt                   *time.Time
	UpdatedAt                    time.Time
}

// QueueBatchNew is a batch being built: it starts in state building.
type QueueBatchNew struct {
	// ID names a new row; empty generates one.
	ID, OrgID, ProjectID, RepoID string
	BaseSHA                      string
	// ParentBatchID is required for a bisect half.
	ParentBatchID string
	// Kind defaults to normal.
	Kind QueueBatchKind
}

// QueueBatchUpdate is what a state change records alongside the state. A
// zero field keeps the stored value.
type QueueBatchUpdate struct {
	CandidateSHA, CandidateRef string
	ExpectedTreesJSON          json.RawMessage
	IsolationBackend           QueueIsolationBackend
	GateMachineID              string
	GateRunner                 QueueGateRunner
	GateRunID                  string
	CheckNames                 []string
	VerdictJSON                json.RawMessage
	StagesJSON                 json.RawMessage
}

// queueBatchTransitions is §3.2's batch life: building → gating → passed →
// landed, or failed, unproven or abandoned. A failed batch is final: its
// rerun or bisect halves are new batches. An unproven one may gate again
// once, on another runner (the batch's attempt counts it). Landed and
// abandoned are final.
var queueBatchTransitions = map[QueueBatchState][]QueueBatchState{
	QueueBatchBuilding: {QueueBatchGating, QueueBatchAbandoned},
	QueueBatchGating:   {QueueBatchPassed, QueueBatchFailed, QueueBatchUnproven, QueueBatchAbandoned},
	QueueBatchPassed:   {QueueBatchLanded, QueueBatchAbandoned},
	QueueBatchUnproven: {QueueBatchGating, QueueBatchAbandoned},
}

// CanTransitionTo reports whether a batch in state s may move to state to.
func (s QueueBatchState) CanTransitionTo(to QueueBatchState) bool {
	for _, next := range queueBatchTransitions[s] {
		if next == to {
			return true
		}
	}
	return false
}

// MainHealthRecord is one verdict on a sha of main (main_health).
type MainHealthRecord struct {
	// ID names a new row; empty generates one.
	ID, OrgID, ProjectID, RepoID string
	SHA                          string
	Source                       MainHealthSource
	Verdict                      MainHealthVerdict
	Signature                    string
	BatchID, RunID               string
	// ObservedAt defaults to now.
	ObservedAt time.Time
}

// StaleQueueEntry is an entry still in the queue whose PR record no longer
// holds it: RecordState is the record's state ("" when the record is gone)
// and RecordHead its head.
type StaleQueueEntry struct {
	Entry       *QueueEntry
	RecordState string
	RecordHead  string
}
