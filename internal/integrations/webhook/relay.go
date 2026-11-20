package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/triggers"
)

type RelayStore interface {
	EventsStore
	GetTrigger(context.Context, string) (*core.Trigger, error)
	ClaimTriggerDeliveries(context.Context, time.Time, time.Time, string, int) ([]core.TriggerDelivery, error)
	RenewTriggerDelivery(context.Context, string, string, time.Time, time.Time) (bool, error)
	SettleTriggerDelivery(context.Context, string, string, core.TriggerDeliverySettlement) (bool, error)
	PruneTriggerDeliveries(context.Context, time.Time) (int64, error)
}

type PollConnectionReader interface {
	GetConnection(context.Context, string, string) (*core.Connection, error)
}

const (
	deliveryWorkers       = 8
	deliveryLease         = 2 * recordTimeout
	deliveryPruneInterval = time.Hour
	deliveryRetention     = 7 * 24 * time.Hour
	deliveryMaxFailures   = 20
)

var errInvalidInboxPayload = errors.New("invalid trigger inbox payload")
var errInboxLeaseLost = errors.New("trigger inbox lease lost")

// Relay processes verified stored deliveries. It does not possess provider
// verification credentials or re-open the original HTTP request.
type Relay struct {
	store       RelayStore
	router      *EventsReceiver
	connections PollConnectionReader
	now         func() time.Time
}

func NewRelay(store RelayStore, intake Intake, connections PollConnectionReader, pause *triggers.AutoPause) *Relay {
	return &Relay{store: store, router: NewEventsReceiver(EventsOptions{Store: store, Intake: intake, AutoPause: pause}),
		connections: connections, now: time.Now}
}

func (r *Relay) Run(ctx context.Context) error {
	// Each worker claims only one row it can start now. Slow deliveries do
	// not lease the rest of a batch while they run.
	done := make(chan struct{}, deliveryWorkers)
	for range deliveryWorkers {
		go func() {
			defer func() { done <- struct{}{} }()
			r.consume(ctx)
		}()
	}
	ticker := time.NewTicker(deliveryPruneInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		pruneCtx, cancel := context.WithTimeout(ctx, recordTimeout)
		if _, err := r.store.PruneTriggerDeliveries(pruneCtx, r.now().Add(-deliveryRetention)); err != nil && ctx.Err() == nil {
			logging.WarnContext(ctx, "trigger inbox retention pass failed")
		}
		cancel()
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
	for range deliveryWorkers {
		<-done
	}
	return nil
}

func (r *Relay) consume(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		worked, err := r.ProcessOne(ctx)
		if err != nil && ctx.Err() == nil {
			logging.WarnContext(ctx, "trigger inbox claim or settlement failed")
		}
		if worked && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

// ProcessOne claims, processes and settles one delivery. Errors without a
// settlement leave its lease to expire, including worker shutdown.
func (r *Relay) ProcessOne(ctx context.Context) (bool, error) {
	now := r.now().UTC()
	claimCtx, cancel := context.WithTimeout(ctx, recordTimeout)
	rows, err := r.store.ClaimTriggerDeliveries(claimCtx, now, now.Add(deliveryLease), uuid.NewString(), 1)
	cancel()
	if err != nil || len(rows) == 0 {
		return false, err
	}
	d := rows[0]
	res, processErr := r.process(triggers.WithDeliveryCatalogCache(ctx), d)
	if ctx.Err() != nil || errors.Is(processErr, errInboxLeaseLost) {
		return true, processErr
	}
	settlement := core.TriggerDeliverySettlement{Now: r.now().UTC(), EventID: d.EventID}
	if res != nil {
		settlement.EventID, settlement.Outcome = res.EventID, res.Outcome
	}
	if processErr != nil {
		settlement.FailureCode = "route_failed"
		settlement.Terminal = d.Failures+1 >= deliveryMaxFailures || errors.Is(processErr, errInvalidInboxPayload)
		if errors.Is(processErr, errInvalidInboxPayload) {
			settlement.FailureCode = "invalid_payload"
		}
		settlement.RetryAt = settlement.Now.Add(time.Second << min(d.Failures, 10))
	}
	finishCtx, finish := context.WithTimeout(ctx, recordTimeout)
	defer finish()
	settled, err := r.store.SettleTriggerDelivery(finishCtx, d.ID, d.LeaseToken, settlement)
	if err != nil {
		return true, err
	}
	if !settled {
		return true, errInboxLeaseLost
	}
	if settlement.Terminal {
		observability.TriggerDeliveryOutcomes.WithLabelValues(d.Source, "dead_letter").Inc()
		logging.ErrorContext(ctx, "trigger inbox delivery dead-lettered", "receipt_id", d.ID, "source", d.Source, "provider", d.Provider,
			"attempt", d.Failures+1, "failure_code", settlement.FailureCode)
	} else if processErr != nil {
		observability.TriggerDeliveryOutcomes.WithLabelValues(d.Source, "retry").Inc()
		logging.WarnContext(ctx, "trigger inbox delivery will retry", "receipt_id", d.ID, "source", d.Source,
			"provider", d.Provider, "attempt", d.Failures+1, "failure_code", settlement.FailureCode)
	} else {
		observability.TriggerDeliveryOutcomes.WithLabelValues(d.Source, "completed").Inc()
	}
	return true, nil
}

func (r *Relay) process(ctx context.Context, d core.TriggerDelivery) (*triggers.AcceptResult, error) {
	switch d.Source {
	case core.TriggerDeliveryIntegration:
		return nil, r.routeIntegration(ctx, d)
	case core.TriggerDeliveryWebhook, core.TriggerDeliveryPoll:
		readCtx, cancel := context.WithTimeout(ctx, recordTimeout)
		defer cancel()
		return r.routeBound(readCtx, d)
	default:
		return nil, errInvalidInboxPayload
	}
}

func (r *Relay) routeIntegration(ctx context.Context, d core.TriggerDelivery) error {
	var body integrationDelivery
	if err := json.Unmarshal(d.Payload, &body); err != nil {
		return errInvalidInboxPayload
	}
	var verified []*core.IntegrationTriggerRoute
	if body.Verified != nil {
		// Connection-signed deliveries may never widen from the authenticated
		// set, even if there are no surviving live routes.
		verified = make([]*core.IntegrationTriggerRoute, 0, len(body.Verified))
		allowed := make(map[verifiedRoute]struct{}, len(body.Verified))
		for _, route := range body.Verified {
			allowed[route] = struct{}{}
		}
		readCtx, cancel := context.WithTimeout(ctx, recordTimeout)
		routes, err := r.store.ListIntegrationTriggers(readCtx, d.Provider)
		cancel()
		if err != nil {
			return err
		}
		for _, rt := range routes {
			t := rt.Trigger
			if t == nil || t.ConnectionID == nil || rt.ConnectionAccount != d.ScopeKey {
				continue
			}
			if _, ok := allowed[verifiedRoute{t.ID, t.UserID, *t.ConnectionID}]; ok {
				verified = append(verified, rt)
			}
		}
	}
	return r.router.route(ctx, d.Provider, body.Events, verified, &routeAttempt{
		receivedAt: d.ReceivedAt,
		beforeTrigger: func(ctx context.Context) error {
			now := r.now().UTC()
			won, err := r.store.RenewTriggerDelivery(ctx, d.ID, d.LeaseToken, now, now.Add(deliveryLease))
			if err != nil {
				return err
			}
			if !won {
				return errInboxLeaseLost
			}
			return nil
		},
	})
}

func (r *Relay) routeBound(ctx context.Context, d core.TriggerDelivery) (*triggers.AcceptResult, error) {
	var body boundDelivery
	if err := json.Unmarshal(d.Payload, &body); err != nil || body.OwnerID == "" || body.Event.Sender == nil {
		return nil, errInvalidInboxPayload
	}
	t, err := r.store.GetTrigger(ctx, d.ScopeKey)
	if errors.Is(err, core.ErrTriggerNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if t == nil || !t.Enabled || t.UserID != body.OwnerID || t.CreatedAt.After(d.ReceivedAt) {
		return nil, nil
	}
	if d.Source == core.TriggerDeliveryWebhook {
		if t.Kind != core.TriggerKindWebhook {
			return nil, nil
		}
		return acceptBoundDelivery(ctx, r.router.opts.Intake, t, d)
	}
	if t.Kind != core.TriggerKindIntegration || t.ConnectionID == nil || *t.ConnectionID != body.ConnectionID || r.connections == nil {
		return nil, nil
	}
	cfg, err := triggers.IntegrationConfigFor(t)
	if err != nil || cfg.Integration != d.Provider || !eventInScope(t, cfg, Event{Type: body.EventType, Attributes: body.Attributes}) {
		return nil, nil
	}
	conn, err := r.connections.GetConnection(ctx, t.UserID, body.ConnectionID)
	if svcerr.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if conn == nil || conn.Status != core.ConnectionStatusActive || conn.IntegrationID != d.Provider || conn.UserID != t.UserID {
		return nil, nil
	}
	account, _ := body.Event.Payload["account"].(string)
	if (conn.ExternalAccountID == nil && account != "") || (conn.ExternalAccountID != nil && *conn.ExternalAccountID != account) {
		return nil, nil
	}
	if ok, err := r.router.policyAdmits(ctx, d.Provider, t); !ok {
		return nil, err
	}
	return r.router.opts.Intake.Accept(ctx, t, body.Event, triggers.AcceptOptions{})
}
