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

// ImpactChatLimit bounds the chats one preview reads, the most recently
// active first; a larger org's answer says it is truncated.
const ImpactChatLimit = 2000

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
	calls, err := s.deps.Store.LatestOrgChatCalls(ctx, orgID, s.now().Add(-ImpactWindow), ImpactChatLimit+1)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	truncated := len(calls) > ImpactChatLimit
	if truncated {
		calls = calls[:ImpactChatLimit]
	}
	out := &pb.GetPolicyImpactResponse{
		WindowDays: int32(ImpactWindow / (24 * time.Hour)), ChatsConsidered: int32(len(calls)), Truncated: truncated,
	}
	byReason := map[string]*pb.PolicyImpactReason{}
	for _, call := range calls {
		unattended, err := s.unattended(ctx, doc, call.ChatID)
		if err != nil {
			return nil, svcerr.Wrap(err)
		}
		reason := refusalOf(doc, call, unattended)
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
		if unattended {
			r.Automations++
			out.AutomationsRefused++
		}
	}
	for _, r := range byReason {
		out.Reasons = append(out.Reasons, r)
	}
	sort.Slice(out.Reasons, func(i, j int) bool { return out.Reasons[i].Reason < out.Reasons[j].Reason })
	return connect.NewResponse(out), nil
}

// unattended is whether chatID's next call has nobody behind it, by the rule
// its calls are scoped with (Deps.Attendance): a run a stored trigger
// launched, deleted since or not, that nobody adopted, and an agent-started
// run whose lineage root is one. Only the automations section reads it, so a
// document without one reads no chat.
func (s *Service) unattended(ctx context.Context, doc *controlplanev1.OrgPolicy, chatID string) (bool, error) {
	if doc.GetAutomations() == nil {
		return false, nil
	}
	chat, err := s.deps.Store.GetChat(ctx, chatID)
	if err != nil {
		return false, err
	}
	return s.deps.Attendance.ChatUnattended(ctx, chat), nil
}

// refusalOf is the reason doc would refuse the next call of a chat whose
// latest call was call with, "" when it admits it. unattended applies the
// automations section too.
func refusalOf(doc *controlplanev1.OrgPolicy, call db.OrgChatCall, unattended bool) string {
	driver := models.DriverID(call.Provider)
	if adm := orgpolicy.ProviderAdmission(doc, call.Provider, drivers.ProviderSource(driver, call.Funding), unattended); adm.Governed && !adm.Admitted {
		return orgpolicy.ReasonProviderNotAllowed
	}
	if driver == models.ManagedDriverID {
		if adm := orgpolicy.ModelAdmission(doc, drivers.ManagedAPIModel(call.Model)); adm.Governed && !adm.Admitted {
			return orgpolicy.ReasonModelNotAllowed
		}
	}
	return ""
}
