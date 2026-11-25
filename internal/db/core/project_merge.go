package core

import (
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Merging two of a person's projects (MergeProjects): every row of the source
// moves onto the target in one transaction, and the source, left empty, is
// scheduled for deletion. store.MergeProjects is the transaction;
// store.PreviewProjectMerge reads what it would do.

// ProjectMerge is one merge, as the handler admitted it.
type ProjectMerge struct {
	// SourceID is folded into TargetID, then scheduled for deletion.
	SourceID string
	TargetID string
	// ActorUserID is who merges: the source's deleted_by.
	ActorUserID string
	// Decided is the owner and org both projects were admitted on. The
	// merge re-checks them under the owner's org lock, so a project that
	// changed owner or org since is refused.
	Decided ProjectDecision
	// DeleteAfter is when the emptied source may be purged
	// (ProjectDeleteWindow from now).
	DeleteAfter time.Time
}

// ProjectMergeCounts is what a merge moves from the source onto the target.
type ProjectMergeCounts struct {
	Chats int
	// Worktrees is every worktree of the source, its main one included
	// (which folds into the target's main one when the target has one).
	Worktrees int
	// Checkouts are the source's checkouts (project_daemons): each becomes
	// the target's on that machine, at the same path.
	Checkouts []ProjectMergeCheckout
	Triggers  int
	// Repos is every repo of the source, those folded into the target's
	// repo at the same relative path included.
	Repos int
	// OtherRows is every other row of the source that moves or folds into
	// the target's (configs, catalogs, settings, plans, processes, ...).
	OtherRows int
}

// ProjectMergeCheckout is one machine the source is checked out on.
type ProjectMergeCheckout struct {
	DaemonID     string
	InstallState ProjectInstallState
}

// ProjectMergeBlockerKind is why a merge is refused now.
type ProjectMergeBlockerKind int

const (
	// ProjectMergeRunningWork: work runs, waits or is queued in one of the
	// projects.
	ProjectMergeRunningWork ProjectMergeBlockerKind = iota + 1
	// ProjectMergeSameMachine: both projects are checked out on one
	// machine, which holds one checkout of a project.
	ProjectMergeSameMachine
)

// ProjectMergeBlocker is one thing that refuses a merge now.
type ProjectMergeBlocker struct {
	Kind ProjectMergeBlockerKind
	// Message is a sentence for the person: what refuses it and what to do.
	Message string
	// DaemonID is the machine, for ProjectMergeSameMachine.
	DaemonID string
	// ChatID is the chat, when a chat is what runs.
	ChatID string
}

// ProjectMergePreview is what a merge would do now, and what refuses it.
type ProjectMergePreview struct {
	Counts   ProjectMergeCounts
	Blockers []ProjectMergeBlocker
}

// ProjectMergeResult is a merge's outcome. On ErrProjectMergeBlocked,
// Blockers says why and nothing was written.
type ProjectMergeResult struct {
	Counts   ProjectMergeCounts
	Blockers []ProjectMergeBlocker
	// MovedTriggerIDs are the triggers now the target's, whose schedules the
	// caller converges after the commit.
	MovedTriggerIDs []string
	// DeleteAfter is when the emptied source may be purged.
	DeleteAfter time.Time
}

var (
	// ErrProjectMergeBlocked refuses a merge while something named in
	// ProjectMergeResult.Blockers stands in its way.
	ErrProjectMergeBlocked = svcerr.FailedPrecondition("these projects cannot be merged now")
	// ErrProjectMergeSameProject refuses folding a project into itself.
	ErrProjectMergeSameProject = svcerr.FailedPrecondition("a project cannot be merged into itself; choose two different projects")
	// ErrProjectMergeOtherOrg refuses merging projects of two organizations.
	ErrProjectMergeOtherOrg = svcerr.FailedPrecondition("these projects are in different organizations; move one into the other's organization first, then merge them")
)

// TriggerChangeProjectMerged is the trigger_changes operation recording a
// trigger's move onto the project its own was merged into.
const TriggerChangeProjectMerged = "project_merged"
