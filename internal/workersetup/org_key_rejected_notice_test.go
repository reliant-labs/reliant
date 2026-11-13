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

// recordingStarter records the notices started, by workflow id.
type recordingStarter struct {
	mu      sync.Mutex
	started []client.StartWorkflowOptions
	notices []controlplane.OrgKeyRejectedNotice
	err     error
}

func (s *recordingStarter) ExecuteWorkflow(_ context.Context, o client.StartWorkflowOptions, wf any, args ...any) (client.WorkflowRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	s.started = append(s.started, o)
	s.notices = append(s.notices, args[0].(controlplane.OrgKeyRejectedNotice))
	return nil, nil
}

// A rejected org key is marked and its notice started, named for the
// incident (the time it was first marked); a person's own key, a failed mark
// and a start that fails send nothing or change nothing.
func TestCredentialMarker_StartsTheNoticeOfARejectedOrgKey(t *testing.T) {
	marked := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	store := func(err error) *MockAccountCredentialStore {
		return &MockAccountCredentialStore{PatchOwnedProviderAccountFunc: func(_ context.Context, owner core.AccountOwner, id string, p core.AccountPatch) (core.ProviderAccount, error) {
			if err != nil {
				return core.ProviderAccount{}, err
			}
			require.NotNil(t, p.NeedsReauth)
			return core.ProviderAccount{ID: id, Provider: "anthropic", Label: "prod", KeyHint: "a1b2", NeedsReauthAt: &marked,
				OwnerKind: owner.Kind, OrgID: owner.ID}, nil
		}}
	}
	queue := func(s *recordingStarter) orgKeyNoticeQueue { return orgKeyNoticeQueue{starter: s, taskQueue: "q"} }

	t.Run("an org's key", func(t *testing.T) {
		s := &recordingStarter{}
		m := credentialMarker{repo: store(nil), notices: queue(s)}
		for range 2 { // a second worker meeting the same rejection
			require.NoError(t, m.MarkNeedsSignIn(context.Background(), core.OrgAccountOwner("org-acme"), "acct-1", "401 invalid key"))
		}
		require.Len(t, s.started, 2)
		assert.Equal(t, s.started[0].ID, s.started[1].ID, "one incident, one workflow id")
		assert.Equal(t, "org-key-rejected/org-acme/acct-1/2030-01-02T03:04:05Z", s.started[0].ID)
		assert.Equal(t, "q", s.started[0].TaskQueue)
		assert.Equal(t, controlplane.OrgKeyRejectedNotice{
			OrgID: "org-acme", IncidentKey: "2030-01-02T03:04:05Z", AccountID: "acct-1",
			Provider: "Anthropic API", Label: "prod", KeyHint: "a1b2", Reason: "401 invalid key",
		}, s.notices[0])
	})
	t.Run("a person's own key", func(t *testing.T) {
		s := &recordingStarter{}
		m := credentialMarker{repo: store(nil), notices: queue(s)}
		require.NoError(t, m.MarkNeedsSignIn(context.Background(), core.UserAccountOwner("alice"), "acct-1", "401"))
		assert.Empty(t, s.started)
	})
	t.Run("a mark that fails tells nobody", func(t *testing.T) {
		s := &recordingStarter{}
		m := credentialMarker{repo: store(errors.New("db down")), notices: queue(s)}
		require.Error(t, m.MarkNeedsSignIn(context.Background(), core.OrgAccountOwner("org-acme"), "acct-1", "401"))
		assert.Empty(t, s.started)
	})
	t.Run("a start that fails leaves the mark standing", func(t *testing.T) {
		m := credentialMarker{repo: store(nil), notices: queue(&recordingStarter{err: errors.New("temporal down")})}
		assert.NoError(t, m.MarkNeedsSignIn(context.Background(), core.OrgAccountOwner("org-acme"), "acct-1", "401"))
	})
	t.Run("no temporal", func(t *testing.T) {
		m := credentialMarker{repo: store(nil)}
		assert.NoError(t, m.MarkNeedsSignIn(context.Background(), core.OrgAccountOwner("org-acme"), "acct-1", "401"))
	})
}

// A start for an incident whose notice already finished is the same notice.
func TestStartOrgKeyNotice_AnAlreadyStartedIncidentIsTheSameNotice(t *testing.T) {
	s := &recordingStarter{err: serviceerror.NewWorkflowExecutionAlreadyStarted("started", "", "")}
	assert.NoError(t, startOrgKeyNotice(context.Background(), s, "q", controlplane.OrgKeyRejectedNotice{OrgID: "o", AccountID: "a", IncidentKey: "i"}))
}

// scriptedNotifier answers each call with the next error of errs (nil once
// they run out), counting calls.
type scriptedNotifier struct {
	mu    sync.Mutex
	errs  []error
	calls int
}

func (s *scriptedNotifier) NotifyOrgKeyRejected(context.Context, controlplane.OrgKeyRejectedNotice) (bool, error) {
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

func runNotice(t *testing.T, admins OrgAdminNotifier) error {
	t.Helper()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(orgKeyNotices{admins: admins}.Notify, activity.RegisterOptions{Name: NotifyOrgKeyRejectedActivityName})
	env.ExecuteWorkflow(OrgKeyRejectedNoticeWorkflow, controlplane.OrgKeyRejectedNotice{OrgID: "org-acme", AccountID: "acct-1", IncidentKey: "i1"})
	require.True(t, env.IsWorkflowCompleted())
	return env.GetWorkflowError()
}

// The control plane's Unavailable (another call holds the incident's lease,
// or the mail provider refused) and an outage are retried with backoff until
// it answers; the notice then completes.
func TestOrgKeyRejectedNotice_RetriesUntilTheControlPlaneAnswers(t *testing.T) {
	leased := &controlplane.ProjectOrgMoveError{Code: "unavailable", Message: "being sent; retry"}
	admins := &scriptedNotifier{errs: []error{leased, errors.New("connection refused"), leased}}
	require.NoError(t, runNotice(t, admins))
	assert.Equal(t, 4, admins.calls)
}

// A refusal a retry cannot change ends the notice, asked once.
func TestOrgKeyRejectedNotice_APermanentRefusalIsNotRetried(t *testing.T) {
	admins := &scriptedNotifier{errs: []error{&controlplane.ProjectOrgMoveError{Code: "not_found", Message: "org deleted"}}}
	err := runNotice(t, admins)
	require.Error(t, err)
	var app *temporal.ApplicationError
	require.ErrorAs(t, err, &app)
	assert.True(t, app.NonRetryable())
	assert.Equal(t, 1, admins.calls)
}

// A control plane without the service, or a worker without a control plane,
// has nothing to send: the notice ends quietly, asked at most once.
func TestOrgKeyRejectedNotice_NothingToSendEndsIt(t *testing.T) {
	admins := &scriptedNotifier{errs: []error{controlplane.ErrOrgNotifyUnsupported}}
	require.NoError(t, runNotice(t, admins))
	assert.Equal(t, 1, admins.calls)
	require.NoError(t, runNotice(t, nil))
}
