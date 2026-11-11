package chat

import (
	"context"
	"sort"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// ListArchivedChats and SearchChats are scoped like ListChats (orgs.ListScope):
// naming an org lists that org's chats, and the default org's also lists the
// chats with no org yet; naming none lists every org's, as before org_id.
func TestArchivedAndSearch_ScopedToTheNamedOrg(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	resolver := orgs.New(memberOfBoth{}) // alice is in acme and home; home is her default
	service := NewTestChat(t, WithChatDeps(Deps{Repo: repo, Orgs: resolver}))
	ctx := authz.WithMembership(context.WithValue(context.Background(), auth.UserIDContextKey, "alice"), resolver)
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	projectID := "scope-project-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "alice", Name: "p", Path: "/p/scope",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	org := func(s string) *string { return &s }
	suffix := uuid.NewString()[:8]
	acme, home, legacy := "acme-"+suffix, "home-"+suffix, "legacy-"+suffix
	for _, archived := range []bool{false, true} {
		for id, orgID := range map[string]*string{acme: org("acme"), home: org("home"), legacy: nil} {
			if archived {
				id += "-archived"
			}
			require.NoError(t, repo.CreateChat(ctx, &db.Chat{
				ID: id, UserID: "alice", OrgID: orgID, Title: "needle " + id, ProjectID: projectID,
				State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now,
			}))
			if archived {
				require.NoError(t, repo.UpdateChatState(ctx, id, db.ChatStateArchived, "test"))
			}
		}
	}
	sorted := func(s ...string) []string { sort.Strings(s); return s }
	archived := func(orgID *string) []string {
		t.Helper()
		resp, err := service.ListArchivedChats(ctx, connect.NewRequest(&reliantv1.ListArchivedChatsRequest{OrgId: orgID}))
		require.NoError(t, err)
		var out []string
		for _, ac := range resp.Msg.Chats {
			if ac.Chat.ProjectId == projectID {
				out = append(out, ac.Chat.Id)
			}
		}
		return sorted(out...)
	}
	searched := func(orgID *string) []string {
		t.Helper()
		resp, err := service.SearchChats(ctx, connect.NewRequest(&reliantv1.SearchChatsRequest{Query: "needle", ProjectId: projectID, OrgId: orgID}))
		require.NoError(t, err)
		var out []string
		for _, c := range resp.Msg.Chats {
			out = append(out, c.Id)
		}
		return sorted(out...)
	}

	// Unset first: before naming the default org stamps the legacy chats.
	require.Equal(t, sorted(acme+"-archived", home+"-archived", legacy+"-archived"), archived(nil), "archived, no org named")
	require.Equal(t, sorted(acme, acme+"-archived", home, home+"-archived", legacy, legacy+"-archived"), searched(nil), "search, no org named")

	require.Equal(t, []string{acme + "-archived"}, archived(ptr.Of("acme")), "archived, acme")
	require.Equal(t, sorted(acme, acme+"-archived"), searched(ptr.Of("acme")), "search, acme")
	require.Equal(t, sorted(home+"-archived", legacy+"-archived"), archived(ptr.Of("home")), "archived, the default org with the chats of no org")
	require.Equal(t, sorted(home, home+"-archived", legacy, legacy+"-archived"), searched(ptr.Of("home")), "search, the default org with the chats of no org")

	// An org the caller is not in is refused, not listed empty.
	_, err := service.ListArchivedChats(ctx, connect.NewRequest(&reliantv1.ListArchivedChatsRequest{OrgId: ptr.Of("globex")}))
	require.Equal(t, connect.CodePermissionDenied, svcerr.Code(err))
	_, err = service.SearchChats(ctx, connect.NewRequest(&reliantv1.SearchChatsRequest{Query: "needle", OrgId: ptr.Of("globex")}))
	require.Equal(t, connect.CodePermissionDenied, svcerr.Code(err))
}
