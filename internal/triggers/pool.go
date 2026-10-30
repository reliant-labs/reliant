// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/machinepool"
)

// poolWaitingPrefix starts the reason a pool-bound fire records while no
// member can take its run: the event stays pending and the redriver fires it
// again, exactly as for workflow capacity.
const poolWaitingPrefix = "waiting for a machine in pool "

// poolPlaceAttempts bounds re-placing after a concurrent launch took the
// chosen member's last slot (machinepool.MemberBusy).
const poolPlaceAttempts = 3

// Placer places a pool-bound trigger's runs. *machinepool.Placer implements it.
type Placer interface {
	Place(ctx context.Context, userID, poolID, projectID string) (machinepool.Placement, error)
	Capacity(ctx context.Context, userID, poolID, projectID string, perMember uint32) (uint32, error)
	SlotGuard(pool machinepool.Pool, member machinepool.Member) func(ctx context.Context) (string, error)
}

// errNoPlacer is a pool-bound trigger fired by a process with no placer
// wired: a configuration bug, retried rather than guessed around.
var errNoPlacer = errors.New("machine pool placement is not configured on this worker")

// isPoolWaiting reports whether a decline reason is a pool's queue.
func isPoolWaiting(reason string) bool {
	return len(reason) >= len(poolWaitingPrefix) && reason[:len(poolWaitingPrefix)] == poolWaitingPrefix
}

// capacityLimit resolves a workflow's concurrency policy to the cap for this
// trigger now; ok is false when the workflow declares no policy.
//
// max_running alone is the cap. per_member K is K for each member of the
// trigger's pool that can take work (machinepool.PerMemberCapacity), or K for
// a trigger on one machine or none (a pool of one); max_running then caps it.
// A pool with no eligible member and nothing running yields 0: its runs wait.
func capacityLimit(ctx context.Context, placer Placer, t *core.Trigger, policy *reliantv1.WorkflowConcurrency) (limit uint32, ok bool, err error) {
	if policy == nil || (policy.GetMaxRunning() == 0 && policy.GetPerMember() == 0) {
		return 0, false, nil
	}
	if policy.GetPerMember() == 0 {
		return policy.GetMaxRunning(), true, nil
	}
	limit = policy.GetPerMember()
	if t.MachinePoolID != "" {
		if placer == nil {
			return 0, true, errNoPlacer
		}
		limit, err = placer.Capacity(ctx, t.UserID, t.MachinePoolID, t.ProjectID, policy.GetPerMember())
		if err != nil {
			return 0, true, fmt.Errorf("pool capacity of trigger %s: %w", t.ID, err)
		}
	}
	if max := policy.GetMaxRunning(); max > 0 && limit > max {
		limit = max
	}
	return limit, true, nil
}

// launchOnPool launches spec on a member of the trigger's pool: place, take
// the member's slot inside the launch transaction (composed ahead of any
// guard spec already has), and on a lost race re-place, up to
// poolPlaceAttempts. A trigger with no pool launches as given.
//
// resuming (a fire whose event already launched) never re-places: the chat
// exists and is pinned to its member, and moving it would split the run from
// its checkout. Placement then only reports the pool to the run's inputs.
//
// A pool with no free member declines with a poolWaitingPrefix reason naming
// why each member was passed over.
func launchOnPool(ctx context.Context, placer Placer, launcher Launcher, t *core.Trigger, ev launch.Event, spec launch.Spec, resuming bool) (*launch.Result, error) {
	if t.MachinePoolID == "" {
		return launcher.Launch(ctx, ev, spec)
	}
	if placer == nil {
		return nil, errNoPlacer
	}
	base := spec.Guard
	for attempt := 1; ; attempt++ {
		placed, err := placer.Place(ctx, t.UserID, t.MachinePoolID, t.ProjectID)
		if errors.Is(err, core.ErrMachinePoolNotFound) {
			return nil, &launch.ValidationError{Reason: "the trigger's machine pool no longer exists; edit the trigger to choose a machine or pool"}
		}
		if err != nil {
			return nil, fmt.Errorf("place trigger %s on pool %s: %w", t.ID, t.MachinePoolID, err)
		}
		spec.MachinePool = poolPlacement(placed)
		if resuming {
			spec.DaemonID = ""
			return launcher.Launch(ctx, ev, spec)
		}
		d := placed.Decision
		if d.Kind == machinepool.Queue {
			return nil, &launch.DeclinedError{Reason: d.Reason(placed.Pool.Name)}
		}
		spec.DaemonID = d.Member.DaemonID
		spec.Guard = composeGuards(placer.SlotGuard(placed.Pool, d.Member), base)
		res, err := launcher.Launch(ctx, ev, spec)
		var declined *launch.DeclinedError
		if errors.As(err, &declined) && declined.Reason == machinepool.MemberBusy && attempt < poolPlaceAttempts {
			continue
		}
		if errors.As(err, &declined) && declined.Reason == machinepool.MemberBusy {
			return nil, &launch.DeclinedError{Reason: poolWaitingPrefix + placed.Pool.Name + " (every free member was taken by a concurrent launch)"}
		}
		return res, err
	}
}

// recordPoolWait keeps a scheduled fire that its pool could not place: a
// pending event row (the slot's identity, with why it waits) that the
// redriver fires again until a member is free or the event expires. An
// inbound event already has its pending row; a capped schedule fire got one
// from prepareCapacity; only an uncapped schedule fire needs it written.
func (f *Firer) recordPoolWait(ctx context.Context, t *core.Trigger, ev launch.Event, reason string) (*FireOutput, error) {
	stored, err := f.repo.GetTriggerEventByDedupe(ctx, ev.Kind, ev.DedupeKey)
	switch {
	case err == nil:
		return &FireOutput{Outcome: string(core.TriggerEventPending), Reason: reason, EventID: stored.ID}, nil
	case !errors.Is(err, core.ErrTriggerEventNotFound):
		return nil, fmt.Errorf("load event for fire %s: %w", ev.DedupeKey, err)
	}
	row := &core.TriggerEvent{
		ID: uuid.NewString(), UserID: t.UserID, TriggerID: &t.ID, Kind: ev.Kind, DedupeKey: ev.DedupeKey,
		OccurredAt: ev.OccurredAt, CreatedAt: f.now(), Payload: ev.Payload,
		Outcome: core.TriggerEventPending, OutcomeDetail: reason,
	}
	if _, err := f.repo.CreateTriggerEvent(ctx, row); err != nil {
		return nil, fmt.Errorf("record the pool wait of fire %s: %w", ev.DedupeKey, err)
	}
	return &FireOutput{Outcome: string(core.TriggerEventPending), Reason: reason, EventID: row.ID}, nil
}

// composeGuards runs first, then next; the first decline wins.
func composeGuards(first, next func(context.Context) (string, error)) func(context.Context) (string, error) {
	if next == nil {
		return first
	}
	return func(ctx context.Context) (string, error) {
		if reason, err := first(ctx); err != nil || reason != "" {
			return reason, err
		}
		return next(ctx)
	}
}

func poolPlacement(p machinepool.Placement) *launch.PoolPlacement {
	out := &launch.PoolPlacement{ID: p.Pool.ID, Name: p.Pool.Name}
	for _, m := range p.Members {
		out.Members = append(out.Members, launch.PoolMember{ID: m.DaemonID, Name: m.Name, State: string(m.Phase)})
	}
	return out
}
