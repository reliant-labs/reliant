package core

import (
	"errors"
	"time"
)

// ErrWorkflowCheckRunNotFound is returned when no workflow_check_runs row
// matches.
var ErrWorkflowCheckRunNotFound = errors.New("workflow check run not found")

// ErrWorkflowCheckLeaseLost is a sync's save refused by its fence: the row is
// no longer held under the lease (holder and generation) the sync claimed it
// with, because the lease expired and another syncer took the row, or the row
// is gone. Nothing was written, so the newer holder's state stands.
var ErrWorkflowCheckLeaseLost = errors.New("workflow check run lease lost")

// WorkflowCheckLease is one claimed row's lease: its id and the generation
// the claim gave it, the fencing token every renewal and save must present.
type WorkflowCheckLease struct {
	ID         string
	Generation int64
}

// The values of WorkflowCheckRun.GitHubStatus: what the check run on GitHub
// was last written as ("" before it exists).
const (
	CheckStatusQueued     = "queued"
	CheckStatusInProgress = "in_progress"
	CheckStatusCompleted  = "completed"
)

// WorkflowCheckRun is the engine's record of the GitHub check run one firing
// of a workflow with a `check:` block posts (internal/workflowcheck). It is
// written when the firing resolves the workflow, stamped by the run as it
// starts and ends, and synced to GitHub by the worker's check syncer.
type WorkflowCheckRun struct {
	ID             string
	TriggerEventID string
	// Name is the check's name on GitHub.
	Name string
	// UserID, OrgID, ActingIdentity and Automation are the trigger's owner,
	// org, acting identity and name when the firing resolved: whose check
	// it is, and as whom it is written.
	UserID         string
	OrgID          *string
	ActingIdentity string
	Automation     string
	// Owner, Repo and HeadSHA are where the check is posted.
	Owner   string
	Repo    string
	HeadSHA string
	// Timeout bounds the check from CreatedAt: past it, a check still not
	// concluded concludes timed_out.
	Timeout time.Duration

	// StartedAt is when the run's first node started; nil before.
	StartedAt *time.Time
	// EndedStatus is how the run saw its own end (completed, failed,
	// cancelled), and Verdict the outcome its graph declared ("success",
	// "failure", "skipped", or "" when no node declared one). Empty until it
	// ended.
	EndedStatus string
	Verdict     string
	// Output is the check's output the run evaluated ({title, summary, text,
	// annotations}); nil when it declares none or evaluation failed, which
	// OutputError then says.
	Output      map[string]any
	OutputError string

	// GitHubCheckRunID is the check run's id on GitHub; nil until created.
	GitHubCheckRunID *int64
	// GitHubStatus and GitHubConclusion are what the check run was last
	// written as (CheckStatus*; the conclusion only once completed).
	GitHubStatus     string
	GitHubConclusion string
	// AnnotationsSent counts the output's annotations already sent.
	AnnotationsSent int
	// GitHubDigest identifies what was last written (status, conclusion and
	// output), so a sync writes only a change.
	GitHubDigest string

	// PausedSince is when the syncer first saw the run paused; nil when it
	// is not.
	PausedSince *time.Time
	// NextSyncAt is when the row is next due.
	NextSyncAt time.Time
	// ClaimedBy, ClaimedUntil and LeaseGeneration are the lease a syncer
	// holds on the row: who holds it, until when (renewed while it syncs),
	// and the fencing token its claim bumped. A save lands only while
	// ClaimedBy and LeaseGeneration are still the row's.
	ClaimedBy       string
	ClaimedUntil    *time.Time
	LeaseGeneration int64
	// Attempts counts consecutive failed GitHub writes; LastError is the
	// last one ("" after a success).
	Attempts  int
	LastError string
	// FinishedAt is set when nothing is left to sync: the check concluded and
	// every annotation was sent, or the syncer gave up.
	FinishedAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// WorkflowCheckEnding is how a run saw its own end, as it reports it to its
// check: the run's status, the verdict its graph declared, and the check
// output it evaluated.
type WorkflowCheckEnding struct {
	Status      string         `json:"status"`            // completed | failed | cancelled
	Verdict     string         `json:"verdict,omitempty"` // success | failure | skipped | ""
	Output      map[string]any `json:"output,omitempty"`
	OutputError string         `json:"output_error,omitempty"`
}

// WorkflowCheckObservation is what the syncer reads about a check's firing and
// the run it launched, in one query.
type WorkflowCheckObservation struct {
	// EventOutcome and EventDetail are the firing's outcome and its detail.
	EventOutcome TriggerEventOutcome
	EventDetail  string
	// ChatID is the chat the firing launched; "" when it launched none.
	ChatID string
	// RunStatus is how the trigger's own run ended
	// (TriggerEvent.RunStatus); "" while it is going. RunOutcome is the
	// outcome recorded beside it (TriggerEvent.RunOutcome, a RunOutcome*
	// value; "" when none was).
	RunStatus  string
	RunOutcome string
	// Root is the lifecycle of the chat's root workflow row, nil when there
	// is none (not launched yet, or deleted). RootVerdict and
	// RootFailureDetail are its declared outcome and failure detail.
	Root              *WorkflowStatus
	RootVerdict       string
	RootFailureDetail string
}
