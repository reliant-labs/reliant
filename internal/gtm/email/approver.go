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

// Refusals Prepare and Execute return besides the gtm service's own (and
// ErrNoGmailAccount): gtm.ErrNotApproved, gtm.ErrAlreadySent, *gtm.SendInDoubtError,
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
	// as stored, or the account it would go out from: the email or its sender
	// changed since the card was shown (FailedPrecondition).
	ErrCardStale = svcerr.FailedPrecondition("the email or the account it goes out from changed since its approval card was shown; ask for a new card. Nothing was sent")
)

// Items is the part of gtm.Service the send path calls. gtm.Service
// satisfies it.
type Items interface {
	GetItem(ctx context.Context, actor gtm.Actor, in gtm.GetItemInput) (gtm.Item, error)
	GetChannel(ctx context.Context, actor gtm.Actor, in gtm.GetChannelInput) (gtm.Channel, error)
	SendItemEmail(ctx context.Context, actor gtm.Actor, in gtm.SendItemEmailInput) (gtm.Item, error)
}

// SendRequest is one send_gtm_email call: the item and the revision the
// agent read, which is the revision the owner is asked to approve. The call
// never names the mailbox: the item's channel does, or the actor's default.
type SendRequest struct {
	ItemID           string
	ExpectedRevision int32
}

// Approver is the send_gtm_email tool's logic, behind the tool's
// AutomationApprovalTool hook (Prepare) and its execution (Execute).
type Approver struct {
	items    Items
	accounts Accounts
}

// NewApprover builds the approver over the GTM service and the Gmail
// accounts the card names as the sender (NewConnectionAccounts). Without
// either, every call is refused.
func NewApprover(items Items, accounts Accounts) *Approver {
	return &Approver{items: items, accounts: accounts}
}

// Prepare is the card for req, for PrepareAutomation: a send ALWAYS needs
// one when attended, and is refused (ErrUnattended) when unattended, before
// anything is read.
//
// It reads the item fresh as actor and refuses, as SendItemEmail would, so
// that no card is raised for an item that cannot be sent: unless it is an
// email-type item (EMAIL or COLD_OUTREACH), APPROVED by a person, never SENT,
// not held by an earlier send (*gtm.SendInDoubtError), at exactly
// req.ExpectedRevision, and its channel, when it has one, is an ACTIVE,
// INTEGRATED gmail channel (gtm.ErrChannelNotSendable).
//
// The sender is resolved here and shown by address: the channel's pinned
// connection, else the actor's default Gmail connection. One whose address
// cannot be told is ErrNoGmailAccount, and no card is shown. The card shows
// the stored recipients, subject and body and the sender's address, and its
// Hash is CardHash of the item and the sender.
func (a *Approver) Prepare(ctx context.Context, actor gtm.Actor, req SendRequest, unattended bool) (Card, error) {
	if unattended {
		return Card{}, ErrUnattended
	}
	it, ch, from, err := a.read(ctx, actor, req)
	if err != nil {
		return Card{}, err
	}
	return BuildCard(it, ch, from)
}

// Execute sends req's item, for the tool's execution after the runtime
// asked for the card. Everything it trusts comes from the runtime, never the
// model: whether the run is unattended and the hash the owner approved for
// this call (automationpolicy.From(ctx), keyed by run.ToolCallID).
//
// It refuses an unattended run and a call with no approved card. Then it
// re-reads the item and applies Prepare's checks again (a SENT item is
// gtm.ErrAlreadySent, so a second send is refused), resolves the sender
// again, and refuses with ErrCardStale unless the approved hash is the
// CardHash of both as they are now: an email that changed, or a sender that
// did (another default connection, a reconnect to another Google account, a
// channel re-pointed to another connection), voids the card.
//
// Last it sends through gtm.Service.SendItemEmail with the item part of the
// APPROVED hash (not one recomputed here), so the service's own
// ErrApprovalMismatch check is a second, independent guard on the email;
// that checks it all once more under the item's send hold, sends as actor
// and records SENT with the item hash. The send is pinned to the account
// resolved and checked here: run.ConnectionID is set to it, and a run that
// names another is refused (gtm.ErrChannelNotSendable).
//
// The window left: a sender that changes after this resolution and before
// the send's own credential lookup (milliseconds) is not caught here; the
// pinned connection id then still decides the mailbox, so only a reconnect
// of that same connection to another account inside that window could.
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
	it, ch, from, err := a.read(ctx, actor, req)
	if err != nil {
		return gtm.Item{}, err
	}
	card, err := BuildCard(it, ch, from)
	if err != nil {
		return gtm.Item{}, err
	}
	itemHash, _, ok := splitCardHash(approved)
	if !ok || subtle.ConstantTimeCompare([]byte(card.Hash), []byte(approved)) != 1 {
		return gtm.Item{}, ErrCardStale
	}
	return a.send(ctx, actor, req, run, from, itemHash)
}

// send is Execute's last step: SendItemEmail under the approved card's item
// hash, pinned to from's connection.
func (a *Approver) send(ctx context.Context, actor gtm.Actor, req SendRequest, run gtm.EmailRun, from Account, itemHash string) (gtm.Item, error) {
	if run.ConnectionID != "" && run.ConnectionID != from.ConnectionID {
		return gtm.Item{}, gtm.ErrChannelNotSendable
	}
	run.ConnectionID = from.ConnectionID
	return a.items.SendItemEmail(ctx, actor, gtm.SendItemEmailInput{
		ItemID:           req.ItemID,
		ExpectedRevision: req.ExpectedRevision,
		ApprovalHash:     itemHash,
		Run:              run,
	})
}

// read is the item, its channel (nil for none) and the account it goes out
// from, checked sendable at req's revision.
func (a *Approver) read(ctx context.Context, actor gtm.Actor, req SendRequest) (gtm.Item, *gtm.Channel, Account, error) {
	if a == nil || a.items == nil || a.accounts == nil {
		return gtm.Item{}, nil, Account{}, gtm.ErrEmailUnavailable
	}
	if strings.TrimSpace(req.ItemID) == "" {
		return gtm.Item{}, nil, Account{}, &gtm.ValidationError{Field: "item_id", Reason: "required"}
	}
	if req.ExpectedRevision < 1 {
		return gtm.Item{}, nil, Account{}, &gtm.ValidationError{Field: "expected_revision", Reason: "required"}
	}
	it, err := a.items.GetItem(ctx, actor, gtm.GetItemInput{ID: req.ItemID})
	if err != nil {
		return gtm.Item{}, nil, Account{}, err
	}
	// The order is SendItemEmail's: a SENT item is refused as sent, and a
	// held one as in doubt, whatever revision was named.
	switch {
	case it.Status == gtm.ItemSent:
		return gtm.Item{}, nil, Account{}, gtm.ErrAlreadySent
	case it.ApprovalHash != "":
		// An approval hash on an unsent item is SendItemEmail's hold: a send
		// was started and may have gone out. No card is raised for it until
		// a person resolves it. Whether it may still be running is the
		// service's call; this read only words the refusal.
		return gtm.Item{}, nil, Account{}, &gtm.SendInDoubtError{ItemID: it.ID, MayBeInFlight: time.Since(it.UpdatedAt) < 2*gtm.EmailSendTimeout}
	case it.Revision != req.ExpectedRevision:
		return gtm.Item{}, nil, Account{}, &gtm.RevisionConflictError{ID: it.ID, Expected: req.ExpectedRevision, Current: it.Revision}
	case !it.Format.EmailType() || it.Status != gtm.ItemApproved:
		return gtm.Item{}, nil, Account{}, gtm.ErrNotApproved
	}
	var ch *gtm.Channel
	pinned := ""
	if it.ChannelID != "" {
		c, err := a.items.GetChannel(ctx, actor, gtm.GetChannelInput{ID: it.ChannelID})
		if err != nil {
			return gtm.Item{}, nil, Account{}, err
		}
		if c.Status != gtm.ChannelActive || c.Mode != gtm.ChannelIntegrated || c.IntegrationKind != gtm.GmailIntegration {
			return gtm.Item{}, nil, Account{}, gtm.ErrChannelNotSendable
		}
		ch, pinned = &c, c.ConnectionID
	}
	from, err := a.accounts.GmailAccount(ctx, actor, pinned)
	if err != nil {
		return gtm.Item{}, nil, Account{}, err
	}
	if from.ConnectionID == "" || (pinned != "" && from.ConnectionID != pinned) {
		return gtm.Item{}, nil, Account{}, ErrNoGmailAccount
	}
	return it, ch, from, nil
}
