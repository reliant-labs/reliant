// Copyright (c) 2025 Reliant Labs
package workflowcheck

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// fakeStore is Store over a map, with the firing and run each check observes
// set by the test.
type fakeStore struct {
	rows map[string]*core.WorkflowCheckRun
	obs  map[string]*core.WorkflowCheckObservation // by trigger event id; absent: firing gone
	// chats maps a chat to its launch firing, for Started/Ended.
	chats map[string]string
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[string]*core.WorkflowCheckRun{}, obs: map[string]*core.WorkflowCheckObservation{}, chats: map[string]string{}}
}

func (s *fakeStore) CreateWorkflowCheckRun(_ context.Context, row *core.WorkflowCheckRun) (bool, error) {
	for _, r := range s.rows {
		if r.TriggerEventID == row.TriggerEventID && r.Name == row.Name {
			return false, nil
		}
	}
	c := *row
	s.rows[row.ID] = &c
	return true, nil
}

func (s *fakeStore) forChat(chatID string, f func(*core.WorkflowCheckRun)) {
	for _, r := range s.rows {
		if r.TriggerEventID == s.chats[chatID] && r.FinishedAt == nil {
			f(r)
		}
	}
}

func (s *fakeStore) MarkWorkflowCheckRunsStarted(_ context.Context, chatID string, at time.Time) error {
	s.forChat(chatID, func(r *core.WorkflowCheckRun) {
		if r.StartedAt == nil {
			r.StartedAt = &at
		}
		r.NextSyncAt = at
	})
	return nil
}

func (s *fakeStore) RecordWorkflowCheckRunsEnded(_ context.Context, chatID string, e core.WorkflowCheckEnding, at time.Time) error {
	s.forChat(chatID, func(r *core.WorkflowCheckRun) {
		if r.EndedStatus == "" {
			r.EndedStatus, r.Verdict, r.Output, r.OutputError = e.Status, e.Verdict, e.Output, e.OutputError
		}
		r.NextSyncAt = at
	})
	return nil
}

func (s *fakeStore) ClaimWorkflowCheckRuns(_ context.Context, now, until time.Time, max int) ([]*core.WorkflowCheckRun, error) {
	var out []*core.WorkflowCheckRun
	for _, r := range s.rows {
		if r.FinishedAt == nil && !r.NextSyncAt.After(now) && (r.ClaimedUntil == nil || r.ClaimedUntil.Before(now)) && len(out) < max {
			r.ClaimedUntil = &until
			c := *r
			out = append(out, &c)
		}
	}
	return out, nil
}

func (s *fakeStore) ObserveWorkflowCheckRun(_ context.Context, id string) (*core.WorkflowCheckObservation, error) {
	o, ok := s.obs[id]
	if !ok {
		return nil, core.ErrWorkflowCheckRunNotFound
	}
	c := *o
	return &c, nil
}

func (s *fakeStore) SaveWorkflowCheckRunSync(_ context.Context, row *core.WorkflowCheckRun) error {
	r := s.rows[row.ID]
	r.GitHubCheckRunID, r.GitHubStatus, r.GitHubConclusion, r.GitHubDigest = row.GitHubCheckRunID, row.GitHubStatus, row.GitHubConclusion, row.GitHubDigest
	r.AnnotationsSent, r.PausedSince, r.NextSyncAt, r.ClaimedUntil = row.AnnotationsSent, row.PausedSince, row.NextSyncAt, nil
	r.Attempts, r.LastError, r.FinishedAt = row.Attempts, row.LastError, row.FinishedAt
	return nil
}

// fakeGitHub records every write; failNext makes the next call fail.
type fakeGitHub struct {
	runs     map[int64]*Write // id -> the check run as GitHub holds it
	external map[string]int64
	writes   []string
	failNext error
	next     int64
	identity Identity
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{runs: map[int64]*Write{}, external: map[string]int64{}, next: 100}
}

func (g *fakeGitHub) CheckRuns(_ context.Context, id Identity, owner, repo string) (CheckRuns, error) {
	g.identity = id
	if err := g.failNext; err != nil {
		g.failNext = nil
		return nil, err
	}
	return g, nil
}

func (g *fakeGitHub) Find(_ context.Context, _, _, externalID string) (int64, error) {
	g.writes = append(g.writes, "find")
	return g.external[externalID], nil
}

func (g *fakeGitHub) Create(_ context.Context, w Write) (int64, error) {
	g.next++
	c := w
	g.runs[g.next] = &c
	g.external[w.ExternalID] = g.next
	g.writes = append(g.writes, "create:"+w.Status+"/"+w.Conclusion)
	return g.next, nil
}

func (g *fakeGitHub) Update(_ context.Context, id int64, w Write) error {
	run := g.runs[id]
	if run == nil {
		return fmt.Errorf("no check run %d", id)
	}
	if w.Status != "" {
		if run.Status == core.CheckStatusCompleted {
			return fmt.Errorf("check run %d is already completed", id)
		}
		run.Status, run.Conclusion = w.Status, w.Conclusion
	}
	if w.Output != nil {
		ann := len(w.Output.Annotations)
		if ann > AnnotationBatch {
			return fmt.Errorf("%d annotations in one request", ann)
		}
		g.writes = append(g.writes, fmt.Sprintf("update:%s/%s+%d", w.Status, w.Conclusion, ann))
		run.Output = w.Output
		return nil
	}
	g.writes = append(g.writes, "update:"+w.Status+"/"+w.Conclusion)
	return nil
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) advance(d time.Duration) { c.now = c.now.Add(d) }

type harness struct {
	store *fakeStore
	gh    *fakeGitHub
	svc   *service
	clock *clock
}

func newHarness() *harness {
	h := &harness{store: newFakeStore(), gh: newFakeGitHub(), clock: &clock{now: t0}}
	h.svc = newService(Deps{Store: h.store, GitHub: h.gh, RunURL: HostedRunURL})
	h.svc.now = h.clock.Now
	return h
}

// open records a check for event "e1", launching chat-1.
func (h *harness) open(t *testing.T, check *reliantv1.WorkflowCheck) *core.WorkflowCheckRun {
	t.Helper()
	p := payload(map[string]any{"check_suite": map[string]any{"head_sha": headSHA}})
	err := h.svc.Open(context.Background(), Firing{
		Workflow: &reliantv1.Workflow{Check: check},
		Trigger:  &core.Trigger{ID: "t1", UserID: "u1", OrgID: ptr.Of("org-1"), ActingIdentity: core.ActingIdentityApp, Name: "reliant-ci"},
		Event:    &core.TriggerEvent{ID: "e1", Payload: p},
		Scope:    scopeOf(p),
	})
	require.NoError(t, err)
	h.store.chats["chat-1"] = "e1"
	for _, r := range h.store.rows {
		return r
	}
	return nil
}

func (h *harness) sync(t *testing.T) {
	t.Helper()
	_, err := h.svc.SyncOnce(context.Background())
	require.NoError(t, err)
}

func (h *harness) observe(o *core.WorkflowCheckObservation) { h.store.obs["e1"] = o }

func TestOpenRecordsTheFiringsCheck(t *testing.T) {
	h := newHarness()
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci", Timeout: "2h"})
	require.NotNil(t, row)
	require.Equal(t, "reliant/ci", row.Name)
	require.Equal(t, "reliant-labs", row.Owner)
	require.Equal(t, "reliant", row.Repo)
	require.Equal(t, headSHA, row.HeadSHA)
	require.Equal(t, 2*time.Hour, row.Timeout)
	require.Equal(t, "app", row.ActingIdentity)
	require.Equal(t, "u1", row.UserID)
	require.Equal(t, t0, row.NextSyncAt, "due at once: queued shows before the run starts")

	// A retried or redriven fire finds the row it wrote.
	h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	require.Len(t, h.store.rows, 1)
}

func TestOpenWithoutACheckOrATarget(t *testing.T) {
	h := newHarness()
	h.open(t, nil)
	require.Empty(t, h.store.rows, "a workflow without check: opens nothing")

	err := h.svc.Open(context.Background(), Firing{
		Workflow: &reliantv1.Workflow{Check: &reliantv1.WorkflowCheck{Name: "ci"}},
		Trigger:  &core.Trigger{ID: "t1", UserID: "u1"},
		Event:    &core.TriggerEvent{ID: "e2", Payload: map[string]any{"scheduled_for": "now"}},
	})
	require.NoError(t, err, "an event with no commit is not the fire's failure")
	require.Empty(t, h.store.rows)
}

// TestLifecycleQueuedRunningConcluded walks one check through its whole life
// as the syncer sees it, and pins that nothing is written twice.
func TestLifecycleQueuedRunningConcluded(t *testing.T) {
	h := newHarness()
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})

	// Fired, waiting for capacity: created queued.
	h.observe(&core.WorkflowCheckObservation{EventOutcome: core.TriggerEventPending})
	h.sync(t)
	require.Equal(t, []string{"find", "create:queued/"}, h.gh.writes)
	require.Equal(t, core.CheckStatusQueued, row.GitHubStatus)
	require.NotNil(t, row.GitHubCheckRunID)
	created := h.gh.runs[*row.GitHubCheckRunID]
	require.Equal(t, "reliant/ci", created.Name)
	require.Equal(t, headSHA, created.HeadSHA)
	require.Equal(t, row.ID, created.ExternalID)
	require.Equal(t, Identity{UserID: "u1", OrgID: "org-1", ActingIdentity: "app", Automation: "reliant-ci"}, h.gh.identity)

	// Nothing changed: no write.
	h.clock.advance(pollLive)
	h.sync(t)
	require.Len(t, h.gh.writes, 2)

	// Launched, machine waking: still queued, saying so once.
	h.observe(launched(status(core.Active())))
	h.clock.advance(pollLive)
	h.sync(t)
	require.Equal(t, "update:queued/+0", h.gh.writes[2])
	h.clock.advance(pollLive)
	h.sync(t)
	require.Len(t, h.gh.writes, 3)

	// The first node starts: in_progress at once (the report makes it due).
	require.NoError(t, h.svc.Started(context.Background(), "chat-1"))
	h.sync(t)
	require.Equal(t, "update:in_progress/+0", h.gh.writes[3])
	require.Equal(t, core.CheckStatusInProgress, row.GitHubStatus)

	// The run ends with its own output and 120 annotations.
	ann := make([]any, 120)
	for i := range ann {
		ann[i] = map[string]any{"path": "a.go", "start_line": i + 1, "message": "m"}
	}
	require.NoError(t, h.svc.Ended(context.Background(), "chat-1", core.WorkflowCheckEnding{
		Status: core.TriggerRunCompleted, Verdict: "failure",
		Output: map[string]any{"title": "120 findings", "summary": "lint failed", "annotations": ann},
	}))
	h.sync(t)
	require.Equal(t, []string{"update:completed/failure+0", "update:/+50", "update:/+50", "update:/+20"}, h.gh.writes[4:])
	require.Equal(t, core.CheckStatusCompleted, row.GitHubStatus)
	require.Equal(t, ConclusionFailure, row.GitHubConclusion)
	require.Equal(t, 120, row.AnnotationsSent)
	require.NotNil(t, row.FinishedAt, "concluded and every annotation sent: nothing left")
	require.Equal(t, "120 findings", h.gh.runs[*row.GitHubCheckRunID].Output.Title)

	// A finished check is never claimed again.
	h.clock.advance(time.Hour)
	h.sync(t)
	require.Len(t, h.gh.writes, 8)
}

func TestASkippedFiringIsCreatedConcluded(t *testing.T) {
	h := newHarness()
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	h.observe(&core.WorkflowCheckObservation{EventOutcome: core.TriggerEventSkipped, EventDetail: "superseded by a newer firing"})
	h.sync(t)
	require.Equal(t, []string{"find", "create:completed/cancelled"}, h.gh.writes)
	run := h.gh.runs[*row.GitHubCheckRunID]
	require.NotNil(t, run.CompletedAt)
	require.Contains(t, run.Output.Title, "superseded")
	require.NotNil(t, row.FinishedAt)
}

func TestALostCreateIsFoundNotDuplicated(t *testing.T) {
	h := newHarness()
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	// GitHub created the run, but the answer never reached us.
	h.gh.runs[7] = &Write{Status: core.CheckStatusQueued}
	h.gh.external[row.ID] = 7
	h.observe(&core.WorkflowCheckObservation{EventOutcome: core.TriggerEventPending})
	h.sync(t)
	require.Equal(t, []string{"find", "update:queued/+0"}, h.gh.writes)
	require.Equal(t, int64(7), *row.GitHubCheckRunID)
}

func TestAFailedWriteBacksOffAndRetries(t *testing.T) {
	h := newHarness()
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	h.observe(&core.WorkflowCheckObservation{EventOutcome: core.TriggerEventPending})
	h.gh.failNext = errors.New("GitHub answered 502")

	_, err := h.svc.SyncOnce(context.Background())
	require.ErrorContains(t, err, "502")
	require.Equal(t, 1, row.Attempts)
	require.Contains(t, row.LastError, "502")
	require.Nil(t, row.FinishedAt)
	require.True(t, row.NextSyncAt.After(t0), "not due again at once")
	require.Nil(t, row.ClaimedUntil, "the lease is released")

	// Not due yet: nothing happens.
	h.sync(t)
	require.Empty(t, h.gh.writes)

	h.clock.advance(time.Minute)
	h.sync(t)
	require.Equal(t, []string{"find", "create:queued/"}, h.gh.writes)
	require.Zero(t, row.Attempts)
	require.Empty(t, row.LastError)
}

func TestAWriteThatNeverLandsIsGivenUpLate(t *testing.T) {
	h := newHarness()
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci", Timeout: "1h"})
	h.observe(&core.WorkflowCheckObservation{EventOutcome: core.TriggerEventPending})

	h.clock.advance(time.Hour + giveUpAfter - time.Minute)
	h.gh.failNext = errors.New("the App is not installed")
	_, _ = h.svc.SyncOnce(context.Background())
	require.Nil(t, row.FinishedAt, "retried until well past the timeout")

	h.clock.advance(time.Hour)
	h.gh.failNext = errors.New("the App is not installed")
	_, _ = h.svc.SyncOnce(context.Background())
	require.NotNil(t, row.FinishedAt, "given up an hour past the timeout")
}

func TestATimedOutCheckConcludes(t *testing.T) {
	h := newHarness()
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci", Timeout: "1h"})
	h.observe(&core.WorkflowCheckObservation{EventOutcome: core.TriggerEventPending})
	h.sync(t)
	h.clock.advance(time.Hour)
	h.sync(t)
	require.Equal(t, ConclusionTimedOut, row.GitHubConclusion)
	require.NotNil(t, row.FinishedAt)
}

func TestTheCheckNeverMovesBackToQueued(t *testing.T) {
	h := newHarness()
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	require.NoError(t, h.svc.Started(context.Background(), "chat-1"))
	h.observe(launched(status(core.Active())))
	h.sync(t)
	require.Equal(t, core.CheckStatusInProgress, row.GitHubStatus)

	// A resumed run's workflow row reads PENDING again for a moment.
	h.observe(launched(status(core.Pending())))
	h.clock.advance(pollLive)
	h.sync(t)
	require.Equal(t, core.CheckStatusInProgress, row.GitHubStatus)
	require.NotContains(t, h.gh.writes, "update:queued/+0")
}

func TestEndedReportsOnceAndRefusesNonsense(t *testing.T) {
	h := newHarness()
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	require.Error(t, h.svc.Ended(context.Background(), "chat-1", core.WorkflowCheckEnding{Status: "paused"}))
	require.NoError(t, h.svc.Ended(context.Background(), "chat-1", core.WorkflowCheckEnding{Status: core.TriggerRunFailed}))
	require.NoError(t, h.svc.Ended(context.Background(), "chat-1", core.WorkflowCheckEnding{Status: core.TriggerRunCompleted}))
	require.Equal(t, core.TriggerRunFailed, row.EndedStatus, "a retried report changes nothing")
	require.NoError(t, h.svc.Started(context.Background(), ""), "a run with no chat reports nothing")
}

func TestSyncOnceNeedsAGitHubWriter(t *testing.T) {
	svc := newService(Deps{Store: newFakeStore()})
	_, err := svc.SyncOnce(context.Background())
	require.Error(t, err)
}
