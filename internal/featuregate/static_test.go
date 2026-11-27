// Copyright (c) 2026 Reliant Labs

package featuregate_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/pkg/config"
)

const (
	tenantZero = "55949942-3ee9-4cfc-bb7c-817bdba75186"
	otherOrg   = "org-not-listed"
	secondOrg  = "org-second"
	member     = "user-member"
	outsider   = "user-outsider"
)

// *orgs.Resolver is the membership internal/app hands NewStatic.
var _ featuregate.Membership = (*orgs.Resolver)(nil)

// fakeMembership answers from a set of "user/org" pairs; err, when set, is
// the outage every call returns, and errOrgs fails just those orgs. noOrgs
// answers "no answer" (ok=false); memberNotOK answers member=true with
// ok=false, which is still no answer; block waits for the context to end.
type fakeMembership struct {
	members     map[string]bool
	noOrgs      bool
	memberNotOK bool
	block       bool
	err         error
	errOrgs     map[string]bool
	calls       int
}

func (f *fakeMembership) IsMember(ctx context.Context, userID, orgID string) (bool, bool, error) {
	f.calls++
	if f.block {
		<-ctx.Done()
		return false, false, ctx.Err()
	}
	if f.err != nil || f.errOrgs[orgID] {
		return false, false, errors.New("control plane unreachable")
	}
	if f.noOrgs {
		return false, false, nil
	}
	if f.memberNotOK {
		return true, false, nil
	}
	return f.members[userID+"/"+orgID], true, nil
}

func gtmConfig(ids ...string) *configv1.GtmConfig {
	return &configv1.GtmConfig{AllowedOrgIds: ids}
}

func TestStaticGateAllowed(t *testing.T) {
	ctx := context.Background()
	membersOf := func() *fakeMembership {
		return &fakeMembership{members: map[string]bool{
			member + "/" + tenantZero: true,
			member + "/" + otherOrg:   true,
		}}
	}
	cases := []struct {
		name       string
		cfg        *configv1.GtmConfig
		membership featuregate.Membership
		feature    featuregate.Feature
		user, org  string
		want       bool
		wantErr    bool
	}{
		{name: "allowlisted member", cfg: gtmConfig(tenantZero), membership: membersOf(), feature: featuregate.GTM, user: member, org: tenantZero, want: true},
		{name: "allowlisted non-member", cfg: gtmConfig(tenantZero), membership: membersOf(), feature: featuregate.GTM, user: outsider, org: tenantZero},
		{name: "member of a non-listed org", cfg: gtmConfig(tenantZero), membership: membersOf(), feature: featuregate.GTM, user: member, org: otherOrg},
		{name: "empty user id", cfg: gtmConfig(tenantZero), membership: membersOf(), feature: featuregate.GTM, user: "", org: tenantZero},
		{name: "empty org id", cfg: gtmConfig(tenantZero), membership: membersOf(), feature: featuregate.GTM, user: member, org: ""},
		{name: "empty allowlist", cfg: gtmConfig(), membership: membersOf(), feature: featuregate.GTM, user: member, org: tenantZero},
		{name: "blank-only allowlist", cfg: gtmConfig(" ", ""), membership: membersOf(), feature: featuregate.GTM, user: member, org: tenantZero},
		{name: "nil config", cfg: nil, membership: membersOf(), feature: featuregate.GTM, user: member, org: tenantZero},
		{name: "nil membership", cfg: gtmConfig(tenantZero), membership: nil, feature: featuregate.GTM, user: member, org: tenantZero},
		{name: "membership outage", cfg: gtmConfig(tenantZero), membership: &fakeMembership{err: errors.New("down")}, feature: featuregate.GTM, user: member, org: tenantZero, wantErr: true},
		{name: "deployment without orgs", cfg: gtmConfig(tenantZero), membership: &fakeMembership{noOrgs: true}, feature: featuregate.GTM, user: member, org: tenantZero},
		{name: "member but no answer (ok=false)", cfg: gtmConfig(tenantZero), membership: &fakeMembership{memberNotOK: true}, feature: featuregate.GTM, user: member, org: tenantZero},
		{name: "unknown feature", cfg: gtmConfig(tenantZero), membership: membersOf(), feature: "other", user: member, org: tenantZero},
		{name: "untrimmed allowlist entry", cfg: gtmConfig(" " + tenantZero + " "), membership: membersOf(), feature: featuregate.GTM, user: member, org: tenantZero, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := featuregate.NewStatic(tc.cfg, tc.membership)
			got, err := gate.Allowed(ctx, tc.feature, tc.user, tc.org)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// Membership is checked before the allowlist, so a refusal looks the same for
// a listed and an unlisted org: nothing about it reveals which orgs have the
// feature. A credential bound to another org is refused, and recorded as
// refused, for both alike.
func TestStaticGateAllowedRefusalIsUniform(t *testing.T) {
	members := map[string]bool{member + "/" + tenantZero: true, member + "/" + otherOrg: true}

	t.Run("membership is asked for an unlisted org too", func(t *testing.T) {
		m := &fakeMembership{members: members}
		got, err := featuregate.NewStatic(gtmConfig(tenantZero), m).Allowed(context.Background(), featuregate.GTM, member, otherOrg)
		require.NoError(t, err)
		assert.False(t, got)
		assert.Equal(t, 1, m.calls)
	})

	for _, org := range []string{tenantZero, otherOrg} {
		t.Run("credential bound to another org, asking "+org, func(t *testing.T) {
			ctx := authz.WithCredentialOrg(context.Background(), secondOrg)
			gate := featuregate.NewStatic(gtmConfig(tenantZero), &fakeMembership{members: members})
			got, err := gate.Allowed(ctx, featuregate.GTM, member, org)
			require.NoError(t, err)
			assert.False(t, got)
			bound, refused, ok := authz.CredentialRefusal(ctx)
			assert.True(t, ok, "the refusal is recorded the same way for a listed and an unlisted org")
			assert.Equal(t, secondOrg, bound)
			assert.Equal(t, org, refused)
		})
	}

	t.Run("credential bound to the org itself", func(t *testing.T) {
		ctx := authz.WithCredentialOrg(context.Background(), tenantZero)
		got, err := featuregate.NewStatic(gtmConfig(tenantZero), &fakeMembership{members: members}).Allowed(ctx, featuregate.GTM, member, tenantZero)
		require.NoError(t, err)
		assert.True(t, got)
	})
}

func TestStaticGateAllowedForUser(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		cfg        *configv1.GtmConfig
		membership featuregate.Membership
		user       string
		want       bool
		wantErr    bool
	}{
		{name: "member of a listed org", cfg: gtmConfig(tenantZero, secondOrg), membership: &fakeMembership{members: map[string]bool{member + "/" + secondOrg: true}}, user: member, want: true},
		{name: "member of no listed org", cfg: gtmConfig(tenantZero), membership: &fakeMembership{members: map[string]bool{member + "/" + otherOrg: true}}, user: member},
		{name: "empty user id", cfg: gtmConfig(tenantZero), membership: &fakeMembership{members: map[string]bool{"/" + tenantZero: true}}, user: ""},
		{name: "empty allowlist", cfg: gtmConfig(), membership: &fakeMembership{members: map[string]bool{member + "/" + tenantZero: true}}, user: member},
		{name: "nil membership", cfg: gtmConfig(tenantZero), membership: nil, user: member},
		{name: "outage", cfg: gtmConfig(tenantZero), membership: &fakeMembership{err: errors.New("down")}, user: member, wantErr: true},
		{name: "outage on one org, member of another", cfg: gtmConfig(tenantZero, secondOrg), membership: &fakeMembership{errOrgs: map[string]bool{tenantZero: true}, members: map[string]bool{member + "/" + secondOrg: true}}, user: member, want: true},
		{name: "outage on one org, member of none", cfg: gtmConfig(tenantZero, secondOrg), membership: &fakeMembership{errOrgs: map[string]bool{tenantZero: true}}, user: member, wantErr: true},
		{name: "deployment without orgs", cfg: gtmConfig(tenantZero), membership: &fakeMembership{noOrgs: true}, user: member},
		{name: "member but no answer (ok=false)", cfg: gtmConfig(tenantZero, secondOrg), membership: &fakeMembership{memberNotOK: true}, user: member},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := featuregate.NewStatic(tc.cfg, tc.membership)
			got, err := gate.AllowedForUser(ctx, featuregate.GTM, tc.user)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// AllowedForUser asks the same check as Allowed, so a credential bound to an
// unlisted org is entitled to nothing, while one bound to a listed org the
// user belongs to is.
func TestStaticGateAllowedForUserRespectsTheCredentialsOrg(t *testing.T) {
	m := &fakeMembership{members: map[string]bool{member + "/" + tenantZero: true, member + "/" + otherOrg: true}}
	gate := featuregate.NewStatic(gtmConfig(tenantZero), m)

	ctx := authz.WithCredentialOrg(context.Background(), otherOrg)
	got, err := gate.AllowedForUser(ctx, featuregate.GTM, member)
	require.NoError(t, err)
	assert.False(t, got, "a token bound to an unlisted org")
	// The refusal of the listed org is not recorded on the caller's ctx: the
	// edge would report it, naming an allowlisted org the caller never asked
	// about, on any later refusal of the same request.
	bound, refused, ok := authz.CredentialRefusal(ctx)
	assert.False(t, ok, "refused %q", refused)
	assert.Equal(t, otherOrg, bound)

	got, err = gate.AllowedForUser(authz.WithCredentialOrg(context.Background(), tenantZero), featuregate.GTM, member)
	require.NoError(t, err)
	assert.True(t, got, "a token bound to the listed org")
}

// A membership check that does not answer in time is "no", with the outage
// as the error (the fake blocks until its context ends, so returning at all
// proves the bound), and AllowedForUser stops asking once its overall
// deadline passes rather than waiting on every listed org in turn.
func TestStaticGateTimeouts(t *testing.T) {
	featuregate.SetTimeoutsForTest(t, 20*time.Millisecond, 50*time.Millisecond)

	t.Run("Allowed", func(t *testing.T) {
		m := &fakeMembership{block: true}
		got, err := featuregate.NewStatic(gtmConfig(tenantZero), m).Allowed(context.Background(), featuregate.GTM, member, tenantZero)
		assert.False(t, got)
		require.ErrorIs(t, err, authz.ErrMembershipUnavailable)
	})

	t.Run("AllowedForUser", func(t *testing.T) {
		orgs := make([]string, 10)
		for i := range orgs {
			orgs[i] = fmt.Sprintf("org-%d", i)
		}
		m := &fakeMembership{block: true}
		got, err := featuregate.NewStatic(gtmConfig(orgs...), m).AllowedForUser(context.Background(), featuregate.GTM, member)
		assert.False(t, got)
		require.Error(t, err)
		assert.Less(t, m.calls, len(orgs), "the orgs left once the deadline passed are skipped")
	})
}

func TestDenyAll(t *testing.T) {
	gate := featuregate.DenyAll()
	got, err := gate.Allowed(context.Background(), featuregate.GTM, member, tenantZero)
	require.NoError(t, err)
	assert.False(t, got)
	got, err = gate.AllowedForUser(context.Background(), featuregate.GTM, member)
	require.NoError(t, err)
	assert.False(t, got)
}

func TestParseOrgIDs(t *testing.T) {
	assert.Equal(t, []string{"a", "b"}, featuregate.ParseOrgIDs([]string{" a", "", " ", "b ", "a"}))
	assert.Empty(t, featuregate.ParseOrgIDs(nil))
}

// GTM_ALLOWED_ORG_IDS reaches GtmConfig through the server roles' loader
// (ServerProcessConfig),
// which trims each comma-separated element and drops blank ones, as it does
// ALLOWED_EMAIL_DOMAINS.
func TestAllowedOrgIDsEnv(t *testing.T) {
	for raw, want := range map[string][]string{
		"":                        nil,
		",":                       nil,
		tenantZero:                {tenantZero},
		" " + tenantZero + " ,":   {tenantZero},
		tenantZero + ",," + "b":   {tenantZero, "b"},
		tenantZero + ", b , ,c ,": {tenantZero, "b", "c"},
	} {
		t.Run("GTM_ALLOWED_ORG_IDS="+raw, func(t *testing.T) {
			t.Setenv("GTM_ALLOWED_ORG_IDS", raw)
			t.Setenv("NATS_URL", "nats://127.0.0.1:4222") // required by every server role
			cmd := &cobra.Command{}
			config.RegisterServerProcessConfigFlags(cmd)
			cfg, err := config.LoadServerProcessConfig(cmd)
			require.NoError(t, err)
			if want == nil {
				assert.Empty(t, cfg.GetGtm().GetAllowedOrgIds())
				return
			}
			assert.Equal(t, want, cfg.GetGtm().GetAllowedOrgIds())
		})
	}
}

// GTM_ANALYTICS_TOKEN, a secret, loads from the environment.
func TestAnalyticsTokenEnv(t *testing.T) {
	t.Setenv("GTM_ANALYTICS_TOKEN", "tok")
	t.Setenv("NATS_URL", "nats://127.0.0.1:4222") // required by every server role
	cmd := &cobra.Command{}
	config.RegisterServerProcessConfigFlags(cmd)
	cfg, err := config.LoadServerProcessConfig(cmd)
	require.NoError(t, err)
	assert.Equal(t, "tok", cfg.GetGtm().GetAnalyticsToken())
}
