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

func (s *pauseStore) ListUnstartedPauseNotices(context.Context, int) ([]*core.Trigger, error) {
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
// with the model the launch refused, and confirms it; a trigger resumed since
// (or paused for another reason) starts nothing; a start that fails leaves it
// unconfirmed, and the sweep starts it later; AlreadyStarted counts as started.
func TestNoOrgAccountPauseNotifier_StartsAndConfirms(t *testing.T) {
	store := &pauseStore{triggers: map[string]*core.Trigger{"t-1": noOrgAccountTrigger("t-1")}}
	s := &pauseStarter{}
	notify := noOrgAccountPauseNotifier(store, orgKeyNoticeQueue{starter: s, taskQueue: "q"})

	notify(context.Background(), "t-1", "claude-opus-4-6")
	require.Equal(t, []string{"org-automation-no-org-account/org-acme/t-1/2030-01-02T03:04:05Z"}, s.ids)
	assert.Equal(t, controlplane.OrgAutomationNoOrgAccountNotice{
		OrgID: "org-acme", IncidentKey: "2030-01-02T03:04:05Z", TriggerID: "t-1", Automation: "Nightly t-1",
		Model: "claude-opus-4-6", OwnerUserID: "u-mia",
	}, s.notices[0])
	assert.Equal(t, []string{"t-1"}, store.confirmed)

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
	assert.Empty(t, failing.confirmed, "a failed start is not confirmed")
	down.err = nil
	assert.Equal(t, 1, sweepPauseNotices(context.Background(), failing, orgKeyNoticeQueue{starter: down, taskQueue: "q"}))
	assert.Equal(t, []string{"t-1"}, failing.confirmed, "the sweep starts it")

	failing.confirmed = nil
	down.err = serviceerror.NewWorkflowExecutionAlreadyStarted("started", "", "")
	assert.Equal(t, 1, sweepPauseNotices(context.Background(), failing, orgKeyNoticeQueue{starter: down, taskQueue: "q"}))
	assert.Equal(t, []string{"t-1"}, failing.confirmed, "already started is started")
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

func runPauseNotice(t *testing.T, admins pauseNotifier) error {
	t.Helper()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(pauseNotices{admins: admins}.Notify, activity.RegisterOptions{Name: NotifyOrgAutomationNoOrgAccountActivityName})
	env.ExecuteWorkflow(OrgAutomationNoOrgAccountNoticeWorkflow, controlplane.OrgAutomationNoOrgAccountNotice{OrgID: "org-acme", TriggerID: "t-1", IncidentKey: "i1"})
	require.True(t, env.IsWorkflowCompleted())
	return env.GetWorkflowError()
}

// The notice retries the lease answer and outages until the control plane
// answers; a control plane that predates the notice kind (invalid_argument)
// ends it at once; one without the service ends it quietly.
func TestOrgAutomationNoOrgAccountNotice_Delivery(t *testing.T) {
	leased := &controlplane.ProjectOrgMoveError{Code: "unavailable", Message: "being sent; retry"}
	retried := &scriptedPauseNotifier{errs: []error{leased, errors.New("connection refused")}}
	require.NoError(t, runPauseNotice(t, retried))
	assert.Equal(t, 3, retried.calls)

	old := &scriptedPauseNotifier{errs: []error{&controlplane.ProjectOrgMoveError{Code: "invalid_argument", Message: "a notice is required"}}}
	err := runPauseNotice(t, old)
	var app *temporal.ApplicationError
	require.ErrorAs(t, err, &app)
	assert.True(t, app.NonRetryable())
	assert.Equal(t, 1, old.calls)

	none := &scriptedPauseNotifier{errs: []error{controlplane.ErrOrgNotifyUnsupported}}
	require.NoError(t, runPauseNotice(t, none))
	require.NoError(t, runPauseNotice(t, nil))
}
