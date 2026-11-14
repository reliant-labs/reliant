// Copyright (c) 2025 Reliant Labs
package core

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
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
	// TriggerEventKindManualRun is a run a person started with "Run now" on a
	// workflow: attended, owned by the caller, and the only way to run an
	// automation-only workflow by hand. Its dedupe key is the chat id. A replay
	// of a past event records the event it replayed in its payload.
	TriggerEventKindManualRun TriggerEventKind = "manual.run"
	// TriggerEventKindCLIRun is a run started from the command line
	// (`reliant workflow run`) — by a script, or by an agent shelling out. It
	// is attended (a person owns it and may answer it) but nobody is in it as
	// a conversation, so like agent.start_run it lists under Runs, not in the
	// sidebar, until adopted. Its dedupe key is the chat id, like chat.start.
	TriggerEventKindCLIRun TriggerEventKind = "cli.run"
)

// IsInbound reports whether events of this kind are recorded by a receiver as
// pending and launched later by the fire workflow.
func (k TriggerEventKind) IsInbound() bool {
	return k == TriggerEventKindWebhook || k == TriggerEventKindIntegration || k == TriggerEventKindWorkflowEvent
}

// Unattended reports whether a run launched by an event of this kind has no
// human behind it, so its trigger stands in for its owner. Two things follow:
//
//   - Only such a run may wake its machine with the delegated daemon:resume
//     token its trigger holds (see internal/automationcred): nobody is signed
//     in to wake it any other way. An attended run acts as the signed-in user
//     or not at all.
//   - Only such a run's finish is governed by its trigger's notification
//     policy (NotifyOnComplete, one notification per failure streak): nobody
//     is waiting on it. An attended run notifies whoever is.
//
// Both also need the run's own trigger — its token, its row — which is the
// launch event's trigger id, not something a kind can promise.
//
// A kind missing from eventKindUnattended reads as attended, so a new kind
// that was never classified can never borrow the token or go quiet — and a
// test fails until it is classified.
func (k TriggerEventKind) Unattended() bool { return eventKindUnattended[k] }

// UnattendedEventKinds lists, sorted, the kinds Unattended reports true for:
// the same rule, for a query that has to apply it in SQL.
func UnattendedEventKinds() []string {
	kinds := make([]string, 0, len(eventKindUnattended))
	for kind, unattended := range eventKindUnattended {
		if unattended {
			kinds = append(kinds, string(kind))
		}
	}
	sort.Strings(kinds)
	return kinds
}

// eventKindUnattended takes a position on every TriggerEventKind.
var eventKindUnattended = map[TriggerEventKind]bool{
	// A stored trigger fired: nobody pressed anything.
	TriggerEventKindSchedule:      true,
	TriggerEventKindWebhook:       true,
	TriggerEventKindIntegration:   true,
	TriggerEventKindWorkflowEvent: true,
	// A human started it, directly or through an agent working for them.
	TriggerEventKindChatStart:     false,
	TriggerEventKindAgentStartRun: false,
	TriggerEventKindBuilderTest:   false,
	TriggerEventKindManualRun:     false,
	TriggerEventKindCLIRun:        false,
}

// eventKindOrigin takes a position on every TriggerEventKind: who started a
// chat its launch event launches. The migration that added chats.origin
// (20261030033817_chat_origin) backfills with the same mapping.
var eventKindOrigin = map[TriggerEventKind]ChatOrigin{
	TriggerEventKindChatStart:     ChatOriginUser,
	TriggerEventKindSchedule:      ChatOriginAutomation,
	TriggerEventKindWebhook:       ChatOriginAutomation,
	TriggerEventKindIntegration:   ChatOriginAutomation,
	TriggerEventKindWorkflowEvent: ChatOriginAutomation,
	TriggerEventKindAgentStartRun: ChatOriginAgent,
	TriggerEventKindManualRun:     ChatOriginManualRun,
	TriggerEventKindBuilderTest:   ChatOriginBuilderTest,
	TriggerEventKindCLIRun:        ChatOriginCLI,
}

// ChatOriginOf is the origin of a chat launched by an event of this kind. A
// kind missing from eventKindOrigin reads as AUTOMATION, so a new launch path
// that was never classified keeps its runs out of the chat list rather than
// flooding it — and a test fails until it is classified.
func ChatOriginOf(kind TriggerEventKind) ChatOrigin {
	if origin, ok := eventKindOrigin[kind]; ok {
		return origin
	}
	return ChatOriginAutomation
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
var ErrTriggerNotFound = svcerr.NotFound("trigger")

// ErrTriggerNameTaken is returned when a create or update would give a trigger
// the name another trigger of the same user already has in the project.
var ErrTriggerNameTaken = svcerr.AlreadyExists("a trigger with this name already exists in this project; choose another name or update the existing trigger")

// ErrTriggerEventNotFound is returned when a trigger event lookup misses.
var ErrTriggerEventNotFound = svcerr.NotFound("trigger event")

// ActingIdentity names who an automation's integration writes speak as.
type ActingIdentity string

const (
	// ActingIdentityOwner: the activation's owner, through their own
	// credential. What every activation did before identities existed, and the
	// only identity a self-hosted deployment has.
	ActingIdentityOwner ActingIdentity = "owner"
	// ActingIdentityApp: the GitHub App's bot (`reliant-labs[bot]`), through a
	// per-repository installation token.
	ActingIdentityApp ActingIdentity = "app"
)

// Valid reports whether i is a stored identity.
func (i ActingIdentity) Valid() bool { return i == ActingIdentityOwner || i == ActingIdentityApp }

// OrOwner is i, or the owner identity when i is empty: the zero value means
// "never chose", which is the owner.
func (i ActingIdentity) OrOwner() ActingIdentity {
	if i == "" {
		return ActingIdentityOwner
	}
	return i
}

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
	// on, validated at write time (there is no FK because the daemons table is
	// a cache of the control plane's). Empty exactly when NoMachine is set.
	DaemonID string
	// NoMachine is the owner's explicit choice that launched runs have no
	// machine: they are offered only tools that run on the server, and a
	// workflow that needs a machine is refused when the trigger is written.
	// See research/DAEMONLESS_RUNS.md.
	NoMachine bool
	// MachinePoolID is the machine pool whose members each launched run is
	// placed on (dev-docs/design/machine-pools.md). Exactly one of DaemonID,
	// NoMachine and MachinePoolID is set.
	MachinePoolID string
	// MachinePoolName is MachinePoolID's name, joined in by reads.
	MachinePoolName string
	// CatalogDaemonID is not stored. For a pool-bound trigger the fire paths
	// set it to a member on the pool's agreed catalog, so the declaration,
	// filter, inputs and concurrency are read from the catalog its runs are
	// placed on (CatalogMachine), not the union of every machine's.
	CatalogDaemonID string
	// NotifyOnComplete opts the owner in to being told when a run of this
	// trigger completes successfully: unread + OS notification, and the run
	// is kept unarchived. Never an Inbox item: a successful run is not
	// waiting on anyone. Unattended completions are otherwise silent.
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
	// ActingIdentity is who the integration writes of every run this trigger
	// launches speak as: ActingIdentityOwner (the owner, through their GitHub
	// App user token) or ActingIdentityApp (the GitHub App's bot, through a
	// per-repository installation token control-plane mints only when the
	// owner can push). Never empty on a stored row.
	ActingIdentity ActingIdentity
	CreatedAt      time.Time
	UpdatedAt      time.Time

	// ProjectName and DaemonName are read-only display names joined in by
	// GetTrigger and ListTriggers. Empty when the project or daemon row is
	// gone or has no name; never written.
	ProjectName  string
	DaemonName   string
	MachineState string
}

// WebhookConfig is Trigger.Config for TriggerKindWebhook. The token hash and
// the HMAC secret are NOT here: config is rendered back to clients, and those
// live in their own columns (see TriggerWebhookCredentials).
type WebhookConfig struct {
	// HMAC, when set, accepts deliveries signed with the trigger's secret.
	HMAC *WebhookHMACConfig `json:"hmac,omitempty"`
	// Source names the sender Reliant wires itself (WebhookSourceAlerting),
	// or "" for one the user configures.
	Source string `json:"source,omitempty"`
}

// WebhookSourceAlerting is a webhook the control plane's alerting delivers
// to: Reliant provisions the sender, and its token is never shown.
const WebhookSourceAlerting = "alerting"

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
	// MatchInput is the declared scope: attribute -> the workflow input whose
	// values the attribute must be one of. It is the declaration's, kept so a
	// row can tell "scoped, projection missing" (matches nothing) from "not
	// scoped".
	MatchInput map[string]string `json:"match_input,omitempty"`
	// MatchAny is MatchInput resolved against the activation's params:
	// attribute -> the values it may take. An attribute present with no values
	// matches nothing. It is a projection, rewritten whenever the activation's
	// params or its declaration change.
	MatchAny map[string][]string `json:"match_any,omitempty"`
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
	Status         string // active | error | needs_reauth
	StatusDetail   string
	// StatusSince is when the source entered Status. Read-only: the store
	// moves it only when Status changes, however often the source is polled.
	StatusSince time.Time
	// LastGapAt and LastGapDetail record the last time the source lost its
	// place (a cursor the provider no longer honoured) and re-baselined,
	// firing nothing for what arrived in between. Nil when it never has.
	LastGapAt     *time.Time
	LastGapDetail string
	CreatedAt     time.Time
	UpdatedAt     time.Time
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
var ErrTriggerRegistrationNotFound = svcerr.NotFound("trigger registration")

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
	// CapacityOrder is non-nil only for intentionally queued workflow triggers.
	// Such events have no delivery-age expiry. Scope survives activation deletion.
	CapacityOrder       *int64
	CapacityProjectID   *string
	CapacityWorkflowRef *string
	// CapacityKey is the rendered concurrency key under `waiting: latest`:
	// enrolling an event with one supersedes the older waiting events of the
	// same key. Nil is FIFO (or a key that could not be rendered): the event
	// supersedes nothing and nothing supersedes it.
	CapacityKey *string
	// RunStatus is how the run this firing launched ended — one of the
	// TriggerRun* values — recorded when that run, the one the trigger itself
	// fired, reaches a terminal status. Empty while it is still going, and for
	// a firing that launched no unattended run. A person can keep a launched
	// chat going with turns of their own; the trigger's health, streak and
	// overlap rules are about its own run, so they read this, not the chat.
	RunStatus string
	// Sender is who or what the event came from, as the source authenticated
	// it: set once at intake and never from the payload. Nil for a start a
	// person made themselves (a chat, a builder test).
	Sender *TriggerSender
	// Origin says whether Reliant caused this event, and which run did: set once
	// at intake from the write ledger (see TriggerOrigin). Nil for an event no
	// enricher looked at; readers treat nil as "not caused by Reliant".
	Origin *TriggerOrigin
	// Definition is which workflow definition the firing resolved: the file,
	// its version and where it was synced from, read when the firing was
	// decided. Nil for a firing recorded without one (an ad hoc start, or a
	// firing recorded before definitions were).
	Definition *TriggerEventDefinition
}

// TriggerEventDefinition is the workflow definition one firing resolved, so
// "which version of which file did this run, from which machine and commit?"
// has an answer after the file has changed again.
type TriggerEventDefinition struct {
	// Workflow is the ref the trigger names.
	Workflow string `json:"workflow"`
	// Source is where it resolved: "builtin", "user" (the owner's account
	// workflow) or "project" (a synced .reliant/workflows file); "" when it
	// did not resolve at all.
	Source string `json:"source,omitempty"`
	// Path is the project file, relative to the project root, for a project
	// workflow; empty otherwise.
	Path string `json:"path,omitempty"`
	// DaemonID and Machine are the machine whose synced copy of Path was
	// read (Machine is its display name); empty when no one machine's copy
	// can be named.
	DaemonID string `json:"daemon_id,omitempty"`
	Machine  string `json:"machine,omitempty"`
	// Repo, Branch, Commit and Dirty are the git state of the checkout the
	// file was synced from (config.SyncedRepoState); empty when the machine
	// reported none.
	Repo   string `json:"repo,omitempty"`
	Branch string `json:"branch,omitempty"`
	Commit string `json:"commit,omitempty"`
	Dirty  bool   `json:"dirty,omitempty"`
	// ContentHash is the hex SHA-256 of the definition's bytes: the version,
	// comparable with `sha256sum` of the file.
	ContentHash string `json:"content_hash,omitempty"`
	// Error is why this definition cannot run (it does not parse, or does not
	// validate, or does not resolve); empty when it can.
	Error string `json:"error,omitempty"`
	// CatalogPin is the catalog snapshot (db.Repo.PinProjectCatalog) the
	// firing read this definition in and launched its run on: the run loads
	// exactly this version, whatever was synced after. Empty when the firing
	// read the live catalog.
	CatalogPin string `json:"catalog_pin,omitempty"`
}

// Broken reports a definition that resolved to one version of one file which
// cannot run as written. A firing against it is refused, never run from some
// other version.
func (d *TriggerEventDefinition) Broken() bool {
	return d != nil && d.Error != "" && d.Path != "" && d.ContentHash != ""
}

// Describe is the definition for display: "machine cicd at
// .reliant/workflows/nightly.yaml @ main 1a2b3c4, clean (sha256 9f86d081884c)".
func (d *TriggerEventDefinition) Describe() string {
	if d == nil {
		return ""
	}
	var b strings.Builder
	switch {
	case d.Path != "":
		if d.Machine != "" {
			b.WriteString("machine " + d.Machine + " at ")
		}
		b.WriteString(d.Path)
	case d.Source == "builtin":
		b.WriteString("builtin workflow " + d.Workflow)
	case d.Source == "user":
		b.WriteString("your account workflow " + strconv.Quote(d.Workflow))
	default:
		b.WriteString("workflow " + strconv.Quote(d.Workflow))
	}
	if d.Commit != "" || d.Branch != "" {
		branch := d.Branch
		if branch == "" {
			branch = "detached HEAD"
		}
		b.WriteString(" @ " + branch)
		if d.Commit != "" {
			b.WriteString(" " + shortHash(d.Commit, 7))
		}
		if d.Dirty {
			b.WriteString(", uncommitted changes")
		} else {
			b.WriteString(", clean")
		}
	}
	if d.ContentHash != "" {
		b.WriteString(" (sha256 " + shortHash(d.ContentHash, 12) + ")")
	}
	return b.String()
}

func shortHash(h string, n int) string {
	if len(h) > n {
		return h[:n]
	}
	return h
}

// TriggerSenderKind is what kind of party sent the event that fired a
// trigger. It is trigger.sender.kind.
type TriggerSenderKind string

// The sender kinds. A test in frontends/web/ mirrors this list.
const (
	TriggerSenderKindSlack    TriggerSenderKind = "slack"
	TriggerSenderKindGitHub   TriggerSenderKind = "github"
	TriggerSenderKindEmail    TriggerSenderKind = "email"
	TriggerSenderKindSMS      TriggerSenderKind = "sms"
	TriggerSenderKindWebhook  TriggerSenderKind = "webhook"
	TriggerSenderKindWorkflow TriggerSenderKind = "workflow"
	TriggerSenderKindSchedule TriggerSenderKind = "schedule"
	// TriggerSenderKindUser is a Reliant user acting directly: "Run now".
	TriggerSenderKindUser TriggerSenderKind = "user"
)

// TriggerSender is the normalized sender of a trigger event: trigger.sender.
//
// Each source sets it at intake from what IT authenticated, never from a field
// the sender controls when a verified one exists. Verified says the source
// attests ID: a signed Slack or GitHub delivery, a Gmail message whose DMARC
// passed, a webhook caller holding the token. An unverified sender (an SMS
// From, a spoofable email From) is still recorded, so a filter can read it,
// but an allowlist must require verified to mean anything.
type TriggerSender struct {
	Kind TriggerSenderKind `json:"kind"`
	// ID is the provider's own stable identifier for the sender: a Slack
	// user id, a GitHub user id (the number, as a string — never the login,
	// which can be renamed and reclaimed), an email address, an E.164 number,
	// a trigger id.
	ID string `json:"id"`
	// DisplayName is for people; never authorize on it.
	DisplayName string `json:"display_name,omitempty"`
	Verified    bool   `json:"verified"`
}

// How a trigger's own run ended (TriggerEvent.RunStatus).
const (
	TriggerRunCompleted = "completed"
	TriggerRunFailed    = "failed"
	TriggerRunCancelled = "cancelled"
)

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

// OriginConfidence is how sure Reliant is that it caused an event.
type OriginConfidence string

const (
	// OriginExact: the event's own object id is one a ledger entry recorded as
	// created or touched by a run.
	OriginExact OriginConfidence = "exact"
	// OriginIntent: no id matched, but the event matches a recent ledger entry
	// on target, action and content (a body digest, a label, a head sha).
	OriginIntent OriginConfidence = "intent"
	// OriginApp: the event came from Reliant's own GitHub App (its bot, or an
	// object created through it), but no ledger entry says which run.
	OriginApp OriginConfidence = "app"
	// OriginClaimed: the event carries a Reliant-Run trailer. Anyone can write
	// one, so a claim may only ever SUPPRESS an event, never authorize one.
	OriginClaimed OriginConfidence = "claimed"
	// OriginNone: nothing says Reliant caused it.
	OriginNone OriginConfidence = "none"
)

// The people an event can be attributed to (trigger.origin.actor).
const (
	// OriginActorApp is Reliant's own app identity.
	OriginActorApp = "app"
	// OriginActorOwner is the person who owns the run that wrote.
	OriginActorOwner = "owner"
	// OriginActorUnknown is anyone else, or nobody.
	OriginActorUnknown = "unknown"
)

// TriggerOrigin is trigger.origin: whether Reliant caused an event and which
// run did, resolved at intake against the write ledger.
//
// The run that wrote is shown only to the trigger of the SAME owner: one event
// fans out to many users' triggers, and a stranger's trigger learns that
// Reliant caused it (CausedByReliant, Foreign) and nothing about whose run.
type TriggerOrigin struct {
	CausedByReliant bool             `json:"caused_by_reliant"`
	Confidence      OriginConfidence `json:"confidence"`
	// RunID is the chat of the run that wrote; Workflow its root workflow;
	// TriggerName the declared trigger that launched it.
	RunID       string `json:"run_id,omitempty"`
	Workflow    string `json:"workflow,omitempty"`
	TriggerName string `json:"trigger_name,omitempty"`
	// SameWorkflow: the run that wrote is a run of the very workflow this
	// trigger belongs to, for the same owner.
	SameWorkflow bool `json:"same_workflow"`
	// Actor is who the write was made as: OriginActorApp, OriginActorOwner or
	// OriginActorUnknown.
	Actor string `json:"actor,omitempty"`
	// Foreign: Reliant caused it, but the run belongs to someone else, so its
	// details are withheld.
	Foreign bool `json:"foreign,omitempty"`
	// Lineage is the chain of integration-event hops that led to the run that
	// wrote, root first; LineageDepth is its length.
	Lineage []OriginLink `json:"lineage,omitempty"`
	// Depth is the number of hops that led to the run that wrote, kept even when
	// Lineage is withheld (Foreign), so the loop cap still counts them.
	Depth int `json:"depth,omitempty"`
	// WriteID is the ledger entry that matched.
	WriteID string `json:"write_id,omitempty"`
}

// OriginLink is one hop of a lineage: Trigger fired because SourceRun wrote.
type OriginLink struct {
	TriggerID   string `json:"trigger_id"`
	SourceRunID string `json:"source_run_id,omitempty"`
}

// LineageDepth is how many hops led to the run that wrote.
func (o *TriggerOrigin) LineageDepth() int {
	if o == nil {
		return 0
	}
	if o.Depth > len(o.Lineage) {
		return o.Depth
	}
	return len(o.Lineage)
}
