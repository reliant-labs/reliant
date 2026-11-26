// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"fmt"

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

	// ErrLinkNotFound is a link to a campaign or idea the org does not have
	// (InvalidArgument). *LinkError names the field.
	ErrLinkNotFound = svcerr.InvalidArgument("linked row not found")

	// ErrNotApproved is a send, or a send record, for an item that is not an
	// APPROVED email (FailedPrecondition).
	ErrNotApproved = svcerr.FailedPrecondition("the item is not an approved email")

	// ErrAlreadySent is a send, or a send record, for an item already SENT
	// (FailedPrecondition). A send is recorded once.
	ErrAlreadySent = svcerr.FailedPrecondition("the item was already sent")

	// ErrSendInDoubt is a send of an item whose earlier send was started and
	// never recorded (FailedPrecondition): the email may have gone out. A
	// person checks the sent mail and re-approves (any person's UpdateItem
	// clears the hold) before it can be sent again. Not retryable.
	ErrSendInDoubt = svcerr.FailedPrecondition("a send of this item was started and not recorded; check the sent mail, then re-approve it")

	// ErrSentUnrecorded is an email that was sent whose SENT record failed
	// to save (DataLoss). The item stays held (ErrSendInDoubt). Never retry
	// it: *SentUnrecordedError names the Gmail message.
	ErrSentUnrecorded = svcerr.DataLoss("the email was sent but its send record was not saved")

	// ErrEmailNotSent is what an Email wraps when it is certain the message
	// did not leave; SendItemEmail then releases the item's hold.
	ErrEmailNotSent = svcerr.FailedPrecondition("the email was not sent")

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

// SentUnrecordedError is an email Gmail accepted whose SENT record did not
// save. It is ErrSentUnrecorded; the cause is kept server-side.
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
