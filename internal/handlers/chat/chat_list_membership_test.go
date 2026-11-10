package chat

import (
	"context"
	"sort"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
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
	now := time.Now().UTC()
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
