// Copyright (c) 2025 Reliant Labs
package seatmatch_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/seatmatch"
)

var workspaceA = core.OrgProviderWorkspace{OrgID: "org-1", Provider: "claude", ExternalOrgID: "ws-a"}

func signIn(id, user, workspace string) core.ProviderAccount {
	return core.ProviderAccount{ID: id, UserID: user, Provider: "claude", AuthKind: core.AuthKindOAuth, OrganizationID: workspace}
}

func seat(id, user, workspace, org, source string) core.ProviderAccount {
	a := signIn(id, user, workspace)
	a.AttributedOrgID, a.AttributionSource = org, source
	return a
}

// fixture is a store holding accounts and bindings, members by org, and the gate.
type fixture struct {
	store    *seatmatch.MockStore
	members  *seatmatch.MockMembers
	bindings map[string]core.OrgProviderWorkspace // by external id
	accounts []core.ProviderAccount
	live     map[string]bool // "user/org"
	enabled  bool
}

func newFixture() *fixture {
	f := &fixture{bindings: map[string]core.OrgProviderWorkspace{"ws-a": workspaceA}, live: map[string]bool{}, enabled: true}
	f.store = &seatmatch.MockStore{
		ListProviderAccountsFunc: func(_ context.Context, userID string) ([]core.ProviderAccount, error) {
			var out []core.ProviderAccount
			for _, a := range f.accounts {
				if a.UserID == userID {
					out = append(out, a)
				}
			}
			return out, nil
		},
		ProviderWorkspaceBindingFunc: func(_ context.Context, provider, ext string) (*core.OrgProviderWorkspace, error) {
			if b, ok := f.bindings[ext]; ok && b.Provider == provider {
				return &b, nil
			}
			return nil, nil
		},
		ListWorkspaceSignInsFunc: func(_ context.Context, provider, ext string) ([]core.ProviderAccount, error) {
			var out []core.ProviderAccount
			for _, a := range f.accounts {
				if a.Provider == provider && core.ProviderWorkspaceID(a) == ext {
					out = append(out, a)
				}
			}
			return out, nil
		},
		AttributeWorkspaceSeatFunc: func(_ context.Context, w core.OrgProviderWorkspace, id string) (bool, error) {
			return true, nil
		},
		ClearWorkspaceSeatFunc: func(_ context.Context, orgID, id string) (bool, error) { return true, nil },
	}
	f.members = &seatmatch.MockMembers{IsMemberFunc: func(_ context.Context, userID, orgID string) (bool, bool, error) {
		return f.live[userID+"/"+orgID], true, nil
	}}
	return f
}

func (f *fixture) service(t *testing.T) seatmatch.Service {
	t.Helper()
	svc, err := seatmatch.New(seatmatch.Deps{
		Store: f.store, Members: f.members,
		Rollout: &seatmatch.MockRollout{GetProviderWorkspaceSeatsEnabledFunc: func() bool { return f.enabled }},
	})
	require.NoError(t, err)
	return svc
}

func TestMatchAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("a live member's sign-in in a bound workspace becomes the org's seat", func(t *testing.T) {
		t.Parallel()
		f := newFixture()
		f.accounts = []core.ProviderAccount{signIn("a1", "dana", "ws-a")}
		f.live["dana/org-1"] = true
		out, err := f.service(t).MatchAccount(ctx, "dana", "a1")
		require.NoError(t, err)
		assert.Equal(t, seatmatch.Attributed, out)
		require.Equal(t, 1, f.store.CallCount("AttributeWorkspaceSeat"))
	})

	t.Run("nothing is attributed", func(t *testing.T) {
		t.Parallel()
		cases := map[string]func(f *fixture){
			"not a member":    func(f *fixture) {},
			"the gate is off": func(f *fixture) { f.live["dana/org-1"], f.enabled = true, false },
			"no binding":      func(f *fixture) { f.live["dana/org-1"] = true; delete(f.bindings, "ws-a") },
			"another org's sign-in": func(f *fixture) {
				f.live["dana/org-1"] = true
				f.accounts[0].AttributedOrgID = "org-2"
				f.accounts[0].AttributionSource = core.AttributionManaged
			},
			"an API key":               func(f *fixture) { f.live["dana/org-1"] = true; f.accounts[0].AuthKind = core.AuthKindAPIKey },
			"another person's account": func(f *fixture) { f.live["dana/org-1"] = true; f.accounts[0].UserID = "eve" },
			"a deployment with no orgs": func(f *fixture) {
				f.members.IsMemberFunc = func(context.Context, string, string) (bool, bool, error) { return false, false, nil }
			},
		}
		for name, setup := range cases {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				f := newFixture()
				f.accounts = []core.ProviderAccount{signIn("a1", "dana", "ws-a")}
				setup(f)
				out, err := f.service(t).MatchAccount(ctx, "dana", "a1")
				require.NoError(t, err)
				assert.Equal(t, seatmatch.Unchanged, out)
				assert.Zero(t, f.store.CallCount("AttributeWorkspaceSeat"))
				assert.Zero(t, f.store.CallCount("ClearWorkspaceSeat"))
			})
		}
	})

	t.Run("a membership outage attributes nothing and says so", func(t *testing.T) {
		t.Parallel()
		f := newFixture()
		f.accounts = []core.ProviderAccount{signIn("a1", "dana", "ws-a")}
		f.members.IsMemberFunc = func(context.Context, string, string) (bool, bool, error) {
			return false, true, errors.New("control plane unavailable")
		}
		out, err := f.service(t).MatchAccount(ctx, "dana", "a1")
		require.Error(t, err)
		assert.Equal(t, seatmatch.Unchanged, out)
		assert.Zero(t, f.store.CallCount("AttributeWorkspaceSeat"))
	})

	t.Run("a seat stays the org's while its org's binding covers it, member or not", func(t *testing.T) {
		t.Parallel()
		f := newFixture()
		f.enabled = false // keeping a seat is not attributing one
		f.accounts = []core.ProviderAccount{seat("a1", "dana", "ws-a", "org-1", core.AttributionWorkspace)}
		out, err := f.service(t).MatchAccount(ctx, "dana", "a1")
		require.NoError(t, err)
		assert.Equal(t, seatmatch.Unchanged, out)
		assert.Zero(t, f.store.CallCount("ClearWorkspaceSeat"))
		assert.Zero(t, f.members.CallCount("IsMember"), "a seat outlives its holder's membership (§6)")
	})

	t.Run("a seat no binding of its org covers is cleared, with the gate off too", func(t *testing.T) {
		t.Parallel()
		for name, setup := range map[string]func(f *fixture){
			"binding gone": func(f *fixture) { delete(f.bindings, "ws-a") },
			"bound to another org now": func(f *fixture) {
				f.bindings["ws-a"] = core.OrgProviderWorkspace{OrgID: "org-2", Provider: "claude", ExternalOrgID: "ws-a"}
			},
			"moved workspace": func(f *fixture) { f.accounts[0].OrganizationID = "ws-b" },
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				f := newFixture()
				f.enabled = false
				f.accounts = []core.ProviderAccount{seat("a1", "dana", "ws-a", "org-1", core.AttributionWorkspace)}
				setup(f)
				out, err := f.service(t).MatchAccount(ctx, "dana", "a1")
				require.NoError(t, err)
				assert.Equal(t, seatmatch.Cleared, out)
				calls := f.store.Calls("ClearWorkspaceSeat")
				require.Len(t, calls, 1)
				assert.Equal(t, "org-1", calls[0].Args[1], "cleared from the org that held it")
			})
		}
	})

	t.Run("an approved seat and the managed key are not the matcher's", func(t *testing.T) {
		t.Parallel()
		for _, source := range []string{core.AttributionApproved, core.AttributionManaged} {
			f := newFixture()
			delete(f.bindings, "ws-a")
			f.accounts = []core.ProviderAccount{seat("a1", "dana", "ws-a", "org-1", source)}
			out, err := f.service(t).MatchAccount(ctx, "dana", "a1")
			require.NoError(t, err)
			assert.Equal(t, seatmatch.Unchanged, out, source)
			assert.Zero(t, f.store.CallCount("ClearWorkspaceSeat"), source)
		}
	})
}

func TestMatchWorkspace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	f := newFixture()
	f.accounts = []core.ProviderAccount{
		signIn("dana-1", "dana", "ws-a"),
		signIn("dana-2", "dana", "ws-a"),
		signIn("eve-1", "eve", "ws-a"),                                   // not a member
		signIn("fay-1", "fay", "ws-a"),                                   // the store refuses it (released)
		seat("gus-1", "gus", "ws-a", "org-1", core.AttributionWorkspace), // already the org's
		signIn("hal-1", "hal", "ws-b"),                                   // another workspace
	}
	f.live["dana/org-1"], f.live["fay/org-1"], f.live["gus/org-1"], f.live["hal/org-1"] = true, true, true, true
	f.store.AttributeWorkspaceSeatFunc = func(_ context.Context, _ core.OrgProviderWorkspace, id string) (bool, error) {
		return id != "fay-1", nil
	}
	n, err := f.service(t).MatchWorkspace(ctx, workspaceA)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	var tried []string
	for _, c := range f.store.Calls("AttributeWorkspaceSeat") {
		tried = append(tried, c.Args[2].(string))
	}
	assert.ElementsMatch(t, []string{"dana-1", "dana-2", "fay-1"}, tried)

	t.Run("an outage for one member does not stop the others", func(t *testing.T) {
		t.Parallel()
		f := newFixture()
		f.accounts = []core.ProviderAccount{signIn("dana-1", "dana", "ws-a"), signIn("eve-1", "eve", "ws-a")}
		f.members.IsMemberFunc = func(_ context.Context, userID, _ string) (bool, bool, error) {
			if userID == "dana" {
				return false, true, errors.New("control plane unavailable")
			}
			return true, true, nil
		}
		n, err := f.service(t).MatchWorkspace(ctx, workspaceA)
		require.Error(t, err)
		assert.Equal(t, 1, n)
	})

	t.Run("the gate off matches nothing", func(t *testing.T) {
		t.Parallel()
		f := newFixture()
		f.enabled = false
		f.accounts = []core.ProviderAccount{signIn("dana-1", "dana", "ws-a")}
		f.live["dana/org-1"] = true
		n, err := f.service(t).MatchWorkspace(ctx, workspaceA)
		require.NoError(t, err)
		assert.Zero(t, n)
		assert.Zero(t, f.store.CallCount("ListWorkspaceSignIns"))
	})
}

func TestNewRequiresAStore(t *testing.T) {
	t.Parallel()
	_, err := seatmatch.New(seatmatch.Deps{})
	require.Error(t, err)
}
