package automation_takeover

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgtakeover"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// ListOrphanedAutomations lists an org's triggers and saved workflows whose
// creator left it. It is the one place an admin reads automations they did
// not create (org-owned configuration whose creator is gone), so it shows
// the definition only: RenderDefinition withholds param values, the
// connection and event payloads, and a rendered trigger carries no secret.
func (s *Service) ListOrphanedAutomations(
	ctx context.Context,
	req *connect.Request[pb.ListOrphanedAutomationsRequest],
) (*connect.Response[pb.ListOrphanedAutomationsResponse], error) {
	callerID := auth.MustGetUserID(ctx)
	orgID := strings.TrimSpace(req.Msg.GetOrgId())
	if orgID == "" {
		return nil, svcerr.InvalidArgument("org_id is required")
	}
	if err := orgtakeover.RequireAdmin(ctx, callerID, orgID); err != nil {
		return nil, err
	}

	var departures orgtakeover.Departures
	stored, err := s.deps.Repo.ListOrgTriggers(ctx, orgID)
	if err != nil {
		return nil, dbError("list the org's triggers", err)
	}
	var orphans []*core.Trigger
	for _, t := range stored {
		left, err := departures.Left(ctx, t.UserID, t.OrgID)
		if err != nil {
			return nil, orgtakeover.Unavailable(ctx, err)
		}
		if left {
			orphans = append(orphans, t)
		}
	}

	pending := map[string]*core.TriggerOffer{}
	if len(orphans) > 0 {
		ids := make([]string, len(orphans))
		for i, t := range orphans {
			ids[i] = t.ID
		}
		offers, err := s.deps.Repo.ListPendingTriggerOffers(ctx, core.TriggerOfferFilters{OrgID: &orgID, TriggerIDs: ids})
		if err != nil {
			return nil, dbError("list the org's offers", err)
		}
		for _, o := range offers {
			pending[o.TriggerID] = o
		}
	}

	resp := &pb.ListOrphanedAutomationsResponse{}
	for _, t := range orphans {
		rendered, names := s.deps.Triggers.RenderDefinition(ctx, t)
		out := &pb.OrphanedTrigger{Trigger: rendered, ParamNames: names, CreatorUserId: t.UserID}
		if o := pending[t.ID]; o != nil {
			live, err := s.liveOffer(ctx, o, t)
			if err != nil {
				return nil, err
			}
			if live {
				out.PendingOffer = offerToProto(o)
			}
		}
		resp.Triggers = append(resp.Triggers, out)
	}

	drafts, err := s.deps.Repo.ListOrgWorkflowDrafts(ctx, orgID)
	if err != nil {
		return nil, dbError("list the org's saved workflows", err)
	}
	for _, d := range drafts {
		left, err := departures.Left(ctx, d.UserID, d.OrgID)
		if err != nil {
			return nil, orgtakeover.Unavailable(ctx, err)
		}
		if !left {
			continue
		}
		resp.Workflows = append(resp.Workflows, &pb.OrphanedWorkflow{
			Id:            d.ID,
			Name:          d.Name,
			Slug:          d.Slug,
			Description:   ptr.From(d.Description),
			Definition:    d.Definition,
			CreatorUserId: d.UserID,
			OrgId:         orgID,
			UpdatedAt:     d.UpdatedAt.UTC().Format(time.RFC3339),
			Status:        string(d.Status),
		})
	}
	return connect.NewResponse(resp), nil
}
