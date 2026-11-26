// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/store"
)

var (
	itemKinds    = []ItemKind{ItemBlogPost, ItemSocialPost, ItemEmail, ItemLaunch, ItemOther}
	itemStatuses = []ItemStatus{ItemDraft, ItemReview, ItemApproved, ItemScheduled, ItemSent, ItemPublished, ItemPaused}
)

const (
	maxChannelLen      = 100
	maxEmailSubjectLen = 500
	maxApprovalHashLen = 200
	maxGmailIDLen      = 200
)

// agentEditable is an item an agent may edit whole: one no person has
// approved.
func agentEditable(st ItemStatus) bool { return st == ItemDraft || st == ItemReview }

// ListItems lists the org's items; a scheduled range makes it the calendar.
func (s *service) ListItems(ctx context.Context, actor Actor, in ListItemsInput) (ListItemsResult, error) {
	if err := s.admit(ctx, actor); err != nil {
		return ListItemsResult{}, err
	}
	limit, offset, err := page(in.PageSize, in.PageToken, DefaultItemPageSize, MaxItemPageSize)
	if err != nil {
		return ListItemsResult{}, err
	}
	if in.Kind != "" && !slices.Contains(itemKinds, in.Kind) {
		return ListItemsResult{}, invalid("kind", "unknown kind")
	}
	if in.Status != "" && !slices.Contains(itemStatuses, in.Status) {
		return ListItemsResult{}, invalid("status", "unknown status")
	}
	from, to := utcPtr(in.ScheduledFrom), utcPtr(in.ScheduledTo)
	if from != nil && to != nil && !from.Before(*to) {
		return ListItemsResult{}, invalid("scheduled_to", "must be after scheduled_from")
	}
	rows, err := s.store.ListItems(ctx, ItemFilter{
		OrgID: actor.OrgID, CampaignID: in.CampaignID, Kind: in.Kind, Status: in.Status,
		ScheduledFrom: from, ScheduledTo: to, Limit: limit, Offset: offset,
	})
	if err != nil {
		return ListItemsResult{}, err
	}
	rows, next := nextToken(rows, limit, offset)
	return ListItemsResult{Items: itemsFromRows(rows), NextPageToken: next}, nil
}

// GetItem returns one of the org's items.
func (s *service) GetItem(ctx context.Context, actor Actor, in GetItemInput) (Item, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Item{}, err
	}
	if err := checkID("id", in.ID); err != nil {
		return Item{}, err
	}
	row, err := s.store.GetItem(ctx, actor.OrgID, in.ID)
	if err != nil {
		return Item{}, readErr(err, "item")
	}
	return itemFromRow(row), nil
}

// CreateItem creates a DRAFT item.
func (s *service) CreateItem(ctx context.Context, actor Actor, in CreateItemInput) (Item, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Item{}, err
	}
	if !slices.Contains(itemKinds, in.Kind) {
		return Item{}, invalid("kind", "required")
	}
	if err := firstErr(
		checkLen("title", in.Title, 1, MaxTitleLen),
		checkLen("body_md", in.BodyMD, 0, MaxBodyLen),
		checkLen("channel", in.Channel, 0, maxChannelLen),
		checkLen("email_subject", in.EmailSubject, 0, maxEmailSubjectLen),
		checkRecipients(in.EmailTo),
		checkEmailFields(in.Kind, in.EmailTo, in.EmailSubject),
	); err != nil {
		return Item{}, err
	}
	now := s.now()
	var out Item
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		if err := firstErr(
			s.checkCampaignLink(ctx, actor.OrgID, in.CampaignID),
			s.checkIdeaLink(ctx, actor.OrgID, in.IdeaID),
		); err != nil {
			return err
		}
		row := &store.GtmItem{
			OrgID:        actor.OrgID,
			CampaignID:   strPtrOrNil(in.CampaignID),
			IdeaID:       strPtrOrNil(in.IdeaID),
			Kind:         string(in.Kind),
			Channel:      in.Channel,
			Title:        in.Title,
			BodyMd:       in.BodyMD,
			Status:       string(ItemDraft),
			ScheduledAt:  utcPtr(in.ScheduledAt),
			EmailTo:      slices.Clone(in.EmailTo),
			EmailSubject: in.EmailSubject,
			Revision:     1,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := s.store.CreateItem(ctx, row); err != nil {
			return err
		}
		out = itemFromRow(row)
		return nil
	})
	return out, err
}

// UpdateItem changes an item's fields and moves its status along the
// lifecycle (checkTransition). An agent touches DRAFT and REVIEW items only.
// A content change to an APPROVED or SCHEDULED item drops it to REVIEW; a
// date alone does not, so a person may date an APPROVED email and it stays
// APPROVED. A SENT item is final.
func (s *service) UpdateItem(ctx context.Context, actor Actor, in UpdateItemInput) (Item, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Item{}, err
	}
	if err := firstErr(checkID("id", in.ID), checkRevision(in.ExpectedRevision), checkItemUpdate(in)); err != nil {
		return Item{}, err
	}
	if in.Status != nil && !actor.Person() {
		switch *in.Status {
		case ItemDraft, ItemReview:
		case ItemApproved:
			return Item{}, personOnly("approve an item")
		default:
			return Item{}, personOnly("set an item's status to " + string(*in.Status))
		}
	}
	var out Item
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		row, err := s.store.GetItem(ctx, actor.OrgID, in.ID)
		if err != nil {
			return readErr(err, "item")
		}
		cur := ItemStatus(row.Status)
		if cur == ItemSent {
			return ErrSentIsFinal
		}
		if row.Revision != in.ExpectedRevision {
			return conflict(in.ID, in.ExpectedRevision, row.Revision)
		}
		if !actor.Person() && !agentEditable(cur) {
			return personOnly("change an item that is " + statusWord(cur))
		}
		before := itemContent(row)
		if err := s.applyItemUpdate(ctx, actor.OrgID, row, in); err != nil {
			return err
		}
		// Changed content needs a new review: an approved (or scheduled)
		// item drops back to REVIEW, and any status asked for moves from there.
		from := cur
		if itemContent(row) != before && (cur == ItemApproved || cur == ItemScheduled) {
			from = ItemReview
			row.Status = string(ItemReview)
		}
		if in.Status != nil && *in.Status != from {
			if err := checkTransition(actor.Person(), ItemKind(row.Kind), from, *in.Status); err != nil {
				return err
			}
			row.Status = string(*in.Status)
		}
		if ItemKind(row.Kind) == ItemEmail && (ItemStatus(row.Status) == ItemScheduled || ItemStatus(row.Status) == ItemPublished) {
			return &TransitionError{From: cur, To: ItemStatus(row.Status), Reason: "an email is never scheduled or published; it stays APPROVED until it is sent"}
		}
		// A person's edit releases a send hold (SendItemEmail): having checked
		// the sent mail, they re-approve, and the new revision needs a new
		// approval card anyway. An agent's edit never does.
		if actor.Person() {
			row.ApprovalHash = ""
		}
		if err := checkEmailFields(ItemKind(row.Kind), row.EmailTo, row.EmailSubject); err != nil {
			return err
		}
		if ItemStatus(row.Status) == ItemScheduled && row.ScheduledAt == nil {
			return invalid("scheduled_at", "a scheduled item needs a date")
		}
		out, err = s.writeItem(ctx, actor.OrgID, row, in.ExpectedRevision)
		return err
	})
	return out, err
}

func checkItemUpdate(in UpdateItemInput) error {
	if in.Kind != nil && !slices.Contains(itemKinds, *in.Kind) {
		return invalid("kind", "unknown kind")
	}
	if in.Status != nil {
		if !slices.Contains(itemStatuses, *in.Status) {
			return invalid("status", "unknown status")
		}
		if *in.Status == ItemSent {
			return invalid("status", "SENT is written only when the approved email is sent")
		}
	}
	checks := []error{}
	if in.Title != nil {
		checks = append(checks, checkLen("title", *in.Title, 1, MaxTitleLen))
	}
	if in.BodyMD != nil {
		checks = append(checks, checkLen("body_md", *in.BodyMD, 0, MaxBodyLen))
	}
	if in.Channel != nil {
		checks = append(checks, checkLen("channel", *in.Channel, 0, maxChannelLen))
	}
	if in.EmailSubject != nil {
		checks = append(checks, checkLen("email_subject", *in.EmailSubject, 0, maxEmailSubjectLen))
	}
	if in.EmailTo != nil {
		checks = append(checks, checkRecipients(*in.EmailTo))
	}
	return firstErr(checks...)
}

// itemContent is what an approval approves: the words and where they go.
// Links and the date are not content.
func itemContent(r *store.GtmItem) string {
	return strings.Join([]string{r.Kind, r.Title, r.BodyMd, r.Channel, r.EmailSubject, strings.Join(r.EmailTo, "\x00")}, "\x01")
}

func statusWord(st ItemStatus) string {
	return strings.ToLower(strings.TrimPrefix(string(st), "GTM_ITEM_STATUS_"))
}

// checkTransition is the item lifecycle. An agent moves between DRAFT and
// REVIEW only. A person also approves a DRAFT or REVIEW item; schedules an
// APPROVED one; marks an APPROVED or SCHEDULED one PUBLISHED (a manual
// marker: GTM never publishes); pauses anything; and returns a PAUSED item
// to DRAFT. An email is never SCHEDULED or PUBLISHED, and SENT is never
// written here.
func checkTransition(person bool, kind ItemKind, from, to ItemStatus) error {
	draftish := from == ItemDraft || from == ItemReview
	if draftish && (to == ItemDraft || to == ItemReview) {
		return nil
	}
	if !person {
		if to == ItemApproved {
			return personOnly("approve an item")
		}
		return personOnly("set an item's status to " + statusWord(to))
	}
	email := kind == ItemEmail
	switch {
	case to == ItemApproved && draftish:
		return nil
	case to == ItemScheduled && from == ItemApproved && !email:
		return nil
	case to == ItemPublished && (from == ItemApproved || from == ItemScheduled) && !email:
		return nil
	case to == ItemPaused:
		return nil
	case to == ItemDraft && from == ItemPaused:
		return nil
	}
	reason := ""
	if email && (to == ItemScheduled || to == ItemPublished) {
		reason = "an email is never scheduled or published; it stays APPROVED until it is sent"
	}
	return &TransitionError{From: from, To: to, Reason: reason}
}

func (s *service) applyItemUpdate(ctx context.Context, orgID string, row *store.GtmItem, in UpdateItemInput) error {
	if in.Kind != nil {
		row.Kind = string(*in.Kind)
	}
	if in.Title != nil {
		row.Title = *in.Title
	}
	if in.BodyMD != nil {
		row.BodyMd = *in.BodyMD
	}
	if in.Channel != nil {
		row.Channel = *in.Channel
	}
	if in.CampaignID != nil {
		if err := s.checkCampaignLink(ctx, orgID, *in.CampaignID); err != nil {
			return err
		}
		row.CampaignID = strPtrOrNil(*in.CampaignID)
	}
	if in.IdeaID != nil {
		if err := s.checkIdeaLink(ctx, orgID, *in.IdeaID); err != nil {
			return err
		}
		row.IdeaID = strPtrOrNil(*in.IdeaID)
	}
	switch {
	case in.ClearScheduledAt:
		row.ScheduledAt = nil
	case in.ScheduledAt != nil:
		row.ScheduledAt = utcPtr(in.ScheduledAt)
	}
	if in.EmailTo != nil {
		row.EmailTo = slices.Clone(*in.EmailTo)
	}
	if in.EmailSubject != nil {
		row.EmailSubject = *in.EmailSubject
	}
	return nil
}

// checkEmailFields keeps recipients and a subject on emails only.
func checkEmailFields(kind ItemKind, to []string, subject string) error {
	if kind != ItemEmail && (len(to) > 0 || subject != "") {
		return invalid("email_to", "only an email item has recipients and a subject")
	}
	return nil
}

// MarkItemSent records the send of an APPROVED email at exactly the revision
// named. It is the only writer of SENT.
func (s *service) MarkItemSent(ctx context.Context, actor Actor, in MarkItemSentInput) (Item, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Item{}, err
	}
	if err := checkSendRecord(in); err != nil {
		return Item{}, err
	}
	var out Item
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		row, err := s.store.GetItem(ctx, actor.OrgID, in.ItemID)
		if err != nil {
			return readErr(err, "item")
		}
		out, err = s.markSent(ctx, actor, row, in)
		return err
	})
	return out, err
}

func checkSendRecord(in MarkItemSentInput) error {
	return firstErr(
		checkID("item_id", in.ItemID),
		checkRevision(in.ExpectedRevision),
		checkLen("gmail_message_id", in.GmailMessageID, 1, maxGmailIDLen),
		checkLen("approval_hash", in.ApprovalHash, 1, maxApprovalHashLen),
	)
}

// sendable is the precondition of a send and of its record: an APPROVED
// email, never sent, at exactly the revision approved. SENT is checked
// first, so a repeat is ErrAlreadySent whatever revision it names.
func sendable(row *store.GtmItem, expected int32) error {
	if ItemStatus(row.Status) == ItemSent {
		return ErrAlreadySent
	}
	if row.Revision != expected {
		return conflict(row.ID, expected, row.Revision)
	}
	if ItemKind(row.Kind) != ItemEmail || ItemStatus(row.Status) != ItemApproved {
		return ErrNotApproved
	}
	return nil
}

// markSent writes row's send record, in the caller's transaction.
func (s *service) markSent(ctx context.Context, actor Actor, row *store.GtmItem, in MarkItemSentInput) (Item, error) {
	if err := sendable(row, in.ExpectedRevision); err != nil {
		return Item{}, err
	}
	// A hold left by another send attempt, under another card, is not ours
	// to record over.
	if row.ApprovalHash != "" && row.ApprovalHash != in.ApprovalHash {
		return Item{}, ErrSendInDoubt
	}
	now := s.now()
	row.Status = string(ItemSent)
	row.GmailMessageID = in.GmailMessageID
	row.SentAt = &now
	row.SentBy = actor.UserID
	row.ApprovalHash = in.ApprovalHash
	return s.writeItem(ctx, actor.OrgID, row, in.ExpectedRevision)
}

// EmailSendTimeout bounds one Email.Send. A send that times out is in doubt
// (it may have left), so the item stays held.
const EmailSendTimeout = 60 * time.Second

// SendItemEmail sends an APPROVED email item through Email and records it,
// in three steps, so that no failure can lead to a second email:
//
//  1. Hold: in one transaction, check the item is sendable and has no hold,
//     and write the card's hash to approval_hash (revision unchanged). The
//     hash on an unsent item is the hold: a concurrent or later send of the
//     item finds it and is ErrSendInDoubt, so at most one Send ever runs.
//  2. Send, bounded by EmailSendTimeout, outside any transaction (a
//     serializable retry must never re-send). An error that wraps
//     ErrEmailNotSent means nothing left, and the hold is released; any
//     other error, a timeout included, may have sent, so the hold stays.
//  3. Record SENT in a second transaction, detached from ctx's cancellation
//     so a caller that gives up after the send still gets it recorded. If
//     that fails the error is a *SentUnrecordedError naming the Gmail
//     message: never retried, and the hold keeps the item from a resend.
//
// The remaining window: a crash or record failure after Gmail accepted the
// message leaves the item APPROVED with a hold and no SENT record. Nothing
// resends it; a person resolves it by checking the sent mail and either
// marking it (MarkItemSent with the same hash) or editing / re-approving it
// (a person's UpdateItem clears the hold), which takes a new approval card.
func (s *service) SendItemEmail(ctx context.Context, actor Actor, in SendItemEmailInput) (Item, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Item{}, err
	}
	if s.email == nil {
		return Item{}, ErrEmailUnavailable
	}
	if err := firstErr(
		checkID("item_id", in.ItemID),
		checkRevision(in.ExpectedRevision),
		checkLen("approval_hash", in.ApprovalHash, 1, maxApprovalHashLen),
		checkID("run.run_id", in.Run.RunID),
	); err != nil {
		return Item{}, err
	}
	org, id, rev, hash := actor.OrgID, in.ItemID, in.ExpectedRevision, in.ApprovalHash

	// 1. Hold.
	var msg EmailMessage
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		row, err := s.store.GetItem(ctx, org, id)
		if err != nil {
			return readErr(err, "item")
		}
		if err := sendable(row, rev); err != nil {
			return err
		}
		if row.ApprovalHash != "" {
			return ErrSendInDoubt
		}
		if len(row.EmailTo) == 0 {
			return invalid("email_to", "an email needs recipients")
		}
		if row.EmailSubject == "" {
			return invalid("email_subject", "an email needs a subject")
		}
		msg = EmailMessage{To: slices.Clone(row.EmailTo), Subject: row.EmailSubject, BodyMD: row.BodyMd}
		if err := s.store.SetItemSendAttempt(ctx, org, id, rev, "", hash); err != nil {
			if errors.Is(err, ErrRevisionConflict) {
				return ErrSendInDoubt
			}
			return err
		}
		return nil
	})
	if err != nil {
		return Item{}, err
	}

	// 2. Send.
	sendCtx, cancel := context.WithTimeout(ctx, EmailSendTimeout)
	sentID, err := s.email.Send(sendCtx, in.Run, msg)
	cancel()
	detached := context.WithoutCancel(ctx)
	if err != nil {
		if errors.Is(err, ErrEmailNotSent) {
			// Best effort: a hold that stays only costs a re-approval.
			_ = s.store.InTx(detached, func(ctx context.Context) error {
				return s.store.SetItemSendAttempt(ctx, org, id, rev, hash, "")
			})
			return Item{}, fmt.Errorf("send email: %w", err)
		}
		return Item{}, svcerr.WithCause(ErrSendInDoubt, fmt.Errorf("send email: %w", err))
	}
	if sentID == "" {
		return Item{}, svcerr.WithCause(ErrSendInDoubt, errors.New("the email client returned no message id"))
	}

	// 3. Record.
	var out Item
	err = s.store.InTx(detached, func(ctx context.Context) error {
		row, err := s.store.GetItem(ctx, org, id)
		if err != nil {
			return readErr(err, "item")
		}
		out, err = s.markSent(ctx, actor, row, MarkItemSentInput{
			ItemID: id, ExpectedRevision: rev, GmailMessageID: sentID, ApprovalHash: hash,
		})
		return err
	})
	if err != nil {
		return Item{}, svcerr.WithCause(&SentUnrecordedError{ItemID: id, GmailMessageID: sentID}, err)
	}
	return out, nil
}

// writeItem writes row at expected and reads back what was stored.
func (s *service) writeItem(ctx context.Context, orgID string, row *store.GtmItem, expected int32) (Item, error) {
	row.UpdatedAt = s.now()
	if err := s.store.UpdateItem(ctx, row, expected); err != nil {
		return Item{}, s.writeErr(err, row.ID, expected, func() (int32, error) {
			cur, err := s.store.GetItem(ctx, orgID, row.ID)
			if err != nil {
				return 0, err
			}
			return cur.Revision, nil
		})
	}
	stored, err := s.store.GetItem(ctx, orgID, row.ID)
	if err != nil {
		return Item{}, readErr(err, "item")
	}
	return itemFromRow(stored), nil
}
