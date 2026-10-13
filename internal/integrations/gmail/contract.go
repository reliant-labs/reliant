// Copyright (c) 2025 Reliant Labs

// Package gmail is the Go half of the Gmail integration
// (catalog/gmail/manifest.yaml): the go: executors for what a declarative
// request cannot express — assembling an RFC 2822 message for messages.send,
// decoding a message's MIME part tree for messages.get — and the poller that
// delivers the new-email trigger from users.history.list.
//
// Importing the package registers the executors in httpaction.Executors().
//
// forge:outbound-io
//
// The poller calls the Gmail API (users.getProfile, users.history.list,
// users.messages.get) at the manifest's base_url, as the trigger's connection,
// through the guarded runner's HTTP client.
//
//forge:not-a-component: built by reliant's hand-wired server (internal/integrations/webhook's poller registry, wired from internal/serverapi), not forge's composition root — until P3c (research/forge-migration/PLAN.md)
package gmail

import (
	"context"
	"net/http"

	"github.com/reliant-labs/reliant/internal/triggers"
)

// Service is Gmail's new-email trigger poller. It satisfies triggers.Poller.
type Service interface {
	// Poll returns the messages that arrived in the inbox after req.Cursor,
	// and the cursor to poll from next. An empty cursor is a baseline: the
	// mailbox's current position and no items.
	Poll(ctx context.Context, req triggers.PollRequest) (*triggers.PollResult, error)
}

// HTTPRunner supplies the guarded HTTP client every Gmail call goes through.
// httpaction's Runner satisfies it.
type HTTPRunner interface {
	Client() *http.Client
}

// Deps are the poller's collaborators.
type Deps struct {
	// BaseURL is the gmail manifest connection's base_url, the API root.
	BaseURL string
	// Runner supplies the guarded HTTP client.
	Runner HTTPRunner
}
