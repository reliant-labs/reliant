// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// EventsStore is what the app-level receiver reads. *db.Repo satisfies it.
type EventsStore interface {
	// ListIntegrationTriggers lists every enabled integration trigger for
	// one integration whose connection is its own owner's.
	ListIntegrationTriggers(ctx context.Context, integration string) ([]*core.IntegrationTriggerRoute, error)
	// ListAccessRoutedTriggers lists the enabled integration triggers whose
	// owner holds an access grant for (account, resource) refreshed at or
	// after freshAfter.
	ListAccessRoutedTriggers(ctx context.Context, integration, account, resource string, freshAfter time.Time) ([]*core.Trigger, error)
	// RevokeIntegrationAccess deletes the grants a revocation names.
	RevokeIntegrationAccess(ctx context.Context, integration string, rev core.IntegrationAccessRevocation) (int64, error)
}

// AccessFreshness is how recently an access grant must have been confirmed
// for an access-gated event to route through it. Grants are refreshed every
// accessRefreshInterval while their owner has a trigger, so a healthy grant
// is never near this; one older than it means refreshing has been failing,
// and access nobody has re-confirmed in that long is not trusted.
const AccessFreshness = time.Hour

// EventsOptions configures the app-level receiver.
type EventsOptions struct {
	Store    EventsStore
	Intake   Intake
	Registry *Registry
	// PublicURL is this server's externally reachable base (PUBLIC_URL).
	// Required: a provider signs the URL it was given, and without the base
	// there is nothing to verify against.
	PublicURL string
}

// EventsReceiver serves POST /integrations/{provider}/events.
type EventsReceiver struct {
	opts EventsOptions
	base *url.URL
	now  func() time.Time
}

// NewEventsReceiver builds the app-level receiver.
func NewEventsReceiver(opts EventsOptions) *EventsReceiver {
	return &EventsReceiver{opts: opts, base: parsePublicBase(opts.PublicURL), now: time.Now}
}

// Register mounts the route.
func (e *EventsReceiver) Register(handle func(pattern string, handler http.Handler)) {
	handle("POST /integrations/{provider}/events", http.HandlerFunc(e.serve))
}

func (e *EventsReceiver) serve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	providerID := r.PathValue("provider")
	provider, ok := e.opts.Registry.Provider(providerID)
	if !ok {
		writeStatus(w, http.StatusNotFound, "not found")
		return
	}
	if e.base == nil {
		writeStatus(w, http.StatusServiceUnavailable, "this server has no PUBLIC_URL, so provider deliveries cannot be verified")
		return
	}

	body, err := readBody(r)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeStatus(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("body exceeds %d bytes", MaxBodyBytes))
			return
		}
		writeStatus(w, http.StatusBadRequest, "could not read body")
		return
	}
	req := &Request{
		PublicURL:  publicURL(e.base, r),
		Method:     r.Method,
		Header:     r.Header.Clone(),
		Body:       body,
		ReceivedAt: e.now().UTC(),
	}
	if isForm(r.Header) {
		if form, err := url.ParseQuery(string(body)); err == nil {
			req.Form = form
		}
	}

	if err := provider.Verify(ctx, req); err != nil {
		if errors.Is(err, ErrUnauthorized) {
			logging.Warn("integration delivery failed verification", "provider", providerID, "error", err)
			writeStatus(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		logging.Error("integration delivery could not be verified", "provider", providerID, "error", err)
		writeStatus(w, http.StatusInternalServerError, "verification error")
		return
	}
	delivery, err := provider.Parse(ctx, req)
	if err != nil {
		logging.Warn("integration delivery could not be parsed", "provider", providerID, "error", err)
		writeStatus(w, http.StatusBadRequest, "could not parse delivery")
		return
	}
	if delivery == nil {
		delivery = &Delivery{}
	}
	if delivery.Respond != nil {
		writeResponse(w, delivery.Respond)
		return
	}

	// Revocations first: an event in the same delivery routes under the
	// reduced access. A failure here is a 503 like a routing failure — the
	// receiver must not ack a revocation it did not apply.
	if err := e.revoke(ctx, providerID, delivery.Revocations); err != nil {
		logging.Error("integration access revocation could not be applied", "provider", providerID, "error", err)
		writeStatus(w, http.StatusServiceUnavailable, "could not record the delivery; retry")
		return
	}
	if err := e.route(ctx, providerID, delivery.Events); err != nil {
		// A partial failure still recorded what it could. Asking the sender
		// to retry is right: the recorded half dedupes.
		logging.Error("integration delivery could not be fully recorded", "provider", providerID, "error", err)
		writeStatus(w, http.StatusServiceUnavailable, "could not record the delivery; retry")
		return
	}
	writeStatus(w, http.StatusOK, "ok")
}

// route hands each event to every trigger it reaches.
//
// An event reaches a trigger only when the trigger's connection records the
// event's account — the store has already dropped triggers whose connection
// is not their own owner's — the connection is active, and the source config
// matches the event's type and attributes. A trigger that passes is handed to
// the intake, which records it (and applies the owner's CEL filter). One
// delivery reaching many triggers is many events, each deduped on its own.
//
// An event with a ResourceKey is access-gated instead: it reaches only the
// triggers whose owner holds a fresh access grant for its (account,
// resource), and the connection-account routes are not consulted for it.
func (e *EventsReceiver) route(ctx context.Context, providerID string, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	var (
		routes       []*core.IntegrationTriggerRoute
		routesLoaded bool
		errs         []error
	)
	for _, ev := range events {
		if ev.AccountKey == "" || ev.DeliveryID == "" || ev.Type == "" {
			logging.Warn("integration event is missing its account, delivery id or type; dropped",
				"provider", providerID, "type", ev.Type)
			continue
		}
		if ev.ResourceKey != "" {
			errs = append(errs, e.routeByAccess(ctx, providerID, ev))
			continue
		}
		if !routesLoaded {
			var err error
			if routes, err = e.opts.Store.ListIntegrationTriggers(ctx, providerID); err != nil {
				return fmt.Errorf("list %s triggers: %w", providerID, err)
			}
			routesLoaded = true
		}
		for _, r := range routes {
			if !routeMatches(r, ev) {
				continue
			}
			if _, err := e.opts.Intake.Accept(ctx, r.Trigger, toInbound(providerID, r.Trigger, ev), triggers.AcceptOptions{}); err != nil {
				errs = append(errs, fmt.Errorf("trigger %s: %w", r.Trigger.ID, err))
			}
		}
	}
	return errors.Join(errs...)
}

// routeByAccess hands an access-gated event to every trigger whose owner can
// see its resource. The decision is made before any row is written, so the
// event never appears in the history of a user who cannot see it.
func (e *EventsReceiver) routeByAccess(ctx context.Context, providerID string, ev Event) error {
	matched, err := e.opts.Store.ListAccessRoutedTriggers(ctx, providerID, ev.AccountKey, ev.ResourceKey, e.now().Add(-AccessFreshness))
	if err != nil {
		return fmt.Errorf("list %s triggers for %s/%s: %w", providerID, ev.AccountKey, ev.ResourceKey, err)
	}
	var errs []error
	for _, t := range matched {
		cfg, err := triggers.IntegrationConfigFor(t)
		if err != nil || !t.Enabled || !triggers.IntegrationEventMatches(cfg, ev.Type, ev.Attributes) {
			continue
		}
		if _, err := e.opts.Intake.Accept(ctx, t, toInbound(providerID, t, ev), triggers.AcceptOptions{}); err != nil {
			errs = append(errs, fmt.Errorf("trigger %s: %w", t.ID, err))
		}
	}
	return errors.Join(errs...)
}

// revoke applies a delivery's access revocations. An unscoped one (no account
// and no subject) would delete every user's access, so it is dropped loudly.
func (e *EventsReceiver) revoke(ctx context.Context, providerID string, revs []core.IntegrationAccessRevocation) error {
	var errs []error
	for _, rev := range revs {
		if rev.AccountKey == "" && rev.SubjectID == "" {
			logging.Warn("integration access revocation names no account or subject; ignored", "provider", providerID)
			continue
		}
		n, err := e.opts.Store.RevokeIntegrationAccess(ctx, providerID, rev)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		logging.Info("integration access revoked", "provider", providerID,
			"account", rev.AccountKey, "resource", rev.ResourceKey, "subject", rev.SubjectID, "grants", n)
	}
	return errors.Join(errs...)
}

func routeMatches(r *core.IntegrationTriggerRoute, ev Event) bool {
	if r.Trigger == nil || !r.Trigger.Enabled || r.ConnectionStatus != core.ConnectionStatusActive {
		return false
	}
	// An account-less connection covers nothing: matching "" to "" would
	// route every event of the provider to it.
	if r.ConnectionAccount == "" || r.ConnectionAccount != ev.AccountKey {
		return false
	}
	cfg, err := triggers.IntegrationConfigFor(r.Trigger)
	if err != nil {
		return false
	}
	return triggers.IntegrationEventMatches(cfg, ev.Type, ev.Attributes)
}

func toInbound(providerID string, trigger *core.Trigger, ev Event) triggers.InboundEvent {
	attrs := make(map[string]any, len(ev.Attributes))
	for k, v := range ev.Attributes {
		attrs[k] = v
	}
	return triggers.InboundEvent{
		Kind:       core.TriggerEventKindIntegration,
		DedupeKey:  trigger.ID + ":" + ev.DeliveryID,
		OccurredAt: ev.OccurredAt,
		Payload:    inboundPayload(providerID, ev, attrs),
	}
}

// inboundPayload is trigger.payload for an integration event. Its shape is
// manifest.TriggerPayloadSchema's envelope; keep the two in step.
func inboundPayload(providerID string, ev Event, attrs map[string]any) map[string]any {
	payload := map[string]any{
		"integration": providerID,
		"event":       ev.Type,
		"account":     ev.AccountKey,
		"delivery_id": ev.DeliveryID,
		"attributes":  attrs,
		"data":        ev.Data,
	}
	if ev.ResourceKey != "" {
		payload["resource"] = ev.ResourceKey
	}
	return payload
}

func writeResponse(w http.ResponseWriter, resp *Response) {
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	contentType := resp.ContentType
	if contentType == "" {
		contentType = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(resp.Body)
}
