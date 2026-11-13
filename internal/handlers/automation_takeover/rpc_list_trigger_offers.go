package automation_takeover

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// ListTriggerOffers lists the caller's live offers, each with the offered
// trigger's definition (values withheld, as an admin sees it). An offer found
// dead as it is read (expired, a party left, the trigger re-owned) is settled
// and left out. Person-only, like accepting: the liveness check reads other
// people's membership, which a credential bound to another org cannot.
func (s *Service) ListTriggerOffers(
	ctx context.Context,
	req *connect.Request[pb.ListTriggerOffersRequest],
) (*connect.Response[pb.ListTriggerOffersResponse], error) {
	if err := auth.RequirePerson(ctx, "list automations offered to you"); err != nil {
		return nil, err
	}
	callerID := auth.MustGetUserID(ctx)
	f := core.TriggerOfferFilters{OffereeUserID: callerID}
	if org := strings.TrimSpace(req.Msg.GetOrgId()); org != "" {
		f.OrgID = &org
	}
	offers, err := s.deps.Repo.ListPendingTriggerOffers(ctx, f)
	if err != nil {
		return nil, dbError("list trigger offers", err)
	}
	resp := &pb.ListTriggerOffersResponse{}
	for _, o := range offers {
		t, err := s.deps.Repo.GetTrigger(ctx, o.TriggerID)
		if errors.Is(err, core.ErrTriggerNotFound) {
			continue // deleted; its offers went with it
		}
		if err != nil {
			return nil, dbError("get trigger", err)
		}
		live, err := s.liveOffer(ctx, o, t)
		if err != nil {
			return nil, err
		}
		if !live {
			continue
		}
		out := offerToProto(o)
		out.Trigger, out.ParamNames = s.deps.Triggers.RenderDefinition(ctx, t)
		resp.Offers = append(resp.Offers, out)
	}
	return connect.NewResponse(resp), nil
}
