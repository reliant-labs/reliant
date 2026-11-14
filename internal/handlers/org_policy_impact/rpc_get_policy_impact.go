package org_policy_impact

import (
	"context"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/protobuf/encoding/protojson"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// ImpactWindow is how far back a chat's latest model call is read: a chat
// idle for longer is not counted, it has nothing to pause.
const ImpactWindow = 14 * 24 * time.Hour

// ReasonPolicyInvalid is the x-forge-error-reason of a policy_json that does
// not parse as an OrgPolicy document: control-plane's reason for a document
// its validation refuses.
const ReasonPolicyInvalid = "policy_invalid"

// previewAction names what an admin is doing, for a refusal's message.
const previewAction = "preview an organization's policy"

// GetPolicyImpact judges the org's live chats against the proposed policy's
// providers, automations and models sections, each by its latest chat-turn
// call in ImpactWindow, the way the resolver will judge its next call
// (drivers.ProviderSource, orgpolicy.ProviderAdmission, ModelAdmission). A
// rule counts whatever its mode: the preview says what each rule refuses once
// it enforces. Only counts leave: no chat, member or provider is named.
func (s *Service) GetPolicyImpact(
	ctx context.Context,
	req *connect.Request[pb.GetPolicyImpactRequest],
) (*connect.Response[pb.GetPolicyImpactResponse], error) {
	orgID := strings.TrimSpace(req.Msg.GetOrgId())
	if orgID == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("org_id is required"))
	}
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, svcerr.Wrap(svcerr.Unauthenticated("user ID not found in context"))
	}
	if err := orgs.RequireAdmin(ctx, userID, orgID, previewAction); err != nil {
		return nil, svcerr.Wrap(err)
	}
	doc := &controlplanev1.OrgPolicy{}
	if raw := strings.TrimSpace(req.Msg.GetPolicyJson()); raw != "" {
		if err := (protojson.UnmarshalOptions{}).Unmarshal([]byte(raw), doc); err != nil {
			return nil, svcerr.Wrap(svcerr.WithReason(svcerr.InvalidArgument("policy_json is not an organization policy document: "+err.Error()), ReasonPolicyInvalid))
		}
	}
	calls, err := s.deps.Store.LatestOrgChatCalls(ctx, orgID, s.deps.Now().Add(-ImpactWindow))
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	return connect.NewResponse(impactOf(doc, calls)), nil
}

// impactOf counts calls by the reason doc would refuse each with.
func impactOf(doc *controlplanev1.OrgPolicy, calls []db.OrgChatCall) *pb.GetPolicyImpactResponse {
	out := &pb.GetPolicyImpactResponse{WindowDays: int32(ImpactWindow / (24 * time.Hour)), ChatsConsidered: int32(len(calls))}
	byReason := map[string]*pb.PolicyImpactReason{}
	for _, call := range calls {
		reason := refusalOf(doc, call)
		if reason == "" {
			continue
		}
		r := byReason[reason]
		if r == nil {
			r = &pb.PolicyImpactReason{Reason: reason}
			byReason[reason] = r
		}
		r.Chats++
		out.ChatsRefused++
		if call.Automation {
			r.Automations++
			out.AutomationsRefused++
		}
	}
	for _, r := range byReason {
		out.Reasons = append(out.Reasons, r)
	}
	sort.Slice(out.Reasons, func(i, j int) bool { return out.Reasons[i].Reason < out.Reasons[j].Reason })
	return out
}

// refusalOf is the reason doc would refuse call's chat's next call with, ""
// when it admits it. A chat a stored trigger launched is judged as an
// unattended run, so the automations section applies to it.
func refusalOf(doc *controlplanev1.OrgPolicy, call db.OrgChatCall) string {
	driver := models.DriverID(call.Provider)
	if adm := orgpolicy.ProviderAdmission(doc, call.Provider, drivers.ProviderSource(driver, call.Funding), call.Automation); adm.Governed && !adm.Admitted {
		return orgpolicy.ReasonProviderNotAllowed
	}
	if driver == models.ManagedDriverID {
		if adm := orgpolicy.ModelAdmission(doc, drivers.ManagedAPIModel(call.Model)); adm.Governed && !adm.Admitted {
			return orgpolicy.ReasonModelNotAllowed
		}
	}
	return ""
}
