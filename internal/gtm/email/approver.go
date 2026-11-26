// Copyright (c) 2026 Reliant Labs

package email

import (
	"context"
	"crypto/subtle"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/gtm"
)

// Refusals Prepare and Execute return besides the gtm service's own:
// gtm.ErrNotApproved, gtm.ErrAlreadySent, *gtm.SendInDoubtError,
// gtm.ErrChannelNotSendable, *gtm.RevisionConflictError, gtm.ErrNotFound,
// and from the send gtm.ErrApprovalMismatch, gtm.ErrEmailNotSent and
// *gtm.SentUnrecordedError. Every error names at most ids and revisions,
// never the item's recipients, subject or body, so its text may be shown to
// the agent and the owner (an ActivationRefusal reason).
var (
	// ErrUnattended is a send in a run nobody attends: it needs a card, and
	// such a run cannot show one (FailedPrecondition).
	ErrUnattended = svcerr.FailedPrecondition("sending a GTM email needs the owner's approval card, and an unattended run cannot show one; nothing was sent")

	// ErrNoApproval is an Execute with no approved card for its call
	// (PermissionDenied).
	ErrNoApproval = svcerr.PermissionDenied("sending a GTM email needs the owner's approval card for exactly this email, in an interactive chat; nothing was sent")

	// ErrCardStale is an approved card whose hash no longer matches the item
	// as stored: the email changed since the card was shown
	// (FailedPrecondition).
	ErrCardStale = svcerr.FailedPrecondition("the email changed since its approval card was shown; ask for a new card. Nothing was sent")
)

// Items is the part of gtm.Service the send path calls. gtm.Service
// satisfies it.
type Items interface {
	GetItem(ctx context.Context, actor gtm.Actor, in gtm.GetItemInput) (gtm.Item, error)
	GetChannel(ctx context.Context, actor gtm.Actor, in gtm.GetChannelInput) (gtm.Channel, error)
	SendItemEmail(ctx context.Context, actor gtm.Actor, in gtm.SendItemEmailInput) (gtm.Item, error)
}

// SendRequest is one send_gtm_email call: the item and the revision the
// agent read, which is the revision the owner is asked to approve.
type SendRequest struct {
	ItemID           string
	ExpectedRevision int32
	// ConnectionID is the Gmail connection the call names; "" (the usual
	// case) lets the item's channel decide, else the actor's default. One
	// that differs from the channel's is gtm.ErrChannelNotSendable.
	ConnectionID string
}

// Approver is the send_gtm_email tool's logic, behind the tool's
// AutomationApprovalTool hook (Prepare) and its execution (Execute).
type Approver struct {
	items Items
}

// NewApprover builds the approver over the GTM service.
func NewApprover(items Items) *Approver { return &Approver{items: items} }

// Prepare is the card for req, for PrepareAutomation: a send ALWAYS needs
// one when attended, and is refused (ErrUnattended) when unattended, before
// anything is read.
//
// It reads the item fresh as actor and refuses, as SendItemEmail would, so
// that no card is raised for an item that cannot be sent: unless it is an
// email-type item (EMAIL or COLD_OUTREACH), APPROVED by a person, never SENT,
// not held by an earlier send (*gtm.SendInDoubtError), at exactly
// req.ExpectedRevision, and its channel, when it has one, is an ACTIVE,
// INTEGRATED gmail channel whose connection req does not contradict
// (gtm.ErrChannelNotSendable). The card shows the stored recipients,
// subject and body, and its Hash is gtm.ApprovalHash of the item.
func (a *Approver) Prepare(ctx context.Context, actor gtm.Actor, req SendRequest, unattended bool) (Card, error) {
	if unattended {
		return Card{}, ErrUnattended
	}
	it, ch, err := a.read(ctx, actor, req)
	if err != nil {
		return Card{}, err
	}
	return BuildCard(it, ch)
}

// Execute sends req's item, for the tool's execution after the runtime
// asked for the card. Everything it trusts comes from the runtime, never the
// model: whether the run is unattended and the hash the owner approved for
// this call (automationpolicy.From(ctx), keyed by run.ToolCallID).
//
// It refuses an unattended run and a call with no approved card. Then it
// re-reads the item and applies Prepare's checks again (a SENT item is
// gtm.ErrAlreadySent, so a second send is refused), and refuses an item
// whose content no longer hashes to the approved card (ErrCardStale).
// Last it sends through gtm.Service.SendItemEmail with run (its
// ConnectionID set to req's), which checks all of it once more under the
// item's send hold, sends as actor and records SENT with the hash.
//
// Content cannot change without a new revision, so an edit after this read
// moves the revision and SendItemEmail refuses it. A send that fails with
// gtm.ErrEmailNotSent left nothing and releases the item: a new call (and
// its own card) may send it. Any other send failure leaves the item held
// (*gtm.SendInDoubtError) until a person resolves it.
func (a *Approver) Execute(ctx context.Context, actor gtm.Actor, req SendRequest, run gtm.EmailRun) (gtm.Item, error) {
	exec := automationpolicy.From(ctx)
	if exec.Unattended {
		return gtm.Item{}, ErrUnattended
	}
	if strings.TrimSpace(run.ToolCallID) == "" {
		return gtm.Item{}, ErrNoApproval
	}
	approved := exec.Approvals[run.ToolCallID]
	if approved == "" {
		return gtm.Item{}, ErrNoApproval
	}
	if run.ConnectionID != "" && run.ConnectionID != req.ConnectionID {
		return gtm.Item{}, gtm.ErrChannelNotSendable
	}
	run.ConnectionID = req.ConnectionID
	it, ch, err := a.read(ctx, actor, req)
	if err != nil {
		return gtm.Item{}, err
	}
	card, err := BuildCard(it, ch)
	if err != nil {
		return gtm.Item{}, err
	}
	if subtle.ConstantTimeCompare([]byte(card.Hash), []byte(approved)) != 1 {
		return gtm.Item{}, ErrCardStale
	}
	return a.items.SendItemEmail(ctx, actor, gtm.SendItemEmailInput{
		ItemID:           req.ItemID,
		ExpectedRevision: req.ExpectedRevision,
		ApprovalHash:     card.Hash,
		Run:              run,
	})
}

// read is the item and its channel (nil for none), checked sendable at
// req's revision.
func (a *Approver) read(ctx context.Context, actor gtm.Actor, req SendRequest) (gtm.Item, *gtm.Channel, error) {
	if a == nil || a.items == nil {
		return gtm.Item{}, nil, gtm.ErrEmailUnavailable
	}
	if strings.TrimSpace(req.ItemID) == "" {
		return gtm.Item{}, nil, &gtm.ValidationError{Field: "item_id", Reason: "required"}
	}
	if req.ExpectedRevision < 1 {
		return gtm.Item{}, nil, &gtm.ValidationError{Field: "expected_revision", Reason: "required"}
	}
	it, err := a.items.GetItem(ctx, actor, gtm.GetItemInput{ID: req.ItemID})
	if err != nil {
		return gtm.Item{}, nil, err
	}
	// The order is SendItemEmail's: a SENT item is refused as sent, and a
	// held one as in doubt, whatever revision was named.
	switch {
	case it.Status == gtm.ItemSent:
		return gtm.Item{}, nil, gtm.ErrAlreadySent
	case it.ApprovalHash != "":
		// An approval hash on an unsent item is SendItemEmail's hold: a send
		// was started and may have gone out. No card is raised for it until
		// a person resolves it. Whether it may still be running is the
		// service's call; this read only words the refusal.
		return gtm.Item{}, nil, &gtm.SendInDoubtError{ItemID: it.ID, MayBeInFlight: time.Since(it.UpdatedAt) < 2*gtm.EmailSendTimeout}
	case it.Revision != req.ExpectedRevision:
		return gtm.Item{}, nil, &gtm.RevisionConflictError{ID: it.ID, Expected: req.ExpectedRevision, Current: it.Revision}
	case !it.Format.EmailType() || it.Status != gtm.ItemApproved:
		return gtm.Item{}, nil, gtm.ErrNotApproved
	}
	if it.ChannelID == "" {
		return it, nil, nil
	}
	ch, err := a.items.GetChannel(ctx, actor, gtm.GetChannelInput{ID: it.ChannelID})
	if err != nil {
		return gtm.Item{}, nil, err
	}
	if ch.Status != gtm.ChannelActive || ch.Mode != gtm.ChannelIntegrated || ch.IntegrationKind != gtm.GmailIntegration {
		return gtm.Item{}, nil, gtm.ErrChannelNotSendable
	}
	if req.ConnectionID != "" && ch.ConnectionID != "" && req.ConnectionID != ch.ConnectionID {
		return gtm.Item{}, nil, gtm.ErrChannelNotSendable
	}
	return it, &ch, nil
}
