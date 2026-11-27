// Copyright (c) 2025 Reliant Labs
package machinepool

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// RunPools is what placing a workflow's run reads of pools beside Store.
// store.MachinePoolExtStore implements it.
type RunPools interface {
	Store
	GetMachinePoolByName(ctx context.Context, userID, name string) (*core.MachinePool, error)
	// ListChatHeldPoolMembers is the members of poolID on which chatID
	// already holds a live run (a trigger placed the chat there, or a live
	// placement of one of its runs is there): each holds the chat's one slot.
	ListChatHeldPoolMembers(ctx context.Context, poolID, chatID string) ([]string, error)
}

// RunPlacements reads and records where a run was placed.
// store.RunMachinePlacementExtStore implements it.
type RunPlacements interface {
	// GetRunMachinePlacement is core.ErrRunMachinePlacementNotFound when
	// nothing is recorded.
	GetRunMachinePlacement(ctx context.Context, workflowID, scope string) (*core.RunMachinePlacement, error)
	// RecordRunMachinePlacement records p unless its (workflow, scope)
	// already has one, and returns the placement recorded.
	RecordRunMachinePlacement(ctx context.Context, p *core.RunMachinePlacement) (*core.RunMachinePlacement, error)
	// MoveRunMachinePlacement moves p's (workflow, scope) placement from
	// fromDaemonID to p.DaemonID, and returns the placement recorded: the
	// moved one, or the one a concurrent move left.
	MoveRunMachinePlacement(ctx context.Context, p *core.RunMachinePlacement, fromDaemonID string) (*core.RunMachinePlacement, error)
}

// TxRunner runs fn in one transaction; the stores above join it through ctx.
// *db.Repo implements it.
type TxRunner interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, fn func(ctx context.Context) error) error
}

// RunPlacer places a workflow's run, or one node of it, on a member of a
// machine pool: the same Decide and
// slot guard a pool-bound trigger's launch uses, recorded so the answer never
// moves.
type RunPlacer struct {
	*Placer
	pools      RunPools
	placements RunPlacements
	tx         TxRunner
}

// NewRunPlacer builds a RunPlacer over its stores.
func NewRunPlacer(pools RunPools, placements RunPlacements, tx TxRunner) *RunPlacer {
	return &RunPlacer{Placer: NewPlacer(pools), pools: pools, placements: placements, tx: tx}
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

// ErrInvalidRunRequest is a request that names no run or no pool. The
// runtime never sends one; it is not retried.
var ErrInvalidRunRequest = errors.New("invalid run placement request")

// PlaceRun places req once and records it. Asking again for the same
// (WorkflowID, Scope) returns the recorded member, so a retried activity or a
// continue-as-new successor never re-places.
//
// A pool: Decide over its members; Run takes the member's slot in one
// transaction with the record (the pool's row lock orders concurrent
// placements), and a lost race re-places, up to runPlaceAttempts; Queue
// waits, with why; Wake asks for the member to wake and waits (wake, then
// place). A member on which the run's chat already holds a live run (its
// trigger placed it there, or an earlier placement of the run, workflow- or
// node-level, is there) is preferred and admits it with no extra slot: a
// chat counts once among a member's live runs, so a second placement of the
// same chat must never wait on the slot the chat itself holds. An unknown
// pool wraps core.ErrMachinePoolNotFound.
func (p *RunPlacer) PlaceRun(ctx context.Context, req RunRequest) (PlaceRunOutput, error) {
	if err := req.validate(); err != nil {
		return PlaceRunOutput{}, err
	}
	recorded, err := p.placements.GetRunMachinePlacement(ctx, req.WorkflowID, req.Scope)
	switch {
	case err == nil && !req.excludes(recorded.DaemonID):
		return p.answerRecorded(ctx, req, recorded), nil
	case err == nil:
		// The recorded member failed the node's preflight: the placement
		// moves, keeping its identity, so a retried ask (the member it
		// moved to is not excluded) answers where it went.
		return p.placeOnPool(ctx, req, recorded.DaemonID)
	case !errors.Is(err, core.ErrRunMachinePlacementNotFound):
		return PlaceRunOutput{}, fmt.Errorf("read placement of %s/%q: %w", req.WorkflowID, req.Scope, err)
	}
	return p.placeOnPool(ctx, req, "")
}

// excludes reports whether daemonID is one of the request's excluded members.
func (r RunRequest) excludes(daemonID string) bool {
	for _, id := range r.Exclude {
		if id == daemonID {
			return true
		}
	}
	return false
}

// withoutExcluded is members minus the request's excluded ones.
func (r RunRequest) withoutExcluded(members []Member) []Member {
	if len(r.Exclude) == 0 {
		return members
	}
	out := make([]Member, 0, len(members))
	for _, m := range members {
		if !r.excludes(m.DaemonID) {
			out = append(out, m)
		}
	}
	return out
}

func (r RunRequest) validate() error {
	switch {
	case r.WorkflowID == "" || r.ChatID == "" || r.UserID == "":
		return fmt.Errorf("%w: a placement names its workflow, chat and owner", ErrInvalidRunRequest)
	case r.Pool == "":
		return fmt.Errorf("%w: a placement names its pool", ErrInvalidRunRequest)
	}
	return nil
}

// placeOnPool places req on a member of its pool. from is the member its
// recorded placement is on when that member is excluded ("" when nothing is
// recorded): the placement moves from it instead of being recorded anew.
func (p *RunPlacer) placeOnPool(ctx context.Context, req RunRequest, from string) (PlaceRunOutput, error) {
	row, err := p.pools.GetMachinePoolByName(ctx, req.UserID, req.Pool)
	if err != nil {
		return PlaceRunOutput{}, fmt.Errorf("machine pool %q: %w", req.Pool, err)
	}
	pool := PoolFromCore(row)
	for attempt := 1; attempt <= runPlaceAttempts; attempt++ {
		all, err := membersOf(ctx, p.pools, row, req.ProjectID)
		if err != nil {
			return PlaceRunOutput{}, err
		}
		members := req.withoutExcluded(all)
		if len(all) > 0 && len(members) == 0 {
			return named(PlaceRunOutput{Exhausted: true,
				Reason: fmt.Sprintf("every member of pool %s (%d) failed this node's preflight", pool.Name, len(all))}, pool), nil
		}
		held, err := p.heldMembers(ctx, req, pool)
		if err != nil {
			return PlaceRunOutput{}, err
		}
		members = withoutOwnSlot(members, held)
		if m, ok := preferHeld(pool, members, held); ok {
			out, err := p.record(ctx, req, pool, m, p.chatSlotGuard(pool, m, req), from)
			if errors.Is(err, errMemberBusy) {
				continue
			}
			return out, err
		}
		if m, ok := stickyMember(req, pool, members); ok {
			// The chat's run is already on m: record it there, never split
			// it from its checkout. The chat holds its slot already (live
			// runs count chats), so there is no slot to take.
			return p.record(ctx, req, pool, m, nil, from)
		}
		d := Decide(pool, members)
		if d.Kind != Run {
			return p.wait(ctx, pool, d), nil
		}
		out, err := p.record(ctx, req, pool, d.Member, p.chatSlotGuard(pool, d.Member, req), from)
		if errors.Is(err, errMemberBusy) {
			continue
		}
		return out, err
	}
	return PlaceRunOutput{PoolID: pool.ID, PoolName: pool.Name, Waiting: true,
		Reason: WaitingPrefix + pool.Name + " (every free member was taken by a concurrent placement)"}, nil
}

// heldMembers is the set of pool's members on which the run's chat already
// holds a live run: its trigger's member, and its runs' live placements.
func (p *RunPlacer) heldMembers(ctx context.Context, req RunRequest, pool Pool) (map[string]bool, error) {
	ids, err := p.pools.ListChatHeldPoolMembers(ctx, pool.ID, req.ChatID)
	if err != nil {
		return nil, fmt.Errorf("read the members chat %s holds in pool %s: %w", req.ChatID, pool.Name, err)
	}
	held := make(map[string]bool, len(ids)+1)
	for _, id := range ids {
		held[id] = true
	}
	if req.ChatPoolID != "" && req.ChatPoolID == pool.ID && req.ChatDaemonID != "" {
		held[req.ChatDaemonID] = true
	}
	return held, nil
}

// withoutOwnSlot is members with the chat's own slot taken off each member
// it holds: placing the chat there again takes no extra slot, so Decide must
// not see that member as full of the chat itself.
func withoutOwnSlot(members []Member, held map[string]bool) []Member {
	out := make([]Member, len(members))
	for i, m := range members {
		if held[m.DaemonID] && m.LiveRuns > 0 {
			m.LiveRuns--
		}
		out[i] = m
	}
	return out
}

// preferHeld is a member the chat already holds that could take the run now
// (Decide's Run rules: eligible, connected, verified, a slot free once the
// chat's own is not counted twice), least loaded first.
func preferHeld(pool Pool, members []Member, held map[string]bool) (Member, bool) {
	if len(held) == 0 {
		return Member{}, false
	}
	expected := ExpectedCatalog(members)
	var runnable []Member
	for _, m := range sortedByName(members) {
		if !held[m.DaemonID] || ineligible(m, expected) != "" || m.Phase != PhaseConnected || !m.Verified {
			continue
		}
		if pool.MaxRunsPerMember > 0 && m.LiveRuns >= pool.MaxRunsPerMember {
			continue
		}
		runnable = append(runnable, m)
	}
	if len(runnable) == 0 {
		return Member{}, false
	}
	return leastLoaded(runnable), true
}

// chatSlotGuard is SlotGuard for a placement of req's chat: under the pool's
// row lock it counts member's live runs, unless the chat already holds a live
// run there (heldMembers, read again under the lock): a chat counts once, and
// keeps that slot.
func (p *RunPlacer) chatSlotGuard(pool Pool, member Member, req RunRequest) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if err := p.pools.LockMachinePool(ctx, pool.ID); err != nil {
			return "", fmt.Errorf("lock machine pool %s: %w", pool.Name, err)
		}
		if pool.MaxRunsPerMember <= 0 {
			return "", nil
		}
		held, err := p.heldMembers(ctx, req, pool)
		if err != nil {
			return "", err
		}
		if held[member.DaemonID] {
			return "", nil
		}
		n, err := p.pools.CountMachinePoolMemberLiveRuns(ctx, pool.ID, member.DaemonID)
		if err != nil {
			return "", err
		}
		if n >= pool.MaxRunsPerMember {
			return MemberBusy, nil
		}
		return "", nil
	}
}

// stickyMember is the member a trigger placed the chat on, when this is the
// run's workflow-level placement on that same pool and the machine is still
// one of its members, even one that cannot take new work now.
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
// one transaction — or, when from names the excluded member it is recorded
// on, moves it from there to m. READ COMMITTED: the pool's row lock, which
// guard takes first, is what orders concurrent placements, and under
// SERIALIZABLE the loser of that race would fail instead of reading the
// winner's placement. The record that comes back may be a concurrent ask's
// for the same (workflow, scope): that one is the answer.
func (p *RunPlacer) record(ctx context.Context, req RunRequest, pool Pool, m Member, guard func(context.Context) (string, error), from string) (PlaceRunOutput, error) {
	var stored *core.RunMachinePlacement
	err := p.tx.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
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
		placement := &core.RunMachinePlacement{
			WorkflowID: req.WorkflowID, Scope: req.Scope, UserID: req.UserID, ChatID: req.ChatID,
			MachinePoolID: pool.ID, Selector: pool.Selector, DaemonID: m.DaemonID,
		}
		var err error
		if from != "" {
			stored, err = p.placements.MoveRunMachinePlacement(ctx, placement, from)
		} else {
			stored, err = p.placements.RecordRunMachinePlacement(ctx, placement)
		}
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
	out.PoolID, out.PoolName = rec.MachinePoolID, req.Pool
	if row, err := p.pools.GetMachinePoolByName(ctx, req.UserID, req.Pool); err == nil {
		rows, _ = p.pools.ListMachinePoolMembers(ctx, row, req.ProjectID)
	}
	for _, r := range rows {
		if r.DaemonID == rec.DaemonID {
			out.DaemonName = r.Name
		}
	}
	return out
}
