// Copyright (c) 2026 Reliant Labs

package email

import (
	"context"

	"github.com/reliant-labs/reliant/internal/gtm"
)

// The send_gtm_email tool (internal/llm/tools) cannot import this package's
// types (this package reaches internal/llm/tools through the Gmail
// integration's poller), so it calls Approver through these two methods,
// which take and return only gtm types and primitives. It holds them behind
// an interface of its own that *Approver satisfies.
//
// Errors are returned as Prepare and Execute return them, unwrapped: the
// tool wraps them (tools.RefuseActivation in PrepareAutomation).

// PrepareSend is Prepare for item itemID at expectedRevision: the card's
// title and hash, for the tool's ActivationPreview{Title, Hash}. The hash is
// opaque (CardHash); the runtime hands it back as the approved hash.
func (a *Approver) PrepareSend(ctx context.Context, actor gtm.Actor, itemID string, expectedRevision int32, unattended bool) (title, hash string, err error) {
	card, err := a.Prepare(ctx, actor, SendRequest{ItemID: itemID, ExpectedRevision: expectedRevision}, unattended)
	if err != nil {
		return "", "", err
	}
	return card.Title, card.Hash, nil
}

// SendApproved is Execute for item itemID at expectedRevision, from run.
func (a *Approver) SendApproved(ctx context.Context, actor gtm.Actor, itemID string, expectedRevision int32, run gtm.EmailRun) (gtm.Item, error) {
	return a.Execute(ctx, actor, SendRequest{ItemID: itemID, ExpectedRevision: expectedRevision}, run)
}
