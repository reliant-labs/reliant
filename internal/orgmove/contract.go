// yours: scaffolded once, never touched again — forge will not overwrite this file
// Package contract; canonical pattern lives in `forge skill load contracts`.

// Package orgmove runs a project's move to another org as a saga
// (control-plane docs/design/org-project-move.md §3): control-plane's
// Prepare (revoke the source org's secret access), reliant's transaction
// (store.MoveProjectOrg, the point of no return), control-plane's Record (the
// activity events in both orgs), and turning the triggers the move paused
// back on in the target. A failure before the transaction commits aborts and
// compensates (control-plane's Abort); after it, every step only rolls
// forward. Every step is idempotent by the move id, so Temporal may retry
// any of them.
//
//forge:not-a-component: built by internal/app's workerOrgMoves (providers_worker.go), only on a worker that can reach control-plane — never a node of the composition graph
package orgmove

import (
	"context"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Service is the move's steps, one Temporal activity each
// (ProjectOrgMoveWorkflow drives them in order).
type Service interface {
	// Prepare runs control-plane's Prepare for a move in state preparing
	// and advances it to prepared. A refusal (the actor is no longer the
	// source's admin, say) is an Outcome to abort with, not an error; an
	// error is an outage, which the workflow retries.
	Prepare(ctx context.Context, moveID string) (Outcome, error)
	// Commit runs reliant's transaction (store.MoveProjectOrg). Running work,
	// a project that left the source org or is gone is an Outcome to abort
	// with. A move that already committed commits nothing again.
	Commit(ctx context.Context, moveID string) (Outcome, error)
	// Abort compensates a move that did not commit: it records why, has
	// control-plane restore what Prepare revoked, and ends the move aborted.
	// It reports false, and changes nothing, for a move that committed
	// after all (a commit whose answer was lost): that one rolls forward.
	Abort(ctx context.Context, moveID, detail string) (aborted bool, err error)
	// Record writes the committed move to both orgs' activity streams.
	Record(ctx context.Context, moveID string) error
	// ResumeTriggers turns the triggers the move paused back on where they
	// check out in the target org, and completes the move.
	ResumeTriggers(ctx context.Context, moveID string) error
}

// Outcome is a step's verdict: Abort, with Detail saying why, when the move
// must stop before it commits.
type Outcome struct {
	Abort  bool
	Detail string
}

// Store is the move ledger and reliant's transaction. *db.Repo is one.
type Store interface {
	GetProjectOrgMove(ctx context.Context, id string) (*core.ProjectOrgMove, error)
	TransitionProjectOrgMove(ctx context.Context, id string, from []core.ProjectOrgMoveState, to core.ProjectOrgMoveState, detail string, counts *core.ProjectOrgMoveCounts) (*core.ProjectOrgMove, error)
	MoveProjectOrg(ctx context.Context, c core.ProjectOrgMoveCommit) (*core.ProjectOrgMoveResult, error)
	GetProject(ctx context.Context, id string) (*core.Project, error)
	ListProjectTriggersPausedBy(ctx context.Context, projectID, reason string) ([]*core.Trigger, error)
}

// ControlPlane is control-plane's half of the move
// (ProjectOrgMoveInternalService). controlplane.Service is one.
type ControlPlane interface {
	PrepareProjectOrgMove(ctx context.Context, s controlplane.ProjectOrgMoveStep, actorUserID string) (int, error)
	AbortProjectOrgMove(ctx context.Context, s controlplane.ProjectOrgMoveStep) error
	RecordProjectOrgMove(ctx context.Context, s controlplane.ProjectOrgMoveStep, projectName string, counts map[string]int64) error
}

// Triggers turns a trigger the move paused back on, as its owner, when it
// checks out in its new org. The trigger handler (*trigger.Service) is one.
type Triggers interface {
	ResumeAfterOrgMove(ctx context.Context, t *core.Trigger) (resumed bool, why string, err error)
}
