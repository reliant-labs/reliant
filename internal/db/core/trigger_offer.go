// Copyright (c) 2025 Reliant Labs
package core

import (
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// TriggerOffer is an org admin's offer of an orphaned trigger (one whose
// creator left its org) to another member, who becomes its owner only by
// accepting it (control-plane docs/design/org-sharing.md §8.3). Every run a
// trigger launches executes as its owner, so an admin never re-owns one onto
// someone else directly.
type TriggerOffer struct {
	ID        string
	TriggerID string
	OrgID     string
	// PreviousOwnerUserID is the trigger's owner when the offer was made:
	// the departed creator. An accept re-owns the trigger only while it
	// still is.
	PreviousOwnerUserID string
	OfferedByUserID     string
	OffereeUserID       string
	Status              string
	// StatusReason says why a revoked offer was (TriggerOfferRevoked*).
	StatusReason string
	CreatedAt    time.Time
	ExpiresAt    time.Time
	SettledAt    *time.Time
}

// TriggerOffer statuses.
const (
	TriggerOfferPending  = "pending"
	TriggerOfferAccepted = "accepted"
	TriggerOfferRevoked  = "revoked"
	TriggerOfferExpired  = "expired"
)

// Why an offer was revoked.
const (
	// A newer offer of the same trigger replaced it.
	TriggerOfferRevokedSuperseded = "superseded"
	// Its offeree declined it.
	TriggerOfferRevokedDeclined = "declined"
	// An admin withdrew it.
	TriggerOfferRevokedWithdrawn = "withdrawn"
	// The admin who made it left the org.
	TriggerOfferRevokedOffererLeft = "offerer_left"
	// Its offeree left the org.
	TriggerOfferRevokedOffereeLeft = "offeree_left"
	// The trigger is no longer its departed creator's: adopted by someone
	// else, or its creator rejoined.
	TriggerOfferRevokedOwnerChanged = "owner_changed"
)

// TriggerOfferFilters narrows ListPendingTriggerOffers. Every set field
// applies.
type TriggerOfferFilters struct {
	OffereeUserID string
	OrgID         *string
	TriggerIDs    []string
}

// ErrTriggerOfferNotFound is an offer id with no row.
var ErrTriggerOfferNotFound = svcerr.NotFound("trigger offer")

// ErrTriggerOwnerChanged refuses an adoption of a trigger whose owner is no
// longer the one the adoption decided on: someone adopted it first, or its
// creator rejoined and it is theirs again.
var ErrTriggerOwnerChanged = svcerr.FailedPrecondition("the automation changed hands since it was read (adopted by someone else, or its creator is back); reload and try again")

// ErrTriggerOfferNotPending refuses accepting an offer that was settled
// (accepted, revoked or expired) since it was read.
var ErrTriggerOfferNotPending = svcerr.FailedPrecondition("the offer is no longer pending")
