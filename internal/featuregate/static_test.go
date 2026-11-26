// Copyright (c) 2026 Reliant Labs

package featuregate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"

	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/pkg/config"
)

const (
	tenantZero = "55949942-3ee9-4cfc-bb7c-817bdba75186"
	otherOrg   = "org-not-listed"
	secondOrg  = "org-second"
	member     = "user-member"
	outsider   = "user-outsider"
)

// fakeMembership answers from a set of "user/org" pairs; err, when set, is
// the outage every call returns, and errOrgs fails just those orgs.
type fakeMembership struct {
	members map[string]bool
	noOrgs  bool
	err     error
	errOrgs map[string]bool
	calls   int
}

func (f *fakeMembership) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	f.calls++
	if f.err != nil || f.errOrgs[orgID] {
		return false, false, errors.New("control plane unreachable")
	}
	if f.noOrgs {
		return false, false, nil
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

// A non-listed org is refused before membership is asked, so a refusal costs
// no control-plane call.
func TestStaticGateAllowedChecksTheListFirst(t *testing.T) {
	m := &fakeMembership{members: map[string]bool{member + "/" + otherOrg: true}}
	gate := featuregate.NewStatic(gtmConfig(tenantZero), m)
	got, err := gate.Allowed(context.Background(), featuregate.GTM, member, otherOrg)
	require.NoError(t, err)
	assert.False(t, got)
	assert.Zero(t, m.calls)
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

// GTM_ALLOWED_ORG_IDS reaches GtmConfig through the root config's loader,
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
			cmd := &cobra.Command{}
			config.RegisterFlags(cmd)
			cfg, err := config.Load(cmd)
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
	cmd := &cobra.Command{}
	config.RegisterFlags(cmd)
	cfg, err := config.Load(cmd)
	require.NoError(t, err)
	assert.Equal(t, "tok", cfg.GetGtm().GetAnalyticsToken())
}
