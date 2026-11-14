package automation_takeover

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgtakeover"
)

// RevokeTriggerOffer ends a pending offer: its offeree declines it, or an
// admin of its org withdraws it. Anyone else's offer is NotFound.
func (s *Service) RevokeTriggerOffer(
	ctx context.Context,
	req *connect.Request[pb.RevokeTriggerOfferRequest],
) (*connect.Response[pb.RevokeTriggerOfferResponse], error) {
	if err := auth.RequirePerson(ctx, "decline or withdraw an automation offer"); err != nil {
		return nil, err
	}
	callerID := auth.MustGetUserID(ctx)
	offer, err := s.deps.Repo.GetTriggerOffer(ctx, req.Msg.GetOfferId())
	if errors.Is(err, core.ErrTriggerOfferNotFound) {
		return nil, svcerr.NotFound("trigger offer")
	}
	if err != nil {
		return nil, dbError("get trigger offer", err)
	}
	reason := core.TriggerOfferRevokedDeclined
	if offer.OffereeUserID != callerID {
		member, err := orgtakeover.LiveMember(ctx, callerID, offer.OrgID)
		if err != nil {
			return nil, orgtakeover.Unavailable(ctx, err)
		}
		if !member {
			return nil, svcerr.NotFound("trigger offer")
		}
		if err := orgtakeover.RequireAdmin(ctx, callerID, offer.OrgID); err != nil {
			// An offer is visible only to its offeree and the org's
			// admins: to another member it does not exist.
			if svcerr.Code(err) == connect.CodePermissionDenied {
				return nil, svcerr.NotFound("trigger offer")
			}
			return nil, err
		}
		reason = core.TriggerOfferRevokedWithdrawn
	}
	if offer.Status != core.TriggerOfferPending {
		return nil, notLive(offer)
	}
	settled, err := s.deps.Repo.SettleTriggerOffer(ctx, offer.ID, core.TriggerOfferRevoked, reason)
	if err != nil {
		return nil, dbError("revoke trigger offer", err)
	}
	if !settled {
		return nil, svcerr.WithReason(svcerr.FailedPrecondition("this offer is no longer pending"), ReasonOfferNotLive)
	}
	offer.Status, offer.StatusReason = core.TriggerOfferRevoked, reason
	return connect.NewResponse(&pb.RevokeTriggerOfferResponse{Offer: offerToProto(offer)}), nil
}
