// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

var pausedAt = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

func noOrgAccountTrigger(id string) *core.Trigger {
	org, at := "org-acme", pausedAt
	return &core.Trigger{ID: id, UserID: "u-mia", OrgID: &org, Name: "Nightly " + id,
		PausedReason: core.TriggerPausedNoOrgAccount, PausedAt: &at}
}

// pauseStore is the triggers, the unconfirmed pauses and the confirms made.
type pauseStore struct {
	mu        sync.Mutex
	triggers  map[string]*core.Trigger
	confirmed []string
}

func (s *pauseStore) GetTrigger(_ context.Context, id string) (*core.Trigger, error) {
	if t, ok := s.triggers[id]; ok {
		return t, nil
	}
	return nil, core.ErrTriggerNotFound
}

func (s *pauseStore) ListUndeliveredPauseNotices(context.Context, int) ([]*core.Trigger, error) {
	var out []*core.Trigger
	for _, t := range s.triggers {
		out = append(out, t)
	}
	return out, nil
}

func (s *pauseStore) ConfirmPauseNotice(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !at.Equal(pausedAt) {
		return errors.New("not this pause")
	}
	s.confirmed = append(s.confirmed, id)
	return nil
}

// pauseStarter records the pause notices started.
type pauseStarter struct {
	mu      sync.Mutex
	ids     []string
	notices []controlplane.OrgAutomationNoOrgAccountNotice
	err     error
}

func (s *pauseStarter) ExecuteWorkflow(_ context.Context, o client.StartWorkflowOptions, _ any, args ...any) (client.WorkflowRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	s.ids = append(s.ids, o.ID)
	s.notices = append(s.notices, args[0].(controlplane.OrgAutomationNoOrgAccountNotice))
	return nil, nil
}

// A pause for want of an org account starts its notice, named for the pause,
// with the model the launch refused, and confirms nothing: the workflow
// records the delivery. A trigger resumed since (or paused for another reason)
// starts nothing; a start that fails is started by the sweep; a notice already
// running is left to run.
func TestNoOrgAccountPauseNotifier_Starts(t *testing.T) {
	store := &pauseStore{triggers: map[string]*core.Trigger{"t-1": noOrgAccountTrigger("t-1")}}
	s := &pauseStarter{}
	notify := noOrgAccountPauseNotifier(store, orgKeyNoticeQueue{starter: s, taskQueue: "q"})

	notify(context.Background(), "t-1", "claude-opus-4-6")
	require.Equal(t, []string{"org-automation-no-org-account/org-acme/t-1/2030-01-02T03:04:05Z"}, s.ids)
	assert.Equal(t, controlplane.OrgAutomationNoOrgAccountNotice{
		OrgID: "org-acme", IncidentKey: "2030-01-02T03:04:05Z", TriggerID: "t-1", Automation: "Nightly t-1",
		Model: "claude-opus-4-6", OwnerUserID: "u-mia",
	}, s.notices[0])
	assert.Empty(t, store.confirmed, "a start is not a delivery")

	resumed := noOrgAccountTrigger("t-2")
	resumed.Enabled = true
	other := noOrgAccountTrigger("t-3")
	other.PausedReason = core.TriggerPausedCreatorLeftOrg
	store.triggers["t-2"], store.triggers["t-3"] = resumed, other
	notify(context.Background(), "t-2", "")
	notify(context.Background(), "t-3", "")
	assert.Len(t, s.ids, 1, "nothing for a trigger no longer paused for it")

	failing := &pauseStore{triggers: map[string]*core.Trigger{"t-1": noOrgAccountTrigger("t-1")}}
	down := &pauseStarter{err: errors.New("temporal down")}
	noOrgAccountPauseNotifier(failing, orgKeyNoticeQueue{starter: down, taskQueue: "q"})(context.Background(), "t-1", "")
	assert.Empty(t, down.ids, "the start failed")
	down.err = nil
	assert.Equal(t, 1, sweepPauseNotices(context.Background(), failing, orgKeyNoticeQueue{starter: down, taskQueue: "q"}))
	assert.Len(t, down.ids, 1, "the sweep starts it")

	down.err = serviceerror.NewWorkflowExecutionAlreadyStarted("started", "", "")
	assert.Equal(t, 0, sweepPauseNotices(context.Background(), failing, orgKeyNoticeQueue{starter: down, taskQueue: "q"}),
		"a notice already running is not started again")
	assert.Empty(t, failing.confirmed, "only the workflow confirms")
}

// scriptedPauseNotifier answers with the next error of errs, nil once spent.
type scriptedPauseNotifier struct {
	mu    sync.Mutex
	errs  []error
	calls int
}

func (s *scriptedPauseNotifier) NotifyOrgAutomationNoOrgAccount(context.Context, controlplane.OrgAutomationNoOrgAccountNotice) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if len(s.errs) == 0 {
		return false, nil
	}
	err := s.errs[0]
	s.errs = s.errs[1:]
	return false, err
}

func runPauseNotice(t *testing.T, admins pauseNotifier) (*pauseStore, error) {
	t.Helper()
	store := &pauseStore{triggers: map[string]*core.Trigger{"t-1": noOrgAccountTrigger("t-1")}}
	n := *pauseNotice(store.triggers["t-1"], "")
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	activities := pauseNotices{admins: admins, store: store}
	env.RegisterActivityWithOptions(activities.Notify, activity.RegisterOptions{Name: NotifyOrgAutomationNoOrgAccountActivityName})
	env.RegisterActivityWithOptions(activities.Confirm, activity.RegisterOptions{Name: ConfirmPauseNoticeActivityName})
	env.ExecuteWorkflow(OrgAutomationNoOrgAccountNoticeWorkflow, n)
	require.True(t, env.IsWorkflowCompleted())
	return store, env.GetWorkflowError()
}

// oldControlPlane is how a control plane that predates the notice kind answers
// it (its NotifyOrgAdmins receives an empty oneof).
var oldControlPlane = &controlplane.ProjectOrgMoveError{Code: "invalid_argument", Message: "a notice is required"}

// The notice retries the lease answer and outages until the control plane
// answers, and only then records the pause as notified. A refusal a retry
// cannot change ends it unconfirmed; no control plane to ask is delivered (the
// inbox item is the notice).
func TestOrgAutomationNoOrgAccountNotice_Delivery(t *testing.T) {
	leased := &controlplane.ProjectOrgMoveError{Code: "unavailable", Message: "being sent; retry"}
	retried := &scriptedPauseNotifier{errs: []error{leased, errors.New("connection refused")}}
	store, err := runPauseNotice(t, retried)
	require.NoError(t, err)
	assert.Equal(t, 3, retried.calls)
	assert.Equal(t, []string{"t-1"}, store.confirmed, "confirmed once delivered")

	gone := &scriptedPauseNotifier{errs: []error{&controlplane.ProjectOrgMoveError{Code: "not_found", Message: "organization not found"}}}
	store, err = runPauseNotice(t, gone)
	var app *temporal.ApplicationError
	require.ErrorAs(t, err, &app)
	assert.True(t, app.NonRetryable())
	assert.Equal(t, 1, gone.calls)
	assert.Empty(t, store.confirmed, "a refusal is not a delivery")

	store, err = runPauseNotice(t, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"t-1"}, store.confirmed, "no control plane: the inbox item is the notice")
}

// B1: a control plane that predates the notice kind (or has no
// OrgNotifyInternalService, or answers Unimplemented) cannot send it yet. The
// notice is not lost: the same pause's workflow waits and asks again, and once
// the control plane is upgraded the email goes out and the pause is
// confirmed, with no new pause.
func TestOrgAutomationNoOrgAccountNotice_WaitsForAnUpgradedControlPlane(t *testing.T) {
	for name, cannot := range map[string]error{
		"predates the kind": oldControlPlane,
		"no service":        controlplane.ErrOrgNotifyUnsupported,
		"unimplemented":     &controlplane.ProjectOrgMoveError{Code: "unimplemented"},
	} {
		t.Run(name, func(t *testing.T) {
			upgraded := &scriptedPauseNotifier{errs: []error{cannot, cannot}}
			store, err := runPauseNotice(t, upgraded)
			require.NoError(t, err)
			assert.Equal(t, 3, upgraded.calls, "asked again after each wait, then sent")
			assert.Equal(t, []string{"t-1"}, store.confirmed, "the same pause, notified")
		})
	}

	// Never upgraded: the workflow gives up unconfirmed after its waits, so the
	// sweep starts the pause's notice again.
	errs := make([]error, len(pauseNoticeUpgradeWaits)+1)
	for i := range errs {
		errs[i] = oldControlPlane
	}
	never := &scriptedPauseNotifier{errs: errs}
	store, err := runPauseNotice(t, never)
	var app *temporal.ApplicationError
	require.ErrorAs(t, err, &app)
	assert.Equal(t, "OrgNoticeUnsupported", app.Type())
	assert.Equal(t, len(pauseNoticeUpgradeWaits)+1, never.calls)
	assert.Empty(t, store.confirmed)
	restarted := &pauseStarter{}
	assert.Equal(t, 1, sweepPauseNotices(context.Background(), store, orgKeyNoticeQueue{starter: restarted, taskQueue: "q"}),
		"still owed: the sweep starts it again")

	// A refusal that is not about the kind is not mistaken for one.
	assert.False(t, cannotSendNoticeKind(&controlplane.ProjectOrgMoveError{Code: "invalid_argument", Message: "a field is too long"}))
}
