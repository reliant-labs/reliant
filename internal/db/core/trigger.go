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
	// TriggerKindWebhook fires on a POST to the trigger's own URL
	// (/hooks/{id}/{token}). Config is WebhookConfig.
	TriggerKindWebhook TriggerKind = "webhook"
	// TriggerKindIntegration fires on a provider event received through the
	// owner's connection: an app-level webhook or a poll. Config is
	// IntegrationConfig; ConnectionID is set.
	TriggerKindIntegration TriggerKind = "integration"
	// TriggerKindWorkflowEvent fires when a run of one of the owner's
	// workflows finishes, fails or blocks. Config is WorkflowEventConfig.
	TriggerKindWorkflowEvent TriggerKind = "workflow_event"
)

// IsEventDriven reports whether a kind fires on an arriving event (and so is
// recorded as a pending event before its launch) rather than on a schedule.
func (k TriggerKind) IsEventDriven() bool {
	return k == TriggerKindWebhook || k == TriggerKindIntegration || k == TriggerKindWorkflowEvent
}

// EventKind is the trigger_events kind a firing of this trigger kind records.
// The kinds share their spelling, so this is a cast with a name.
func (k TriggerKind) EventKind() TriggerEventKind { return TriggerEventKind(k) }

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
	// TriggerEventKindWebhook is a delivery to a webhook trigger. Its dedupe
	// key is "<trigger id>:<sender idempotency key>", or a body hash within a
	// one-minute bucket when the sender gives none.
	TriggerEventKindWebhook TriggerEventKind = "webhook"
	// TriggerEventKindIntegration is a provider event. Its dedupe key is
	// "<trigger id>:<provider delivery id>": one delivery fans out to every
	// matching trigger, exactly once each.
	TriggerEventKindIntegration TriggerEventKind = "integration"
	// TriggerEventKindWorkflowEvent is another run reaching an outcome.
	TriggerEventKindWorkflowEvent TriggerEventKind = "workflow_event"
)

// IsInbound reports whether events of this kind are recorded by a receiver as
// pending and launched later by the fire workflow.
func (k TriggerEventKind) IsInbound() bool {
	return k == TriggerEventKindWebhook || k == TriggerEventKindIntegration || k == TriggerEventKindWorkflowEvent
}

// TriggerEventOutcome records what a firing did.
type TriggerEventOutcome string

const (
	TriggerEventLaunched TriggerEventOutcome = "launched"
	TriggerEventSkipped  TriggerEventOutcome = "skipped"
	TriggerEventFailed   TriggerEventOutcome = "failed"
	// TriggerEventPending is an inbound event that was recorded and passed
	// its filter but has not been launched yet. The fire activity settles it.
	TriggerEventPending TriggerEventOutcome = "pending"
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
	Config json.RawMessage
	// Filter is a CEL bool over the `trigger` root that an arriving event
	// must satisfy. Empty matches everything. Event-driven kinds only.
	Filter string
	// ConnectionID is the connection an integration trigger listens
	// through; nil for other kinds, or when the connection was deleted.
	ConnectionID *string
	// WorkflowTrigger names the declared trigger (WorkflowTrigger.name in
	// Workflow's `triggers:`) this row activates; nil for an ad hoc trigger
	// whose source is written inline. For an activation, Kind, Config and
	// Filter are a projection of the declaration, kept for routing and the
	// schedule syncer; the declaration is re-read and authoritative at fire
	// time.
	WorkflowTrigger *string
	CreatedAt       time.Time
	UpdatedAt       time.Time

	// ProjectName and DaemonName are read-only display names joined in by
	// GetTrigger and ListTriggers. Empty when the project or daemon row is
	// gone or has no name; never written.
	ProjectName string
	DaemonName  string
}

// WebhookConfig is Trigger.Config for TriggerKindWebhook. The token hash and
// the HMAC secret are NOT here: config is rendered back to clients, and those
// live in their own columns (see TriggerWebhookCredentials).
type WebhookConfig struct {
	// HMAC, when set, accepts deliveries signed with the trigger's secret.
	HMAC *WebhookHMACConfig `json:"hmac,omitempty"`
}

// WebhookHMACConfig describes how a sender signs a body.
type WebhookHMACConfig struct {
	Header    string `json:"header,omitempty"`    // empty: X-Signature-256
	Algorithm string `json:"algorithm,omitempty"` // sha256 (default) | sha1 | sha512
	Prefix    string `json:"prefix,omitempty"`    // e.g. "sha256="
	Encoding  string `json:"encoding,omitempty"`  // hex (default) | base64
}

// IntegrationConfig is Trigger.Config for TriggerKindIntegration.
type IntegrationConfig struct {
	// Integration is the provider id ("github"); routing indexes it.
	Integration string `json:"integration"`
	// Events are the event types to fire on; a trailing ".*" matches every
	// action of a type.
	Events []string `json:"events"`
	// Match are attributes the event must carry with exactly these values.
	Match map[string]string `json:"match,omitempty"`
	// PollInterval is a Go duration for polled integrations; empty means the
	// integration's default.
	PollInterval string `json:"poll_interval,omitempty"`
}

// WorkflowEventConfig is Trigger.Config for TriggerKindWorkflowEvent.
type WorkflowEventConfig struct {
	// Workflows are the source workflow refs; empty matches any.
	Workflows []string `json:"workflows,omitempty"`
	// Outcomes are finished | failed | blocked; empty matches all three.
	Outcomes []string `json:"outcomes,omitempty"`
}

// Workflow-event outcomes.
const (
	WorkflowEventFinished = "finished"
	WorkflowEventFailed   = "failed"
	WorkflowEventBlocked  = "blocked"
)

// TriggerWebhookCredentials are a webhook trigger's secrets, read only by the
// receiver. TokenHash is SHA-256 of the token; SecretSealed is the vault-
// sealed HMAC secret (nil when HMAC is not configured).
type TriggerWebhookCredentials struct {
	TokenHash    []byte
	SecretSealed []byte
}

// IntegrationTriggerRoute is an enabled integration trigger together with its
// connection's routing identity, as the app-level router reads it.
type IntegrationTriggerRoute struct {
	Trigger *Trigger
	// ConnectionAccount is the connection's external account id (the
	// installation, team or account the provider routes by). Empty when the
	// connection has not recorded one.
	ConnectionAccount string
	// ConnectionStatus is the connection's status: only an active one may
	// deliver.
	ConnectionStatus string
}

// TriggerRegistration is a trigger's state at its provider: the poll cursor,
// or the id of a per-trigger webhook registration.
type TriggerRegistration struct {
	TriggerID      string
	Provider       string
	RegistrationID string
	Cursor         string
	LastPolledAt   *time.Time
	Status         string // active | error
	StatusDetail   string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Trigger registration statuses.
const (
	TriggerRegistrationActive = "active"
	TriggerRegistrationError  = "error"
	// TriggerRegistrationNeedsReauth: the trigger's connection can no longer
	// authenticate (its refresh grant was refused, or it was marked for
	// reconnect). Polls do nothing until the owner reconnects it, then resume
	// on their own.
	TriggerRegistrationNeedsReauth = "needs_reauth"
)

// ErrTriggerRegistrationNotFound is returned when a trigger has no
// registration yet — for a poll, that is "no baseline taken".
var ErrTriggerRegistrationNotFound = errors.New("trigger registration not found")

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

// TriggerProjection is what an activation caches of its declaration, for
// routing and the schedule syncer.
type TriggerProjection struct {
	Config json.RawMessage
	Filter string
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
	// SetTriggerProjection refreshes an activation's projection of its
	// declaration. It returns ErrTriggerNotFound when the id does not
	// resolve to an activation.
	SetTriggerProjection(ctx context.Context, id string, p TriggerProjection) error
	// ListWorkflowTriggerActivations lists one user's activations of the
	// triggers a workflow declares.
	ListWorkflowTriggerActivations(ctx context.Context, userID, workflow string) ([]*Trigger, error)
	// ListAllWorkflowTriggerActivations lists every activation, for the
	// periodic reconcile.
	ListAllWorkflowTriggerActivations(ctx context.Context) ([]*Trigger, error)

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

	// SetTriggerWebhookTokenHash replaces the webhook token hash. Returns
	// ErrTriggerNotFound when the id does not resolve.
	SetTriggerWebhookTokenHash(ctx context.Context, id string, hash []byte) error
	// SetTriggerWebhookSecret replaces (nil clears) the sealed HMAC secret.
	SetTriggerWebhookSecret(ctx context.Context, id string, sealed []byte) error
	// GetTriggerWebhookCredentials returns ErrTriggerNotFound on a miss.
	GetTriggerWebhookCredentials(ctx context.Context, id string) (*TriggerWebhookCredentials, error)
	// ListIntegrationTriggers lists every enabled integration trigger for
	// one integration whose connection is its owner's and not deleted.
	ListIntegrationTriggers(ctx context.Context, integration string) ([]*IntegrationTriggerRoute, error)
	// ListStalePendingTriggerEvents lists pending events created before
	// olderThan, oldest first.
	ListStalePendingTriggerEvents(ctx context.Context, olderThan time.Time, limit int) ([]*TriggerEvent, error)
	// ClaimPendingTriggerEvent moves a pending event to launched with the
	// given payload. claimed=false means it was not pending (another fire
	// claimed or settled it first).
	ClaimPendingTriggerEvent(ctx context.Context, id string, payload map[string]any) (claimed bool, err error)
	// SettlePendingTriggerEvent records a non-launch verdict on a pending
	// event. settled=false means it was no longer pending.
	SettlePendingTriggerEvent(ctx context.Context, id string, outcome TriggerEventOutcome, detail string) (settled bool, err error)

	// GetTriggerRegistration returns ErrTriggerRegistrationNotFound on a miss.
	GetTriggerRegistration(ctx context.Context, triggerID string) (*TriggerRegistration, error)
	UpsertTriggerRegistration(ctx context.Context, reg *TriggerRegistration) error
	DeleteTriggerRegistration(ctx context.Context, triggerID string) error
}
