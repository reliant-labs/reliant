// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Errors. Each is, or wraps, a forge svcerr sentinel, so svcerr.Wrap maps it
// to its Connect code in the handler without a table of its own.
var (
	// ErrNotFound is a row the org does not have, and every gate refusal:
	// a caller outside GTM, or whose gate check failed, sees only this.
	ErrNotFound = svcerr.ErrNotFound

	// ErrInvalidArgument is bad input; *ValidationError names the field.
	ErrInvalidArgument = svcerr.ErrInvalidArgument

	// ErrRevisionConflict is a write naming a revision the row has moved
	// past (Aborted). *RevisionConflictError carries the current one.
	ErrRevisionConflict = svcerr.Aborted("revision conflict")

	// ErrPersonOnly is an agent doing what only a person may
	// (PermissionDenied). *PersonOnlyError names the action.
	ErrPersonOnly = svcerr.PermissionDenied("only a person can do this")

	// ErrLinkNotFound is a link to a strategy, campaign, channel, idea or
	// item the org does not have (NotFound, as the GtmService contract
	// says). *LinkError names the field. It is only ever returned to an
	// entitled caller: a refused one never gets past the gate.
	ErrLinkNotFound = svcerr.NotFound("linked row")

	// ErrNotApproved is a send, or a send record, for an item that is not an
	// APPROVED email (FailedPrecondition).
	ErrNotApproved = svcerr.FailedPrecondition("the item is not an approved email")

	// ErrAlreadySent is a send, or a send record, for an item already SENT
	// (FailedPrecondition). A send is recorded once.
	ErrAlreadySent = svcerr.FailedPrecondition("the item was already sent")

	// ErrSendInDoubt is an item held by a send that was started and never
	// recorded (FailedPrecondition): the email may have gone out. The item
	// is frozen — no send, no edit, no status change — until a person checks
	// the sent mail in Gmail and calls ResolveInDoubtSend. Not retryable.
	// *SendInDoubtError names the item.
	ErrSendInDoubt = svcerr.FailedPrecondition("a send of this item was started and not recorded; check the sent mail in Gmail, then resolve the send")

	// ErrSendMayBeInFlight is a release of a hold whose send may still be
	// running (FailedPrecondition): under twice EmailSendTimeout old.
	// *SendInFlightError says when it may be released.
	ErrSendMayBeInFlight = svcerr.FailedPrecondition("a send of this item may still be in progress; wait a few minutes, then check the sent mail in Gmail")

	// ErrNotHeld is ResolveInDoubtSend on an item with no send in doubt
	// (FailedPrecondition).
	ErrNotHeld = svcerr.FailedPrecondition("the item has no send in doubt")

	// ErrApprovalMismatch is an approval hash that is not the item's
	// (FailedPrecondition): SendItemEmail's against ApprovalHash of the
	// stored item, ResolveInDoubtSend's against the hold. The item changed
	// since the card was shown, or the card is another item's.
	ErrApprovalMismatch = svcerr.FailedPrecondition("the approval does not match the item as it is now; approve it again")

	// ErrSentUnrecorded is an email that was sent whose SENT record failed
	// to save (DataLoss). The item stays held (ErrSendInDoubt). Never retry
	// it: *SentUnrecordedError names the Gmail message.
	ErrSentUnrecorded = svcerr.DataLoss("the email was sent but its send record was not saved")

	// ErrEmailNotSent is what an Email wraps when it is certain the message
	// did not leave; SendItemEmail then releases the item's hold and
	// returns it, with this fixed message.
	ErrEmailNotSent = svcerr.FailedPrecondition("the email was not sent")

	// ErrAnalyticsFailed is a product-analytics query the client could not
	// answer (Unavailable). The client's error is logged, never returned.
	ErrAnalyticsFailed = svcerr.Unavailable("the product analytics query failed")

	// ErrInvalidTransition is an item status change the lifecycle does not
	// allow (FailedPrecondition). *TransitionError names it.
	ErrInvalidTransition = svcerr.FailedPrecondition("this status change is not allowed")

	// ErrSentIsFinal is a change to a SENT item, which is a record
	// (FailedPrecondition).
	ErrSentIsFinal = svcerr.FailedPrecondition("a sent item cannot be changed")

	// ErrClaimExists is a claim text the org already holds (AlreadyExists).
	ErrClaimExists = svcerr.AlreadyExists("a claim with this text already exists")

	// ErrAnalyticsUnavailable is ReadProductAnalytics in a process with no
	// analytics client (Unavailable).
	ErrAnalyticsUnavailable = svcerr.Unavailable("product analytics is not configured")

	// ErrEmailUnavailable is SendItemEmail in a process with no email client
	// (Unavailable).
	ErrEmailUnavailable = svcerr.Unavailable("email is not configured")

	// ErrChannelExists is a channel key the org already holds
	// (AlreadyExists).
	ErrChannelExists = svcerr.AlreadyExists("a channel with this key already exists")

	// ErrTooManyChannels is a channel past MaxChannels for the org
	// (ResourceExhausted).
	ErrTooManyChannels = svcerr.ResourceExhausted("the org has the most channels allowed")

	// ErrChannelNotSendable is a send of an item whose channel is not an
	// ACTIVE, INTEGRATED gmail channel (FailedPrecondition).
	ErrChannelNotSendable = svcerr.FailedPrecondition("the item's channel cannot send email: it must be an active, integrated gmail channel")

	// ErrAssetsUnavailable is a byte-touching asset call in a process with
	// no asset store (Unavailable): assets fail closed.
	ErrAssetsUnavailable = svcerr.Unavailable("asset storage is not configured")

	// ErrAttachmentsUnavailable is AttachFromAttachment in a process with no
	// attachment reader (Unavailable).
	ErrAttachmentsUnavailable = svcerr.Unavailable("chat attachments are not available here")

	// ErrAssetTooLarge is an asset over its size limit (ResourceExhausted).
	ErrAssetTooLarge = svcerr.ResourceExhausted("the file is too large")

	// ErrAssetQuota is an upload past the org's asset quota
	// (ResourceExhausted).
	ErrAssetQuota = svcerr.ResourceExhausted("the org's asset storage is full")

	// ErrAssetRejected is uploaded bytes that failed a check: size, type or
	// read (InvalidArgument). *AssetRejectedError says which; the object and
	// the row are gone.
	ErrAssetRejected = svcerr.InvalidArgument("the file was rejected")

	// ErrAssetNotPending is a confirm of an asset that is not PENDING
	// (FailedPrecondition).
	ErrAssetNotPending = svcerr.FailedPrecondition("the asset is not waiting for its upload")

	// ErrAssetNotUploaded is a confirm before the bytes were PUT
	// (FailedPrecondition); the asset stays PENDING.
	ErrAssetNotUploaded = svcerr.FailedPrecondition("the file has not been uploaded yet")

	// ErrAssetUploadExpired is a confirm of a PENDING asset past its upload
	// window (FailedPrecondition); the asset is deleted, and the upload
	// starts again with CreateAssetUpload.
	ErrAssetUploadExpired = svcerr.FailedPrecondition("the upload expired; start it again")

	// ErrAssetNotReady is a download of an asset that is not READY
	// (FailedPrecondition).
	ErrAssetNotReady = svcerr.FailedPrecondition("the asset is not ready")

	// ErrAssetStorageFailed is an asset store failure (Unavailable); the
	// store's error is logged, never returned.
	ErrAssetStorageFailed = svcerr.Unavailable("asset storage failed")

	// ErrObjectNotFound is what an AssetStore's Open returns for a missing
	// object.
	ErrObjectNotFound = svcerr.NotFound("object")
)

// RevisionConflictError is a write that named a revision the row has moved
// past. The caller re-reads and tries again. It is ErrRevisionConflict.
type RevisionConflictError struct {
	// ID is the row's id (the org id for the positioning).
	ID       string
	Expected int32
	Current  int32
}

// Error implements error.
func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("revision conflict: %s is at revision %d, not %d", e.ID, e.Current, e.Expected)
}

// Unwrap returns ErrRevisionConflict.
func (e *RevisionConflictError) Unwrap() error { return ErrRevisionConflict }

// SendInDoubtError is an item held by an unrecorded send. It is
// ErrSendInDoubt.
type SendInDoubtError struct {
	ItemID string
	// MayBeInFlight is true while the hold is young enough (under twice
	// EmailSendTimeout) that its send may still be running.
	MayBeInFlight bool
}

// Error implements error.
func (e *SendInDoubtError) Error() string {
	if e.MayBeInFlight {
		return fmt.Sprintf("a send of item %s may still be in progress; wait a few minutes, then check the sent mail in Gmail and resolve the send", e.ItemID)
	}
	return fmt.Sprintf("a send of item %s was started and not recorded; check the sent mail in Gmail, then resolve the send", e.ItemID)
}

// Unwrap returns ErrSendInDoubt.
func (e *SendInDoubtError) Unwrap() error { return ErrSendInDoubt }

// SendInFlightError is a release (ResolveInDoubtSend with no Gmail id) of a
// hold whose send may still be running. It is ErrSendMayBeInFlight.
type SendInFlightError struct {
	ItemID string
	// HeldSince is when the send took the hold; ReleasableAt is when the
	// hold may be released.
	HeldSince    time.Time
	ReleasableAt time.Time
}

// Error implements error.
func (e *SendInFlightError) Error() string {
	return fmt.Sprintf("a send of item %s may still be in progress; wait until %s, then check the sent mail in Gmail before releasing it",
		e.ItemID, e.ReleasableAt.UTC().Format(time.RFC3339))
}

// Unwrap returns ErrSendMayBeInFlight.
func (e *SendInFlightError) Unwrap() error { return ErrSendMayBeInFlight }

// SentUnrecordedError is an email Gmail accepted whose SENT record did not
// save. It is ErrSentUnrecorded and nothing else: the record's failure is
// logged, never attached.
type SentUnrecordedError struct {
	ItemID         string
	GmailMessageID string
}

// Error implements error.
func (e *SentUnrecordedError) Error() string {
	return fmt.Sprintf("item %s was sent as Gmail message %s but its send record was not saved; do not resend", e.ItemID, e.GmailMessageID)
}

// Unwrap returns ErrSentUnrecorded.
func (e *SentUnrecordedError) Unwrap() error { return ErrSentUnrecorded }

// TransitionError is an item status change the lifecycle refuses. It is
// ErrInvalidTransition.
type TransitionError struct {
	From   ItemStatus
	To     ItemStatus
	Reason string
}

// Error implements error.
func (e *TransitionError) Error() string {
	msg := fmt.Sprintf("an item cannot move from %s to %s", statusWord(e.From), statusWord(e.To))
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg
}

// Unwrap returns ErrInvalidTransition.
func (e *TransitionError) Unwrap() error { return ErrInvalidTransition }

// AssetRejectedError is uploaded bytes that failed a check. It is
// ErrAssetRejected.
type AssetRejectedError struct {
	// Reason says which check, in words a person can act on.
	Reason string
}

// Error implements error.
func (e *AssetRejectedError) Error() string { return "the file was rejected: " + e.Reason }

// Unwrap returns ErrAssetRejected.
func (e *AssetRejectedError) Unwrap() error { return ErrAssetRejected }

// PersonOnlyError is an agent doing what only a person may. It is
// ErrPersonOnly.
type PersonOnlyError struct {
	// Action is what was refused, e.g. "approve an item".
	Action string
}

// Error implements error.
func (e *PersonOnlyError) Error() string { return "only a person can " + e.Action }

// Unwrap returns ErrPersonOnly.
func (e *PersonOnlyError) Unwrap() error { return ErrPersonOnly }

// LinkError is a link to a row the org does not have: missing, or another
// org's, which read alike. It is ErrLinkNotFound.
type LinkError struct {
	// Field is the link: "campaign_id" or "idea_id".
	Field string
	// ID is the id linked to, when known.
	ID string
}

// Error implements error.
func (e *LinkError) Error() string {
	if e.ID == "" {
		return e.Field + ": no such row in this org"
	}
	return fmt.Sprintf("%s: no %q in this org", e.Field, e.ID)
}

// Unwrap returns ErrLinkNotFound.
func (e *LinkError) Unwrap() error { return ErrLinkNotFound }

// ValidationError is a field that failed validation. It is ErrInvalidArgument.
type ValidationError struct {
	Field  string
	Reason string
}

// Error implements error.
func (e *ValidationError) Error() string { return e.Field + ": " + e.Reason }

// Unwrap returns ErrInvalidArgument.
func (e *ValidationError) Unwrap() error { return ErrInvalidArgument }

func invalid(field, reason string) error { return &ValidationError{Field: field, Reason: reason} }

func personOnly(action string) error { return &PersonOnlyError{Action: action} }

func conflict(id string, expected, current int32) error {
	return &RevisionConflictError{ID: id, Expected: expected, Current: current}
}
