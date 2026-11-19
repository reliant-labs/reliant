// Copyright (c) 2025 Reliant Labs
package machinepool

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// RunStore is what placing a workflow's run reads and writes beside Store.
// *db.Repo implements it.
type RunStore interface {
	Store
	GetMachinePoolByName(ctx context.Context, userID, name string) (*core.MachinePool, error)
	// ListLabelledMachines is the owner's machines whose durable labels
	// contain labels, LiveRuns counting every live run placed on each.
	ListLabelledMachines(ctx context.Context, userID string, labels map[string]string, projectID string) ([]core.MachinePoolMemberRow, error)
	// GetRunMachinePlacement is core.ErrRunMachinePlacementNotFound when
	// nothing is recorded.
	GetRunMachinePlacement(ctx context.Context, workflowID, scope string) (*core.RunMachinePlacement, error)
	// RecordRunMachinePlacement records p unless its (workflow, scope)
	// already has one, and returns the placement recorded.
	RecordRunMachinePlacement(ctx context.Context, p *core.RunMachinePlacement) (*core.RunMachinePlacement, error)
	RunTx(ctx context.Context, policy orm.TxPolicy, fn func(ctx context.Context) error) error
}

// RunPlacer places a workflow's run, or one node of it, on a member of a
// machine pool or on a machine matching durable labels: the same Decide and
// slot guard a pool-bound trigger's launch uses, recorded so the answer never
// moves.
type RunPlacer struct {
	*Placer
	runs RunStore
}

// NewRunPlacer builds a RunPlacer over store.
func NewRunPlacer(store RunStore) *RunPlacer {
	return &RunPlacer{Placer: NewPlacer(store), runs: store}
}

// WithWaker lets the RunPlacer act on a Wake decision.
func (p *RunPlacer) WithWaker(w Waker) *RunPlacer {
	p.Placer.WithWaker(w)
	return p
}

// RunRequest is one placement to make: the activity's input plus what it
// read about the run's chat.
type RunRequest struct {
	PlaceRunInput
	// UserID owns the chat, and so the pool and the machines.
	UserID string
	// ProjectID is the chat's project: the checkout and catalog a member
	// must hold.
	ProjectID string
	// ChatPoolID and ChatDaemonID are the pool and member a trigger placed
	// the chat on ("" when none). A workflow-level placement on that same
	// pool keeps the run on that member: its checkout is there.
	ChatPoolID   string
	ChatDaemonID string
}

// runPlaceAttempts bounds re-placing after a concurrent placement took the
// chosen member's last slot (MemberBusy), as a trigger's launch does.
const runPlaceAttempts = 3

// ErrInvalidRunRequest is a request that names no run, or not exactly one
// of a pool and labels. The runtime never sends one; it is not retried.
var ErrInvalidRunRequest = errors.New("invalid run placement request")

// PlaceRun places req once and records it. Asking again for the same
// (WorkflowID, Scope) returns the recorded member, so a retried activity or a
// continue-as-new successor never re-places.
//
// A pool: Decide over its members; Run takes the member's slot in one
// transaction with the record (the pool's row lock orders concurrent
// placements), and a lost race re-places, up to runPlaceAttempts; Queue
// waits, with why; Wake asks for the member to wake and waits (wake, then
// place). A workflow-level placement on the pool a trigger placed the chat on
// stays on that member. An unknown pool wraps core.ErrMachinePoolNotFound.
//
// Labels: the same over the owner's machines whose durable labels contain
// them, an unnamed pool with no per-member bound.
func (p *RunPlacer) PlaceRun(ctx context.Context, req RunRequest) (PlaceRunOutput, error) {
	if err := req.validate(); err != nil {
		return PlaceRunOutput{}, err
	}
	recorded, err := p.runs.GetRunMachinePlacement(ctx, req.WorkflowID, req.Scope)
	switch {
	case err == nil:
		return p.answerRecorded(ctx, req, recorded), nil
	case !errors.Is(err, core.ErrRunMachinePlacementNotFound):
		return PlaceRunOutput{}, fmt.Errorf("read placement of %s/%q: %w", req.WorkflowID, req.Scope, err)
	}
	if req.Pool != "" {
		return p.placeOnPool(ctx, req)
	}
	return p.placeOnLabels(ctx, req)
}

func (r RunRequest) validate() error {
	switch {
	case r.WorkflowID == "" || r.ChatID == "" || r.UserID == "":
		return fmt.Errorf("%w: a placement names its workflow, chat and owner", ErrInvalidRunRequest)
	case r.Pool != "" && len(r.Labels) > 0:
		return fmt.Errorf("%w: a pool and labels are exclusive", ErrInvalidRunRequest)
	case r.Pool == "" && len(r.Labels) == 0:
		return fmt.Errorf("%w: a pool or labels to place on", ErrInvalidRunRequest)
	}
	return nil
}

func (p *RunPlacer) placeOnPool(ctx context.Context, req RunRequest) (PlaceRunOutput, error) {
	row, err := p.runs.GetMachinePoolByName(ctx, req.UserID, req.Pool)
	if err != nil {
		return PlaceRunOutput{}, fmt.Errorf("machine pool %q: %w", req.Pool, err)
	}
	pool := PoolFromCore(row)
	for attempt := 1; attempt <= runPlaceAttempts; attempt++ {
		members, err := membersOf(ctx, p.runs, row, req.ProjectID)
		if err != nil {
			return PlaceRunOutput{}, err
		}
		if m, ok := stickyMember(req, pool, members); ok {
			// The chat's run is already on m: record it there, never split
			// it from its checkout. The chat holds its slot already (live
			// runs count chats), so there is no slot to take.
			return p.record(ctx, req, pool, m, nil)
		}
		d := Decide(pool, members)
		if d.Kind != Run {
			return p.wait(ctx, pool, d), nil
		}
		out, err := p.record(ctx, req, pool, d.Member, p.SlotGuard(pool, d.Member))
		if errors.Is(err, errMemberBusy) {
			continue
		}
		return out, err
	}
	return PlaceRunOutput{PoolID: pool.ID, PoolName: pool.Name, Waiting: true,
		Reason: WaitingPrefix + pool.Name + " (every free member was taken by a concurrent placement)"}, nil
}

func (p *RunPlacer) placeOnLabels(ctx context.Context, req RunRequest) (PlaceRunOutput, error) {
	members, err := p.labelledMembers(ctx, req)
	if err != nil {
		return PlaceRunOutput{}, err
	}
	pool := Pool{UserID: req.UserID, Name: LabelsPoolName(req.Labels), Selector: req.Labels}
	d := Decide(pool, members)
	if d.Kind != Run {
		return p.wait(ctx, pool, d), nil
	}
	return p.record(ctx, req, pool, d.Member, nil)
}

func (p *RunPlacer) labelledMembers(ctx context.Context, req RunRequest) ([]Member, error) {
	rows, err := p.runs.ListLabelledMachines(ctx, req.UserID, req.Labels, req.ProjectID)
	if err != nil {
		return nil, err
	}
	members := make([]Member, 0, len(rows))
	for _, r := range rows {
		m, err := memberFromRow(r)
		if err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, nil
}

// stickyMember is the member a trigger placed the chat on, when this is the
// run's workflow-level placement on that same pool and the machine is still
// one of its members.
func stickyMember(req RunRequest, pool Pool, members []Member) (Member, bool) {
	if req.Scope != "" || req.ChatPoolID == "" || req.ChatPoolID != pool.ID || req.ChatDaemonID == "" {
		return Member{}, false
	}
	for _, m := range members {
		if m.DaemonID == req.ChatDaemonID {
			return m, true
		}
	}
	return Member{}, false
}

// errMemberBusy is guard's MemberBusy decline, inside record.
var errMemberBusy = errors.New(MemberBusy)

// record takes m's slot (guard, nil for none) and records the placement in
// one transaction. READ COMMITTED: the pool's row lock, which guard takes
// first, is what orders concurrent placements, and under SERIALIZABLE the
// loser of that race would fail instead of reading the winner's placement.
// The record that comes back may be a concurrent ask's for the same
// (workflow, scope): that one is the answer.
func (p *RunPlacer) record(ctx context.Context, req RunRequest, pool Pool, m Member, guard func(context.Context) (string, error)) (PlaceRunOutput, error) {
	var stored *core.RunMachinePlacement
	err := p.runs.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		if guard != nil {
			reason, err := guard(ctx)
			switch {
			case err != nil:
				return err
			case reason == MemberBusy:
				return errMemberBusy
			case reason != "":
				return fmt.Errorf("place on %s of pool %s: %s", m.label(), pool.Name, reason)
			}
		}
		var err error
		stored, err = p.runs.RecordRunMachinePlacement(ctx, &core.RunMachinePlacement{
			WorkflowID: req.WorkflowID, Scope: req.Scope, UserID: req.UserID, ChatID: req.ChatID,
			MachinePoolID: pool.ID, Selector: pool.Selector, DaemonID: m.DaemonID,
		})
		return err
	})
	if err != nil {
		if errors.Is(err, errMemberBusy) {
			return PlaceRunOutput{}, err
		}
		return PlaceRunOutput{}, fmt.Errorf("record placement of %s/%q: %w", req.WorkflowID, req.Scope, err)
	}
	out := named(PlaceRunOutput{DaemonID: stored.DaemonID}, pool)
	if stored.DaemonID == m.DaemonID {
		out.DaemonName = m.Name
	}
	return out, nil
}

// wait answers a Queue or Wake decision: no member can take the run now.
// Wake asks for the member to wake first (wake, then place); a wake that
// fails waits too, saying why, and the next ask tries again.
func (p *RunPlacer) wait(ctx context.Context, pool Pool, d Decision) PlaceRunOutput {
	out := named(PlaceRunOutput{Waiting: true, Reason: d.Reason(pool.Name)}, pool)
	if d.Kind == Wake {
		if err := p.Wake(ctx, pool.UserID, d.Member.DaemonID); err != nil {
			out.Reason = fmt.Sprintf("%s%s (could not wake %s: %v)", WaitingPrefix, pool.Name, d.Member.label(), err)
		}
	}
	return out
}

// named sets out's pool, for a named pool (a labels placement names none).
func named(out PlaceRunOutput, pool Pool) PlaceRunOutput {
	if pool.ID != "" {
		out.PoolID, out.PoolName = pool.ID, pool.Name
	}
	return out
}

// answerRecorded is the answer for a placement already recorded. The member's
// name is read back for the run's messages when it can be; the placement
// stands either way (its pool may be gone, or the machine relabelled since).
func (p *RunPlacer) answerRecorded(ctx context.Context, req RunRequest, rec *core.RunMachinePlacement) PlaceRunOutput {
	out := PlaceRunOutput{DaemonID: rec.DaemonID}
	var rows []core.MachinePoolMemberRow
	if req.Pool != "" {
		out.PoolID, out.PoolName = rec.MachinePoolID, req.Pool
		if row, err := p.runs.GetMachinePoolByName(ctx, req.UserID, req.Pool); err == nil {
			rows, _ = p.runs.ListMachinePoolMembers(ctx, row, req.ProjectID)
		}
	} else {
		rows, _ = p.runs.ListLabelledMachines(ctx, req.UserID, req.Labels, req.ProjectID)
	}
	for _, r := range rows {
		if r.DaemonID == rec.DaemonID {
			out.DaemonName = r.Name
		}
	}
	return out
}

// LabelsPoolName names the unnamed pool a labels-only selector places on, in
// reasons: "labelled os=linux, size=2xl".
func LabelsPoolName(labels map[string]string) string {
	pairs := make([]string, 0, len(labels))
	for k, v := range labels {
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	return "labelled " + strings.Join(pairs, ", ")
}
