// yours: scaffolded once, never touched again — forge will not overwrite this file
package orgmove_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgmove"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// ledger is an in-memory move ledger behind orgmove.MockStore: the moves'
// states as the steps transition them, with the store's compare-and-set.
type ledger struct {
	moves  map[string]*core.ProjectOrgMove
	commit func(core.ProjectOrgMoveCommit) (*core.ProjectOrgMoveResult, error)
	// paused are the triggers the move paused and has not turned back on;
	// resumed, those it has (the triggers' history).
	paused, resumed []*core.Trigger
}

// resume moves a paused trigger to resumed, as the store's resume does.
func (l *ledger) resume(id string) {
	for i, t := range l.paused {
		if t.ID == id {
			l.resumed = append(l.resumed, t)
			l.paused = append(l.paused[:i:i], l.paused[i+1:]...)
			return
		}
	}
}

func newLedger(m core.ProjectOrgMove) *ledger {
	return &ledger{moves: map[string]*core.ProjectOrgMove{m.ID: &m}}
}

func (l *ledger) store() *orgmove.MockStore {
	return &orgmove.MockStore{
		GetProjectOrgMoveFunc: func(_ context.Context, id string) (*core.ProjectOrgMove, error) {
			m, ok := l.moves[id]
			if !ok {
				return nil, core.ErrProjectOrgMoveNotFound
			}
			c := *m
			return &c, nil
		},
		TransitionProjectOrgMoveFunc: func(_ context.Context, id string, from []core.ProjectOrgMoveState, to core.ProjectOrgMoveState, detail string, counts *core.ProjectOrgMoveCounts) (*core.ProjectOrgMove, error) {
			m := l.moves[id]
			for _, s := range from {
				if m.State == s {
					m.State, m.Detail = to, detail
					if counts != nil {
						m.Counts = *counts
					}
					c := *m
					return &c, nil
				}
			}
			return nil, core.ErrProjectOrgMoveState
		},
		MoveProjectOrgFunc: func(_ context.Context, c core.ProjectOrgMoveCommit) (*core.ProjectOrgMoveResult, error) {
			if l.commit != nil {
				if res, err := l.commit(c); err != nil || res != nil {
					return res, err
				}
			}
			m := l.moves[c.MoveID]
			if m.State != core.ProjectOrgMoveStatePrepared {
				return nil, core.ErrProjectOrgMoveState
			}
			m.State = core.ProjectOrgMoveStateMoved
			m.Counts.Chats, m.Counts.TriggersPaused = 3, 2
			cp := *m
			return &core.ProjectOrgMoveResult{Move: &cp}, nil
		},
		GetProjectFunc: func(context.Context, string) (*core.Project, error) {
			return &core.Project{ID: "p-1", Name: "shop"}, nil
		},
		ListProjectTriggersPausedByFunc: func(_ context.Context, _ string, reason string) ([]*core.Trigger, error) {
			var out []*core.Trigger
			for _, t := range l.paused {
				if t.PausedReason == reason {
					out = append(out, t)
				}
			}
			return out, nil
		},
		ListProjectTriggersResumedSinceFunc: func(context.Context, string, time.Time) ([]*core.Trigger, error) {
			return append([]*core.Trigger(nil), l.resumed...), nil
		},
	}
}

func move() core.ProjectOrgMove {
	return core.ProjectOrgMove{ID: "m-1", ProjectID: "p-1", OwnerUserID: "u-1", ActorUserID: "u-1",
		FromOrgID: "acme", ToOrgID: "globex", State: core.ProjectOrgMoveStatePreparing}
}

type cpCalls struct{ prepare, abort, record int }

func controlPlane(calls *cpCalls, prepareErr error) *orgmove.MockControlPlane {
	return &orgmove.MockControlPlane{
		PrepareProjectOrgMoveFunc: func(context.Context, controlplane.ProjectOrgMoveStep, string) (int, error) {
			calls.prepare++
			return 4, prepareErr
		},
		AbortProjectOrgMoveFunc: func(context.Context, controlplane.ProjectOrgMoveStep) error {
			calls.abort++
			return nil
		},
		RecordProjectOrgMoveFunc: func(_ context.Context, _ controlplane.ProjectOrgMoveStep, name string, counts map[string]int64) error {
			calls.record++
			return nil
		},
	}
}

func resumer(resume map[string]bool) *orgmove.MockTriggers {
	return &orgmove.MockTriggers{
		ResumeAfterOrgMoveFunc: func(_ context.Context, t *core.Trigger) (bool, string, error) {
			if resume[t.ID] {
				return true, "", nil
			}
			return false, "needs setup", nil
		},
		SyncAfterOrgMoveFunc: func(context.Context, *core.Trigger) error { return nil },
	}
}

// flakyTriggers is the trigger handler over l: ids in ok resume, others stay
// paused for setup; outages[id] attempts at resuming id fail with an outage
// first, and syncFails[id] attempts at syncing it fail after it resumed.
type flakyTriggers struct {
	l                  *ledger
	ok                 map[string]bool
	outages, syncFails map[string]int
	synced             map[string]int
}

func (f *flakyTriggers) mock() *orgmove.MockTriggers {
	f.synced = map[string]int{}
	return &orgmove.MockTriggers{
		ResumeAfterOrgMoveFunc: func(_ context.Context, t *core.Trigger) (bool, string, error) {
			if f.outages[t.ID] > 0 {
				f.outages[t.ID]--
				return false, "", errors.New("membership: control-plane unavailable")
			}
			if !f.ok[t.ID] {
				return false, "needs setup", nil
			}
			f.l.resume(t.ID)
			return true, "", nil
		},
		SyncAfterOrgMoveFunc: func(_ context.Context, t *core.Trigger) error {
			if f.syncFails[t.ID] > 0 {
				f.syncFails[t.ID]--
				return errors.New("temporal: schedule update failed")
			}
			f.synced[t.ID]++
			return nil
		},
	}
}

// movedLedger is a move that committed, with triggers paused by it.
func movedLedger(paused ...string) *ledger {
	m := move()
	m.State = core.ProjectOrgMoveStateMoved
	l := newLedger(m)
	for _, id := range paused {
		l.paused = append(l.paused, &core.Trigger{ID: id, PausedReason: core.TriggerPausedOrgMove})
	}
	return l
}

// runSaga drives ProjectOrgMoveWorkflow in the Temporal test environment with
// svc's steps as its activities.
func runSaga(t *testing.T, svc orgmove.Service) {
	t.Helper()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(orgmove.ProjectOrgMoveWorkflow, workflow.RegisterOptions{Name: orgmove.WorkflowName})
	env.RegisterActivityWithOptions(svc.Prepare, activity.RegisterOptions{Name: orgmove.PrepareActivityName})
	env.RegisterActivityWithOptions(svc.Commit, activity.RegisterOptions{Name: orgmove.CommitActivityName})
	env.RegisterActivityWithOptions(svc.Abort, activity.RegisterOptions{Name: orgmove.AbortActivityName})
	env.RegisterActivityWithOptions(svc.Record, activity.RegisterOptions{Name: orgmove.RecordActivityName})
	env.RegisterActivityWithOptions(svc.ResumeTriggers, activity.RegisterOptions{Name: orgmove.ResumeTriggersActivityName})
	env.ExecuteWorkflow(orgmove.WorkflowName, "m-1")
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
}

func newService(t *testing.T, l *ledger, cp *orgmove.MockControlPlane, tr *orgmove.MockTriggers) orgmove.Service {
	t.Helper()
	svc, err := orgmove.New(orgmove.Deps{Store: l.store(), ControlPlane: cp, Triggers: tr})
	require.NoError(t, err)
	return svc
}

// The happy path: prepared, committed, recorded in both orgs, the triggers
// that check out turned back on, and the move completed with its counts.
func TestSaga_CommitsRecordsAndResumes(t *testing.T) {
	l := newLedger(move())
	l.paused = []*core.Trigger{
		{ID: "t-ok", PausedReason: core.TriggerPausedOrgMove},
		{ID: "t-setup", PausedReason: core.TriggerPausedOrgMove},
		// Its source-org connection binding was cleared: never resumed by
		// the move, and counted as staying paused.
		{ID: "t-rebind", PausedReason: core.TriggerPausedOrgMoveSetup},
	}
	var calls cpCalls
	f := &flakyTriggers{l: l, ok: map[string]bool{"t-ok": true}}
	runSaga(t, newService(t, l, controlPlane(&calls, nil), f.mock()))

	m := l.moves["m-1"]
	assert.Equal(t, core.ProjectOrgMoveStateCompleted, m.State)
	assert.Equal(t, 4, m.Counts.SecretEntriesRevoked)
	assert.Equal(t, 3, m.Counts.Chats)
	assert.Equal(t, 1, m.Counts.TriggersReenabled)
	assert.Contains(t, m.Detail, "2 triggers stay paused", "the one that needs setup and the one to bind again")
	assert.Equal(t, cpCalls{prepare: 1, record: 1}, calls)
}

// Control-plane refusing Prepare (the actor is no longer the source's admin)
// aborts before anything moves, and Abort restores what Prepare revoked.
func TestSaga_APrepareRefusalAborts(t *testing.T) {
	l := newLedger(move())
	var calls cpCalls
	refusal := &controlplane.ProjectOrgMoveError{Code: "permission_denied", Reason: controlplane.ReasonMoveSourceAdminRequired}
	runSaga(t, newService(t, l, controlPlane(&calls, refusal), resumer(nil)))

	m := l.moves["m-1"]
	assert.Equal(t, core.ProjectOrgMoveStateAborted, m.State)
	assert.Equal(t, controlplane.ReasonMoveSourceAdminRequired, m.Detail)
	assert.Equal(t, cpCalls{prepare: 1, abort: 1}, calls)
}

// Work that started before the commit aborts the move, with the blockers in
// its detail, and compensates.
func TestSaga_RunningWorkAtCommitAborts(t *testing.T) {
	l := newLedger(move())
	l.commit = func(core.ProjectOrgMoveCommit) (*core.ProjectOrgMoveResult, error) {
		return &core.ProjectOrgMoveResult{Blockers: &core.ProjectOrgMoveBlockers{ChatsTotal: 2}}, core.ErrProjectNotQuiescent
	}
	var calls cpCalls
	runSaga(t, newService(t, l, controlPlane(&calls, nil), resumer(nil)))

	m := l.moves["m-1"]
	assert.Equal(t, core.ProjectOrgMoveStateAborted, m.State)
	assert.Contains(t, m.Detail, orgmove.DetailProjectBusy)
	assert.Contains(t, m.Detail, "2 chats")
	assert.Equal(t, cpCalls{prepare: 1, abort: 1}, calls, "Abort restores the secret access Prepare revoked")
}

// A project that left the source org meanwhile aborts with org_changed.
func TestSaga_AnOrgChangeAtCommitAborts(t *testing.T) {
	l := newLedger(move())
	l.commit = func(core.ProjectOrgMoveCommit) (*core.ProjectOrgMoveResult, error) { return nil, store.ErrOrgChanged }
	var calls cpCalls
	runSaga(t, newService(t, l, controlPlane(&calls, nil), resumer(nil)))
	assert.Equal(t, core.ProjectOrgMoveStateAborted, l.moves["m-1"].State)
	assert.Equal(t, orgmove.DetailOrgChanged, l.moves["m-1"].Detail)
}

// A commit whose answer was lost after it committed rolls forward: Abort sees
// the move moved and changes nothing, and the saga records and completes it.
func TestSaga_ALostCommitAnswerRollsForward(t *testing.T) {
	l := newLedger(move())
	l.commit = func(c core.ProjectOrgMoveCommit) (*core.ProjectOrgMoveResult, error) {
		m := l.moves[c.MoveID]
		if m.State == core.ProjectOrgMoveStatePrepared {
			m.State = core.ProjectOrgMoveStateMoved // committed ...
		}
		return nil, errors.New("connection reset") // ... and the answer lost
	}
	var calls cpCalls
	svc := newService(t, l, controlPlane(&calls, nil), resumer(nil))

	ctx := context.Background()
	_, err := svc.Prepare(ctx, "m-1")
	require.NoError(t, err)
	out, err := svc.Commit(ctx, "m-1")
	require.Error(t, err)
	assert.False(t, out.Abort)
	aborted, err := svc.Abort(ctx, "m-1", "the move could not be prepared or committed in time")
	require.NoError(t, err)
	assert.False(t, aborted, "a committed move is never aborted")
	assert.Equal(t, core.ProjectOrgMoveStateMoved, l.moves["m-1"].State)
	assert.Zero(t, calls.abort, "nothing is restored for a move that committed")
	require.NoError(t, svc.Record(ctx, "m-1"))
	require.NoError(t, svc.ResumeTriggers(ctx, "m-1"))
	assert.Equal(t, core.ProjectOrgMoveStateCompleted, l.moves["m-1"].State)
	assert.Equal(t, 1, calls.record)
}

// Every step repeats safely: a second Prepare of a prepared move calls
// control-plane no more, a second Abort of an aborted move does nothing, and
// Record and ResumeTriggers of a move that is not moved do nothing.
func TestSteps_AreIdempotent(t *testing.T) {
	ctx := context.Background()
	l := newLedger(move())
	var calls cpCalls
	svc := newService(t, l, controlPlane(&calls, nil), resumer(nil))

	_, err := svc.Prepare(ctx, "m-1")
	require.NoError(t, err)
	_, err = svc.Prepare(ctx, "m-1")
	require.NoError(t, err)
	assert.Equal(t, 1, calls.prepare)

	aborted, err := svc.Abort(ctx, "m-1", "stop")
	require.NoError(t, err)
	assert.True(t, aborted)
	aborted, err = svc.Abort(ctx, "m-1", "again")
	require.NoError(t, err)
	assert.True(t, aborted)
	assert.Equal(t, 1, calls.abort)
	assert.Equal(t, "stop", l.moves["m-1"].Detail)

	out, err := svc.Prepare(ctx, "m-1")
	require.NoError(t, err)
	assert.True(t, out.Abort, "an aborted move does not prepare again")
	require.NoError(t, svc.Record(ctx, "m-1"))
	require.NoError(t, svc.ResumeTriggers(ctx, "m-1"))
	assert.Zero(t, calls.record)
	assert.Equal(t, core.ProjectOrgMoveStateAborted, l.moves["m-1"].State)
}

// An outage while turning a trigger back on is retried, never taken for the
// trigger's own state: the move stays moved, the other triggers resume in
// the same attempt, and the retry converges with the counts of both.
func TestResumeTriggers_AnOutageIsRetried(t *testing.T) {
	ctx := context.Background()
	l := movedLedger("t-a", "t-b", "t-setup")
	f := &flakyTriggers{l: l, ok: map[string]bool{"t-a": true, "t-b": true}, outages: map[string]int{"t-b": 1}}
	svc := newService(t, l, controlPlane(&cpCalls{}, nil), f.mock())

	require.Error(t, svc.ResumeTriggers(ctx, "m-1"))
	assert.Equal(t, core.ProjectOrgMoveStateMoved, l.moves["m-1"].State, "an outage does not complete the move")
	assert.Equal(t, 1, f.synced["t-a"], "the others resume in the same attempt")

	require.NoError(t, svc.ResumeTriggers(ctx, "m-1"))
	m := l.moves["m-1"]
	assert.Equal(t, core.ProjectOrgMoveStateCompleted, m.State)
	assert.Equal(t, 2, m.Counts.TriggersReenabled, "both, though they resumed in different attempts")
	assert.Contains(t, m.Detail, "1 triggers stay paused")
}

// A trigger turned back on whose schedule then failed to converge is synced
// again on the retry, though it is no longer paused, and counted once.
func TestResumeTriggers_ASyncFailureAfterEnablingIsRetried(t *testing.T) {
	ctx := context.Background()
	l := movedLedger("t-a")
	f := &flakyTriggers{l: l, ok: map[string]bool{"t-a": true}, syncFails: map[string]int{"t-a": 1}}
	svc := newService(t, l, controlPlane(&cpCalls{}, nil), f.mock())

	require.Error(t, svc.ResumeTriggers(ctx, "m-1"))
	assert.Equal(t, core.ProjectOrgMoveStateMoved, l.moves["m-1"].State)
	assert.Empty(t, l.paused, "the row was enabled before the sync failed")

	require.NoError(t, svc.ResumeTriggers(ctx, "m-1"))
	m := l.moves["m-1"]
	assert.Equal(t, core.ProjectOrgMoveStateCompleted, m.State)
	assert.Equal(t, 1, f.synced["t-a"], "its schedule converged on the retry")
	assert.Equal(t, 1, m.Counts.TriggersReenabled)
	assert.Empty(t, m.Detail)
}

// A control plane without the service is a refusal, not an outage: the move
// aborts rather than retrying forever.
func TestPrepare_NoControlPlaneServiceAborts(t *testing.T) {
	l := newLedger(move())
	var calls cpCalls
	svc := newService(t, l, controlPlane(&calls, controlplane.ErrProjectOrgMoveUnsupported), resumer(nil))
	out, err := svc.Prepare(context.Background(), "m-1")
	require.NoError(t, err)
	assert.Equal(t, orgmove.Outcome{Abort: true, Detail: orgmove.DetailControlPlaneUnavailable}, out)

	// An outage is retried: an error, no verdict.
	svc = newService(t, l, controlPlane(&calls, errors.New("dial tcp: connection refused")), resumer(nil))
	out, err = svc.Prepare(context.Background(), "m-1")
	require.Error(t, err)
	assert.False(t, out.Abort)
}

// The commit carries the target org's live-member count, asked as the
// project's owner, so the moved triggers are re-stamped from it; a count that
// cannot be read fails the step (retried), never committing an "any" stamp.
func TestCommit_CarriesTheTargetOrgsMemberCount(t *testing.T) {
	m := move()
	m.State = core.ProjectOrgMoveStatePrepared
	l := newLedger(m)
	var got []core.ProjectOrgMoveCommit
	l.commit = func(c core.ProjectOrgMoveCommit) (*core.ProjectOrgMoveResult, error) {
		got = append(got, c)
		return nil, nil
	}
	countErr := errors.New("control-plane down")
	members := &orgmove.MockMembers{LiveMembersFreshFunc: func(_ context.Context, userID, orgID string) (int, bool, error) {
		assert.Equal(t, "u-1", userID)
		assert.Equal(t, "globex", orgID)
		return 0, false, countErr
	}}
	svc, err := orgmove.New(orgmove.Deps{Store: l.store(), ControlPlane: controlPlane(&cpCalls{}, nil), Triggers: resumer(nil), Members: members})
	require.NoError(t, err)

	_, err = svc.Commit(context.Background(), "m-1")
	require.ErrorIs(t, err, countErr)
	assert.Empty(t, got, "nothing committed on an unread count")

	members.LiveMembersFreshFunc = func(context.Context, string, string) (int, bool, error) { return 3, true, nil }
	_, err = svc.Commit(context.Background(), "m-1")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, core.ProjectOrgMoveCommit{MoveID: "m-1", TargetLiveMembers: 3, TargetLiveMembersKnown: true}, got[0])
}
