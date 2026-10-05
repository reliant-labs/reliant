// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
)

// fakeRepo is enough of Repo to drive the fire path without a database.
type fakeRepo struct {
	mu sync.Mutex

	triggers map[string]*core.Trigger
	events   []*core.TriggerEvent
	statuses map[string]core.WorkflowStatus

	getTriggerErr error
	// daemons are the daemon ids that exist; GetDaemon misses on the rest.
	daemons map[string]bool
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		triggers: map[string]*core.Trigger{},
		statuses: map[string]core.WorkflowStatus{},
		daemons:  map[string]bool{testDaemonID: true},
	}
}

const testDaemonID = "daemon-1"

func (r *fakeRepo) GetDaemon(_ context.Context, id string) (*db.Daemon, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.daemons[id] {
		return nil, sql.ErrNoRows
	}
	return &db.Daemon{ID: id, UserID: "user-1"}, nil
}

func (r *fakeRepo) GetTrigger(_ context.Context, id string) (*core.Trigger, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getTriggerErr != nil {
		return nil, r.getTriggerErr
	}
	t, ok := r.triggers[id]
	if !ok {
		return nil, core.ErrTriggerNotFound
	}
	return t, nil
}

func (r *fakeRepo) ListAllTriggers(_ context.Context) ([]*core.Trigger, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*core.Trigger
	for _, t := range r.triggers {
		out = append(out, t)
	}
	return out, nil
}

func (r *fakeRepo) LockTrigger(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.triggers[id]; !ok {
		return core.ErrTriggerNotFound
	}
	return nil
}

func (r *fakeRepo) GetTriggerEventByDedupe(_ context.Context, kind core.TriggerEventKind, dedupeKey string) (*core.TriggerEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range r.events {
		if ev.Kind == kind && ev.DedupeKey == dedupeKey {
			copied := *ev
			return &copied, nil
		}
	}
	return nil, core.ErrTriggerEventNotFound
}

func (r *fakeRepo) CreateTriggerEvent(_ context.Context, ev *core.TriggerEvent) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.events {
		if existing.Kind == ev.Kind && existing.DedupeKey == ev.DedupeKey {
			return false, nil
		}
	}
	copied := *ev
	r.events = append(r.events, &copied)
	return true, nil
}

func (r *fakeRepo) GetLatestTriggerEvent(_ context.Context, triggerID string, outcome *core.TriggerEventOutcome) (*core.TriggerEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var latest *core.TriggerEvent
	for _, ev := range r.events {
		if ev.TriggerID == nil || *ev.TriggerID != triggerID {
			continue
		}
		if outcome != nil && ev.Outcome != *outcome {
			continue
		}
		if latest == nil || ev.OccurredAt.After(latest.OccurredAt) {
			latest = ev
		}
	}
	return latest, nil
}

func (r *fakeRepo) GetRootWorkflowStatusForChats(_ context.Context, chatIDs []string) (map[string]core.WorkflowStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]core.WorkflowStatus, len(chatIDs))
	for _, id := range chatIDs {
		if status, ok := r.statuses[id]; ok {
			out[id] = status
		}
	}
	return out, nil
}

func (r *fakeRepo) SettlePendingTriggerEvent(_ context.Context, id string, outcome core.TriggerEventOutcome, detail string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range r.events {
		if ev.ID == id {
			if ev.Outcome != core.TriggerEventPending {
				return false, nil
			}
			ev.Outcome, ev.OutcomeDetail = outcome, detail
			return true, nil
		}
	}
	return false, nil
}

func (r *fakeRepo) ListStalePendingTriggerEvents(_ context.Context, olderThan time.Time, limit int) ([]*core.TriggerEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*core.TriggerEvent
	for _, ev := range r.events {
		if ev.Outcome == core.TriggerEventPending && ev.CreatedAt.Before(olderThan) {
			copied := *ev
			out = append(out, &copied)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *fakeRepo) eventsFor(triggerID string) []*core.TriggerEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*core.TriggerEvent
	for _, ev := range r.events {
		if ev.TriggerID != nil && *ev.TriggerID == triggerID {
			out = append(out, ev)
		}
	}
	return out
}

// fakeLauncher records what it was asked to launch and returns a scripted
// result or error.
type fakeLauncher struct {
	mu    sync.Mutex
	calls []launchCall

	err    error
	result *launch.Result
}

type launchCall struct {
	Event launch.Event
	Spec  launch.Spec
}

func (l *fakeLauncher) Launch(ctx context.Context, ev launch.Event, spec launch.Spec) (*launch.Result, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, launchCall{Event: ev, Spec: spec})
	if spec.Guard != nil {
		reason, err := spec.Guard(ctx)
		if err != nil {
			return nil, err
		}
		if reason != "" {
			return nil, &launch.DeclinedError{Reason: reason}
		}
	}
	if l.err != nil {
		return nil, l.err
	}
	if l.result != nil {
		return l.result, nil
	}
	chatID := spec.NewChatID
	if chatID == "" {
		chatID = uuid.NewString()
	}
	return &launch.Result{
		Chat:       &db.Chat{ID: chatID},
		WorkflowID: chatID,
		EventID:    uuid.NewString(),
	}, nil
}

func (l *fakeLauncher) snapshot() []launchCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]launchCall(nil), l.calls...)
}

// testTrigger builds a schedule trigger with the given config overrides
// applied to a working default.
func testTrigger(t *testing.T, mutate func(*core.Trigger, *core.ScheduleConfig)) *core.Trigger {
	t.Helper()
	cfg := core.ScheduleConfig{Cron: []string{"0 9 * * *"}, Timezone: "UTC"}
	trigger := &core.Trigger{
		ID:        uuid.NewString(),
		UserID:    "user-1",
		ProjectID: "project-1",
		Name:      "nightly audit",
		Kind:      core.TriggerKindSchedule,
		Enabled:   true,
		Workflow:  "builtin://agent",
		Presets:   map[string]string{"model": "fast"},
		Params:    map[string]any{"depth": float64(2)},
		Message:   "Audit the dependency tree.",
		DaemonID:  testDaemonID,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if mutate != nil {
		mutate(trigger, &cfg)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	trigger.Config = raw
	return trigger
}
