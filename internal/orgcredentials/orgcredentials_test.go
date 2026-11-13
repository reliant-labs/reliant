// Copyright (c) 2025 Reliant Labs
package orgcredentials_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/orgcredentials"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

type key struct{ userID, orgID string }

// membership answers from a table; an absent pair is a non-member.
type membership struct {
	answers map[key]accesstokenclient.OrgMembership
	errs    map[key]error
	// during runs inside the call, after the sweep took its "asked" time.
	during func(userID, orgID string)
	asked  []key
}

func (m *membership) ResolveOrgMembership(_ context.Context, userID, orgID string) (accesstokenclient.OrgMembership, error) {
	k := key{userID, orgID}
	m.asked = append(m.asked, k)
	if m.during != nil {
		m.during(userID, orgID)
	}
	if err := m.errs[k]; err != nil {
		return accesstokenclient.OrgMembership{}, err
	}
	if a, ok := m.answers[k]; ok {
		return a, nil
	}
	return accesstokenclient.OrgMembership{OrgID: orgID}, nil
}

type goneOrgs struct {
	gone  map[string]time.Time
	err   error
	asked []string
}

func (g *goneOrgs) ResolveGoneOrgs(_ context.Context, ids []string) ([]accesstokenclient.GoneOrg, error) {
	g.asked = append(g.asked, ids...)
	if g.err != nil {
		return nil, g.err
	}
	var out []accesstokenclient.GoneOrg
	for _, id := range ids {
		if at, ok := g.gone[id]; ok {
			out = append(out, accesstokenclient.GoneOrg{OrgID: id, At: at})
		}
	}
	return out, nil
}

type fixture struct {
	t   *testing.T
	env *storetest.Env
}

func newFixture(t *testing.T) *fixture { return &fixture{t: t, env: storetest.New(t)} }

func (f *fixture) exec(query string, args ...any) {
	f.t.Helper()
	_, err := f.env.SQL.ExecContext(context.Background(), query, args...)
	require.NoError(f.t, err, query)
}

func (f *fixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.env.SQL.QueryRowContext(context.Background(), query, args...).Scan(&n), query)
	return n
}

func (f *fixture) orgKey(id, org string) {
	f.t.Helper()
	f.exec(`INSERT INTO provider_accounts (id, user_id, provider, auth_kind, identity_key, priority, secret_sealed,
		owner_kind, org_id, org_access, created_by_user_id)
		VALUES ($1, 'org:' || $2, 'anthropic', 'api_key', 'key:' || $1,
		(SELECT count(*) FROM provider_accounts WHERE user_id = 'org:' || $2), '\x01', 'org', $2, 'selected', 'admin')`, id, org)
}

func (f *fixture) grant(accountID, org, user string, at time.Time) {
	f.t.Helper()
	f.exec(`INSERT INTO provider_account_grants (account_id, org_id, user_id, granted_by_user_id, granted_at)
		VALUES ($1, $2, $3, 'admin', $4)`, accountID, org, user, at)
}

func (f *fixture) grants() []string {
	f.t.Helper()
	rows, err := f.env.SQL.QueryContext(context.Background(), `SELECT account_id || '/' || user_id FROM provider_account_grants`)
	require.NoError(f.t, err)
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(f.t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(f.t, rows.Err())
	sort.Strings(out)
	return out
}

// A grant is bound to the membership it was made in: once its member rejoins,
// the earlier membership's grants are deleted and the rejoin's own kept; a
// removed member's grants go; a grant made after a negative answer may be a
// rejoin's and is left for the next pass; a membership start the control
// plane cannot name, and an answer about another org, delete nothing.
func TestSweepStaleGrants_KeepsOnlyGrantsOfTheCurrentMembership(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.orgKey("k1", "org-a")
	f.orgKey("k2", "org-a")
	rejoined := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	before, after := rejoined.Add(-time.Hour), rejoined.Add(time.Hour)
	f.grant("k1", "org-a", "rejoiner", before) // the first membership's: void
	f.grant("k2", "org-a", "rejoiner", after)  // made after the rejoin: counts
	f.grant("k1", "org-a", "removed", before)
	f.grant("k1", "org-a", "old-cp", before)
	f.grant("k1", "org-a", "elsewhere", before)
	f.grant("k1", "org-a", "late", before)

	m := &membership{answers: map[key]accesstokenclient.OrgMembership{
		{"rejoiner", "org-a"}:  {OrgID: "org-a", Member: true, Role: "member", MemberSince: rejoined},
		{"old-cp", "org-a"}:    {OrgID: "org-a", Member: true, Role: "member"},
		{"elsewhere", "org-a"}: {OrgID: "org-b", Member: true, Role: "member", MemberSince: after},
	}}
	// "late" is no member when asked, and is granted again while the answer is
	// in flight: a rejoin and a grant the sweep must not judge on that answer.
	regranted := false
	m.during = func(userID, _ string) {
		if userID == "late" && !regranted {
			regranted = true
			f.grant("k2", "org-a", "late", time.Now().Add(time.Second))
		}
	}
	s := orgcredentials.New(orgcredentials.Deps{Store: f.env.Repo, Membership: m, GoneOrgs: &goneOrgs{}})

	n, err := s.SweepStaleGrants(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 3, n)
	assert.Equal(t, []string{"k1/elsewhere", "k1/old-cp", "k2/late", "k2/rejoiner"}, f.grants())

	// A second pass changes nothing.
	n, err = s.SweepStaleGrants(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)
}

// The sweep pages through every member, so a long table is swept whole.
func TestSweepStaleGrants_SweepsEveryPage(t *testing.T) {
	f := newFixture(t)
	f.orgKey("k1", "org-a")
	for i := range 450 {
		f.grant("k1", "org-a", fmt.Sprintf("u%03d", i), time.Now().Add(-time.Hour))
	}
	s := orgcredentials.New(orgcredentials.Deps{Store: f.env.Repo, Membership: &membership{}, GoneOrgs: &goneOrgs{}})
	n, err := s.SweepStaleGrants(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 450, n)
	assert.Empty(t, f.grants())
}

// An outage ends the pass with an error, and a control plane without the rpc
// ends it quietly; neither deletes what it could not judge.
func TestSweepStaleGrants_DeletesNothingItCannotJudge(t *testing.T) {
	for name, err := range map[string]error{
		"outage":      errors.New("control plane unavailable"),
		"unsupported": fmt.Errorf("%w: ResolveOrgMembership", accesstokenclient.ErrUnsupported),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.orgKey("k1", "org-a")
			f.grant("k1", "org-a", "a-first", time.Now().Add(-time.Hour))
			f.grant("k1", "org-a", "b-second", time.Now().Add(-time.Hour))
			m := &membership{errs: map[key]error{{"a-first", "org-a"}: err}}
			s := orgcredentials.New(orgcredentials.Deps{Store: f.env.Repo, Membership: m, GoneOrgs: &goneOrgs{}})
			_, got := s.SweepStaleGrants(context.Background())
			if name == "unsupported" {
				assert.NoError(t, got)
			} else {
				assert.Error(t, got)
			}
			assert.Equal(t, []string{"k1/a-first", "k1/b-second"}, f.grants())
			assert.Equal(t, []key{{"a-first", "org-a"}}, m.asked, "the pass stops at the first failed answer")
		})
	}
}

// An org gone for longer than the grace is purged; one gone more recently, and
// a live one, keep everything.
func TestSweepGoneOrgs_PurgesOnlyOrgsGonePastTheGrace(t *testing.T) {
	f := newFixture(t)
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	for _, org := range []string{"org-old", "org-recent", "org-live"} {
		f.orgKey("k-"+org, org)
		f.exec(`INSERT INTO vault_keys (id, tenant_kind, tenant_id, version, kek_id, wrapped_dek, state)
			VALUES ('vk-' || $1, 'org', $1, 1, 'v1', '\x01', 'primary')`, org)
	}
	g := &goneOrgs{gone: map[string]time.Time{
		"org-old":    now.Add(-orgcredentials.GoneOrgGrace - time.Minute),
		"org-recent": now.Add(-orgcredentials.GoneOrgGrace + time.Minute),
	}}
	s := orgcredentials.New(orgcredentials.Deps{Store: f.env.Repo, Membership: &membership{}, GoneOrgs: g, Now: func() time.Time { return now }})

	purged, err := s.SweepGoneOrgs(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 1, purged.ProviderAccounts)
	assert.EqualValues(t, 1, purged.VaultKeys)
	assert.ElementsMatch(t, []string{"org-live", "org-old", "org-recent"}, g.asked, "every holder is asked about")
	assert.Equal(t, 0, f.count(`SELECT count(*) FROM provider_accounts WHERE org_id = 'org-old'`))
	assert.Equal(t, 0, f.count(`SELECT count(*) FROM vault_keys WHERE tenant_id = 'org-old'`))
	assert.Equal(t, 2, f.count(`SELECT count(*) FROM provider_accounts WHERE org_id IN ('org-recent', 'org-live')`))
	assert.Equal(t, 2, f.count(`SELECT count(*) FROM vault_keys WHERE tenant_id IN ('org-recent', 'org-live')`))
}

// A control plane that cannot say which orgs are gone purges nothing.
func TestSweepGoneOrgs_PurgesNothingWithoutAnAnswer(t *testing.T) {
	for name, err := range map[string]error{
		"outage":      errors.New("control plane unavailable"),
		"unsupported": fmt.Errorf("%w: ResolveGoneOrgs", accesstokenclient.ErrUnsupported),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.orgKey("k-1", "org-1")
			s := orgcredentials.New(orgcredentials.Deps{Store: f.env.Repo, Membership: &membership{}, GoneOrgs: &goneOrgs{err: err}})
			_, got := s.SweepGoneOrgs(context.Background())
			if name == "unsupported" {
				assert.NoError(t, got)
			} else {
				assert.Error(t, got)
			}
			assert.Equal(t, 1, f.count(`SELECT count(*) FROM provider_accounts WHERE org_id = 'org-1'`))
		})
	}
}
