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
	// TriggerEventKindAgentStartRun is a top-level run started by an agent's
	// start_run tool. Its dedupe key is "<calling chat id>:<tool call id>",
	// and its payload carries parent_chat_id — the lineage the fork-bomb
	// guard walks.
	TriggerEventKindAgentStartRun TriggerEventKind = "agent.start_run"
	// TriggerEventKindBuilderTest is a test run started from the workflow
	// builder's Run tab. It is attended (a human pressed Run), has no stored
	// trigger, and is kept out of the sidebar and the default Runs list. Its
	// dedupe key is the chat id, like chat.start.
	TriggerEventKindBuilderTest TriggerEventKind = "builder.test"
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
	// DaemonID is the daemon every tool call in each launched run executes
	// on. Required; validated at write time (there is no FK because the
	// daemons table is a cache of the control plane's).
	DaemonID string
	// NotifyOnComplete opts the owner in to being told when a run of this
	// trigger completes (unread + OS notification + a Run finished Inbox
	// item). Unattended completions are otherwise silent.
	NotifyOnComplete bool
	// Config is the kind-specific source configuration, e.g. ScheduleConfig
	// for TriggerKindSchedule. Stored as jsonb.
	Config    json.RawMessage
	CreatedAt time.Time
	UpdatedAt time.Time

	// ProjectName and DaemonName are read-only display names joined in by
	// GetTrigger and ListTriggers. Empty when the project or daemon row is
	// gone or has no name; never written.
	ProjectName string
	DaemonName  string
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

// TriggerEventRun is the run a launched firing started, as the firing's
// reader sees it now. DisplayState is derived in SQL by the same table as
// RunListItem.DisplayState.
type TriggerEventRun struct {
	ChatID       string
	Title        string
	DisplayState RunDisplayState
	RootStatus   WorkflowStatus
}

// TriggerEventWithRun is a firing with its run. Run is nil unless the firing
// launched a chat that still exists.
type TriggerEventWithRun struct {
	Event *TriggerEvent
	Run   *TriggerEventRun
}

// TriggerEventCursor is a keyset position in a trigger's firing list, which is
// ordered by (OccurredAt, ID) descending.
type TriggerEventCursor struct {
	OccurredAt time.Time
	ID         string
}

// TriggerEventFilters narrows ListTriggerEvents. UserID and TriggerID are both
// mandatory; UserID is the only scoping and is never taken from a request.
// Empty Outcomes means every outcome.
type TriggerEventFilters struct {
	UserID    string
	TriggerID string
	Outcomes  []TriggerEventOutcome
	After     *TriggerEventCursor
	Limit     int
}

// TriggerFilters narrows ListTriggers. UserID is required: the unscoped
// listing is ListAllTriggers.
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
	// ListTriggers lists one user's triggers; f.UserID must be set.
	ListTriggers(ctx context.Context, f TriggerFilters) ([]*Trigger, error)
	// ListAllTriggers lists every user's triggers, for schedule reconciliation.
	ListAllTriggers(ctx context.Context) ([]*Trigger, error)
	// LockTrigger takes a row lock on the trigger until the surrounding
	// transaction ends, returning ErrTriggerNotFound when it does not exist.
	LockTrigger(ctx context.Context, id string) error
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
	// GetTriggerEventByChatID returns the event that launched the chat, or
	// ErrTriggerEventNotFound for a chat with none (it predates trigger events).
	GetTriggerEventByChatID(ctx context.Context, chatID string) (*TriggerEvent, error)
	// GetTriggerEventByChat returns the event of the given kind that launched
	// chatID, or ErrTriggerEventNotFound when that chat was not launched by
	// that kind of event.
	GetTriggerEventByChat(ctx context.Context, kind TriggerEventKind, chatID string) (*TriggerEvent, error)
	// CountLiveLaunchedRuns counts the user's chats launched by an event of
	// the given kind whose root run is still live (pending, active or
	// paused). A chat deleted since its launch is not counted.
	CountLiveLaunchedRuns(ctx context.Context, userID string, kind TriggerEventKind) (int, error)
	// UpdateTriggerEventOutcome returns ErrTriggerEventNotFound when the id
	// does not resolve.
	UpdateTriggerEventOutcome(ctx context.Context, id string, outcome TriggerEventOutcome, detail string, chatID *string) error
	// UpdateTriggerEventPayload replaces the event's payload. It returns
	// ErrTriggerEventNotFound when the id does not resolve.
	UpdateTriggerEventPayload(ctx context.Context, id string, payload map[string]any) error
	// ListTriggerEvents returns up to f.Limit of the trigger's firings newest
	// first, each with the run it launched, and whether more follow.
	ListTriggerEvents(ctx context.Context, f TriggerEventFilters) ([]*TriggerEventWithRun, bool, error)
	// RecentTriggerFirings returns each named trigger's newest perTrigger
	// firings (newest first, with their runs) in one query. A trigger with no
	// firings has no map entry. Triggers belonging to other users never appear.
	RecentTriggerFirings(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*TriggerEventWithRun, error)
	// FiringsSinceLastSuccess returns each trigger's firings newer than its
	// newest success (a launched firing whose run completed), newest first,
	// capped at perTrigger. It is the failure episode, not a fixed window.
	FiringsSinceLastSuccess(ctx context.Context, userID string, triggerIDs []string, perTrigger int) (map[string][]*TriggerEventWithRun, error)
	// GetLatestTriggerEvent returns the trigger's latest event, optionally
	// filtered by outcome, and (nil, nil) when it has none.
	GetLatestTriggerEvent(ctx context.Context, triggerID string, outcome *TriggerEventOutcome) (*TriggerEvent, error)
}
