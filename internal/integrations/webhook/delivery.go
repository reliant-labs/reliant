package webhook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// ReceiptStore records only authenticated application data, never credentials.
type ReceiptStore interface {
	RecordTriggerDelivery(context.Context, core.TriggerDelivery, []core.IntegrationAccessRevocation) (bool, error)
	FindTriggerDelivery(context.Context, core.TriggerDelivery) (*core.TriggerDelivery, error)
	RetryCallerNotifiedTriggerDelivery(context.Context, core.TriggerDelivery) (*core.TriggerDelivery, error)
	SettleTriggerDelivery(context.Context, string, string, core.TriggerDeliverySettlement) (bool, error)
	TriggerDeliveryEvent(context.Context, string, string, string) (*core.TriggerEvent, error)
}

type verifiedRoute struct {
	TriggerID, UserID, ConnectionID string
}

type integrationDelivery struct {
	Events []Event
	// A non-nil set restricts replay to the trigger, owner, and connection
	// combinations authenticated at receipt. Live routes must still match.
	Verified []verifiedRoute
}

func newIntegrationReceipt(provider string, delivery *Delivery, verified []*core.IntegrationTriggerRoute, receivedAt time.Time) (core.TriggerDelivery, error) {
	id := uuid.NewString()
	key := delivery.ID
	if key == "" && len(delivery.Events) == 1 {
		key = delivery.Events[0].DeliveryID
	}
	if key == "" {
		// Future multi-event providers should expose their delivery ID. Without
		// it do not conflate distinct batches: their events still dedupe later.
		key = id
	}
	for i := range delivery.Events {
		if delivery.Events[i].OccurredAt.IsZero() {
			delivery.Events[i].OccurredAt = receivedAt
		}
	}
	scope := ""
	if len(delivery.Events) == 1 {
		scope = delivery.Events[0].AccountKey
	}
	body := integrationDelivery{Events: delivery.Events}
	for _, r := range verified {
		body.Verified = append(body.Verified, verifiedRoute{r.Trigger.ID, r.Trigger.UserID, *r.Trigger.ConnectionID})
		scope = r.ConnectionAccount
	}
	payload, err := json.Marshal(body)
	return core.TriggerDelivery{ID: id, Source: core.TriggerDeliveryIntegration, Provider: provider,
		ScopeKey: scope, DeliveryKey: storageKey(key), ReceivedAt: receivedAt, Payload: payload}, err
}

func recordReceiptMetric(source string, created bool, err error) {
	outcome := "accepted"
	if err != nil {
		outcome = "receipt_failed"
	} else if !created {
		outcome = "duplicate"
	}
	observability.TriggerDeliveryOutcomes.WithLabelValues(source, outcome).Inc()
}

func storageKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

type boundDelivery struct {
	OwnerID      string
	ConnectionID string // required for polled items
	EventType    string
	Attributes   map[string]string
	Event        triggers.InboundEvent
}

func prefersAsync(header http.Header) bool {
	for _, value := range header.Values("Prefer") {
		for _, preference := range strings.Split(value, ",") {
			if name, _, _ := strings.Cut(preference, ";"); strings.EqualFold(strings.TrimSpace(name), "respond-async") {
				return true
			}
		}
	}
	return false
}

// receiveHook records first and gives the legacy mode an exclusive inline
// lease. The worker can recover a crash, but will not retry an error already
// reported to the caller. Legacy success still names a real trigger event.
func (h *HooksReceiver) receiveHook(ctx context.Context, w http.ResponseWriter, r *http.Request, trigger *core.Trigger, ev triggers.InboundEvent, now time.Time) {
	async := prefersAsync(r.Header)
	id := uuid.NewString()
	key := explicitDeliveryKey(r.Header)
	if key == "" {
		key = "receipt:" + id
		if async {
			ev.DedupeKey = trigger.ID + ":" + key
		}
	}
	d := core.TriggerDelivery{ID: id, Source: core.TriggerDeliveryWebhook, ScopeKey: trigger.ID,
		DeliveryKey: storageKey(key), ReceivedAt: now}
	if !async {
		d.LeaseToken, d.LeaseUntil = uuid.NewString(), now.Add(recordTimeout+30*time.Second)
	}
	var err error
	d.Payload, err = json.Marshal(boundDelivery{OwnerID: trigger.UserID, Event: ev})
	if err == nil {
		err = h.recordHook(ctx, w, trigger, &d, async)
	}
	if err != nil {
		// SQL and provider errors can contain authored payload text; log only
		// the internal identifiers at the intake edge.
		logging.ErrorContext(ctx, "webhook delivery could not be recorded or processed", "trigger_id", trigger.ID, "receipt_id", d.ID)
		writeStatus(w, http.StatusServiceUnavailable, "could not record the delivery; retry")
	}
}

func (h *HooksReceiver) recordHook(ctx context.Context, w http.ResponseWriter, t *core.Trigger, d *core.TriggerDelivery, async bool) error {
	created, err := h.opts.Inbox.RecordTriggerDelivery(ctx, *d, nil)
	recordReceiptMetric(d.Source, created, err)
	if err != nil {
		return err
	}
	if !created {
		stored, err := h.opts.Inbox.FindTriggerDelivery(ctx, *d)
		if err != nil || stored == nil {
			return errors.New("read existing webhook receipt")
		}
		d.ID, d.EventID = stored.ID, stored.EventID
		if stored.CompletedAt != nil && !async {
			if stored.EventID == "" {
				return errors.New("webhook receipt completed without an event")
			}
			event, err := h.opts.Inbox.TriggerDeliveryEvent(ctx, stored.EventID, t.ID, t.UserID)
			if err != nil {
				return err
			}
			writeHookAccepted(w, &triggers.AcceptResult{EventID: event.ID, Outcome: event.Outcome, Duplicate: true})
			return nil
		}
		if stored.FailureCode == core.TriggerDeliveryCallerNotified {
			if async {
				return errors.New("retry an inline webhook failure without respond-async")
			}
			retry, err := h.opts.Inbox.RetryCallerNotifiedTriggerDelivery(ctx, *d)
			if err != nil || retry == nil {
				return errors.New("reclaim webhook receipt")
			}
			d = retry
		} else if !async || stored.DeadAt != nil {
			return errors.New("webhook receipt is already being processed or failed")
		}
	}
	if async {
		w.Header().Set("Preference-Applied", "respond-async")
		writeHookJSON(w, map[string]any{"delivery_id": d.ID, "status": "pending"})
		return nil
	}
	res, err := acceptBoundDelivery(ctx, h.opts.Intake, t, *d)
	settlement := core.TriggerDeliverySettlement{Now: h.now().UTC()}
	if err != nil {
		settlement.FailureCode, settlement.Terminal = core.TriggerDeliveryCallerNotified, true
	} else {
		settlement.EventID, settlement.Outcome = res.EventID, res.Outcome
	}
	// Finish the receipt even when the processing context reached its limit.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	settled, finishErr := h.opts.Inbox.SettleTriggerDelivery(finishCtx, d.ID, d.LeaseToken, settlement)
	if err != nil || finishErr != nil || !settled {
		return errors.New("finish webhook receipt")
	}
	writeHookAccepted(w, res)
	return nil
}

// PollInbox adapts polled items to the same private inbox as HTTP receipts.
// The poller advances its cursor only after this insert succeeds.
type PollInbox struct {
	Store interface {
		RecordTriggerDelivery(context.Context, core.TriggerDelivery, []core.IntegrationAccessRevocation) (bool, error)
	}
}

func (p PollInbox) RecordPolledDelivery(ctx context.Context, t *core.Trigger, provider, connectionID string, item triggers.PollItem, ev triggers.InboundEvent, receivedAt time.Time) error {
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = receivedAt
	}
	payload, err := json.Marshal(boundDelivery{OwnerID: t.UserID, ConnectionID: connectionID, EventType: item.Type, Attributes: item.Attributes, Event: ev})
	if err != nil {
		return errors.New("encode polled trigger delivery")
	}
	created, err := p.Store.RecordTriggerDelivery(ctx, core.TriggerDelivery{ID: uuid.NewString(), Source: core.TriggerDeliveryPoll,
		Provider: provider, ScopeKey: t.ID, DeliveryKey: storageKey(item.ID), ReceivedAt: receivedAt, Payload: payload}, nil)
	recordReceiptMetric(core.TriggerDeliveryPoll, created, err)
	if err != nil {
		return errors.New("persist polled trigger delivery")
	}
	return nil
}

func acceptBoundDelivery(ctx context.Context, intake Intake, t *core.Trigger, d core.TriggerDelivery) (*triggers.AcceptResult, error) {
	var body boundDelivery
	if err := json.Unmarshal(d.Payload, &body); err != nil {
		return nil, err
	}
	if !authz.CanAccessPersonal(body.OwnerID, t.UserID) {
		return nil, errors.New("targeted delivery owner changed")
	}
	opts := triggers.AcceptOptions{}
	if d.Source == core.TriggerDeliveryWebhook {
		opts.Receipt = &triggers.IntakeReceipt{ID: d.ID, LeaseToken: d.LeaseToken, EventID: d.EventID, ReceivedAt: d.ReceivedAt}
	}
	return intake.Accept(ctx, t, body.Event, opts)
}
