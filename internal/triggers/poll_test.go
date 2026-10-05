// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

// scriptedPoller is a test poller over an append-only feed. The cursor is
// the index of the next unseen item.
type scriptedPoller struct {
	mu    sync.Mutex
	items []PollItem
	calls int
	err   error
	last  PollRequest
}

// fakeCred is a resolved credential as the poller sees it.
type fakeCred struct{ id string }

func (c fakeCred) ConnectionID() string    { return c.id }
func (fakeCred) Apply(*http.Request) error { return nil }
func (fakeCred) Scrub(s string) string     { return s }
func (fakeCred) Params() map[string]string { return nil }

// fakeCreds records what the activity asked for. It answers from the
// trigger id alone, as the real source does.
type fakeCreds struct {
	mu    sync.Mutex
	asked []string
	err   error
	cred  httpaction.Credential
}

func (f *fakeCreds) ForTrigger(_ context.Context, triggerID string) (httpaction.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, triggerID)
	if f.err != nil {
		return nil, f.err
	}
	return f.cred, nil
}

func (p *scriptedPoller) add(item PollItem) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.items = append(p.items, item)
}

func (p *scriptedPoller) Poll(_ context.Context, req PollRequest) (*PollResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.last = req
	if p.err != nil {
		return nil, p.err
	}
	end := len(p.items)
	if req.Cursor == "" {
		// The baseline: where the feed is now.
		return &PollResult{Cursor: itoa(end)}, nil
	}
	start := atoi(req.Cursor)
	return &PollResult{Cursor: itoa(end), Items: append([]PollItem(nil), p.items[start:end]...)}, nil
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
func atoi(s string) int { var n int; _ = json.Unmarshal([]byte(s), &n); return n }

type fakePollers map[string]Poller

func (f fakePollers) Poller(integration string) (Poller, bool) { p, ok := f[integration]; return p, ok }

// pollRepo adds registrations and integration routing to fakeRepo.
type pollRepo struct {
	*fakeRepo
	mu       sync.Mutex
	regs     map[string]*core.TriggerRegistration
	accounts map[string]*core.Connection
}

func (r *pollRepo) GetTriggerRegistration(_ context.Context, id string) (*core.TriggerRegistration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg, ok := r.regs[id]
	if !ok {
		return nil, core.ErrTriggerRegistrationNotFound
	}
	copied := *reg
	return &copied, nil
}

func (r *pollRepo) UpsertTriggerRegistration(_ context.Context, reg *core.TriggerRegistration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := *reg
	r.regs[reg.TriggerID] = &copied
	return nil
}

func (r *pollRepo) GetConnection(_ context.Context, userID, id string) (*core.Connection, error) {
	conn, ok := r.accounts[id]
	if !ok || conn.UserID != userID {
		return nil, errors.New("not found")
	}
	return conn, nil
}

func pollTrigger(t *testing.T) *core.Trigger {
	t.Helper()
	trigger := testTrigger(t, nil)
	trigger.Kind = core.TriggerKindIntegration
	connID := "conn-1"
	trigger.ConnectionID = &connID
	raw, err := json.Marshal(core.IntegrationConfig{Integration: "feed", Events: []string{"item.created"}})
	require.NoError(t, err)
	trigger.Config = raw
	return trigger
}

func newPollEnv(t *testing.T) (*pollRepo, *core.Trigger, *scriptedPoller, *recordingStarter, *TriggerPoller) {
	repo, trigger, poller, starter, p, _ := newPollEnvWithCreds(t)
	return repo, trigger, poller, starter, p
}

func newPollEnvWithCreds(t *testing.T) (*pollRepo, *core.Trigger, *scriptedPoller, *recordingStarter, *TriggerPoller, *fakeCreds) {
	t.Helper()
	repo := &pollRepo{fakeRepo: newFakeRepo(), regs: map[string]*core.TriggerRegistration{}, accounts: map[string]*core.Connection{}}
	trigger := pollTrigger(t)
	repo.triggers[trigger.ID] = trigger
	repo.accounts["conn-1"] = &core.Connection{ID: "conn-1", UserID: trigger.UserID, IntegrationID: "feed", Status: core.ConnectionStatusActive}
	poller := &scriptedPoller{}
	starter := &recordingStarter{}
	creds := &fakeCreds{cred: fakeCred{id: "conn-1"}}
	p := NewTriggerPoller(repo, fakePollers{"feed": poller}, NewIntake(repo, starter, ""), creds)
	return repo, trigger, poller, starter, p, creds
}

// The activity resolves the credential by trigger id — nothing else crosses
// that boundary — and the poller receives it on the request.
func TestPollHandsThePollerTheTriggersCredential(t *testing.T) {
	_, trigger, poller, _, p, creds := newPollEnvWithCreds(t)
	_, err := p.Poll(context.Background(), PollInput{TriggerID: trigger.ID})
	require.NoError(t, err)
	assert.Equal(t, []string{trigger.ID}, creds.asked)
	require.NotNil(t, poller.last.Credential, "the poller is handed a credential, never a source")
	assert.Equal(t, "conn-1", poller.last.Credential.ConnectionID())
	assert.Equal(t, trigger.UserID, poller.last.OwnerUserID, "the owner is the trigger row's")
}

// A refused refresh (Gmail's seven-day testing tokens) is recorded on the
// registration as needs_reauth and the poll is skipped: no retry loop, the
// cursor kept, and the poller never called.
func TestPollNeedsReauthIsRecordedAndNotRetried(t *testing.T) {
	repo, trigger, poller, _, p, creds := newPollEnvWithCreds(t)
	ctx := context.Background()
	_, err := p.Poll(ctx, PollInput{TriggerID: trigger.ID})
	require.NoError(t, err)
	callsAfterBaseline := poller.calls

	creds.err = &httpaction.CredentialError{Code: httpaction.CodeNeedsReauth, Message: `connection "work" needs to be reconnected`}
	out, err := p.Poll(ctx, PollInput{TriggerID: trigger.ID})
	require.NoError(t, err, "a non-nil error would make Temporal retry a dead grant")
	assert.True(t, out.Skipped)
	assert.Contains(t, out.Reason, "reconnect")
	assert.Equal(t, callsAfterBaseline, poller.calls, "nothing is polled without a credential")

	reg, err := repo.GetTriggerRegistration(ctx, trigger.ID)
	require.NoError(t, err)
	assert.Equal(t, core.TriggerRegistrationNeedsReauth, reg.Status)
	assert.Contains(t, reg.StatusDetail, "reconnect")
	assert.Equal(t, "0", reg.Cursor, "the cursor survives, so a reconnect resumes without replay")
	require.NotNil(t, reg.LastPolledAt)

	// Reconnected: the next poll clears the state and resumes from the cursor.
	creds.err = nil
	_, err = p.Poll(ctx, PollInput{TriggerID: trigger.ID})
	require.NoError(t, err)
	reg, _ = repo.GetTriggerRegistration(ctx, trigger.ID)
	assert.Equal(t, core.TriggerRegistrationActive, reg.Status)
}

// A provider that refuses the credential mid-poll (Google answering 401 for a
// token revoked before it expired) is the same verdict, reported by the
// poller: needs_reauth, no retry, cursor kept. Anything else the poller
// returns is scrubbed before it is stored.
func TestPollerReportedNeedsReauthAndScrubbedErrors(t *testing.T) {
	repo, trigger, poller, _, p, creds := newPollEnvWithCreds(t)
	ctx := context.Background()
	_, err := p.Poll(ctx, PollInput{TriggerID: trigger.ID})
	require.NoError(t, err)

	poller.err = &httpaction.CredentialError{Code: httpaction.CodeNeedsReauth, Message: "Gmail rejected the credential: reconnect Gmail"}
	out, err := p.Poll(ctx, PollInput{TriggerID: trigger.ID})
	require.NoError(t, err)
	assert.True(t, out.Skipped)
	reg, _ := repo.GetTriggerRegistration(ctx, trigger.ID)
	assert.Equal(t, core.TriggerRegistrationNeedsReauth, reg.Status)
	assert.Equal(t, "0", reg.Cursor)

	creds.cred = scrubbingCred{fakeCred: fakeCred{id: "conn-1"}, secret: "ya29.SECRET"}
	poller.err = errors.New("upstream echoed Authorization: Bearer ya29.SECRET")
	_, err = p.Poll(ctx, PollInput{TriggerID: trigger.ID})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "ya29.SECRET")
	reg, _ = repo.GetTriggerRegistration(ctx, trigger.ID)
	assert.Equal(t, core.TriggerRegistrationError, reg.Status)
	assert.NotContains(t, reg.StatusDetail, "ya29.SECRET")
}

type scrubbingCred struct {
	fakeCred
	secret string
}

func (c scrubbingCred) Scrub(s string) string { return strings.ReplaceAll(s, c.secret, "[redacted]") }

// A transient credential failure (a 503 from the token endpoint) is a poll
// error: recorded, and retried by the schedule.
func TestPollTransientCredentialFailureIsRetried(t *testing.T) {
	repo, trigger, _, _, p, creds := newPollEnvWithCreds(t)
	creds.err = &httpaction.CredentialError{Code: httpaction.CodeUnavailable, Message: "token refresh is temporarily unavailable"}
	_, err := p.Poll(context.Background(), PollInput{TriggerID: trigger.ID})
	require.Error(t, err)
	reg, _ := repo.GetTriggerRegistration(context.Background(), trigger.ID)
	assert.Equal(t, core.TriggerRegistrationError, reg.Status)
}

// A connection the source refuses outright (foreign, deleted, wrong
// integration) is a skip, like the activity's own pre-check.
func TestPollRefusedCredentialIsASkip(t *testing.T) {
	_, trigger, poller, _, p, creds := newPollEnvWithCreds(t)
	creds.err = &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "no such connection"}
	out, err := p.Poll(context.Background(), PollInput{TriggerID: trigger.ID})
	require.NoError(t, err)
	assert.True(t, out.Skipped)
	assert.Zero(t, poller.calls)
}

func TestPollWithoutCredentialsIsSkippedNotUnauthenticated(t *testing.T) {
	repo, trigger, poller, starter, _ := newPollEnv(t)
	p := NewTriggerPoller(repo, fakePollers{"feed": poller}, NewIntake(repo, starter, ""), nil)
	out, err := p.Poll(context.Background(), PollInput{TriggerID: trigger.ID})
	require.NoError(t, err)
	assert.True(t, out.Skipped)
	assert.Zero(t, poller.calls, "a poller never runs without a credential")
}

func TestPollBaselineFiresNothingThenANewItemFiresOnce(t *testing.T) {
	repo, trigger, poller, starter, p := newPollEnv(t)
	ctx := context.Background()
	poller.add(PollItem{ID: "old-1", Type: "item.created"})
	poller.add(PollItem{ID: "old-2", Type: "item.created"})

	out, err := p.Poll(ctx, PollInput{TriggerID: trigger.ID})
	require.NoError(t, err)
	assert.True(t, out.Baseline)
	assert.Empty(t, repo.eventsFor(trigger.ID), "history that existed before the trigger never fires")
	reg, err := repo.GetTriggerRegistration(ctx, trigger.ID)
	require.NoError(t, err)
	assert.Equal(t, "2", reg.Cursor)
	assert.Equal(t, "feed", reg.Provider)

	poller.add(PollItem{ID: "new-1", Type: "item.created", Data: map[string]any{"title": "hello"}})
	out, err = p.Poll(ctx, PollInput{TriggerID: trigger.ID})
	require.NoError(t, err)
	assert.Equal(t, 1, out.Accepted)

	// A re-poll that sees the same item (a cursor that did not advance, a
	// provider that re-lists) is the same event.
	repo.regs[trigger.ID].Cursor = "2"
	_, err = p.Poll(ctx, PollInput{TriggerID: trigger.ID})
	require.NoError(t, err)

	events := repo.eventsFor(trigger.ID)
	require.Len(t, events, 1, "exactly one fire for the one new item")
	assert.Equal(t, trigger.ID+":new-1", events[0].DedupeKey)
	assert.Equal(t, core.TriggerEventKindIntegration, events[0].Kind)
	data, _ := events[0].Payload["data"].(map[string]any)
	assert.Equal(t, "hello", data["title"])
	assert.Len(t, starter.snapshot(), 2, "the redelivery restarts the same still-pending fire, which is harmless")
	for _, s := range starter.snapshot() {
		assert.Equal(t, EventFireWorkflowID(events[0].ID), s.ID)
	}
}

func TestPollAppliesTheTriggersSourceConfig(t *testing.T) {
	repo, trigger, poller, _, p := newPollEnv(t)
	ctx := context.Background()
	_, err := p.Poll(ctx, PollInput{TriggerID: trigger.ID})
	require.NoError(t, err)

	poller.add(PollItem{ID: "x", Type: "item.deleted"})
	_, err = p.Poll(ctx, PollInput{TriggerID: trigger.ID})
	require.NoError(t, err)
	assert.Empty(t, repo.eventsFor(trigger.ID), "an event type the trigger does not listen to writes nothing")
}

func TestPollFailureRecordsAnErrorAndKeepsTheCursor(t *testing.T) {
	repo, trigger, poller, _, p := newPollEnv(t)
	ctx := context.Background()
	_, err := p.Poll(ctx, PollInput{TriggerID: trigger.ID})
	require.NoError(t, err)

	poller.err = errors.New("provider returned 503")
	_, err = p.Poll(ctx, PollInput{TriggerID: trigger.ID})
	require.Error(t, err)
	reg, _ := repo.GetTriggerRegistration(ctx, trigger.ID)
	assert.Equal(t, core.TriggerRegistrationError, reg.Status)
	assert.Contains(t, reg.StatusDetail, "503")
	assert.Equal(t, "0", reg.Cursor, "a failed poll must not move the cursor past items it never delivered")
}

func TestPollSkipsADisabledTriggerOrAnInactiveConnection(t *testing.T) {
	repo, trigger, poller, _, p := newPollEnv(t)
	ctx := context.Background()

	repo.triggers[trigger.ID].Enabled = false
	out, err := p.Poll(ctx, PollInput{TriggerID: trigger.ID})
	require.NoError(t, err)
	assert.True(t, out.Skipped)

	repo.triggers[trigger.ID].Enabled = true
	repo.accounts["conn-1"].Status = core.ConnectionStatusNeedsReauth
	out, err = p.Poll(ctx, PollInput{TriggerID: trigger.ID})
	require.NoError(t, err)
	assert.True(t, out.Skipped)
	assert.Zero(t, poller.calls, "nothing is polled through a connection that cannot authenticate")
}

func TestPollRefusesAnotherUsersConnection(t *testing.T) {
	repo, trigger, poller, _, p := newPollEnv(t)
	repo.accounts["conn-1"].UserID = "someone-else"
	out, err := p.Poll(context.Background(), PollInput{TriggerID: trigger.ID})
	require.NoError(t, err)
	assert.True(t, out.Skipped)
	assert.Zero(t, poller.calls)
}

// A polled integration trigger gets a schedule that runs the poll workflow at
// its interval, paused when the trigger is disabled. A webhook trigger, and
// an integration trigger whose integration is pushed rather than polled, get
// none — and Sync removes one left over from an earlier definition.
func TestSyncConvergesAPollScheduleOnlyForPolledTriggers(t *testing.T) {
	c := temporalForTest(t)
	repo := newFakeRepo()
	s := NewSyncer(c.ScheduleClient(), repo, "triggers-test-queue").WithPolledIntegrations(func(id string) bool { return id == "feed" })
	ctx := context.Background()

	polled := pollTrigger(t)
	raw, err := json.Marshal(core.IntegrationConfig{Integration: "feed", Events: []string{"*"}, PollInterval: "7m"})
	require.NoError(t, err)
	polled.Config = raw
	repo.triggers[polled.ID] = polled
	cleanupSchedule(t, s, polled.ID)

	require.NoError(t, s.Sync(ctx, polled.ID))
	desc, err := s.schedules.GetHandle(ctx, ScheduleID(polled.ID)).Describe(ctx)
	require.NoError(t, err)
	action := desc.Schedule.Action.(*client.ScheduleWorkflowAction)
	assert.Equal(t, PollWorkflowName, action.Workflow)
	assert.Equal(t, PollWorkflowID(polled.ID), action.ID)
	require.Len(t, desc.Schedule.Spec.Intervals, 1)
	assert.Equal(t, 7*time.Minute, desc.Schedule.Spec.Intervals[0].Every)
	assert.Equal(t, enumspb.SCHEDULE_OVERLAP_POLICY_SKIP, desc.Schedule.Policy.Overlap,
		"a poll that overruns its interval is skipped, never stacked")
	assert.False(t, desc.Schedule.State.Paused)

	repo.triggers[polled.ID].Enabled = false
	require.NoError(t, s.Sync(ctx, polled.ID))
	desc, err = s.schedules.GetHandle(ctx, ScheduleID(polled.ID)).Describe(ctx)
	require.NoError(t, err)
	assert.True(t, desc.Schedule.State.Paused)

	pushed := pollTrigger(t)
	raw, err = json.Marshal(core.IntegrationConfig{Integration: "github", Events: []string{"*"}})
	require.NoError(t, err)
	pushed.Config = raw
	repo.triggers[pushed.ID] = pushed
	cleanupSchedule(t, s, pushed.ID)
	require.NoError(t, s.Sync(ctx, pushed.ID))
	_, err = s.schedules.GetHandle(ctx, ScheduleID(pushed.ID)).Describe(ctx)
	assert.True(t, isNotFound(err), "a pushed integration needs no schedule: %v", err)

	// A webhook trigger never has one, and a leftover is removed.
	hook := webhookTrigger(t, "")
	repo.triggers[hook.ID] = hook
	cleanupSchedule(t, s, hook.ID)
	_, err = c.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID: ScheduleID(hook.ID), Spec: client.ScheduleSpec{Intervals: []client.ScheduleIntervalSpec{{Every: time.Hour}}},
		Action: &client.ScheduleWorkflowAction{ID: "x", Workflow: PollWorkflowName, TaskQueue: "triggers-test-queue"},
	})
	require.NoError(t, err)
	require.NoError(t, s.Sync(ctx, hook.ID))
	_, err = s.schedules.GetHandle(ctx, ScheduleID(hook.ID)).Describe(ctx)
	assert.True(t, isNotFound(err), "a stale schedule on a webhook trigger is removed: %v", err)
}
