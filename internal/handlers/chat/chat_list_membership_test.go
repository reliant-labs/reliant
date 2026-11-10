package chat

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// Every chat collection shows what GetChat would: a chat of an org its owner
// has left is listed by none of them, whether or not the request names an
// org, while a live org's chats and chats with no org stay listed.
func TestChatCollections_LeaveOutARemovedMembersOrgChats(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	resolver := orgs.New(orgSource{}) // alice is a live member of home only
	service := NewTestChat(t, WithChatDeps(Deps{Repo: repo, Orgs: resolver}))
	ctx := authz.WithMembership(context.WithValue(context.Background(), auth.UserIDContextKey, "alice"), resolver)
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC) // a fixture, not the wall clock
	projectID := "member-project-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "alice", Name: "p", Path: "/p/member",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	org := func(s string) *string { return &s }
	create := func(id string, orgID *string, archived bool) {
		t.Helper()
		require.NoError(t, repo.CreateChat(ctx, &db.Chat{
			ID: id, UserID: "alice", OrgID: orgID, Title: "needle " + id, ProjectID: projectID,
			State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
		if archived {
			require.NoError(t, repo.UpdateChatState(ctx, id, db.ChatStateArchived, "test"))
		}
	}
	suffix := uuid.NewString()[:8]
	left, home, legacy := "left-"+suffix, "home-"+suffix, "legacy-"+suffix
	for _, archived := range []bool{false, true} {
		tag := ""
		if archived {
			tag = "-archived"
		}
		create(left+tag, org("globex"), archived)
		create(home+tag, org("home"), archived)
		create(legacy+tag, nil, archived)
	}
	want := []string{home, legacy}
	sort.Strings(want)

	ids := func(chats []*reliantv1.Chat) []string {
		out := make([]string, 0, len(chats))
		for _, c := range chats {
			out = append(out, c.Id)
		}
		sort.Strings(out)
		return out
	}

	listed, err := service.ListChats(ctx, connect.NewRequest(&reliantv1.ListChatsRequest{ProjectId: projectID}))
	require.NoError(t, err)
	require.Equal(t, want, ids(listed.Msg.Chats), "ListChats naming no org")

	searched, err := service.SearchChats(ctx, connect.NewRequest(&reliantv1.SearchChatsRequest{Query: "needle", ProjectId: projectID}))
	require.NoError(t, err)
	// Search covers archived chats too.
	require.Equal(t, []string{home, home + "-archived", legacy, legacy + "-archived"}, ids(searched.Msg.Chats), "SearchChats")

	archived, err := service.ListArchivedChats(ctx, connect.NewRequest(&reliantv1.ListArchivedChatsRequest{}))
	require.NoError(t, err)
	var archivedIDs []string
	for _, ac := range archived.Msg.Chats {
		if ac.Chat.ProjectId == projectID {
			archivedIDs = append(archivedIDs, ac.Chat.Id)
		}
	}
	sort.Strings(archivedIDs)
	require.Equal(t, []string{home + "-archived", legacy + "-archived"}, archivedIDs, "ListArchivedChats")
}

// memberOfBoth: alice is a live member of acme and home; home is her default.
type memberOfBoth struct{}

func (memberOfBoth) ResolveOrgMembership(_ context.Context, _, orgID string) (accesstokenclient.OrgMembership, error) {
	if orgID == "" {
		return accesstokenclient.OrgMembership{OrgID: "home", Member: true}, nil
	}
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: orgID == "acme" || orgID == "home"}, nil
}

// ListChats' main workspaces show only projects the caller may read in the
// list's scope: a removed member, and a credential bound to another org, get
// none of the other org's, with or without project_id, while the projects
// they may read keep theirs even with no chat in them.
func TestListChats_MainWorkspacesFollowTheProjectsAuthz(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	bg := context.WithValue(context.Background(), auth.UserIDContextKey, "alice")
	org := func(s string) *string { return &s }
	projects := map[string]*string{"p-acme-" + uuid.NewString()[:6]: org("acme"), "p-home-" + uuid.NewString()[:6]: org("home"), "p-legacy-" + uuid.NewString()[:6]: nil}
	ids := map[string]string{}
	for id, orgID := range projects {
		ids[id[:strings.LastIndex(id, "-")]] = id
		require.NoError(t, repo.CreateProject(authz.WithMembership(bg, authz.NoOrgs{}), &db.Project{
			ID: id, UserID: "alice", OrgID: orgID, Name: id, Path: "/p/" + id, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
		require.NoError(t, repo.CreateWorktree(bg, &db.Worktree{
			ID: "w-" + id, Name: "main", Path: "/p/" + id, Branch: "main", BaseBranch: "main", ProjectID: id,
			Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE), IsMain: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
	}
	mains := func(svc *Service, ctx context.Context, projectID string) []string {
		t.Helper()
		resp, err := svc.ListChats(ctx, connect.NewRequest(&reliantv1.ListChatsRequest{ProjectId: projectID}))
		require.NoError(t, err)
		var out []string
		for _, w := range resp.Msg.GetMainWorktrees() {
			out = append(out, w.GetProjectId())
		}
		sort.Strings(out)
		return out
	}
	sorted := func(s ...string) []string { sort.Strings(s); return s }

	// A removed member of acme (a member of home only).
	removed := orgs.New(orgSource{})
	svc := NewTestChat(t, WithChatDeps(Deps{Repo: repo, Orgs: removed}))
	ctx := authz.WithMembership(bg, removed)
	require.Equal(t, sorted(ids["p-home"], ids["p-legacy"]), mains(svc, ctx, ""), "a removed member, no project_id")
	require.Empty(t, mains(svc, ctx, ids["p-acme"]), "a removed member, the left org's project_id")
	require.Equal(t, []string{ids["p-home"]}, mains(svc, ctx, ids["p-home"]), "a removed member, a project they may read")

	// A member of both, through a credential bound to home.
	both := orgs.New(memberOfBoth{})
	svc = NewTestChat(t, WithChatDeps(Deps{Repo: repo, Orgs: both}))
	machine := auth.WithMachineToken(authz.WithMembership(bg, both), &fat.Principal{OrgID: "home", ActingUserID: "alice"})
	require.Equal(t, sorted(ids["p-home"], ids["p-legacy"]), mains(svc, machine, ""), "a home credential, no project_id")
	require.Empty(t, mains(svc, machine, ids["p-acme"]), "a home credential, acme's project_id")
}
