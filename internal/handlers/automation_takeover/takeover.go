package automation_takeover

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/orgtakeover"
)

// The x-forge-error-reason of a takeover refused on the state of things
// rather than the caller's authority.
const (
	// ReasonNotOrphaned: the automation's creator is a live member of its
	// org, so it is theirs and only they can change it.
	ReasonNotOrphaned = "automation_not_orphaned"
	// ReasonOffereeNotMember: an offer named someone who is not a live
	// member of the automation's org.
	ReasonOffereeNotMember = "offeree_not_member"
	// ReasonOfferNotLive: the offer was accepted, revoked or expired; the
	// message says which.
	ReasonOfferNotLive = "trigger_offer_not_live"
)

// dbError is a store failure as the caller reads it: a classified error keeps
// its code, anything else is "database error".
func dbError(op string, err error) error {
	return svcerr.WrapOr(fmt.Errorf("%s: %w", op, err), svcerr.Internal("database error"))
}

// adminTrigger loads trigger id for callerID acting as an admin of its org.
// A missing trigger, one with no org, and one of an org the caller is not a
// live member of are all NotFound (no telling an outsider what exists); a
// member who is not an admin is PermissionDenied (org_admin_required).
func (s *Service) adminTrigger(ctx context.Context, callerID, id string) (*core.Trigger, error) {
	t, err := s.deps.Repo.GetTrigger(ctx, id)
	if errors.Is(err, core.ErrTriggerNotFound) {
		return nil, svcerr.NotFound("trigger")
	}
	if err != nil {
		return nil, dbError("get trigger", err)
	}
	if t.OrgID == nil {
		return nil, svcerr.NotFound("trigger")
	}
	member, err := orgtakeover.LiveMember(ctx, callerID, *t.OrgID)
	if err != nil {
		return nil, orgtakeover.Unavailable(ctx, err)
	}
	if !member {
		return nil, svcerr.NotFound("trigger")
	}
	if err := orgtakeover.RequireAdmin(ctx, callerID, *t.OrgID); err != nil {
		return nil, err
	}
	return t, nil
}

// requireOrphan refuses a trigger whose creator is still a live member of
// its org: it is theirs, and nobody else takes it.
func requireOrphan(ctx context.Context, t *core.Trigger) error {
	left, err := orgtakeover.CreatorLeft(ctx, t.UserID, t.OrgID)
	if err != nil {
		return orgtakeover.Unavailable(ctx, err)
	}
	if !left {
		return svcerr.WithReason(svcerr.FailedPrecondition(
			"this automation's creator is a member of its organization, so it is theirs: only they can change it"), ReasonNotOrphaned)
	}
	return nil
}

// offerState is whether o is still live, read now: its stored status when it
// was settled; otherwise expired once past its expiry, and revoked when the
// trigger is no longer its departed creator's (adopted, or the creator is
// back), when its offeree left the org, or when the admin who made it did.
// An error is a membership outage: nothing is decided.
func (s *Service) offerState(ctx context.Context, o *core.TriggerOffer, t *core.Trigger) (status, reason string, err error) {
	if o.Status != core.TriggerOfferPending {
		return o.Status, o.StatusReason, nil
	}
	if !s.now().Before(o.ExpiresAt) {
		return core.TriggerOfferExpired, "", nil
	}
	if t == nil || t.UserID != o.PreviousOwnerUserID {
		return core.TriggerOfferRevoked, core.TriggerOfferRevokedOwnerChanged, nil
	}
	org := o.OrgID
	if left, err := orgtakeover.CreatorLeft(ctx, t.UserID, &org); err != nil {
		return "", "", err
	} else if !left {
		return core.TriggerOfferRevoked, core.TriggerOfferRevokedOwnerChanged, nil
	}
	for _, party := range []struct{ user, reason string }{
		{o.OffereeUserID, core.TriggerOfferRevokedOffereeLeft},
		{o.OfferedByUserID, core.TriggerOfferRevokedOffererLeft},
	} {
		member, err := orgtakeover.LiveMember(ctx, party.user, o.OrgID)
		if err != nil {
			return "", "", err
		}
		if !member {
			return core.TriggerOfferRevoked, party.reason, nil
		}
	}
	return core.TriggerOfferPending, "", nil
}

// settle records a dead offer's state on its row (best effort: the next read
// decides it again) and on o.
func (s *Service) settle(ctx context.Context, o *core.TriggerOffer, status, reason string) {
	if o.Status == status {
		return
	}
	if o.Status == core.TriggerOfferPending {
		if _, err := s.deps.Repo.SettleTriggerOffer(ctx, o.ID, status, reason); err != nil {
			logging.WarnContext(ctx, "[takeover] could not record a dead offer's state; the next read decides it again",
				"offer_id", o.ID, "status", status, "error", err)
		}
	}
	o.Status, o.StatusReason = status, reason
}

// liveOffer reads o's state, settles it when it is dead, and reports whether
// it is still live.
func (s *Service) liveOffer(ctx context.Context, o *core.TriggerOffer, t *core.Trigger) (bool, error) {
	status, reason, err := s.offerState(ctx, o, t)
	if err != nil {
		return false, orgtakeover.Unavailable(ctx, err)
	}
	s.settle(ctx, o, status, reason)
	return status == core.TriggerOfferPending, nil
}

// notLive is the refusal of an offer that is no longer live, saying why.
func notLive(o *core.TriggerOffer) error {
	var msg string
	switch {
	case o.Status == core.TriggerOfferAccepted:
		msg = "this offer was already accepted"
	case o.Status == core.TriggerOfferExpired:
		msg = "this offer expired; ask an admin of the organization to offer it again"
	case o.StatusReason == core.TriggerOfferRevokedSuperseded:
		msg = "this offer was replaced by a newer one"
	case o.StatusReason == core.TriggerOfferRevokedOffererLeft:
		msg = "the admin who made this offer is no longer in the organization; ask another admin to offer it again"
	case o.StatusReason == core.TriggerOfferRevokedOffereeLeft:
		msg = "you are no longer a member of this automation's organization"
	case o.StatusReason == core.TriggerOfferRevokedOwnerChanged:
		msg = "this automation is no longer waiting for a new owner: someone adopted it, or its creator is back"
	default:
		msg = "this offer was withdrawn or declined"
	}
	return svcerr.WithReason(svcerr.FailedPrecondition(msg), ReasonOfferNotLive)
}

// offerToProto renders an offer (without its trigger).
func offerToProto(o *core.TriggerOffer) *pb.TriggerOffer {
	return &pb.TriggerOffer{
		Id:              o.ID,
		TriggerId:       o.TriggerID,
		OrgId:           o.OrgID,
		OfferedByUserId: o.OfferedByUserID,
		OffereeUserId:   o.OffereeUserID,
		Status:          o.Status,
		StatusReason:    o.StatusReason,
		CreatedAt:       o.CreatedAt.UTC().Format(time.RFC3339),
		ExpiresAt:       o.ExpiresAt.UTC().Format(time.RFC3339),
	}
}
