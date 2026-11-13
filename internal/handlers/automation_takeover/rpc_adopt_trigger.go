package automation_takeover

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/protobuf/proto"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/orgtakeover"
)

// AdoptTrigger takes an orphaned trigger back for the org: an admin adopts
// it to themselves now, or offers it to another member. It never re-owns a
// trigger onto anyone but the caller: runs would begin executing as that
// member, on their machines and credentials, without their say (decision
// D12).
func (s *Service) AdoptTrigger(
	ctx context.Context,
	req *connect.Request[pb.AdoptTriggerRequest],
) (*connect.Response[pb.AdoptTriggerResponse], error) {
	callerID := auth.MustGetUserID(ctx)
	t, err := s.adminTrigger(ctx, callerID, req.Msg.GetTriggerId())
	if err != nil {
		return nil, err
	}
	if err := requireOrphan(ctx, t); err != nil {
		return nil, err
	}

	newOwner := strings.TrimSpace(req.Msg.GetNewOwnerUserId())
	if newOwner == "" || newOwner == callerID {
		if req.Msg.Rebinding == nil {
			return nil, svcerr.InvalidArgument("rebinding is required: adopting an automation means choosing your own project, machine and connections for it")
		}
		adopted, err := s.deps.Triggers.AdoptAs(ctx, t, callerID, req.Msg.Rebinding, "")
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(&pb.AdoptTriggerResponse{
			Trigger: adopted.Trigger,
			Webhook: adopted.Webhook,
			Notices: adopted.Notices,
		}), nil
	}

	// An offer: the offeree chooses their own project, machine and
	// connections when they accept, so the admin may not choose them.
	if rb := req.Msg.Rebinding; rb != nil && !proto.Equal(rb, &pb.TriggerRebinding{}) {
		return nil, svcerr.InvalidArgument("rebinding must be unset when offering an automation to someone else: they choose their own project, machine and connections when they accept")
	}
	member, err := orgtakeover.LiveMember(ctx, newOwner, *t.OrgID)
	if err != nil {
		return nil, orgtakeover.Unavailable(ctx, err)
	}
	if !member {
		return nil, svcerr.WithReason(svcerr.FailedPrecondition(
			"that person is not a member of this automation's organization"), ReasonOffereeNotMember)
	}
	now := s.now().UTC()
	offer := &core.TriggerOffer{
		ID:                  uuid.NewString(),
		TriggerID:           t.ID,
		OrgID:               *t.OrgID,
		PreviousOwnerUserID: t.UserID,
		OfferedByUserID:     callerID,
		OffereeUserID:       newOwner,
		Status:              core.TriggerOfferPending,
		CreatedAt:           now,
		ExpiresAt:           now.Add(OfferTTL),
	}
	if err := s.deps.Repo.CreateTriggerOffer(ctx, offer); err != nil {
		return nil, dbError("offer trigger", err)
	}
	logging.InfoContext(ctx, "[takeover] offered an automation whose creator left its organization",
		"trigger_id", t.ID, "offer_id", offer.ID, "offered_by", callerID, "offeree", newOwner, "org_id", offer.OrgID)
	return connect.NewResponse(&pb.AdoptTriggerResponse{Offer: offerToProto(offer)}), nil
}
