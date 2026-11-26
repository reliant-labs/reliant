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
	if ItemKind(row.Kind) != ItemEmail || ItemStatus(row.Status) != ItemApproved {
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

// SendItemEmail sends an APPROVED email item through Email and records it,
// in three steps, so that no failure can lead to a second email:
//
//  1. Hold: in one transaction, re-read the item; check it is sendable at
//     the approved revision, not already held, and that the card's hash is
//     ApprovalHash of what is stored now; then write the hash to
//     approval_hash (revision unchanged). The hash on an unsent item is the
//     hold: it freezes the item (UpdateItem refuses it) and makes any other
//     send of it SendInDoubtError, so at most one Send ever runs.
//  2. Send, bounded by EmailSendTimeout, outside any transaction (a
//     serializable retry must never re-send), as the actor. An error that
//     wraps ErrEmailNotSent means nothing left, and the hold is released;
//     any other error, a timeout included, may have sent, so the hold stays.
//  3. Record SENT in a second transaction, detached from ctx's cancellation
//     so a caller that gives up after the send still gets it recorded. If
//     that fails the error is a *SentUnrecordedError naming the Gmail
//     message: never retried, and the hold stays.
//
// The remaining window: a crash or record failure after Gmail accepted the
// message leaves the item APPROVED and held with no SENT record. Nothing
// resends it. A person checks the sent mail in Gmail and calls
// ResolveInDoubtSend: with the message id it records SENT, without one it
// releases the hold.
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
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		row, err := s.store.GetItem(ctx, org, id)
		if err != nil {
			return s.notFound(ctx, err, "item", id)
		}
		if err := sendable(row, rev); err != nil {
			return err
		}
		if held(row) {
			return &SendInDoubtError{ItemID: id}
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
		if err := s.store.SetItemSendAttempt(ctx, org, id, rev, "", hash); err != nil {
			if errors.Is(err, ErrRevisionConflict) {
				return &SendInDoubtError{ItemID: id}
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
	sentID, err := s.email.Send(sendCtx, actor, in.Run, msg)
	cancel()
	detached := context.WithoutCancel(ctx)
	if err != nil {
		if errors.Is(err, ErrEmailNotSent) {
			s.log.WarnContext(ctx, "gtm: email not sent; releasing the hold", "item_id", id, "error", err)
			if relErr := s.store.InTx(detached, func(ctx context.Context) error {
				return s.store.SetItemSendAttempt(ctx, org, id, rev, hash, "")
			}); relErr != nil {
				// A hold that stays costs a person's resolve, never a resend.
				s.log.WarnContext(ctx, "gtm: releasing a send hold failed", "item_id", id, "error", relErr)
			}
			return Item{}, ErrEmailNotSent
		}
		s.log.ErrorContext(ctx, "gtm: email send failed; the send is in doubt and the item held", "item_id", id, "error", err)
		return Item{}, &SendInDoubtError{ItemID: id}
	}
	if sentID == "" {
		s.log.ErrorContext(ctx, "gtm: the email client returned no message id; the item is held", "item_id", id)
		return Item{}, &SendInDoubtError{ItemID: id}
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
		s.log.ErrorContext(ctx, "gtm: an email was sent but its record was not saved; the item is held",
			"item_id", id, "gmail_message_id", sentID, "error", err)
		return Item{}, &SentUnrecordedError{ItemID: id, GmailMessageID: sentID}
	}
	return out, nil
}

// ResolveInDoubtSend settles a held item, a person's call after checking
// the sent mail in Gmail. With a Gmail message id the item is recorded SENT
// under the hold's hash; without one the hold is released ("it did not go
// out"), and the item can be sent again under a new card.
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
		if err := s.store.SetItemSendAttempt(ctx, actor.OrgID, row.ID, row.Revision, row.ApprovalHash, ""); err != nil {
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
