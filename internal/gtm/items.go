// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"slices"
	"strings"

	"github.com/reliant-labs/reliant/internal/store"
)

var (
	itemFormats  = []ItemFormat{FormatShortPost, FormatLongPost, FormatEmail, FormatColdOutreach, FormatVideo, FormatMediaPitch, FormatAd, FormatOther}
	itemStatuses = []ItemStatus{ItemDraft, ItemReview, ItemApproved, ItemScheduled, ItemSent, ItemPublished, ItemPaused}
)

const (
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
	if in.Format != "" && !slices.Contains(itemFormats, in.Format) {
		return ListItemsResult{}, invalid("format", "unknown format")
	}
	if in.Status != "" && !slices.Contains(itemStatuses, in.Status) {
		return ListItemsResult{}, invalid("status", "unknown status")
	}
	from, to := utcPtr(in.ScheduledFrom), utcPtr(in.ScheduledTo)
	if from != nil && to != nil && !from.Before(*to) {
		return ListItemsResult{}, invalid("scheduled_to", "must be after scheduled_from")
	}
	rows, err := s.store.ListItems(ctx, ItemFilter{
		OrgID: actor.OrgID, CampaignID: in.CampaignID, ChannelID: in.ChannelID, Format: in.Format, Status: in.Status,
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
		return Item{}, s.notFound(ctx, err, "item", in.ID)
	}
	return itemFromRow(row), nil
}

// CreateItem creates a DRAFT or REVIEW item, for a person and an agent
// alike: no item is born approved.
func (s *service) CreateItem(ctx context.Context, actor Actor, in CreateItemInput) (Item, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Item{}, err
	}
	if !slices.Contains(itemFormats, in.Format) {
		return Item{}, invalid("format", "required")
	}
	status := in.Status
	switch status {
	case "":
		status = ItemDraft
	case ItemDraft, ItemReview:
	default:
		return Item{}, invalid("status", "an item is created DRAFT or REVIEW")
	}
	if err := firstErr(
		checkLen("title", in.Title, 1, MaxTitleLen),
		checkLen("body_md", in.BodyMD, 0, MaxBodyLen),
		checkSubject(in.EmailSubject),
		checkRecipients(in.EmailTo),
		checkEmailFields(in.Format, in.EmailTo, in.EmailSubject),
	); err != nil {
		return Item{}, err
	}
	now := s.now()
	var out Item
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		if err := firstErr(
			s.checkCampaignLink(ctx, actor.OrgID, in.CampaignID),
			s.checkIdeaLink(ctx, actor.OrgID, in.IdeaID),
			s.checkChannelLink(ctx, actor.OrgID, in.ChannelID),
		); err != nil {
			return err
		}
		row := &store.GtmItem{
			OrgID:        actor.OrgID,
			CampaignID:   strPtrOrNil(in.CampaignID),
			IdeaID:       strPtrOrNil(in.IdeaID),
			Format:       string(in.Format),
			ChannelID:    strPtrOrNil(in.ChannelID),
			Title:        in.Title,
			BodyMd:       in.BodyMD,
			Status:       string(status),
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
	var out Item
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		row, err := s.store.GetItem(ctx, actor.OrgID, in.ID)
		if err != nil {
			return s.notFound(ctx, err, "item", in.ID)
		}
		cur := ItemStatus(row.Status)
		if cur == ItemSent {
			return ErrSentIsFinal
		}
		// A held item (a send started and not recorded) is frozen, for a
		// person too: changing it mid-send would let a second send out. Only
		// ResolveInDoubtSend lifts the hold.
		if held(row) {
			return s.inDoubt(row)
		}
		if row.Revision != in.ExpectedRevision {
			return conflict(in.ID, in.ExpectedRevision, row.Revision)
		}
		if !actor.Person() && !agentEditable(cur) {
			return personOnly("change an item that is " + statusWord(cur))
		}
		if in.PublishedURL != nil && !actor.Person() {
			return personOnly("record where an item was published")
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
			if err := checkTransition(actor.Person(), ItemFormat(row.Format), from, *in.Status); err != nil {
				return err
			}
			row.Status = string(*in.Status)
		}
		if ItemFormat(row.Format).EmailType() && (ItemStatus(row.Status) == ItemScheduled || ItemStatus(row.Status) == ItemPublished) {
			return &TransitionError{From: cur, To: ItemStatus(row.Status), Reason: "an email is never scheduled or published; it stays APPROVED until it is sent"}
		}
		if err := checkEmailFields(ItemFormat(row.Format), row.EmailTo, row.EmailSubject); err != nil {
			return err
		}
		if err := s.checkPublishedURL(ctx, actor.OrgID, row, cur); err != nil {
			return err
		}
		if ItemStatus(row.Status) == ItemScheduled && row.ScheduledAt == nil {
			if cur == ItemScheduled {
				// Un-scheduling is not a date edit: there is no SCHEDULED ->
				// APPROVED, so it is PAUSED -> DRAFT and approve again.
				return &TransitionError{From: cur, To: ItemScheduled,
					Reason: "a scheduled item keeps its date; to take it off the calendar, pause it, return it to draft and approve it again"}
			}
			return invalid("scheduled_at", "a scheduled item needs a date")
		}
		out, err = s.writeItem(ctx, actor.OrgID, row, in.ExpectedRevision)
		return err
	})
	return out, err
}

func checkItemUpdate(in UpdateItemInput) error {
	if in.Format != nil && !slices.Contains(itemFormats, *in.Format) {
		return invalid("format", "unknown format")
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
	if in.PublishedURL != nil && *in.PublishedURL != "" {
		checks = append(checks, checkURL("published_url", *in.PublishedURL))
	}
	if in.EmailSubject != nil {
		checks = append(checks, checkSubject(*in.EmailSubject))
	}
	if in.EmailTo != nil {
		checks = append(checks, checkRecipients(*in.EmailTo))
	}
	return firstErr(checks...)
}

// itemContent is what an approval approves: title, body_md, format,
// channel_id, email_to and email_subject. Links to a campaign or an idea,
// the date and published_url are not content.
func itemContent(r *store.GtmItem) string {
	return strings.Join([]string{r.Format, r.Title, r.BodyMd, deref(r.ChannelID), r.EmailSubject, strings.Join(r.EmailTo, "\x00")}, "\x01")
}

func statusWord(st ItemStatus) string { return statusName(string(st), "GTM_ITEM_STATUS_") }

// statusName is an enum value name in words: "GTM_ITEM_STATUS_APPROVED" is
// "approved".
func statusName(v, prefix string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(v, prefix), "_", " "))
}

// checkTransition is the item lifecycle. An agent moves between DRAFT and
// REVIEW only. A person also approves a DRAFT or REVIEW item; schedules an
// APPROVED one; marks an APPROVED or SCHEDULED one PUBLISHED (a manual
// marker: GTM never publishes); pauses anything; and returns a PAUSED item
// to DRAFT. An email is never SCHEDULED or PUBLISHED, and SENT is never
// written here (nor reached: a SENT item is final before this is asked).
//
// A move no one may make is a *TransitionError (FailedPrecondition); one only
// a person may make, asked by an agent, is a *PersonOnlyError
// (PermissionDenied).
func checkTransition(person bool, format ItemFormat, from, to ItemStatus) error {
	draftish := from == ItemDraft || from == ItemReview
	if draftish && (to == ItemDraft || to == ItemReview) {
		return nil
	}
	email := format.EmailType()
	var personMay bool
	switch {
	case to == ItemApproved && draftish:
		personMay = true
	case to == ItemScheduled && from == ItemApproved && !email:
		personMay = true
	case to == ItemPublished && (from == ItemApproved || from == ItemScheduled) && !email:
		personMay = true
	case to == ItemPaused && from != ItemSent:
		personMay = true
	case to == ItemDraft && from == ItemPaused:
		personMay = true
	}
	switch {
	case !personMay:
		reason := ""
		if email && (to == ItemScheduled || to == ItemPublished) {
			reason = "an email is never scheduled or published; it stays APPROVED until it is sent"
		}
		return &TransitionError{From: from, To: to, Reason: reason}
	case !person && to == ItemApproved:
		return personOnly("approve an item")
	case !person:
		return personOnly("set an item's status to " + statusWord(to))
	}
	return nil
}

func (s *service) applyItemUpdate(ctx context.Context, orgID string, row *store.GtmItem, in UpdateItemInput) error {
	if in.Format != nil {
		row.Format = string(*in.Format)
	}
	if in.Title != nil {
		row.Title = *in.Title
	}
	if in.BodyMD != nil {
		row.BodyMd = *in.BodyMD
	}
	if in.ChannelID != nil {
		if err := s.checkChannelLink(ctx, orgID, *in.ChannelID); err != nil {
			return err
		}
		row.ChannelID = strPtrOrNil(*in.ChannelID)
	}
	if in.PublishedURL != nil {
		row.PublishedURL = strPtrOrNil(*in.PublishedURL)
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
func checkEmailFields(format ItemFormat, to []string, subject string) error {
	if !format.EmailType() && (len(to) > 0 || subject != "") {
		return invalid("email_to", "only an email-type item has recipients and a subject")
	}
	return nil
}

// checkPublishedURL is the PUBLISHED rule for a MANUAL channel: an item
// marked PUBLISHED there carries the URL a person recorded, set in the same
// UpdateItem or before; one whose URL is cleared while PUBLISHED is refused
// alike.
func (s *service) checkPublishedURL(ctx context.Context, orgID string, row *store.GtmItem, from ItemStatus) error {
	if ItemStatus(row.Status) != ItemPublished || deref(row.PublishedURL) != "" || row.ChannelID == nil {
		return nil
	}
	ch, err := s.store.GetChannel(ctx, orgID, *row.ChannelID)
	if err != nil {
		return s.notFound(ctx, err, "channel", *row.ChannelID)
	}
	if ChannelMode(ch.Mode) == ChannelManual {
		return &TransitionError{From: from, To: ItemPublished,
			Reason: "an item on a manual channel needs its published URL: record where it went out"}
	}
	return nil
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
		return Item{}, s.notFound(ctx, err, "item", row.ID)
	}
	return itemFromRow(stored), nil
}
