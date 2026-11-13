package org_provider_account

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/accountlimits"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// manageAction names what an admin-only rpc does, for the refusal message.
const manageAction = "manage an organization's AI accounts"

// ReasonProviderAccountsDisabled is the x-forge-error-reason of adding an org account or binding
// a workspace while the deployment has not enabled org accounts (orgs.provider_accounts_enabled).
const ReasonProviderAccountsDisabled = "org_provider_accounts_disabled"

// ReasonProviderSeatsDisabled is the x-forge-error-reason of binding a workspace while the
// deployment has not enabled org-paid seats (orgs.provider_workspace_seats_enabled).
const ReasonProviderSeatsDisabled = "org_provider_seats_disabled"

// maxLabelRunes bounds an account's label, as for a person's (provider_account).
const maxLabelRunes = 80

// maxGrants bounds one SetOrgProviderAccountGrants call.
const maxGrants = 500

// apiKeyProviders are the drivers an org account may be a key of. An org owns API keys only,
// never a sign-in (provider_accounts_org_api_key_only): a subscription is a person's.
var apiKeyProviders = map[string]bool{"anthropic": true, "openai": true, "gemini": true, "openrouter": true}

func callerID(ctx context.Context) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return "", svcerr.Wrap(svcerr.Unauthenticated("user ID not found in context"))
	}
	return userID, nil
}

// requireOrgID is the request's org, which every rpc names.
func requireOrgID(orgID string) (string, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return "", svcerr.Wrap(svcerr.InvalidArgument("org_id is required"))
	}
	return orgID, nil
}

// admin is the caller of an rpc that changes orgID's accounts: a person signed in to the app who
// is an owner or admin of orgID, read fresh (orgs.RequireAdmin).
func admin(ctx context.Context, orgID string) (string, error) {
	userID, err := callerID(ctx)
	if err != nil {
		return "", err
	}
	if err := orgs.RequireAdmin(ctx, userID, orgID, manageAction); err != nil {
		return "", svcerr.Wrap(err)
	}
	return userID, nil
}

// reader is the caller of ListOrgProviderAccounts: a person signed in to the app who is a live
// member of orgID. isAdmin is whether they are an owner or admin, read fresh, and so see each
// account whole.
func reader(ctx context.Context, orgID string) (userID string, isAdmin bool, err error) {
	const action = "list an organization's AI accounts"
	if userID, err = callerID(ctx); err != nil {
		return "", false, err
	}
	if err := orgs.RequirePerson(ctx, action); err != nil {
		return "", false, svcerr.Wrap(err)
	}
	switch err := orgs.RequireAdmin(ctx, userID, orgID, action); {
	case err == nil:
		return userID, true, nil
	case svcerr.IsPermissionDenied(err):
		// Not an owner or admin: a live member reads the narrowed list.
		if err := orgs.RequireMember(ctx, userID, orgID); err != nil {
			return "", false, svcerr.Wrap(err)
		}
		return userID, false, nil
	default:
		return "", false, svcerr.Wrap(err)
	}
}

// addingEnabled is whether the deployment lets an org add accounts and bind workspaces.
func (s *Service) addingEnabled() bool {
	if s.deps.OrgRollout == nil {
		return false
	}
	return s.deps.OrgRollout.GetProviderAccountsEnabled()
}

// gated refuses an add or a bind while the deployment has not enabled org accounts.
func (s *Service) gated() error {
	if s.addingEnabled() {
		return nil
	}
	return svcerr.Wrap(svcerr.WithReason(svcerr.FailedPrecondition(
		"adding organization AI accounts is not enabled on this deployment"), ReasonProviderAccountsDisabled))
}

// seatsGated refuses a bind while the deployment has not enabled org-paid seats
// (orgs.provider_workspace_seats_enabled): a release a rollback could return to may not honour
// a seat's org yet.
func (s *Service) seatsGated() error {
	if s.deps.OrgRollout != nil && s.deps.OrgRollout.GetProviderWorkspaceSeatsEnabled() {
		return nil
	}
	return svcerr.Wrap(svcerr.WithReason(svcerr.FailedPrecondition(
		"paying for members' provider workspace seats is not enabled on this deployment"), ReasonProviderSeatsDisabled))
}

// label validates an org account's label: required, at most maxLabelRunes.
func label(raw string) (string, error) {
	l := strings.TrimSpace(raw)
	if l == "" {
		return "", svcerr.Wrap(svcerr.InvalidArgument("label is required"))
	}
	if utf8.RuneCountInString(l) > maxLabelRunes {
		return "", svcerr.Wrap(svcerr.InvalidArgument("label is too long"))
	}
	return l, nil
}

// validKey checks an API key with its provider before it is stored.
func (s *Service) validKey(ctx context.Context, provider, apiKey string) error {
	if apiKey == "" {
		return svcerr.Wrap(svcerr.InvalidArgument("api_key is required"))
	}
	if ok, msg := s.deps.ValidateKey(ctx, models.Family(provider), apiKey); !ok {
		return svcerr.Wrap(svcerr.InvalidArgument(msg))
	}
	return nil
}

func accessToCore(a pb.OrgProviderAccountAccess) (string, bool) {
	switch a {
	case pb.OrgProviderAccountAccess_ORG_PROVIDER_ACCOUNT_ACCESS_MEMBERS:
		return core.OrgAccessMembers, true
	case pb.OrgProviderAccountAccess_ORG_PROVIDER_ACCOUNT_ACCESS_ADMINS:
		return core.OrgAccessAdmins, true
	case pb.OrgProviderAccountAccess_ORG_PROVIDER_ACCOUNT_ACCESS_SELECTED:
		return core.OrgAccessSelected, true
	}
	return "", false
}

func accessToProto(a string) pb.OrgProviderAccountAccess {
	switch a {
	case core.OrgAccessMembers:
		return pb.OrgProviderAccountAccess_ORG_PROVIDER_ACCOUNT_ACCESS_MEMBERS
	case core.OrgAccessAdmins:
		return pb.OrgProviderAccountAccess_ORG_PROVIDER_ACCOUNT_ACCESS_ADMINS
	case core.OrgAccessSelected:
		return pb.OrgProviderAccountAccess_ORG_PROVIDER_ACCOUNT_ACCESS_SELECTED
	}
	return pb.OrgProviderAccountAccess_ORG_PROVIDER_ACCOUNT_ACCESS_UNSPECIFIED
}

// orgView is what one caller sees of the org's accounts: every account with its limit state, and
// (for an admin) its grants.
type orgView struct {
	admin  bool
	limits map[string]core.LimitState
	grants map[string][]core.OrgAccountGrant
	now    time.Time
}

// load reads the limit state, and for an admin the grants, of orgID's accounts.
func (s *Service) load(ctx context.Context, orgID string, isAdmin bool) (orgView, error) {
	v := orgView{admin: isAdmin, now: time.Now()}
	var err error
	if v.limits, err = s.deps.Store.ListOwnedAccountLimits(ctx, core.OrgAccountOwner(orgID)); err != nil {
		return orgView{}, svcerr.Wrap(err)
	}
	if !isAdmin {
		return v, nil
	}
	grants, err := s.deps.Store.ListOrgAccountGrants(ctx, orgID)
	if err != nil {
		return orgView{}, svcerr.Wrap(err)
	}
	v.grants = map[string][]core.OrgAccountGrant{}
	for _, g := range grants {
		v.grants[g.AccountID] = append(v.grants[g.AccountID], g)
	}
	return v, nil
}

// proto renders one org account as v's caller may see it. An admin sees it whole; a member sees
// its provider, label and status, never its key hint, usage or a needs-attention reason (§3.5).
// No account carries its key: the secret columns are never read here.
func (v orgView) proto(a core.ProviderAccount) *pb.OrgProviderAccount {
	var l *core.LimitState
	if st, ok := v.limits[a.ID]; ok {
		l = &st
	}
	full := accountlimits.ProtoAccount(a, l, v.now)
	out := &pb.OrgProviderAccount{Access: accessToProto(a.OrgAccess)}
	if !v.admin {
		out.Account = &pb.ProviderAccount{
			Id: full.Id, Provider: full.Provider, ProviderDisplayName: full.ProviderDisplayName,
			AuthKind: full.AuthKind, Label: memberLabel(a), Priority: full.Priority, Status: full.Status,
			LimitedUntil: full.LimitedUntil, CreatedAt: full.CreatedAt,
			Funding: full.Funding, FundingOrgId: full.FundingOrgId,
		}
		return out
	}
	out.Account = full
	if a.CreatedByUserID != "" {
		out.CreatedByUserId = &a.CreatedByUserID
	}
	for _, g := range v.grants[a.ID] {
		out.Grants = append(out.Grants, &pb.OrgProviderAccountGrant{
			UserId: g.UserID, GrantedByUserId: g.GrantedByUserID, GrantedAt: g.GrantedAt.UTC().Format(time.RFC3339),
		})
	}
	return out
}

// memberLabel is the account's label, never core.ProviderAccount.DisplayLabel's fallback, which
// carries the key hint a member may not see.
func memberLabel(a core.ProviderAccount) string {
	if a.Label != "" {
		return a.Label
	}
	return models.ProviderDisplayName(a.Provider) + " key"
}

// protoAll renders accounts, optionally only those of one provider.
func (v orgView) protoAll(accounts []core.ProviderAccount, provider string) []*pb.OrgProviderAccount {
	out := make([]*pb.OrgProviderAccount, 0, len(accounts))
	for _, a := range accounts {
		if provider == "" || a.Provider == provider {
			out = append(out, v.proto(a))
		}
	}
	return out
}

// one renders a single account for an admin, after a change.
func (s *Service) one(ctx context.Context, orgID string, a core.ProviderAccount) (*pb.OrgProviderAccount, error) {
	v, err := s.load(ctx, orgID, true)
	if err != nil {
		return nil, err
	}
	return v.proto(a), nil
}

// find returns orgID's account with the given id, or NotFound.
func (s *Service) find(ctx context.Context, orgID, accountID string) (core.ProviderAccount, error) {
	if accountID == "" {
		return core.ProviderAccount{}, svcerr.Wrap(svcerr.InvalidArgument("account_id is required"))
	}
	accounts, err := s.deps.Store.ListOwnedProviderAccounts(ctx, core.OrgAccountOwner(orgID))
	if err != nil {
		return core.ProviderAccount{}, svcerr.Wrap(err)
	}
	for _, a := range accounts {
		if a.ID == accountID {
			return a, nil
		}
	}
	return core.ProviderAccount{}, svcerr.Wrap(core.ErrProviderAccountNotFound)
}

func workspaceProto(w core.OrgProviderWorkspace) *pb.OrgProviderWorkspace {
	return &pb.OrgProviderWorkspace{
		Provider: w.Provider, ExternalOrgId: w.ExternalOrgID, DisplayName: w.DisplayName,
		BoundByUserId: w.BoundByUserID, BoundAt: w.BoundAt.UTC().Format(time.RFC3339),
	}
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
