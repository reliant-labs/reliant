// Copyright (c) 2025 Reliant Labs

package core

import (
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Moving a project to another org (control-plane
// docs/design/org-project-move.md; owner decision 2026-11-12). The project, its
// chats (archived included), their runs and the project's triggers are
// restamped with the target org in one transaction; history follows by id;
// shares are cleared; org-owned bindings are cleared; spend never moves.

// ProjectOrgMoveState is where a move's saga is (project_org_moves.state).
type ProjectOrgMoveState string

const (
	// ProjectOrgMoveStatePreparing: the row exists; control-plane is
	// revoking the source org's per-project state.
	ProjectOrgMoveStatePreparing ProjectOrgMoveState = "preparing"
	// ProjectOrgMoveStatePrepared: control-plane is done; reliant's
	// transaction may commit.
	ProjectOrgMoveStatePrepared ProjectOrgMoveState = "prepared"
	// ProjectOrgMoveStateMoved: reliant's transaction committed, the point
	// of no return. Every later step only rolls forward.
	ProjectOrgMoveStateMoved ProjectOrgMoveState = "moved"
	// ProjectOrgMoveStateCompleted: every roll-forward step is done.
	ProjectOrgMoveStateCompleted ProjectOrgMoveState = "completed"
	// ProjectOrgMoveStateAborting: a step before the commit failed, and
	// control-plane is restoring what Prepare revoked.
	ProjectOrgMoveStateAborting ProjectOrgMoveState = "aborting"
	// ProjectOrgMoveStateAborted: restored; Detail says why it stopped.
	ProjectOrgMoveStateAborted ProjectOrgMoveState = "aborted"
)

// Open reports whether a move in state s still holds its project: at most
// one open move per project (project_org_moves_one_open).
func (s ProjectOrgMoveState) Open() bool {
	return s == ProjectOrgMoveStatePreparing || s == ProjectOrgMoveStatePrepared || s == ProjectOrgMoveStateMoved
}

// ProjectOrgMove is one requested move (project_org_moves).
type ProjectOrgMove struct {
	// ID is the move id: the saga's Temporal workflow id and the key of
	// every control-plane step.
	ID        string
	ProjectID string
	// OwnerUserID is the project's owner, whose org lock the commit takes.
	OwnerUserID string
	// ActorUserID is who asked for the move (the owner, in v1).
	ActorUserID string
	FromOrgID   string
	ToOrgID     string
	// RequestKey is the client's idempotency key: a repeat of the same
	// (actor, key) is the same move.
	RequestKey string
	State      ProjectOrgMoveState
	// Detail is why the move stopped, or what a roll-forward step is
	// waiting on; "" otherwise.
	Detail    string
	Counts    ProjectOrgMoveCounts
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ProjectOrgMoveCounts is what a move changed (project_org_moves.counts).
// The commit writes the reliant counts; later steps add theirs.
type ProjectOrgMoveCounts struct {
	// Chats is every chat of the project, archived included.
	Chats int `json:"chats"`
	// Runs is every run of those chats.
	Runs int `json:"runs"`
	// Triggers is every trigger of the project.
	Triggers int `json:"triggers"`
	// TriggersPaused is the enabled triggers the move paused
	// (TriggerPausedOrgMove).
	TriggersPaused int `json:"triggers_paused"`
	// TriggersReenabled is those the move later re-enabled in the target.
	TriggersReenabled int `json:"triggers_reenabled"`
	// SharesCleared is the project and chats whose share was cleared.
	SharesCleared int `json:"shares_cleared"`
	// BindingsCleared is the triggers whose org-connection binding was
	// cleared.
	BindingsCleared int `json:"bindings_cleared"`
	// SecretEntriesRevoked is control-plane's count of the source org's
	// secret-access entries it revoked for the project.
	SecretEntriesRevoked int `json:"secret_entries_revoked"`
}

// ProjectOrgMoveBlockers is the running work that refuses a move: the
// project is quiescent exactly when Empty (design §2). Read with the chat
// rows locked, it is the commit's check; read without, it is the preview's.
type ProjectOrgMoveBlockers struct {
	// Chats are the chats whose activity is neither idle nor stopped on an
	// error (what MoveChatToProject also refuses), at most
	// ProjectOrgMoveBlockerLimit of them; ChatsTotal counts them all.
	Chats      []ProjectOrgMoveBlockedChat
	ChatsTotal int
	// LiveRuns counts the runs of the project's chats that are pending or
	// running, sub-agents and background runs included.
	LiveRuns int
	// BackgroundProcesses counts the project's running background
	// processes.
	BackgroundProcesses int
}

// ProjectOrgMoveBlockerLimit bounds the blocked chats a read names.
const ProjectOrgMoveBlockerLimit = 50

// ProjectOrgMoveBlockedChat is one chat that refuses the move, and why
// (its activity, as the sidebar shows it).
type ProjectOrgMoveBlockedChat struct {
	ChatID   string
	Title    string
	Activity int32
}

// Empty reports whether nothing blocks the move.
func (b *ProjectOrgMoveBlockers) Empty() bool {
	return b == nil || (b.ChatsTotal == 0 && b.LiveRuns == 0 && b.BackgroundProcesses == 0)
}

// ProjectOrgMoveEffects is what moving a project would do (design §1.2), as
// reliant can tell it: the preview's half that is not control-plane's.
type ProjectOrgMoveEffects struct {
	// Chats is every chat of the project, archived included; Runs every run
	// of those chats; Triggers every trigger of the project.
	Chats    int
	Runs     int
	Triggers int
	// TriggersToPause is the enabled triggers the move pauses.
	TriggersToPause int
	// TriggersNeedingSetup are the triggers that will stay paused until
	// set up again in the target, and why.
	TriggersNeedingSetup []ProjectOrgMoveTriggerSetup
	// SharesToClear counts the project (when shared) and its shared chats.
	SharesToClear int
	// OrgBindingsToClear counts the triggers bound to an org connection the
	// target does not own.
	OrgBindingsToClear int
	// Machines are the machines the project is attached to or a chat of it
	// is pinned to, ordered by id. The move changes none of them.
	Machines []ProjectOrgMoveMachine
	// ForgeProjectName is the hosted forge project name the project deploys
	// as; "" for none.
	ForgeProjectName string
}

// Why a trigger stays paused after a move (ProjectOrgMoveTriggerSetup.Reason).
const (
	// ProjectOrgMoveSetupOrgConnection: it is bound to the source org's
	// connection, which the move clears.
	ProjectOrgMoveSetupOrgConnection = "org_connection"
	// ProjectOrgMoveSetupOrgGitHubInstallation: it listens through an org's
	// GitHub App installation, which must be linked to the target.
	ProjectOrgMoveSetupOrgGitHubInstallation = "org_github_installation"
)

// ProjectOrgMoveTriggerSetup is one trigger a move leaves paused, and why.
type ProjectOrgMoveTriggerSetup struct {
	TriggerID string
	Name      string
	Reason    string
}

// ProjectOrgMoveMachine is one machine a moved project keeps using.
type ProjectOrgMoveMachine struct {
	DaemonID string
	Name     string
	// Attached: the project has a checkout there (project_daemons).
	Attached bool
	// PinnedChats counts the project's chats pinned to it.
	PinnedChats int
}

// ProjectOrgMoveFilters lists moves: an org's ledger (the moves out of and
// into OrgID) when OrgID is set, else UserID's own.
type ProjectOrgMoveFilters struct {
	OrgID  string
	UserID string
	Limit  int
	Offset int
}

// TriggerChangeOrgMoveResumed is the trigger_changes operation recording a
// move turning a trigger it paused back on in the target org.
const TriggerChangeOrgMoveResumed = "org_move_resumed"

// ProjectOrgMoveCommit is the input of the move's transaction.
type ProjectOrgMoveCommit struct {
	// MoveID names the move row, in state prepared; the row decides the
	// project, owner, actor and both orgs.
	MoveID string
	// DefaultOrgID is the owner's default org, read by the caller: a
	// project with no org yet is in it, as the backfill would stamp it.
	// "" when the owner has none, which leaves such a project unmovable.
	DefaultOrgID string
	At           time.Time
}

// ProjectOrgMoveResult is what the move's transaction did.
type ProjectOrgMoveResult struct {
	Move *ProjectOrgMove
	// Blockers is set, and nothing was written, when the project was not
	// quiescent under the locks; the error is then ErrProjectNotQuiescent.
	Blockers *ProjectOrgMoveBlockers
	// AlreadyMoved is true when the move had committed before this call (a
	// retried step): nothing was written, and Move holds its counts.
	AlreadyMoved bool
}

// TriggerPausedOrgMove is Trigger.PausedReason of a trigger a project move
// paused. Only triggers with this reason are re-enabled by the move, once
// they check out in the target org; one that cannot stays paused with it.
const TriggerPausedOrgMove = "org_move"

// TriggerPausedOrgMoveSetup is Trigger.PausedReason of an enabled trigger a
// project move paused and cannot turn back on by itself: its binding to the
// source org's connection was cleared, so it waits for someone to bind it in
// the target org (or to their own connection) and resume it.
const TriggerPausedOrgMoveSetup = "org_move_setup"

// TriggerChangeOrgMoved is the trigger_changes operation recording a
// trigger's move to another org, one row per trigger.
const TriggerChangeOrgMoved = "org_moved"

// ShareActionMoved is a share cleared because its resource's project moved
// to another org, recorded in the source org (the audience that lost it).
const ShareActionMoved ShareAction = "moved"

var (
	// ErrProjectOrgMoveNotFound is a move id with no row.
	ErrProjectOrgMoveNotFound = svcerr.NotFound("project move")
	// ErrProjectOrgMoveInProgress refuses a second open move of a project.
	ErrProjectOrgMoveInProgress = svcerr.FailedPrecondition("this project is already being moved to another organization")
	// ErrProjectOrgMoveState refuses a step the move's state does not
	// allow (a commit of a move that is not prepared, say).
	ErrProjectOrgMoveState = svcerr.FailedPrecondition("the project move is not in a state that allows this step")
	// ErrProjectNotQuiescent refuses a move while work is running in the
	// project; ProjectOrgMoveResult.Blockers names it.
	ErrProjectNotQuiescent = svcerr.FailedPrecondition("work is running in this project; stop it before moving the project to another organization")
)
