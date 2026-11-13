package org_provider_account_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	org_provider_account "github.com/reliant-labs/reliant/internal/handlers/org_provider_account"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/seatmatch"
)

// These tests run the service over a real database (control-plane
// docs/design/org-provider-accounts.md §3.5, §7, §14 P4): who may act is the handler's, what an
// org owns is the store's, and a fake store would only restate the handler's assumptions.

const (
	org      = "org-1"
	orgKey   = "sk-ant-org-plaintext-key-qzwx" // its hint, "qzwx", is not hex: no generated id contains it
	newKey   = "sk-ant-org-rotated-key-c3d4"
	adminID  = "ada"
	memberID = "mia"
	strayID  = "nick" // a member of another org only
	demoted  = "dan"  // an admin until control-plane says otherwise
)

// roles is control-plane's answer: org -> user -> role, read at every call. fresh counts the
// fresh reads, which is what every admin decision must make.
type roles struct {
	mu    sync.Mutex
	role  map[string]map[string]string
	fresh int
}

func (r *roles) set(orgID, user, role string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.role[orgID] == nil {
		r.role[orgID] = map[string]string{}
	}
	r.role[orgID][user] = role
}

func (r *roles) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.role[orgID][userID] != "", true, nil
}

func (r *roles) RoleFresh(_ context.Context, userID, orgID string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fresh++
	return r.role[orgID][userID], true, nil
}

type env struct {
	t        *testing.T
	repo     *db.Repo
	roles    *roles
	svc      *org_provider_account.Service
	enabled  bool
	keysSeen []string
	reject   bool
	seats    *seatmatch.MockService
}

func newEnv(t *testing.T, enabled bool) *env {
	t.Helper()
	return newEnvWith(t, &configv1.OrgsConfig{ProviderAccountsEnabled: enabled, ProviderWorkspaceSeatsEnabled: enabled})
}

// newEnvWith is newEnv with the deployment's org rollout gates as given.
func newEnvWith(t *testing.T, rollout *configv1.OrgsConfig) *env {
	t.Helper()
	enabled := rollout.GetProviderAccountsEnabled()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	r := &roles{role: map[string]map[string]string{}}
	r.set(org, adminID, orgs.RoleAdmin)
	r.set(org, "olga", orgs.RoleOwner)
	r.set(org, memberID, orgs.RoleMember)
	r.set(org, demoted, orgs.RoleAdmin)
	r.set("org-2", strayID, orgs.RoleAdmin)
	e := &env{t: t, repo: repo, roles: r, enabled: enabled, seats: &seatmatch.MockService{
		MatchWorkspaceFunc: func(context.Context, core.OrgProviderWorkspace) (int, error) { return 0, nil },
	}}
	e.svc = org_provider_account.NewTestOrgProviderAccount(t, org_provider_account.WithOrgProviderAccountDeps(org_provider_account.Deps{
		Store:      repo,
		OrgRollout: rollout,
		Seats:      e.seats,
		ValidateKey: func(_ context.Context, _ models.Family, key string) (bool, string) {
			e.keysSeen = append(e.keysSeen, key)
			if e.reject {
				return false, "the provider rejected this key"
			}
			return true, ""
		},
	}))
	return e
}

// as is user's request through credential c, with control-plane's membership on the context as
// the API server puts it there.
func (e *env) as(user string, c auth.Credential) context.Context {
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, user)
	return auth.WithCredential(authz.WithMembership(ctx, e.roles), c)
}

func (e *env) person(user string) context.Context { return e.as(user, auth.CredentialSession) }

// fixture is what every rpc needs to succeed for an admin.
type fixture struct {
	account, second string // org-1's API-key accounts (anthropic)
	ownTeam         string // the admin's own Claude Team sign-in, in workspace ws-new
	seat            string // the member's Claude Team sign-in, attributed to org-1 through ws-1
}

func (e *env) seed() fixture {
	e.t.Helper()
	ctx := context.Background()
	add := func(key, label string) string {
		owner := core.OrgAccountOwner(org)
		a, _, err := e.repo.UpsertProviderAccount(ctx, core.AccountUpsert{
			Owner: owner, OrgAccess: core.OrgAccessMembers, CreatedByUserID: adminID,
			Provider: "anthropic", AuthKind: core.AuthKindAPIKey,
			IdentityKey: core.APIKeyIdentityKey(owner.Key(), "anthropic", key), KeyHint: core.KeyHint(key),
			Secrets: core.AccountSecrets{Secret: key},
		})
		require.NoError(e.t, err)
		a, err = e.repo.PatchOwnedProviderAccount(ctx, owner, a.ID, core.AccountPatch{Label: &label})
		require.NoError(e.t, err)
		return a.ID
	}
	f := fixture{account: add(orgKey, "Acme prod"), second: add("sk-ant-org-second-key-e5f6", "Acme batch")}
	f.ownTeam = e.claudeSignIn(adminID, "ws-new", "team")
	f.seat = e.claudeSignIn(memberID, "ws-1", "team")
	_, _, err := e.repo.BindOrgProviderWorkspace(ctx, core.OrgProviderWorkspace{
		OrgID: org, Provider: "claude", ExternalOrgID: "ws-1", DisplayName: "Acme Inc.", BoundByUserID: adminID,
	})
	require.NoError(e.t, err)
	_, err = e.repo.ORM().Bun().NewRaw(`UPDATE provider_accounts SET attributed_org_id = ?, attribution_source = 'workspace' WHERE id = ?`,
		org, f.seat).Exec(ctx)
	require.NoError(e.t, err)
	return f
}

// claudeSignIn connects user's Claude sign-in in workspace ws on plan.
func (e *env) claudeSignIn(user, ws, plan string) string {
	e.t.Helper()
	a, _, err := e.repo.UpsertProviderAccount(context.Background(), core.AccountUpsert{
		UserID: user, Provider: "claude", AuthKind: core.AuthKindOAuth,
		IdentityKey: core.ClaudeIdentityKey("acct-"+user, ws), Email: user + "@acme.example",
		ExternalAccountID: "acct-" + user, OrganizationID: ws, OrganizationName: "Workspace " + ws, Plan: plan,
		Secrets: core.AccountSecrets{Secret: "oauth-token-" + user, Refresh: "refresh-" + user},
	})
	require.NoError(e.t, err)
	return a.ID
}

// call is one rpc, made by ctx, returning its response message.
type call struct {
	name string
	run  func(ctx context.Context, svc *org_provider_account.Service, f fixture) (proto.Message, error)
	// adminOnly: a member is refused (everything but the list).
	adminOnly bool
}

func res[T any](r *connect.Response[T], err error) (proto.Message, error) {
	if err != nil {
		return nil, err
	}
	return any(r.Msg).(proto.Message), nil
}

// calls is every rpc of the service with a request an admin's call succeeds on, in an order in
// which one fixture serves them all (the seat is released before its workspace is unbound).
// TestEveryRPCIsInTheMatrix keeps it complete.
var calls = []call{
	{name: "ListOrgProviderAccounts", run: func(ctx context.Context, s *org_provider_account.Service, _ fixture) (proto.Message, error) {
		return res(s.ListOrgProviderAccounts(ctx, connect.NewRequest(&pb.ListOrgProviderAccountsRequest{OrgId: org})))
	}},
	{name: "AddOrgProviderAPIKey", adminOnly: true, run: func(ctx context.Context, s *org_provider_account.Service, _ fixture) (proto.Message, error) {
		return res(s.AddOrgProviderAPIKey(ctx, connect.NewRequest(&pb.AddOrgProviderAPIKeyRequest{
			OrgId: org, Provider: "openai", ApiKey: "sk-openai-org-added-key-g7h8", Label: "Acme OpenAI",
			Access: pb.OrgProviderAccountAccess_ORG_PROVIDER_ACCOUNT_ACCESS_SELECTED,
		})))
	}},
	{name: "RotateOrgProviderAPIKey", adminOnly: true, run: func(ctx context.Context, s *org_provider_account.Service, f fixture) (proto.Message, error) {
		return res(s.RotateOrgProviderAPIKey(ctx, connect.NewRequest(&pb.RotateOrgProviderAPIKeyRequest{OrgId: org, AccountId: f.account, ApiKey: newKey})))
	}},
	{name: "UpdateOrgProviderAccount", adminOnly: true, run: func(ctx context.Context, s *org_provider_account.Service, f fixture) (proto.Message, error) {
		paused, access := true, pb.OrgProviderAccountAccess_ORG_PROVIDER_ACCOUNT_ACCESS_ADMINS
		return res(s.UpdateOrgProviderAccount(ctx, connect.NewRequest(&pb.UpdateOrgProviderAccountRequest{
			OrgId: org, AccountId: f.account, Paused: &paused, Access: &access,
		})))
	}},
	{name: "ReorderOrgProviderAccounts", adminOnly: true, run: func(ctx context.Context, s *org_provider_account.Service, f fixture) (proto.Message, error) {
		return res(s.ReorderOrgProviderAccounts(ctx, connect.NewRequest(&pb.ReorderOrgProviderAccountsRequest{
			OrgId: org, Provider: "anthropic", AccountIds: []string{f.second, f.account},
		})))
	}},
	{name: "SetOrgProviderAccountGrants", adminOnly: true, run: func(ctx context.Context, s *org_provider_account.Service, f fixture) (proto.Message, error) {
		return res(s.SetOrgProviderAccountGrants(ctx, connect.NewRequest(&pb.SetOrgProviderAccountGrantsRequest{
			OrgId: org, AccountId: f.account, UserIds: []string{memberID},
		})))
	}},
	{name: "DeleteOrgProviderAccount", adminOnly: true, run: func(ctx context.Context, s *org_provider_account.Service, f fixture) (proto.Message, error) {
		return res(s.DeleteOrgProviderAccount(ctx, connect.NewRequest(&pb.DeleteOrgProviderAccountRequest{OrgId: org, AccountId: f.second})))
	}},
	{name: "ListOrgProviderWorkspaces", adminOnly: true, run: func(ctx context.Context, s *org_provider_account.Service, _ fixture) (proto.Message, error) {
		return res(s.ListOrgProviderWorkspaces(ctx, connect.NewRequest(&pb.ListOrgProviderWorkspacesRequest{OrgId: org})))
	}},
	{name: "BindOrgProviderWorkspace", adminOnly: true, run: func(ctx context.Context, s *org_provider_account.Service, f fixture) (proto.Message, error) {
		return res(s.BindOrgProviderWorkspace(ctx, connect.NewRequest(&pb.BindOrgProviderWorkspaceRequest{OrgId: org, AccountId: f.ownTeam})))
	}},
	{name: "ReleaseOrgProviderSeat", adminOnly: true, run: func(ctx context.Context, s *org_provider_account.Service, f fixture) (proto.Message, error) {
		return res(s.ReleaseOrgProviderSeat(ctx, connect.NewRequest(&pb.ReleaseOrgProviderSeatRequest{OrgId: org, AccountId: f.seat})))
	}},
	{name: "UnbindOrgProviderWorkspace", adminOnly: true, run: func(ctx context.Context, s *org_provider_account.Service, _ fixture) (proto.Message, error) {
		return res(s.UnbindOrgProviderWorkspace(ctx, connect.NewRequest(&pb.UnbindOrgProviderWorkspaceRequest{OrgId: org, Provider: "claude", ExternalOrgId: "ws-1"})))
	}},
}

func requireRefused(t *testing.T, err error, code connect.Code, reason string, msgAndArgs ...any) {
	t.Helper()
	var ce *connect.Error
	require.True(t, errors.As(err, &ce), "want a connect error, got %v", err)
	require.Equal(t, code, ce.Code(), msgAndArgs...)
	if reason != "" {
		require.Equal(t, reason, ce.Meta().Get(svcerr.ReasonHeader), msgAndArgs...)
	}
}

// The matrix: every rpc for {admin, member, non-member, machine credential, demoted admin}.
// Every call is a person's act, and every admin decision reads the role fresh.
func TestCallerMatrix(t *testing.T) {
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, true)
			f := e.seed()

			// A machine credential is refused as such, an admin's included: the CLI login an
			// agent's shell inherits must not administer the org's money.
			for _, cred := range []auth.Credential{auth.CredentialAccessToken, auth.CredentialOAuthClient, auth.CredentialUnknown} {
				_, err := c.run(e.as(adminID, cred), e.svc, f)
				requireRefused(t, err, connect.CodePermissionDenied, orgs.ReasonHumanOnly, "machine credential %v", cred)
			}

			// A member of another org is not this org's member.
			_, err := c.run(e.person(strayID), e.svc, f)
			if c.adminOnly {
				requireRefused(t, err, connect.CodePermissionDenied, orgs.ReasonOrgAdminRequired, "non-member")
			} else {
				requireRefused(t, err, connect.CodePermissionDenied, orgs.ReasonNotOrgMember, "non-member")
			}

			// A member, and an admin demoted at control-plane a moment ago (the fresh read says
			// member, whatever was true before), may only list.
			e.roles.set(org, demoted, orgs.RoleMember)
			for _, who := range []string{memberID, demoted} {
				got, err := c.run(e.person(who), e.svc, f)
				if c.adminOnly {
					requireRefused(t, err, connect.CodePermissionDenied, orgs.ReasonOrgAdminRequired, who)
					continue
				}
				require.NoError(t, err, who)
				list := got.(*pb.ListOrgProviderAccountsResponse)
				assert.False(t, list.CallerIsAdmin)
				assert.Len(t, list.Accounts, 2)
			}

			// An admin, signed in as a person, succeeds; so does an owner (on a fresh fixture
			// where the admin's call consumed it).
			before := e.roles.fresh
			_, err = c.run(e.person(adminID), e.svc, f)
			require.NoError(t, err, "admin")
			assert.Greater(t, e.roles.fresh, before, "the admin's role is read fresh at the call")
		})
	}
}

// An owner is an admin too.
func TestAnOwnerAdministers(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	f := e.seed()
	resp, err := e.svc.UpdateOrgProviderAccount(e.person("olga"), connect.NewRequest(&pb.UpdateOrgProviderAccountRequest{
		OrgId: org, AccountId: f.account, Label: proto.String("Acme main"),
	}))
	require.NoError(t, err)
	assert.Equal(t, "Acme main", resp.Msg.Account.Account.Label)
}

// TestEveryRPCIsInTheMatrix keeps calls complete: a new rpc must say who may call it.
func TestEveryRPCIsInTheMatrix(t *testing.T) {
	sd := pb.File_reliant_v1_org_provider_account_proto.Services().ByName("OrgProviderAccountService")
	require.NotNil(t, sd)
	want := map[string]bool{}
	for i := 0; i < sd.Methods().Len(); i++ {
		want[string(sd.Methods().Get(i).Name())] = true
	}
	got := map[string]bool{}
	for _, c := range calls {
		got[c.name] = true
	}
	assert.Equal(t, want, got)
}

// No response of any rpc carries a key: the descriptor of every response message (and every
// message inside one) has no field for one, and no string in any response an admin or a member
// receives holds the key. A member's responses hold not even its last four characters.
func TestPlaintextNeverInAnyResponse(t *testing.T) {
	t.Parallel()

	sd := pb.File_reliant_v1_org_provider_account_proto.Services().ByName("OrgProviderAccountService")
	seen := map[protoreflect.FullName]bool{}
	var walk func(md protoreflect.MessageDescriptor)
	walk = func(md protoreflect.MessageDescriptor) {
		if seen[md.FullName()] {
			return
		}
		seen[md.FullName()] = true
		for i := 0; i < md.Fields().Len(); i++ {
			fd := md.Fields().Get(i)
			name := string(fd.Name())
			if name != "masked_key" {
				for _, bad := range []string{"api_key", "secret", "token", "refresh", "plaintext", "key"} {
					assert.NotContains(t, name, bad, "%s.%s looks like it could carry a credential", md.FullName(), name)
				}
			}
			if fd.Message() != nil {
				walk(fd.Message())
			}
		}
	}
	for i := 0; i < sd.Methods().Len(); i++ {
		walk(sd.Methods().Get(i).Output())
	}

	e := newEnv(t, true)
	f := e.seed()
	hint := core.KeyHint(orgKey)
	var adminResponses []proto.Message
	for _, c := range calls {
		got, err := c.run(e.person(adminID), e.svc, f)
		require.NoError(t, err, c.name)
		adminResponses = append(adminResponses, got)
	}
	memberList, err := calls[0].run(e.person(memberID), e.svc, f)
	require.NoError(t, err)

	secrets := []string{orgKey, newKey, "sk-ant-org-second-key-e5f6", "sk-openai-org-added-key-g7h8", "oauth-token-", "refresh-"}
	for _, m := range adminResponses {
		for _, s := range allStrings(m) {
			for _, secret := range secrets {
				assert.NotContains(t, s, secret, "%T", m)
			}
		}
	}
	listed := 0
	for _, s := range allStrings(memberList) {
		for _, secret := range secrets {
			assert.NotContains(t, s, secret)
		}
		assert.NotContains(t, s, hint, "a member never sees a key's hint")
		listed++
	}
	require.Positive(t, listed)
	// And on the fields that could carry it: never the masked key, and never a label
	// (core.ProviderAccount.DisplayLabel's fallback is "<Provider> key …<hint>").
	members := memberList.(*pb.ListOrgProviderAccountsResponse).Accounts
	require.NotEmpty(t, members)
	for _, a := range members {
		assert.Nil(t, a.Account.MaskedKey)
		assert.NotContains(t, a.Account.Label, "…")
		assert.NotContains(t, a.Account.Label, hint)
		assert.Empty(t, a.Grants)
		assert.Nil(t, a.CreatedByUserId)
		assert.Empty(t, a.Account.Windows)
	}
}

// allStrings is every string value anywhere in m: fields, lists and maps, recursively.
func allStrings(m proto.Message) []string {
	var out []string
	var visit func(protoreflect.Message)
	value := func(fd protoreflect.FieldDescriptor, v protoreflect.Value) {
		switch {
		case fd.Kind() == protoreflect.StringKind:
			out = append(out, v.String())
		case fd.Kind() == protoreflect.BytesKind:
			out = append(out, string(v.Bytes()))
		case fd.Message() != nil:
			visit(v.Message())
		}
	}
	visit = func(msg protoreflect.Message) {
		msg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
			switch {
			case fd.IsList():
				for i := 0; i < v.List().Len(); i++ {
					value(fd, v.List().Get(i))
				}
			case fd.IsMap():
				v.Map().Range(func(k protoreflect.MapKey, mv protoreflect.Value) bool {
					out = append(out, k.String())
					value(fd.MapValue(), mv)
					return true
				})
			default:
				value(fd, v)
			}
			return true
		})
	}
	visit(m.ProtoReflect())
	return out
}

// An admin sees the masked key and the grants; adding validates the key first, refuses a bad
// one, and a key the org already holds is the same account.
func TestAddValidatesAndAnAdminSeesTheHint(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	ctx := e.person(adminID)
	add := func(key string) (*pb.AddOrgProviderAPIKeyResponse, error) {
		r, err := e.svc.AddOrgProviderAPIKey(ctx, connect.NewRequest(&pb.AddOrgProviderAPIKeyRequest{
			OrgId: org, Provider: "anthropic", ApiKey: key, Label: "Acme prod",
		}))
		if err != nil {
			return nil, err
		}
		return r.Msg, nil
	}

	e.reject = true
	_, err := add(orgKey)
	requireRefused(t, err, connect.CodeInvalidArgument, "")
	assert.Equal(t, []string{orgKey}, e.keysSeen, "validated before anything is stored")
	accounts, err := e.repo.ListOwnedProviderAccounts(context.Background(), core.OrgAccountOwner(org))
	require.NoError(t, err)
	assert.Empty(t, accounts)

	e.reject = false
	first, err := add(orgKey)
	require.NoError(t, err)
	assert.True(t, first.Created)
	assert.Equal(t, "Acme prod", first.Account.Account.Label)
	assert.Equal(t, "…"+core.KeyHint(orgKey), first.Account.Account.GetMaskedKey())
	assert.Equal(t, pb.OrgProviderAccountAccess_ORG_PROVIDER_ACCOUNT_ACCESS_MEMBERS, first.Account.Access, "everyone in the org is the default")
	assert.Equal(t, adminID, first.Account.GetCreatedByUserId())
	again, err := add(orgKey)
	require.NoError(t, err)
	assert.False(t, again.Created)
	assert.Equal(t, first.Account.Account.Id, again.Account.Account.Id)

	// The org's key is not the admin's: their own list does not show it.
	mine, err := e.repo.ListProviderAccounts(context.Background(), adminID)
	require.NoError(t, err)
	assert.Empty(t, mine)

	for _, bad := range []*pb.AddOrgProviderAPIKeyRequest{
		{OrgId: org, Provider: "claude", ApiKey: orgKey, Label: "x"},   // a sign-in, never an org's
		{OrgId: org, Provider: "anthropic", ApiKey: orgKey, Label: ""}, // label required
		{OrgId: org, Provider: "anthropic", ApiKey: "", Label: "x"},
		{OrgId: "", Provider: "anthropic", ApiKey: orgKey, Label: "x"},
		{OrgId: org, Provider: "anthropic", ApiKey: orgKey, Label: "x", Access: pb.OrgProviderAccountAccess(99)},
	} {
		_, err := e.svc.AddOrgProviderAPIKey(ctx, connect.NewRequest(bad))
		requireRefused(t, err, connect.CodeInvalidArgument, "", "%v", bad)
	}
}

// Rotation keeps the account: its id, label, access and grants; the new key is validated with
// the account's own provider, and its hint replaces the old.
func TestRotateKeepsTheAccount(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false) // never gated
	f := e.seed()
	ctx := e.person(adminID)
	_, err := e.svc.SetOrgProviderAccountGrants(ctx, connect.NewRequest(&pb.SetOrgProviderAccountGrantsRequest{
		OrgId: org, AccountId: f.account, UserIds: []string{memberID},
	}))
	require.NoError(t, err)

	e.reject = true
	_, err = e.svc.RotateOrgProviderAPIKey(ctx, connect.NewRequest(&pb.RotateOrgProviderAPIKeyRequest{OrgId: org, AccountId: f.account, ApiKey: newKey}))
	requireRefused(t, err, connect.CodeInvalidArgument, "")
	e.reject = false
	got, err := e.svc.RotateOrgProviderAPIKey(ctx, connect.NewRequest(&pb.RotateOrgProviderAPIKeyRequest{OrgId: org, AccountId: f.account, ApiKey: newKey}))
	require.NoError(t, err)
	a := got.Msg.Account
	assert.Equal(t, f.account, a.Account.Id)
	assert.Equal(t, "Acme prod", a.Account.Label)
	assert.Equal(t, "…"+core.KeyHint(newKey), a.Account.GetMaskedKey())
	require.Len(t, a.Grants, 1)
	assert.Equal(t, memberID, a.Grants[0].UserId)
	opened, err := e.repo.GetOwnedProviderAccountWithSecrets(context.Background(), core.OrgAccountOwner(org), f.account)
	require.NoError(t, err)
	assert.Equal(t, newKey, opened.Secrets.Secret)

	// The second account's key is not a new key for the first.
	_, err = e.svc.RotateOrgProviderAPIKey(ctx, connect.NewRequest(&pb.RotateOrgProviderAPIKeyRequest{
		OrgId: org, AccountId: f.account, ApiKey: "sk-ant-org-second-key-e5f6",
	}))
	requireRefused(t, err, connect.CodeAlreadyExists, "")
	// Another org's admin reaches nothing of this org: an id from elsewhere is NotFound.
	e.roles.set("org-2", adminID, orgs.RoleAdmin)
	_, err = e.svc.RotateOrgProviderAPIKey(ctx, connect.NewRequest(&pb.RotateOrgProviderAPIKeyRequest{OrgId: "org-2", AccountId: f.account, ApiKey: newKey}))
	requireRefused(t, err, connect.CodeNotFound, "")
}

// The switch gates adding an account and binding a workspace, nothing else: an admin can always
// pause, rotate and delete the org's keys, and unbind and release what it pays for.
func TestOnlyAddingIsGated(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	f := e.seed()
	ctx := e.person(adminID)
	for _, c := range calls {
		_, err := c.run(ctx, e.svc, f)
		switch c.name {
		case "AddOrgProviderAPIKey", "BindOrgProviderWorkspace":
			// The refusal names its reason, so a client says "not enabled here" without
			// reading the message.
			requireRefused(t, err, connect.CodeFailedPrecondition, org_provider_account.ReasonProviderAccountsDisabled, c.name)
		default:
			require.NoError(t, err, "%s is never gated", c.name)
		}
	}
	assert.NotContains(t, e.keysSeen, "sk-openai-org-added-key-g7h8", "a refused add never reaches the provider")
	list, err := e.svc.ListOrgProviderAccounts(ctx, connect.NewRequest(&pb.ListOrgProviderAccountsRequest{OrgId: org}))
	require.NoError(t, err)
	assert.False(t, list.Msg.AddingEnabled)
	assert.True(t, list.Msg.CallerIsAdmin)
}

// A workspace is bound only from the caller's own sign-in on a team plan; another person's
// sign-in is not found, and a workspace that pays for another org is refused.
func TestBindOnlyFromTheCallersOwnTeamPlanAccount(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	f := e.seed()
	ctx := e.person(adminID)
	bind := func(accountID string) (*pb.BindOrgProviderWorkspaceResponse, error) {
		r, err := e.svc.BindOrgProviderWorkspace(ctx, connect.NewRequest(&pb.BindOrgProviderWorkspaceRequest{OrgId: org, AccountId: accountID}))
		if err != nil {
			return nil, err
		}
		return r.Msg, nil
	}

	_, err := bind(f.seat) // the member's own Team sign-in: not the admin's
	requireRefused(t, err, connect.CodeNotFound, "")
	_, err = bind(f.account) // the org's API key is no sign-in, and not the admin's
	requireRefused(t, err, connect.CodeNotFound, "")
	pro := e.claudeSignIn(adminID, "ws-personal", "pro")
	_, err = bind(pro)
	requireRefused(t, err, connect.CodeFailedPrecondition, "", "a Pro plan is no workspace an org pays for")
	ownKey, _, err := e.repo.UpsertProviderAccount(context.Background(), core.AccountUpsert{
		UserID: adminID, Provider: "anthropic", AuthKind: core.AuthKindAPIKey, IdentityKey: "key:own", KeyHint: "9999",
		Secrets: core.AccountSecrets{Secret: "sk-ant-own-9999"},
	})
	require.NoError(t, err)
	_, err = bind(ownKey.ID)
	requireRefused(t, err, connect.CodeFailedPrecondition, "")

	got, err := bind(f.ownTeam)
	require.NoError(t, err)
	assert.True(t, got.Created)
	assert.Equal(t, "ws-new", got.Workspace.ExternalOrgId, "the id is the sign-in's provider-issued workspace")
	assert.Equal(t, "Workspace ws-new", got.Workspace.DisplayName)
	assert.Equal(t, adminID, got.Workspace.BoundByUserId)
	again, err := bind(f.ownTeam)
	require.NoError(t, err)
	assert.False(t, again.Created)
	// Each bind matches the sign-ins already in the workspace to the org.
	matched := e.seats.Calls("MatchWorkspace")
	require.Len(t, matched, 2)
	assert.Equal(t, core.OrgProviderWorkspace{OrgID: org, Provider: "claude", ExternalOrgID: "ws-new"},
		withoutBindMeta(matched[0].Args[1].(core.OrgProviderWorkspace)))

	// Another org's admin, in the same workspace, cannot make it pay for theirs.
	e.roles.set("org-2", "zed", orgs.RoleAdmin)
	theirs := e.claudeSignIn("zed", "ws-new", "enterprise")
	_, err = e.svc.BindOrgProviderWorkspace(e.person("zed"), connect.NewRequest(&pb.BindOrgProviderWorkspaceRequest{OrgId: "org-2", AccountId: theirs}))
	requireRefused(t, err, connect.CodeAlreadyExists, "")

	list, err := e.svc.ListOrgProviderWorkspaces(ctx, connect.NewRequest(&pb.ListOrgProviderWorkspacesRequest{OrgId: org}))
	require.NoError(t, err)
	var ids []string
	for _, w := range list.Msg.Workspaces {
		ids = append(ids, w.ExternalOrgId)
	}
	assert.ElementsMatch(t, []string{"ws-1", "ws-new"}, ids)
	require.Len(t, list.Msg.Seats, 1)
	assert.Equal(t, f.seat, list.Msg.Seats[0].AccountId)
	assert.Equal(t, memberID, list.Msg.Seats[0].UserId)
	assert.Equal(t, "ws-1", list.Msg.Seats[0].GetExternalOrgId())
}

func withoutBindMeta(w core.OrgProviderWorkspace) core.OrgProviderWorkspace {
	return core.OrgProviderWorkspace{OrgID: w.OrgID, Provider: w.Provider, ExternalOrgID: w.ExternalOrgID}
}

// Binding also needs orgs.provider_workspace_seats_enabled: a release a rollback could return to
// may not honour a seat's org yet. Adding an org's own API key does not.
func TestBindNeedsTheSeatsGate(t *testing.T) {
	t.Parallel()
	e := newEnvWith(t, &configv1.OrgsConfig{ProviderAccountsEnabled: true})
	f := e.seed()
	ctx := e.person(adminID)
	_, err := e.svc.BindOrgProviderWorkspace(ctx, connect.NewRequest(&pb.BindOrgProviderWorkspaceRequest{OrgId: org, AccountId: f.ownTeam}))
	requireRefused(t, err, connect.CodeFailedPrecondition, org_provider_account.ReasonProviderSeatsDisabled)
	assert.Zero(t, e.seats.CallCount("MatchWorkspace"))
	_, err = e.svc.AddOrgProviderAPIKey(ctx, connect.NewRequest(&pb.AddOrgProviderAPIKeyRequest{
		OrgId: org, Provider: "openai", Label: "Ops", ApiKey: "sk-openai-org-seats-gate-0001", Access: pb.OrgProviderAccountAccess_ORG_PROVIDER_ACCOUNT_ACCESS_MEMBERS,
	}))
	require.NoError(t, err, "org keys need only provider_accounts_enabled")
}

// A grant names a live member of the org; anyone else is refused before anything is written.
func TestGrantsNameLiveMembersOnly(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	f := e.seed()
	ctx := e.person(adminID)
	_, err := e.svc.SetOrgProviderAccountGrants(ctx, connect.NewRequest(&pb.SetOrgProviderAccountGrantsRequest{
		OrgId: org, AccountId: f.account, UserIds: []string{memberID, strayID},
	}))
	requireRefused(t, err, connect.CodeInvalidArgument, "")
	grants, err := e.repo.ListOrgAccountGrants(context.Background(), org)
	require.NoError(t, err)
	assert.Empty(t, grants)

	got, err := e.svc.SetOrgProviderAccountGrants(ctx, connect.NewRequest(&pb.SetOrgProviderAccountGrantsRequest{
		OrgId: org, AccountId: f.account, UserIds: []string{memberID, " " + memberID + " ", "olga"},
	}))
	require.NoError(t, err)
	var users []string
	for _, g := range got.Msg.Account.Grants {
		users = append(users, g.UserId)
		assert.Equal(t, adminID, g.GrantedByUserId)
	}
	assert.ElementsMatch(t, []string{memberID, "olga"}, users)
}
