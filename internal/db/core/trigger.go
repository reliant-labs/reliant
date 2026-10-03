// Copyright (c) 2025 Reliant Labs
package core

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// A Trigger is a standing instruction to start a run without a human typing:
// "every weekday at 9am, run workflow W in project P with prompt M". A
// TriggerEvent is one firing of a trigger — or of an ad hoc source such as an
// interactive chat start, which has no stored Trigger behind it.
//
// See research/TRIGGERS.md for the model.

// TriggerKind is the source a stored trigger listens to.
type TriggerKind string

const (
	// TriggerKindSchedule fires on a cron / interval schedule (Temporal Schedule).
	TriggerKindSchedule TriggerKind = "schedule"
)

// TriggerEventKind is the kind of a single firing. It is wider than
// TriggerKind: ad hoc sources (an interactive chat start) produce events
// without a stored trigger.
type TriggerEventKind string

const (
	// TriggerEventKindChatStart is the first send of a chat. Its dedupe key is
	// the chat id, which is what makes "a chat starts exactly once" a database
	// guarantee rather than a client convention.
	TriggerEventKindChatStart TriggerEventKind = "chat.start"
	// TriggerEventKindSchedule is a scheduled fire. Its dedupe key is the
	// Temporal fire-workflow id, unique per scheduled time.
	TriggerEventKindSchedule TriggerEventKind = "schedule"
)

// TriggerEventOutcome records what a firing did.
type TriggerEventOutcome string

const (
	TriggerEventLaunched TriggerEventOutcome = "launched"
	TriggerEventSkipped  TriggerEventOutcome = "skipped"
	TriggerEventFailed   TriggerEventOutcome = "failed"
)

// ErrTriggerNotFound is returned when a trigger id does not resolve.
var ErrTriggerNotFound = errors.New("trigger not found")

// ErrTriggerEventNotFound is returned when a trigger event lookup misses.
var ErrTriggerEventNotFound = errors.New("trigger event not found")

// Trigger is a stored trigger definition. UserID is the owner, and the
// identity every run it launches executes as.
type Trigger struct {
	ID         string
	UserID     string
	ProjectID  string
	WorktreeID *string // nil → the project's main worktree
	Name       string
	Kind       TriggerKind
	Enabled    bool
	Workflow   string
	Presets    map[string]string
	Params     map[string]any
	Message    string // seed prompt for each launched run
	// Config is the kind-specific source configuration, e.g. ScheduleConfig
	// for TriggerKindSchedule. Stored as jsonb.
	Config    json.RawMessage
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ScheduleConfig is Trigger.Config for TriggerKindSchedule.
type ScheduleConfig struct {
	// Cron is a union of 5-field cron expressions.
	Cron []string `json:"cron,omitempty"`
	// Interval is a Go duration ("15m", "1h"); combined with Cron as a union.
	Interval string `json:"interval,omitempty"`
	// Timezone is an IANA zone name; empty means UTC.
	Timezone string `json:"timezone,omitempty"`
	// Overlap is "skip" (default: do not fire while this trigger's previous
	// run is still active or paused) or "allow".
	Overlap string `json:"overlap,omitempty"`
	// CatchupWindow is a Go duration bounding how late a missed fire may still
	// run after an outage; empty means 10m.
	CatchupWindow string `json:"catchup_window,omitempty"`
}

// Schedule overlap policies.
const (
	ScheduleOverlapSkip  = "skip"
	ScheduleOverlapAllow = "allow"
)

// TriggerEvent is one firing.
type TriggerEvent struct {
	ID            string
	TriggerID     *string // nil for ad hoc kinds (chat.start)
	UserID        string
	Kind          TriggerEventKind
	DedupeKey     string // unique within Kind
	OccurredAt    time.Time
	Payload       map[string]any
	Outcome       TriggerEventOutcome
	OutcomeDetail string
	ChatID        *string // the chat this firing launched, when it launched one
	CreatedAt     time.Time
}

// TriggerFilters narrows ListTriggers. An empty UserID lists every user's
// triggers, which only the schedule syncer's startup reconciliation does.
type TriggerFilters struct {
	UserID    string
	ProjectID *string
}

// TriggerStore is the shared contract for trigger and trigger-event
// persistence.
type TriggerStore interface {
	CreateTrigger(ctx context.Context, t *Trigger) error
	// GetTrigger returns ErrTriggerNotFound when the id does not resolve.
	GetTrigger(ctx context.Context, id string) (*Trigger, error)
	ListTriggers(ctx context.Context, f TriggerFilters) ([]*Trigger, error)
	// UpdateTrigger returns ErrTriggerNotFound when the id does not resolve.
	UpdateTrigger(ctx context.Context, t *Trigger) error
	DeleteTrigger(ctx context.Context, id string) error
	// SetTriggerEnabled returns ErrTriggerNotFound when the id does not resolve.
	SetTriggerEnabled(ctx context.Context, id string, enabled bool) error

	// CreateTriggerEvent inserts the event, doing nothing on a (Kind,
	// DedupeKey) that already exists. created=false means a row for that key
	// was already there and nothing was written — the caller lost the race,
	// or is a retry of its own earlier attempt.
	CreateTriggerEvent(ctx context.Context, ev *TriggerEvent) (created bool, err error)
	// GetTriggerEventByDedupe returns ErrTriggerEventNotFound on a miss.
	GetTriggerEventByDedupe(ctx context.Context, kind TriggerEventKind, dedupeKey string) (*TriggerEvent, error)
	// UpdateTriggerEventOutcome returns ErrTriggerEventNotFound when the id
	// does not resolve.
	UpdateTriggerEventOutcome(ctx context.Context, id string, outcome TriggerEventOutcome, detail string, chatID *string) error
	// ListTriggerEvents returns the trigger's events newest first.
	ListTriggerEvents(ctx context.Context, triggerID string, limit int) ([]*TriggerEvent, error)
	// GetLatestTriggerEvent returns the trigger's latest event, optionally
	// filtered by outcome, and (nil, nil) when it has none.
	GetLatestTriggerEvent(ctx context.Context, triggerID string, outcome *TriggerEventOutcome) (*TriggerEvent, error)
}
