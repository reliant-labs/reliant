package inputoptions_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/inputoptions"
	"github.com/reliant-labs/reliant/internal/integrations/inputoptions/inputoptionstest"
)

// These run the service over the SHIPPED github manifest: its real types, its
// real list actions through the real declarative runner, its real CEL. Only
// GitHub is a stand-in (an httptest fake).

func githubAccounts() map[string]*inputoptionstest.Account {
	return map[string]*inputoptionstest.Account{
		"alice": {
			Installations: []inputoptionstest.Installation{
				{ID: 11, Account: "acme", Repos: []inputoptionstest.Repo{
					{FullName: "acme/web", Private: true, Push: true},
					{FullName: "acme/docs", Push: false},
				}},
				{ID: 22, Account: "globex", Repos: []inputoptionstest.Repo{{FullName: "globex/site", Push: true}}},
			},
			Labels: map[string][]string{"acme/web": {"bug", "needs-triage"}},
		},
		"bob": {
			Installations: []inputoptionstest.Installation{{ID: 33, Account: "bobco", Repos: []inputoptionstest.Repo{{FullName: "bobco/private", Push: true}}}},
		},
	}
}

func TestGitHubRepositoriesAreListedAsTheCaller(t *testing.T) {
	f := inputoptionstest.NewFake(t, githubAccounts())
	svc := f.Service()
	ctx := context.Background()

	res, err := svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "github.repository"})
	require.NoError(t, err)
	assert.Equal(t, inputoptions.StateConnected, res.State)
	assert.Equal(t, []string{"acme/web", "acme/docs", "globex/site"}, values(res.Options))
	web := res.Options[0]
	assert.Equal(t, "acme/web", web.Label)
	assert.Equal(t, "private", web.Description)
	assert.Equal(t, "acme", web.Group)
	assert.Equal(t, true, web.Annotations["can_push"])
	assert.Equal(t, 11.0, web.Annotations["installation_id"])
	assert.Equal(t, "public", res.Options[1].Description)
	assert.Equal(t, "globex", res.Options[2].Group)

	// Only alice's token ever reached GitHub, and bob sees only his own.
	assert.Equal(t, map[string]bool{"tok-alice": true}, set(f.Tokens()))
	bob, err := svc.List(ctx, inputoptions.ListRequest{UserID: "bob", Type: "github.repository"})
	require.NoError(t, err)
	assert.Equal(t, []string{"bobco/private"}, values(bob.Options))
}

func TestGitHubRepositoryWarningsAreTheBotsToCarry(t *testing.T) {
	f := inputoptionstest.NewFake(t, githubAccounts())
	svc := f.Service()
	ctx := context.Background()

	owner, err := svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "github.repository"})
	require.NoError(t, err)
	for _, o := range owner.Options {
		assert.Empty(t, o.Warnings, "the owner's own access is the user's own: %s", o.Value)
	}

	app, err := svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "github.repository", ActingIdentity: "app"})
	require.NoError(t, err)
	byValue := map[string][]string{}
	for _, o := range app.Options {
		byValue[o.Value] = o.Warnings
	}
	assert.Empty(t, byValue["acme/web"])
	assert.Equal(t, []string{"The Reliant bot can only write where you have push access, so it will not act in this repository."}, byValue["acme/docs"])
}

func TestGitHubRepositoryScopedToOneInstallation(t *testing.T) {
	f := inputoptionstest.NewFake(t, githubAccounts())
	svc := f.Service()
	res, err := svc.List(context.Background(), inputoptions.ListRequest{UserID: "alice", Type: "github.repository", Scope: map[string]any{"installation": "22"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"globex/site"}, values(res.Options))
	for _, r := range f.Requests() {
		assert.NotContains(t, r, "/installations/11/", "the other installation is not walked")
	}

	_, err = svc.List(context.Background(), inputoptions.ListRequest{UserID: "alice", Type: "github.repository", Scope: map[string]any{"installation": "not-a-number"}})
	require.ErrorIs(t, err, inputoptions.ErrBadScope)
}

func TestGitHubRepositoryPagesAndSearchesClientSide(t *testing.T) {
	f := inputoptionstest.NewFake(t, githubAccounts())
	svc := f.Service()
	ctx := context.Background()

	p1, err := svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "github.repository", PageSize: 2})
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/web", "acme/docs"}, values(p1.Options))
	require.NotEmpty(t, p1.NextPageToken)
	p2, err := svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "github.repository", PageSize: 2, PageToken: p1.NextPageToken})
	require.NoError(t, err)
	assert.Equal(t, []string{"globex/site"}, values(p2.Options))
	assert.Empty(t, p2.NextPageToken)

	hit, err := svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "github.repository", Query: "GLOBEX"})
	require.NoError(t, err)
	assert.Equal(t, []string{"globex/site"}, values(hit.Options))

	// All of it from one walk of the installations: 1 + 2 requests.
	assert.Len(t, f.Requests(), 3)
}

func TestGitHubCacheKeepsTypingOffGitHubAndIsPerUser(t *testing.T) {
	f := inputoptionstest.NewFake(t, githubAccounts())
	svc := f.Service()
	ctx := context.Background()
	for _, q := range []string{"a", "ac", "acm", "acme"} {
		_, err := svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "github.repository", Query: q})
		require.NoError(t, err)
	}
	assert.Len(t, f.Requests(), 3, "four keystrokes, one walk")

	f.Reset()
	_, err := svc.List(ctx, inputoptions.ListRequest{UserID: "bob", Type: "github.repository", Query: "a"})
	require.NoError(t, err)
	assert.NotEmpty(t, f.Requests(), "bob never reads alice's cached listing")
	assert.Equal(t, map[string]bool{"tok-bob": true}, set(f.Tokens()))
}

func TestGitHubLabelsAreScopedByRepository(t *testing.T) {
	f := inputoptionstest.NewFake(t, githubAccounts())
	svc := f.Service()
	ctx := context.Background()

	_, err := svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "github.label"})
	require.ErrorIs(t, err, inputoptions.ErrBadScope, "a label list needs a repository")

	res, err := svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "github.label", Scope: map[string]any{"repository": "acme/web"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"bug", "needs-triage"}, values(res.Options))
	assert.Equal(t, "ededed", res.Options[0].Annotations["color"])

	// A repository the user cannot see is GitHub's 404, in words.
	_, err = svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "github.label", Scope: map[string]any{"repository": "bobco/private"}})
	var ue *inputoptions.UpstreamError
	require.ErrorAs(t, err, &ue)
	assert.False(t, ue.Retryable)
	assert.Equal(t, 404, ue.StatusCode)
}

func TestGitHubUsersAreSearchedServerSideAndStoredByID(t *testing.T) {
	f := inputoptionstest.NewFake(t, githubAccounts())
	svc := f.Service()
	ctx := context.Background()

	none, err := svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "github.user"})
	require.NoError(t, err)
	assert.Empty(t, none.Options)
	assert.Empty(t, f.Requests(), "no query, no search")

	res, err := svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "github.user", Query: "octo"})
	require.NoError(t, err)
	require.Len(t, res.Options, 1)
	assert.Equal(t, "583231", res.Options[0].Value, "the id, never the login")
	assert.Equal(t, "octocat", res.Options[0].Label)
	assert.Contains(t, f.Requests()[0], "q=octo")
}

func TestGitHubUserResolveLabelsIDsAndFlagsGoneOnes(t *testing.T) {
	f := inputoptionstest.NewFake(t, githubAccounts())
	res, err := f.Service().Resolve(context.Background(), inputoptions.ResolveRequest{
		UserID: "alice", Type: "github.user", Values: []string{"583231", "42", "9999999", "octocat"},
	})
	require.NoError(t, err)
	assert.Equal(t, []inputoptions.Option{{Value: "583231", Label: "octocat", Description: "User"}, {Value: "42", Label: "hubot", Description: "User"}}, res.Options)
	assert.ElementsMatch(t, []string{"9999999", "octocat"}, res.Unreachable, "a deleted id, and a login that was never an id")
}

func TestGitHubRepositoryResolve(t *testing.T) {
	f := inputoptionstest.NewFake(t, githubAccounts())
	res, err := f.Service().Resolve(context.Background(), inputoptions.ResolveRequest{
		UserID: "alice", Type: "github.repository", Values: []string{"acme/web", "acme/gone", "bobco/private", "not a repo"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/web"}, values(res.Options))
	assert.ElementsMatch(t, []string{"acme/gone", "bobco/private", "not a repo"}, res.Unreachable,
		"a repository the user cannot see is unreachable for them even though it exists")
}

func TestGitHubNeedsConnectionAndUpstreamErrors(t *testing.T) {
	f := inputoptionstest.NewFake(t, githubAccounts())
	svc := f.Service("carol")
	ctx := context.Background()

	res, err := svc.List(ctx, inputoptions.ListRequest{UserID: "carol", Type: "github.repository"})
	require.NoError(t, err)
	assert.Equal(t, inputoptions.StateNeedsConnection, res.State)
	assert.Empty(t, f.Requests())

	_, err = svc.List(ctx, inputoptions.ListRequest{UserID: "bob", Type: "github.repository", ConnectionID: "conn-alice"})
	require.ErrorIs(t, err, inputoptions.ErrConnectionNotFound)

	f.SetDown("alice", true)
	_, err = svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "github.repository"})
	var ue *inputoptions.UpstreamError
	require.ErrorAs(t, err, &ue)
	assert.True(t, ue.Retryable, "a 502 is worth retrying")
}

func TestGitHubInstallationsListAsAccounts(t *testing.T) {
	f := inputoptionstest.NewFake(t, githubAccounts())
	res, err := f.Service().List(context.Background(), inputoptions.ListRequest{UserID: "alice", Type: "github.installation"})
	require.NoError(t, err)
	assert.Equal(t, []string{"11", "22"}, values(res.Options))
	assert.Equal(t, "acme", res.Options[0].Label)
}

func set(in []string) map[string]bool {
	out := map[string]bool{}
	for _, s := range in {
		out[s] = true
	}
	return out
}
