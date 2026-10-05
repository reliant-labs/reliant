// Copyright (c) 2025 Reliant Labs

// Package webhook receives inbound trigger events on the api-server.
//
// Two receivers, one job each:
//
//	POST <PUBLIC_URL>/hooks/{trigger_id}[/{token}]   a trigger's own URL
//	POST <PUBLIC_URL>/integrations/{provider}/events a provider's app-level hook
//
// A receiver verifies the request, turns it into events, and hands each to
// the trigger layer (triggers.Intake), which records it and starts its fire.
// It never launches anything itself and returns as soon as the rows are
// written: GitHub drops a delivery not acked in 10 seconds (and never retries
// it on its own), Slack wants an ack in 3.
//
// Providers plug into the app-level route through a Registry. A provider is
// pure translation — verify a signature, parse a body into events — with no
// database or trigger knowledge, so adding one is a file plus a Register call.
// See Provider. Every payload is untrusted: bodies are size-capped and
// secret-bearing headers never reach the stored event.
package webhook

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Provider is an integration that delivers events through its app-level
// webhook: POST <PUBLIC_URL>/integrations/{ID()}/events.
//
// The receiver calls Verify, then Parse, both synchronously while the sender
// waits, so neither may make a network call. Routing to triggers is the
// receiver's: a provider reports, for each event, which ACCOUNT it belongs to
// (the installation, team or account a connection records as its
// external_account_id) and leaves the question of whose triggers that reaches
// to the receiver.
type Provider interface {
	// ID is the integration id, the {provider} path segment, and what an
	// IntegrationSource names.
	ID() string

	// Verify authenticates the request: a signature over the body, a
	// timestamp window, whatever the provider uses. Return an error wrapping
	// ErrUnauthorized for a request that is not genuinely from the provider
	// (the receiver answers 401); any other error is a 500.
	//
	// Verify runs before Parse and before anything is written. A handshake
	// (Slack's url_verification) is signed like any delivery and is verified
	// here too.
	Verify(ctx context.Context, req *Request) error

	// Parse turns a verified request into either a synchronous reply
	// (Delivery.Respond: a handshake, a ping) or the events it carries
	// (Delivery.Events). A delivery the provider sends but no trigger can
	// listen to (a bot's own message, an event type with no trigger) may
	// return no events; the receiver acks it.
	Parse(ctx context.Context, req *Request) (*Delivery, error)
}

// Request is an inbound delivery as a provider sees it.
type Request struct {
	// PublicURL is the URL the sender addressed — PUBLIC_URL plus the path
	// and raw query — rebuilt from configuration, not from the request's own
	// Host and scheme, which the ingress rewrote. Providers that sign the
	// URL (Twilio) verify against this.
	PublicURL *url.URL
	Method    string
	Header    http.Header
	// Body is the raw body, size-capped. Signatures are over these bytes.
	Body []byte
	// Form is the parsed body of an application/x-www-form-urlencoded
	// request, nil otherwise.
	Form url.Values
	// ReceivedAt is when the receiver read the request, for timestamp
	// windows.
	ReceivedAt time.Time
}

// Delivery is what Parse found in a request.
type Delivery struct {
	// Respond, when set, is written back to the sender and no events are
	// recorded: the request was a handshake, not an event.
	Respond *Response
	// Events are the events the request carries; usually one.
	Events []Event
	// Revocations are access the provider says has ended (a repository
	// removed from an installation, a member removed from an org). The
	// receiver applies them before routing Events, so an event in the same
	// delivery is already routed under the reduced access. Each must name an
	// account or a subject; an unscoped one is ignored.
	Revocations []core.IntegrationAccessRevocation
}

// Response is a synchronous reply to the sender.
type Response struct {
	Status      int // zero means 200
	ContentType string
	Body        []byte
}

// Event is one provider event, before routing.
type Event struct {
	// Type is what an IntegrationSource's events match: "issues.opened",
	// "app_mention", "message.received". Use "<type>.<action>" where the
	// provider has actions, so "issues.*" can select a family.
	Type string
	// AccountKey is the provider account the event belongs to. It must
	// not be empty.
	//
	// Without a ResourceKey it must equal the external_account_id that
	// provider's connections record, and the event only reaches triggers
	// whose connection records exactly it.
	AccountKey string
	// ResourceKey, when set, makes routing ACCESS-GATED: the event is about
	// one resource inside the account (a GitHub repository in an App
	// installation), and it reaches only triggers whose owner holds a fresh
	// access grant for (AccountKey, ResourceKey) — recorded from the owner's
	// own provider credential (core.IntegrationAccessGrant). Use it whenever
	// one account is shared by users who do not all see the same resources,
	// which is what an org-wide installation is. The connection-account path
	// is not consulted for such an event.
	ResourceKey string
	// DeliveryID is the provider's id for this event, stable across its
	// redeliveries (X-GitHub-Delivery, Slack event_id, Twilio MessageSid).
	// It is the dedupe key, so a redelivery never fires twice.
	DeliveryID string
	// OccurredAt is when the provider says it happened; zero means now.
	OccurredAt time.Time
	// Attributes are the routing facts an IntegrationSource.match compares
	// with equality: repository, channel, to. Keep them small and stable.
	Attributes map[string]string
	// Data is the provider's payload, untrusted, recorded on the event and
	// visible as trigger.payload.data.
	Data map[string]any
}

// ErrUnauthorized marks a request that failed verification.
var ErrUnauthorized = errors.New("webhook: unauthorized")
