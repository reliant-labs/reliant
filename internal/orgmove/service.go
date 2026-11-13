// yours: scaffolded once, never touched again — forge will not overwrite this file
// Service implementation; canonical pattern in `forge skill load service-layer`.
package orgmove

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the orgmove package.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config
	// Store is the move ledger and reliant's transaction.
	Store Store
	// ControlPlane is control-plane's half of the move.
	ControlPlane ControlPlane
	// Triggers turns the triggers the move paused back on.
	Triggers Triggers
}

// Details a move ends aborted with (project_org_moves.detail), beyond
// control-plane's refusal reasons, which are recorded as it gives them.
const (
	// DetailProjectBusy: work started in the project before the commit.
	DetailProjectBusy = "project_busy"
	// DetailOrgChanged: the project is no longer in the move's source org.
	DetailOrgChanged = "org_changed"
	// DetailProjectGone: the project was deleted.
	DetailProjectGone = "project_gone"
	// DetailControlPlaneUnavailable: this deployment's control plane cannot
	// take a move (no service, or no identity to call it with).
	DetailControlPlaneUnavailable = "control_plane_unavailable"
)

type service struct {
	deps Deps
}

// New creates a new orgmove service instance.
//
// forge:constructor
func New(deps Deps) (Service, error) {
	if deps.Store == nil || deps.ControlPlane == nil || deps.Triggers == nil {
		return nil, fmt.Errorf("orgmove: Store, ControlPlane and Triggers are required")
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &service{deps: deps}, nil
}

func step(m *core.ProjectOrgMove) controlplane.ProjectOrgMoveStep {
	return controlplane.ProjectOrgMoveStep{MoveID: m.ID, ProjectID: m.ProjectID, FromOrgID: m.FromOrgID, ToOrgID: m.ToOrgID}
}

// refusal is control-plane's permanent answer, and the detail to record for
// it; ok false for an outage, which is retried.
func refusal(err error) (string, bool) {
	if errors.Is(err, controlplane.ErrProjectOrgMoveUnsupported) {
		return DetailControlPlaneUnavailable, true
	}
	var refused *controlplane.ProjectOrgMoveError
	if errors.As(err, &refused) && refused.Permanent() {
		if refused.Reason != "" {
			return refused.Reason, true
		}
		return refused.Code, true
	}
	return "", false
}

func (s *service) Prepare(ctx context.Context, moveID string) (Outcome, error) {
	m, err := s.deps.Store.GetProjectOrgMove(ctx, moveID)
	if err != nil {
		return Outcome{}, err
	}
	switch m.State {
	case core.ProjectOrgMoveStatePreparing:
	case core.ProjectOrgMoveStateAborting, core.ProjectOrgMoveStateAborted:
		return Outcome{Abort: true, Detail: m.Detail}, nil
	default: // prepared, or further along: a repeated step
		return Outcome{}, nil
	}
	revoked, err := s.deps.ControlPlane.PrepareProjectOrgMove(ctx, step(m), m.ActorUserID)
	if err != nil {
		if detail, ok := refusal(err); ok {
			return Outcome{Abort: true, Detail: detail}, nil
		}
		return Outcome{}, fmt.Errorf("prepare the move at control-plane: %w", err)
	}
	counts := m.Counts
	counts.SecretEntriesRevoked = revoked
	if _, err := s.deps.Store.TransitionProjectOrgMove(ctx, moveID,
		[]core.ProjectOrgMoveState{core.ProjectOrgMoveStatePreparing}, core.ProjectOrgMoveStatePrepared, "", &counts); err != nil {
		if errors.Is(err, core.ErrProjectOrgMoveState) {
			return s.Prepare(ctx, moveID) // another attempt advanced it; decide on where it is
		}
		return Outcome{}, err
	}
	return Outcome{}, nil
}

func (s *service) Commit(ctx context.Context, moveID string) (Outcome, error) {
	res, err := s.deps.Store.MoveProjectOrg(ctx, core.ProjectOrgMoveCommit{MoveID: moveID})
	switch {
	case err == nil:
		return Outcome{}, nil
	case errors.Is(err, core.ErrProjectNotQuiescent):
		detail := DetailProjectBusy
		if res != nil && res.Blockers != nil {
			b := res.Blockers
			detail = fmt.Sprintf("%s: %d chats, %d runs and %d background processes were running", DetailProjectBusy, b.ChatsTotal, b.LiveRuns, b.BackgroundProcesses)
		}
		return Outcome{Abort: true, Detail: detail}, nil
	case errors.Is(err, store.ErrOrgChanged):
		return Outcome{Abort: true, Detail: DetailOrgChanged}, nil
	case errors.Is(err, core.ErrProjectNotFound):
		return Outcome{Abort: true, Detail: DetailProjectGone}, nil
	case errors.Is(err, core.ErrProjectOrgMoveState):
		m, getErr := s.deps.Store.GetProjectOrgMove(ctx, moveID)
		if getErr != nil {
			return Outcome{}, getErr
		}
		if m.State == core.ProjectOrgMoveStateAborting || m.State == core.ProjectOrgMoveStateAborted {
			return Outcome{Abort: true, Detail: m.Detail}, nil
		}
		return Outcome{}, fmt.Errorf("commit a move in state %s: %w", m.State, err)
	default:
		return Outcome{}, fmt.Errorf("commit the move: %w", err)
	}
}

func (s *service) Abort(ctx context.Context, moveID, detail string) (bool, error) {
	m, err := s.deps.Store.GetProjectOrgMove(ctx, moveID)
	if err != nil {
		return false, err
	}
	switch m.State {
	case core.ProjectOrgMoveStateAborted:
		return true, nil
	case core.ProjectOrgMoveStateMoved, core.ProjectOrgMoveStateCompleted:
		return false, nil
	case core.ProjectOrgMoveStateAborting:
		detail = m.Detail
	default:
		if m, err = s.deps.Store.TransitionProjectOrgMove(ctx, moveID,
			[]core.ProjectOrgMoveState{core.ProjectOrgMoveStatePreparing, core.ProjectOrgMoveStatePrepared},
			core.ProjectOrgMoveStateAborting, detail, nil); err != nil {
			if errors.Is(err, core.ErrProjectOrgMoveState) {
				return s.Abort(ctx, moveID, detail)
			}
			return false, err
		}
	}
	if err := s.deps.ControlPlane.AbortProjectOrgMove(ctx, step(m)); err != nil {
		reason, permanent := refusal(err)
		if !permanent {
			return false, fmt.Errorf("restore the source org's secret access: %w", err)
		}
		// Nothing more to restore: no service (Prepare revoked nothing), or
		// a refusal no retry changes, which an operator must look at.
		s.deps.Logger.ErrorContext(ctx, "control-plane refused to abort a project move; recording it aborted",
			"move_id", moveID, "reason", reason, "error", err)
	}
	if _, err := s.deps.Store.TransitionProjectOrgMove(ctx, moveID,
		[]core.ProjectOrgMoveState{core.ProjectOrgMoveStateAborting}, core.ProjectOrgMoveStateAborted, detail, nil); err != nil &&
		!errors.Is(err, core.ErrProjectOrgMoveState) {
		return false, err
	}
	return true, nil
}

// countsWire is a move's counts as control-plane records them.
func countsWire(c core.ProjectOrgMoveCounts) map[string]int64 {
	return map[string]int64{
		"chats": int64(c.Chats), "runs": int64(c.Runs), "triggers": int64(c.Triggers),
		"triggers_paused": int64(c.TriggersPaused), "shares_cleared": int64(c.SharesCleared),
		"bindings_cleared": int64(c.BindingsCleared), "secret_entries_revoked": int64(c.SecretEntriesRevoked),
	}
}

func (s *service) Record(ctx context.Context, moveID string) error {
	m, err := s.deps.Store.GetProjectOrgMove(ctx, moveID)
	if err != nil {
		return err
	}
	if m.State != core.ProjectOrgMoveStateMoved && m.State != core.ProjectOrgMoveStateCompleted {
		return nil
	}
	name := m.ProjectID
	if p, err := s.deps.Store.GetProject(ctx, m.ProjectID); err == nil && p != nil && p.Name != "" {
		name = p.Name
	}
	if err := s.deps.ControlPlane.RecordProjectOrgMove(ctx, step(m), name, countsWire(m.Counts)); err != nil {
		reason, permanent := refusal(err)
		if !permanent {
			return fmt.Errorf("record the move at control-plane: %w", err)
		}
		// The move committed; the audit events cannot be written. Rolls
		// forward, loudly.
		s.deps.Logger.ErrorContext(ctx, "control-plane refused to record a committed project move",
			"move_id", moveID, "reason", reason, "error", err)
	}
	return nil
}

func (s *service) ResumeTriggers(ctx context.Context, moveID string) error {
	m, err := s.deps.Store.GetProjectOrgMove(ctx, moveID)
	if err != nil {
		return err
	}
	if m.State != core.ProjectOrgMoveStateMoved {
		return nil
	}
	paused, err := s.deps.Store.ListProjectTriggersPausedBy(ctx, m.ProjectID, core.TriggerPausedOrgMove)
	if err != nil {
		return err
	}
	counts := m.Counts
	stayed := 0
	for _, t := range paused {
		resumed, why, err := s.deps.Triggers.ResumeAfterOrgMove(ctx, t)
		switch {
		case err != nil:
			// Fails closed: it stays paused, where the owner can resume it.
			stayed++
			s.deps.Logger.WarnContext(ctx, "could not turn a moved trigger back on; it stays paused",
				"move_id", moveID, "trigger_id", t.ID, "error", err)
		case resumed:
			counts.TriggersReenabled++
		default:
			stayed++
			s.deps.Logger.InfoContext(ctx, "a moved trigger stays paused", "move_id", moveID, "trigger_id", t.ID, "why", why)
		}
	}
	detail := ""
	if stayed > 0 {
		detail = fmt.Sprintf("%d triggers stay paused until they are set up in the target organization", stayed)
	}
	if _, err := s.deps.Store.TransitionProjectOrgMove(ctx, moveID,
		[]core.ProjectOrgMoveState{core.ProjectOrgMoveStateMoved}, core.ProjectOrgMoveStateCompleted, detail, &counts); err != nil &&
		!errors.Is(err, core.ErrProjectOrgMoveState) {
		return err
	}
	return nil
}
