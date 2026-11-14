package automation_takeover

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// AcceptTriggerOffer is the offeree taking the automation an admin offered
// them: the only way anyone but an admin adopting for themselves becomes an
// automation's owner. A person signed in to the app, never a machine or
// agent credential: accepting makes runs execute as them.
func (s *Service) AcceptTriggerOffer(
	ctx context.Context,
	req *connect.Request[pb.AcceptTriggerOfferRequest],
) (*connect.Response[pb.AcceptTriggerOfferResponse], error) {
	if err := auth.RequirePerson(ctx, "accept an automation"); err != nil {
		return nil, err
	}
	callerID := auth.MustGetUserID(ctx)
	offer, err := s.ownOffer(ctx, callerID, req.Msg.GetOfferId())
	if err != nil {
		return nil, err
	}
	if req.Msg.Rebinding == nil {
		return nil, svcerr.InvalidArgument("rebinding is required: accepting an automation means choosing your own project, machine and connections for it")
	}
	t, err := s.deps.Repo.GetTrigger(ctx, offer.TriggerID)
	if err != nil && !errors.Is(err, core.ErrTriggerNotFound) {
		return nil, dbError("get trigger", err)
	}
	live, err := s.liveOffer(ctx, offer, t)
	if err != nil {
		return nil, err
	}
	if !live {
		return nil, notLive(offer)
	}
	adopted, err := s.deps.Triggers.AdoptAs(ctx, t, callerID, req.Msg.Rebinding, offer.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(adopted), nil
}

// ownOffer loads offer id for its offeree. Anyone else's, like a missing
// one, is NotFound: an offer is visible only to its offeree and the org's
// admins.
func (s *Service) ownOffer(ctx context.Context, callerID, id string) (*core.TriggerOffer, error) {
	offer, err := s.deps.Repo.GetTriggerOffer(ctx, id)
	if errors.Is(err, core.ErrTriggerOfferNotFound) {
		return nil, svcerr.NotFound("trigger offer")
	}
	if err != nil {
		return nil, dbError("get trigger offer", err)
	}
	if offer.OffereeUserID != callerID {
		return nil, svcerr.NotFound("trigger offer")
	}
	return offer, nil
}
