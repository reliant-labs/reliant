// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Polled integration triggers (Gmail's history.list, an RSS feed): a
// Temporal Schedule per trigger runs PollTrigger, which asks the
// integration's poller for what is new since the stored cursor and hands each
// new item to the same Intake a webhook delivery goes through.
//
// The first poll is a BASELINE: it records where the feed is now and fires
// nothing, so enabling a trigger never replays the inbox. Every later item is
// an event whose dedupe key is "<trigger id>:<item id>", so a re-poll that
// sees an item again — a cursor that did not advance, a provider that
// re-lists — cannot fire it twice.

// Names registered with Temporal for polling.
const (
	PollWorkflowName = "TriggerPollWorkflow"
	PollActivityName = "PollTrigger"
)

// DefaultPollInterval is the poll period when neither the trigger nor the
// integration names one.
const DefaultPollInterval = 5 * time.Minute

// Poller is one integration's poll function. Implementations make the
// outbound calls (through the trigger's connection) and do no bookkeeping:
// the cursor, dedupe and routing are TriggerPoller's.
type Poller interface {
	// Poll returns the items newer than req.Cursor and the cursor to resume
	// from. An empty req.Cursor is the baseline: return the feed's current
	// position and NO items. A cursor the provider no longer honours (Gmail's
	// 404 on a stale history id) should be answered with a fresh baseline
	// and no items, never a replay.
	//
	// A provider that refuses req.Credential outright (a 401 after the token
	// was revoked) should be reported as an *httpaction.CredentialError with
	// CodeNeedsReauth: the poll is then recorded as waiting on a reconnect
	// and is not retried.
	Poll(ctx context.Context, req PollRequest) (*PollResult, error)
}

// PollRequest is one poll.
type PollRequest struct {
	TriggerID    string
	OwnerUserID  string
	ConnectionID string
	// Credential authenticates the poll's requests as the trigger's owner,
	// through the trigger's connection. The activity resolved it from the
	// trigger ROW (PollCredentials), so a poller holds one account's
	// credential and has nothing it could ask for another with. Apply writes
	// it only into a request to one of the integration's own hosts, and
	// Scrub removes it from anything the poller is about to return.
	Credential httpaction.Credential
	// Cursor is what the previous poll returned; empty for the baseline.
	Cursor string
	// Config is the trigger's source config, for pollers that narrow the
	// query (a label, a folder).
	Config core.IntegrationConfig
}

// PollResult is what a poll found.
type PollResult struct {
	// Cursor is where the next poll resumes. Required.
	Cursor string
	Items  []PollItem
	// Gap, when set, says the poller lost its place (the provider no longer
	// honoured the cursor) and re-baselined: Cursor is a fresh position and
	// what arrived in between will never fire. It is recorded on the
	// registration and shown in trigger health; the text says what was lost.
	Gap string
}

// PollItem is one new item.
type PollItem struct {
	// ID is stable for the item across polls: it is the dedupe key.
	ID string
	// Type is matched against the trigger's events, like Event.Type.
	Type       string
	OccurredAt time.Time
	Attributes map[string]string
	// Data is untrusted and becomes trigger.payload.data.
	Data map[string]any
	// Sender is trigger.sender, normalized by the poller from what the
	// provider attests (Gmail: the From address, verified only when its
	// DMARC or aligned DKIM passed). Required: an item without one is not
	// delivered.
	Sender *core.TriggerSender
}

// Pollers looks up an integration's poller. The webhook Registry satisfies it.
type Pollers interface {
	Poller(integration string) (Poller, bool)
}

// PollRepo is what polling reads and writes. *db.Repo satisfies it.
type PollRepo interface {
	EventRepo
	GetTriggerRegistration(ctx context.Context, triggerID string) (*core.TriggerRegistration, error)
	UpsertTriggerRegistration(ctx context.Context, reg *core.TriggerRegistration) error
	// GetConnection returns the user's connection; a foreign id is an error.
	GetConnection(ctx context.Context, userID, id string) (*core.Connection, error)
}

// PollInput is the poll workflow's and activity's input.
type PollInput struct {
	TriggerID string `json:"trigger_id"`
}

// PollOutput reports what a poll did.
type PollOutput struct {
	Baseline bool   `json:"baseline,omitempty"`
	Gap      bool   `json:"gap,omitempty"`
	Skipped  bool   `json:"skipped,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Items    int    `json:"items"`
	Accepted int    `json:"accepted"`
}

// PollCredentials resolves the credential a polled trigger authenticates with.
// It is handed the trigger id and nothing else: the owner, the connection and
// the integration are read from the trigger row. connauth.Source satisfies it.
type PollCredentials interface {
	ForTrigger(ctx context.Context, triggerID string) (httpaction.Credential, error)
}

// TriggerPoller runs polls. It is the PollTrigger activity.
type TriggerPoller struct {
	repo    PollRepo
	pollers Pollers
	intake  *Intake
	creds   PollCredentials
	now     func() time.Time
}

// NewTriggerPoller builds the poll activity's receiver. creds may be nil (a
// worker with no connections), in which case every poll is skipped rather
// than run unauthenticated.
func NewTriggerPoller(repo PollRepo, pollers Pollers, intake *Intake, creds PollCredentials) *TriggerPoller {
	return &TriggerPoller{repo: repo, pollers: pollers, intake: intake, creds: creds, now: time.Now}
}

// Poll is the PollTrigger activity.
func (p *TriggerPoller) Poll(ctx context.Context, in PollInput) (*PollOutput, error) {
	trigger, err := p.repo.GetTrigger(ctx, in.TriggerID)
	if err != nil {
		if errors.Is(err, core.ErrTriggerNotFound) {
			return &PollOutput{Skipped: true, Reason: "trigger deleted"}, nil
		}
		return nil, fmt.Errorf("load trigger %s: %w", in.TriggerID, err)
	}
	if !trigger.Enabled {
		return &PollOutput{Skipped: true, Reason: "trigger is disabled"}, nil
	}
	cfg, err := IntegrationConfigFor(trigger)
	if err != nil {
		return nil, temporal.NewNonRetryableApplicationError(err.Error(), nonRetryableFireError, err)
	}
	poller, ok := p.pollers.Poller(cfg.Integration)
	if !ok {
		return &PollOutput{Skipped: true, Reason: cfg.Integration + " is not polled on this server"}, nil
	}
	if trigger.ConnectionID == nil {
		return &PollOutput{Skipped: true, Reason: "the trigger's connection was deleted"}, nil
	}
	// The connection must be the owner's own and able to authenticate; a
	// poll through anything else would read an account the owner does not
	// hold.
	conn, err := p.repo.GetConnection(ctx, trigger.UserID, *trigger.ConnectionID)
	if err != nil || conn == nil || conn.UserID != trigger.UserID || conn.IntegrationID != cfg.Integration {
		return &PollOutput{Skipped: true, Reason: "the trigger's connection is unavailable"}, nil
	}

	reg, err := p.repo.GetTriggerRegistration(ctx, trigger.ID)
	switch {
	case errors.Is(err, core.ErrTriggerRegistrationNotFound):
		reg = &core.TriggerRegistration{TriggerID: trigger.ID, Provider: cfg.Integration}
	case err != nil:
		return nil, fmt.Errorf("load registration for %s: %w", trigger.ID, err)
	}
	baseline := reg.Cursor == ""

	if conn.Status == core.ConnectionStatusNeedsReauth {
		// Marked by an earlier refresh (an action node's, or this trigger's
		// last poll). Record it here too, so trigger health says why nothing
		// arrives.
		now := p.now().UTC()
		reg.LastPolledAt, reg.Provider = &now, cfg.Integration
		return p.needsReauth(ctx, reg, &httpaction.CredentialError{
			Code: httpaction.CodeNeedsReauth, Message: fmt.Sprintf("connection %q needs to be reconnected", conn.Name),
		})
	}
	if conn.Status != core.ConnectionStatusActive {
		return &PollOutput{Skipped: true, Reason: "the trigger's connection is unavailable"}, nil
	}

	if p.creds == nil {
		return &PollOutput{Skipped: true, Reason: "connections are not available on this worker"}, nil
	}
	// By trigger id alone: the source reads the owner and the connection from
	// the trigger row again, so the checks above are not what keeps a poll on
	// its owner's account — the resolver is.
	cred, credErr := p.creds.ForTrigger(ctx, trigger.ID)
	if credErr == nil && cred == nil {
		credErr = &httpaction.CredentialError{Code: httpaction.CodeInternal, Message: "the trigger's connection resolved to no credential"}
	}
	if credErr != nil {
		return p.credentialFailed(ctx, reg, cfg, credErr)
	}

	res, pollErr := poller.Poll(ctx, PollRequest{
		TriggerID: trigger.ID, OwnerUserID: trigger.UserID, ConnectionID: conn.ID,
		Credential: cred, Cursor: reg.Cursor, Config: cfg,
	})
	now := p.now().UTC()
	reg.LastPolledAt = &now
	reg.Provider = cfg.Integration
	if pollErr != nil {
		if isNeedsReauth(pollErr) {
			// The provider refused the credential mid-poll (a revoked grant
			// whose access token had not yet expired).
			return p.needsReauth(ctx, reg, pollErr)
		}
		// What is stored and returned is scrubbed: a provider error body can
		// echo the request, credential included.
		pollErr = errors.New(cred.Scrub(pollErr.Error()))
		// Keep the cursor: advancing past items never delivered loses them.
		reg.Status, reg.StatusDetail = core.TriggerRegistrationError, truncate(pollErr.Error(), 500)
		if err := p.repo.UpsertTriggerRegistration(ctx, reg); err != nil {
			logging.Warn("could not record a poll failure", "trigger_id", trigger.ID, "error", err)
		}
		return nil, fmt.Errorf("poll %s for trigger %s: %w", cfg.Integration, trigger.ID, pollErr)
	}
	if res == nil || res.Cursor == "" {
		return nil, fmt.Errorf("poller %s returned no cursor", cfg.Integration)
	}

	out := &PollOutput{Baseline: baseline}
	if !baseline {
		out.Items = len(res.Items)
		for _, item := range res.Items {
			if item.ID == "" || !IntegrationEventMatches(cfg, item.Type, item.Attributes) {
				continue
			}
			if item.Sender == nil {
				// A poller bug, not the item's: say so instead of recording an
				// event no "Only from" filter could ever pass.
				logging.Warn("polled item has no sender; dropped", "trigger_id", trigger.ID, "integration", cfg.Integration, "item_id", item.ID)
				continue
			}
			if _, err := p.intake.Accept(ctx, trigger, pollEvent(trigger, cfg.Integration, conn, item), AcceptOptions{}); err != nil {
				// Do not advance: the next poll re-lists, and what was
				// already recorded dedupes.
				return nil, fmt.Errorf("record polled item %s: %w", item.ID, err)
			}
			out.Accepted++
		}
	}
	reg.Cursor = res.Cursor
	reg.Status, reg.StatusDetail = core.TriggerRegistrationActive, ""
	if res.Gap != "" {
		reg.LastGapAt, reg.LastGapDetail = &now, truncate(res.Gap, 500)
		out.Gap = true
		logging.Warn("polled trigger lost its place and re-baselined", "trigger_id", trigger.ID, "integration", cfg.Integration, "detail", reg.LastGapDetail)
	}
	if err := p.repo.UpsertTriggerRegistration(ctx, reg); err != nil {
		return nil, fmt.Errorf("save cursor for %s: %w", trigger.ID, err)
	}
	return out, nil
}

// credentialFailed records why a poll could not get its credential. A
// connection that needs reconnecting, or that the resolver refuses outright,
// is a skip: retrying cannot help, and a refresh grant that is dead must not
// be hammered (Gmail testing-mode tokens die weekly). Anything else is
// transient and retried.
func (p *TriggerPoller) credentialFailed(ctx context.Context, reg *core.TriggerRegistration, cfg core.IntegrationConfig, err error) (*PollOutput, error) {
	now := p.now().UTC()
	reg.LastPolledAt = &now
	reg.Provider = cfg.Integration
	if isNeedsReauth(err) {
		return p.needsReauth(ctx, reg, err)
	}
	var ce *httpaction.CredentialError
	if errors.As(err, &ce) {
		switch ce.Code {
		case httpaction.CodeFailedPrecondition, httpaction.CodeNotFound, httpaction.CodeInvalidArgument:
			return &PollOutput{Skipped: true, Reason: "the trigger's connection is unavailable: " + ce.Message}, nil
		}
	}
	reg.Status, reg.StatusDetail = core.TriggerRegistrationError, truncate(err.Error(), 500)
	if uerr := p.repo.UpsertTriggerRegistration(ctx, reg); uerr != nil {
		logging.Warn("could not record a poll credential failure", "trigger_id", reg.TriggerID, "error", uerr)
	}
	return nil, fmt.Errorf("resolve credential for trigger %s: %w", reg.TriggerID, err)
}

// needsReauth records that the trigger's connection must be reconnected and
// ends the poll without an error, so Temporal does not retry it. The cursor
// is kept: once the owner reconnects, the next poll resumes where this one
// stopped, with nothing replayed.
func (p *TriggerPoller) needsReauth(ctx context.Context, reg *core.TriggerRegistration, cause error) (*PollOutput, error) {
	detail := "the connection needs to be reconnected"
	var ce *httpaction.CredentialError
	if errors.As(cause, &ce) && ce.Message != "" {
		detail = ce.Message
	}
	reg.Status, reg.StatusDetail = core.TriggerRegistrationNeedsReauth, truncate(detail, 500)
	if err := p.repo.UpsertTriggerRegistration(ctx, reg); err != nil {
		return nil, fmt.Errorf("record needs_reauth for %s: %w", reg.TriggerID, err)
	}
	return &PollOutput{Skipped: true, Reason: detail}, nil
}

func isNeedsReauth(err error) bool {
	var ce *httpaction.CredentialError
	return errors.As(err, &ce) && ce.Code == httpaction.CodeNeedsReauth
}

func pollEvent(trigger *core.Trigger, integration string, conn *core.Connection, item PollItem) InboundEvent {
	attrs := make(map[string]any, len(item.Attributes))
	for k, v := range item.Attributes {
		attrs[k] = v
	}
	account := ""
	if conn.ExternalAccountID != nil {
		account = *conn.ExternalAccountID
	}
	return InboundEvent{
		Kind:       core.TriggerEventKindIntegration,
		DedupeKey:  trigger.ID + ":" + item.ID,
		OccurredAt: item.OccurredAt,
		Payload: map[string]any{
			"integration": integration,
			"event":       item.Type,
			"account":     account,
			"delivery_id": item.ID,
			"attributes":  attrs,
			"data":        item.Data,
		},
		Sender: item.Sender,
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// TriggerPollWorkflow is one scheduled poll.
func TriggerPollWorkflow(ctx workflow.Context, input PollInput) (*PollOutput, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:        5 * time.Second,
			BackoffCoefficient:     2.0,
			MaximumInterval:        time.Minute,
			MaximumAttempts:        3,
			NonRetryableErrorTypes: []string{nonRetryableFireError},
		},
	})
	var out PollOutput
	if err := workflow.ExecuteActivity(ctx, PollActivityName, input).Get(ctx, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PollWorkflowID is a scheduled poll's workflow id.
func PollWorkflowID(triggerID string) string { return schedulePrefix + "poll-" + triggerID }

// pollIntervalFor is a polled trigger's period.
func pollIntervalFor(cfg core.IntegrationConfig) time.Duration {
	if cfg.PollInterval != "" {
		if d, err := time.ParseDuration(cfg.PollInterval); err == nil && d >= minPollInterval {
			return d
		}
	}
	return DefaultPollInterval
}
