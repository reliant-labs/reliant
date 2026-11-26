// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/reliant-labs/reliant/internal/store"
)

// EmailSendTimeout bounds one Email.Send. A send that times out is in doubt
// (it may have left), so the item stays held.
const EmailSendTimeout = 60 * time.Second

// approvalHashVersion opens every ApprovalHash input; a change to the format
// changes it, so an old card never matches a new hash by accident.
const approvalHashVersion = "gtm-approval-v1"

// ApprovalHash is the canonical hash an email's approval card binds: what
// the person saw and approved. It is the lowercase hex SHA-256 of, in order,
//
//	"gtm-approval-v1", item.ID, decimal item.Revision, decimal len(item.EmailTo),
//	each of item.EmailTo, item.EmailSubject, item.BodyMD
//
// each written as an 8-byte big-endian length followed by its bytes, so no
// two different items encode the same. The format is stable: changing it
// means a new version string.
func ApprovalHash(item Item) string {
	h := sha256.New()
	field := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	field(approvalHashVersion)
	field(item.ID)
	field(strconv.FormatInt(int64(item.Revision), 10))
	field(strconv.Itoa(len(item.EmailTo)))
	for _, to := range item.EmailTo {
		field(to)
	}
	field(item.EmailSubject)
	field(item.BodyMD)
	return hex.EncodeToString(h.Sum(nil))
}

// held reports whether a send of row was started and not recorded: an
// unsent item carrying an approval hash.
func held(row *store.GtmItem) bool {
	return row.ApprovalHash != "" && ItemStatus(row.Status) != ItemSent
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
	if !ItemFormat(row.Format).EmailType() || ItemStatus(row.Status) != ItemApproved {
		return ErrNotApproved
	}
	return nil
}

// sendRecord is what markSent writes.
type sendRecord struct {
	GmailMessageID string
	ApprovalHash   string
}

// markSent writes row's send record, in the caller's transaction. The row
// must be sendable at its own revision and held under exactly rec's hash.
func (s *service) markSent(ctx context.Context, actor Actor, row *store.GtmItem, rec sendRecord) (Item, error) {
	if err := sendable(row, row.Revision); err != nil {
		return Item{}, err
	}
	if row.ApprovalHash != rec.ApprovalHash {
		return Item{}, &SendInDoubtError{ItemID: row.ID}
	}
	expected := row.Revision
	now := s.now()
	row.Status = string(ItemSent)
	row.GmailMessageID = rec.GmailMessageID
	row.SentAt = &now
	row.SentBy = actor.UserID
	row.ApprovalHash = rec.ApprovalHash
	return s.writeItem(ctx, actor.OrgID, row, expected)
}

// holdMayBeInFlight reports whether row's hold is young enough that the send
// which took it may still be running: under twice the send timeout. A held
// item is frozen, so its updated_at is the hold's time.
//
// The rule rests on two assumptions. Email.Send returns by its context's
// deadline (the Email contract requires it), so no send runs past
// EmailSendTimeout. And updated_at was stamped by the clock of the replica
// that took the hold while the age is read off this replica's clock, so
// the replicas' clocks agree to well within EmailSendTimeout: the second
// EmailSendTimeout of the window is the margin for that skew (and for a
// send's last response in flight). A clock more than EmailSendTimeout fast
// could release a hold whose send is still running.
func (s *service) holdMayBeInFlight(row *store.GtmItem) bool {
	return s.now().Sub(row.UpdatedAt) < 2*s.sendTimeout
}

// inDoubt is the refusal for a held row, saying whether its send may still
// be running.
func (s *service) inDoubt(row *store.GtmItem) error {
	return &SendInDoubtError{ItemID: row.ID, MayBeInFlight: s.holdMayBeInFlight(row)}
}

// checkSendChannel is the send path's channel rule: an email-type item's
// channel, when it has one, is ACTIVE, INTEGRATED and gmail. It returns the
// channel (nil for none).
func (s *service) checkSendChannel(ctx context.Context, orgID string, row *store.GtmItem) (*store.GtmChannel, error) {
	if row.ChannelID == nil {
		return nil, nil
	}
	ch, err := s.store.GetChannel(ctx, orgID, *row.ChannelID)
	if err != nil {
		return nil, s.notFound(ctx, err, "channel", *row.ChannelID)
	}
	if ChannelStatus(ch.Status) != ChannelActive || ChannelMode(ch.Mode) != ChannelIntegrated || deref(ch.IntegrationKind) != GmailIntegration {
		return nil, ErrChannelNotSendable
	}
	return ch, nil
}

// sendRun is the run a send goes through: in's, on the channel's Gmail
// connection when the channel names one. A run that names another is
// ErrChannelNotSendable: the channel decides which mailbox it sends from.
func sendRun(in EmailRun, ch *store.GtmChannel) (EmailRun, error) {
	if ch == nil || deref(ch.ConnectionID) == "" {
		return in, nil
	}
	conn := *ch.ConnectionID
	if in.ConnectionID != "" && in.ConnectionID != conn {
		return EmailRun{}, ErrChannelNotSendable
	}
	in.ConnectionID = conn
	return in, nil
}

// SendItemEmail sends an APPROVED email item through Email and records it,
// in three steps, so that no failure can lead to a second email:
//
//  1. Hold: in one transaction, re-read the item; check it is sendable at
//     the approved revision, not already held, and that the card's hash is
//     ApprovalHash of what is stored now; then write the hash to
//     approval_hash, stamped with the hold's time (revision unchanged). The
//     hash on an unsent item is the hold: it freezes the item (UpdateItem
//     refuses it) and makes any other send of it a SendInDoubtError, so at
//     most one Send runs per card.
//  2. Send, bounded by EmailSendTimeout, outside any transaction (a
//     serializable retry must never re-send), as the actor. An error that
//     wraps ErrEmailNotSent means nothing left, and the hold is released
//     (the same card may try again); any other error, a timeout included,
//     may have sent, so the hold stays.
//  3. Record SENT in a second transaction, detached from ctx's cancellation
//     so a caller that gives up after the send still gets it recorded. If
//     that fails the error is a *SentUnrecordedError naming the Gmail
//     message: never retried, and the hold stays.
//
// The remaining window: a crash or record failure after Gmail accepted the
// message leaves the item APPROVED and held with no SENT record. Nothing
// resends it. A person checks the sent mail in Gmail and calls
// ResolveInDoubtSend: with the message id it records SENT; without one it
// releases the hold, which it refuses while the send may still be running
// (a hold younger than twice EmailSendTimeout), and which voids every card
// issued so far (the revision moves), so only a new approval sends again.
// A person who releases a hold whose send did in fact go out, and then
// approves a new card, sends a second email: that is their call, made with
// Gmail in front of them.
//
// Clients' error text never reaches the caller: each failure is a sentinel
// with a fixed message, and the cause is logged.
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
	var run EmailRun
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		row, err := s.store.GetItem(ctx, org, id)
		if err != nil {
			return s.notFound(ctx, err, "item", id)
		}
		if held(row) {
			return s.inDoubt(row)
		}
		if err := sendable(row, rev); err != nil {
			return err
		}
		ch, err := s.checkSendChannel(ctx, org, row)
		if err != nil {
			return err
		}
		if run, err = sendRun(in.Run, ch); err != nil {
			return err
		}
		want := ApprovalHash(itemFromRow(row))
		if subtle.ConstantTimeCompare([]byte(want), []byte(hash)) != 1 {
			return ErrApprovalMismatch
		}
		if len(row.EmailTo) == 0 {
			return invalid("email_to", "an email needs recipients")
		}
		if row.EmailSubject == "" {
			return invalid("email_subject", "an email needs a subject")
		}
		msg = EmailMessage{To: slices.Clone(row.EmailTo), Subject: row.EmailSubject, BodyMD: row.BodyMd}
		if err := s.store.HoldItemSend(ctx, org, id, rev, hash, s.now()); err != nil {
			if errors.Is(err, ErrRevisionConflict) {
				return &SendInDoubtError{ItemID: id, MayBeInFlight: true}
			}
			return err
		}
		return nil
	})
	if err != nil {
		return Item{}, err
	}

	// 2. Send.
	sendCtx, cancel := context.WithTimeout(ctx, s.sendTimeout)
	sentID, err := s.email.Send(sendCtx, actor, run, msg)
	cancel()
	detached := context.WithoutCancel(ctx)
	if err != nil {
		if errors.Is(err, ErrEmailNotSent) {
			s.log.WarnContext(ctx, "gtm: email not sent; releasing the hold", "item_id", id, "error", err)
			if relErr := s.store.InTx(detached, func(ctx context.Context) error {
				return s.store.ReleaseItemSend(ctx, org, id, rev, hash, s.now(), false)
			}); relErr != nil {
				// A hold that stays costs a person's resolve, never a resend.
				s.log.WarnContext(ctx, "gtm: releasing a send hold failed", "item_id", id, "error", relErr)
			}
			return Item{}, ErrEmailNotSent
		}
		s.log.ErrorContext(ctx, "gtm: email send failed; the send is in doubt and the item held", "item_id", id, "error", err)
		return Item{}, &SendInDoubtError{ItemID: id, MayBeInFlight: true}
	}
	if sentID == "" {
		s.log.ErrorContext(ctx, "gtm: the email client returned no message id; the item is held", "item_id", id)
		return Item{}, &SendInDoubtError{ItemID: id, MayBeInFlight: true}
	}

	// 3. Record.
	var out Item
	err = s.store.InTx(detached, func(ctx context.Context) error {
		row, err := s.store.GetItem(ctx, org, id)
		if err != nil {
			return err
		}
		out, err = s.markSent(ctx, actor, row, sendRecord{GmailMessageID: sentID, ApprovalHash: hash})
		return err
	})
	if err != nil {
		attrs := []any{"item_id", id, "gmail_message_id", sentID, "error", err}
		switch {
		case errors.Is(err, ErrAlreadySent):
			s.log.ErrorContext(ctx, "gtm: an email was sent but the item was already recorded SENT (a person resolved it during the send); this send is not recorded", attrs...)
		case errors.Is(err, ErrSendInDoubt):
			s.log.ErrorContext(ctx, "gtm: an email was sent but its hold was released or replaced during the send; the send is not recorded", attrs...)
		default:
			s.log.ErrorContext(ctx, "gtm: an email was sent but its record was not saved; the item stays held", attrs...)
		}
		return Item{}, &SentUnrecordedError{ItemID: id, GmailMessageID: sentID}
	}
	return out, nil
}

// ResolveInDoubtSend settles a held item, a person's call after checking
// the sent mail in Gmail.
//
// With a Gmail message id the item is recorded SENT under the hold's hash.
// The id is the person's manual marker of what they found in the sent mail:
// it is stored as given, and nothing checks it against Gmail.
//
// Without one the hold is released ("it did not go out"). That is refused
// with *SendInFlightError while the hold is younger than twice
// EmailSendTimeout, since its send may still be running; and a release
// advances the revision, so every approval card issued before it, the one
// the in-doubt send ran under included, stops matching ApprovalHash, and
// only a new approval sends the item again.
func (s *service) ResolveInDoubtSend(ctx context.Context, actor Actor, in ResolveInDoubtSendInput) (Item, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Item{}, err
	}
	if !actor.Person() {
		return Item{}, personOnly("resolve a send in doubt")
	}
	checks := []error{
		checkID("item_id", in.ItemID),
		checkLen("approval_hash", in.ApprovalHash, 1, maxApprovalHashLen),
	}
	if in.GmailMessageID != nil {
		checks = append(checks, checkLen("gmail_message_id", *in.GmailMessageID, 1, maxGmailIDLen))
	}
	if err := firstErr(checks...); err != nil {
		return Item{}, err
	}
	var out Item
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		row, err := s.store.GetItem(ctx, actor.OrgID, in.ItemID)
		if err != nil {
			return s.notFound(ctx, err, "item", in.ItemID)
		}
		if ItemStatus(row.Status) == ItemSent {
			return ErrAlreadySent
		}
		if !held(row) {
			return ErrNotHeld
		}
		if subtle.ConstantTimeCompare([]byte(row.ApprovalHash), []byte(in.ApprovalHash)) != 1 {
			return ErrApprovalMismatch
		}
		if in.GmailMessageID != nil {
			out, err = s.markSent(ctx, actor, row, sendRecord{GmailMessageID: *in.GmailMessageID, ApprovalHash: in.ApprovalHash})
			return err
		}
		if s.holdMayBeInFlight(row) {
			return &SendInFlightError{ItemID: row.ID, HeldSince: row.UpdatedAt, ReleasableAt: row.UpdatedAt.Add(2 * s.sendTimeout)}
		}
		if err := s.store.ReleaseItemSend(ctx, actor.OrgID, row.ID, row.Revision, row.ApprovalHash, s.now(), true); err != nil {
			return s.writeErr(err, row.ID, row.Revision, func() (int32, error) {
				cur, err := s.store.GetItem(ctx, actor.OrgID, row.ID)
				if err != nil {
					return 0, err
				}
				return cur.Revision, nil
			})
		}
		stored, err := s.store.GetItem(ctx, actor.OrgID, row.ID)
		if err != nil {
			return s.notFound(ctx, err, "item", row.ID)
		}
		out = itemFromRow(stored)
		return nil
	})
	return out, err
}
