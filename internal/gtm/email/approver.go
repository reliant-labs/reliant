// Copyright (c) 2026 Reliant Labs

package email

import (
	"context"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/gtm"
)

// Refusals Prepare and Execute return besides the gtm service's own
// (gtm.ErrNotApproved, gtm.ErrAlreadySent, gtm.ErrSendInDoubt,
// *gtm.RevisionConflictError, gtm.ErrNotFound, and from the send
// gtm.ErrEmailNotSent and *gtm.SentUnrecordedError). Every error either returns names at most the item's id
// and revisions, never its recipients, subject or body, so its text may be
// shown to the agent and the owner (an ActivationRefusal reason).
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
	SendItemEmail(ctx context.Context, actor gtm.Actor, in gtm.SendItemEmailInput) (gtm.Item, error)
}

// SendRequest is one send_gtm_email call: the item and the revision the
// agent read, which is the revision the owner is asked to approve.
type SendRequest struct {
	ItemID           string
	ExpectedRevision int32
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
// It reads the item fresh as actor and refuses unless it is an EMAIL item,
// APPROVED by a person, never SENT, at exactly req.ExpectedRevision, and not
// held by an earlier send whose outcome is in doubt (gtm.ErrSendInDoubt). The
// card shows the stored recipients, subject and body, and its Hash binds
// them with the item's id and revision (ApprovalHash).
func (a *Approver) Prepare(ctx context.Context, actor gtm.Actor, req SendRequest, unattended bool) (Card, error) {
	if unattended {
		return Card{}, ErrUnattended
	}
	it, err := a.read(ctx, actor, req)
	if err != nil {
		return Card{}, err
	}
	return BuildCard(it)
}

// Execute sends req's item, for the tool's execution after the runtime
// asked for the card. Everything it trusts comes from the runtime, never the
// model: whether the run is unattended and the hash the owner approved for
// this call (automationpolicy.From(ctx), keyed by run.ToolCallID).
//
// It refuses an unattended run, a call with no approved card, and, on a
// fresh read, an item that is not a sendable APPROVED email at
// req.ExpectedRevision (a SENT one is gtm.ErrAlreadySent, so a second send
// is refused), one held by an earlier send in doubt (gtm.ErrSendInDoubt), or
// one whose content no longer hashes to the approved card (ErrCardStale).
// Then it sends through gtm.Service.SendItemEmail, which holds the item
// under the card's hash (checking status, revision and hold again in that
// transaction), sends, and records SENT with the hash.
//
// The fresh read and the service's hold close the gap together: content
// cannot change without a new revision, so an edit after this read moves the
// revision and SendItemEmail refuses it. A send that fails with
// gtm.ErrEmailNotSent left nothing and releases the item: a new call (and
// its own card) may send it. Any other send failure leaves the item held (gtm.ErrSendInDoubt) until a
// person re-approves it.
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
	it, err := a.read(ctx, actor, req)
	if err != nil {
		return gtm.Item{}, err
	}
	card, err := BuildCard(it)
	if err != nil {
		return gtm.Item{}, err
	}
	if card.Hash != approved {
		return gtm.Item{}, ErrCardStale
	}
	return a.items.SendItemEmail(ctx, actor, gtm.SendItemEmailInput{
		ItemID:           req.ItemID,
		ExpectedRevision: req.ExpectedRevision,
		ApprovalHash:     card.Hash,
		Run:              run,
	})
}

// read is the item, checked sendable at req's revision.
func (a *Approver) read(ctx context.Context, actor gtm.Actor, req SendRequest) (gtm.Item, error) {
	if a == nil || a.items == nil {
		return gtm.Item{}, gtm.ErrEmailUnavailable
	}
	if strings.TrimSpace(req.ItemID) == "" {
		return gtm.Item{}, &gtm.ValidationError{Field: "item_id", Reason: "required"}
	}
	if req.ExpectedRevision < 1 {
		return gtm.Item{}, &gtm.ValidationError{Field: "expected_revision", Reason: "required"}
	}
	it, err := a.items.GetItem(ctx, actor, gtm.GetItemInput{ID: req.ItemID})
	if err != nil {
		return gtm.Item{}, err
	}
	// The order is the service's (sendable in items.go): a SENT item is
	// refused as sent whatever revision was named.
	switch {
	case it.Status == gtm.ItemSent:
		return gtm.Item{}, gtm.ErrAlreadySent
	case it.Revision != req.ExpectedRevision:
		return gtm.Item{}, &gtm.RevisionConflictError{ID: it.ID, Expected: req.ExpectedRevision, Current: it.Revision}
	case it.Kind != gtm.ItemEmail || it.Status != gtm.ItemApproved:
		return gtm.Item{}, gtm.ErrNotApproved
	case it.ApprovalHash != "":
		// An approval hash on an unsent item is SendItemEmail's hold: a send
		// was started and may have gone out. No card is raised for it until
		// a person checks and re-approves the item.
		return gtm.Item{}, gtm.ErrSendInDoubt
	}
	return it, nil
}
