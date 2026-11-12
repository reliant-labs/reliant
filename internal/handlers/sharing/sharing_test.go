package sharing

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

func sharingFixture(t *testing.T) (*Service, *viewertest.Fixture) {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	return NewTestSharing(t, WithSharingDeps(Deps{Repo: repo, Orgs: f.Membership})), f
}

const (
	org     = pb.ResourceVisibility_RESOURCE_VISIBILITY_ORG
	private = pb.ResourceVisibility_RESOURCE_VISIBILITY_PRIVATE
)

func setChat(svc *Service, ctx context.Context, chatID string, v pb.ResourceVisibility) (*pb.SetChatVisibilityResponse, error) {
	resp, err := svc.SetChatVisibility(ctx, connect.NewRequest(&pb.SetChatVisibilityRequest{ChatId: chatID, Visibility: v}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func requireRefusal(t *testing.T, err error, code connect.Code, reason string) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, code, svcerr.Code(err), "error: %v", err)
	if reason != "" {
		assert.Equal(t, reason, svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader), "error: %v", err)
	}
}

func events(t *testing.T, f *viewertest.Fixture, kind core.ShareKind, id string) []*core.ShareEvent {
	t.Helper()
	ev, err := f.Repo.ListShareEvents(context.Background(), kind, id)
	require.NoError(t, err)
	return ev
}

// Who may share and clear a chat, and what each change records (§2, D4):
// only the owner shares, as a person; the owner clears with any credential;
// an admin of the org clears a member's share but never sets one; anyone
// else gets what an outsider gets. Every change writes exactly one share
// event, and a no-op writes none.
func TestSetChatVisibility(t *testing.T) {
	svc, f := sharingFixture(t)
	chat := f.PrivateChatID // private, in the org

	// Not shared yet: a member who is not the owner cannot even see it.
	_, err := setChat(svc, f.Viewer, chat, private)
	requireRefusal(t, err, connect.CodeNotFound, "")
	_, err = setChat(svc, f.Admin, chat, private)
	requireRefusal(t, err, connect.CodeNotFound, "")
	_, err = setChat(svc, f.Outsider, chat, org)
	requireRefusal(t, err, connect.CodeNotFound, "")

	// The owner, through an agent's CLI login, cannot share.
	_, err = setChat(svc, f.ContextAs(f.OwnerID, auth.CredentialAccessToken), chat, org)
	requireRefusal(t, err, connect.CodePermissionDenied, auth.ReasonPersonRequired)
	assert.Empty(t, events(t, f, core.ShareKindChat, chat))

	// The owner shares, as a person.
	got, err := setChat(svc, f.Owner, chat, org)
	require.NoError(t, err)
	assert.True(t, got.Changed)
	assert.Equal(t, org, got.Chat.GetVisibility())
	assert.False(t, got.Chat.GetViewerAccess())
	ev := events(t, f, core.ShareKindChat, chat)
	require.Len(t, ev, 1)
	assert.Equal(t, core.ShareActionShare, ev[0].Action)
	assert.Equal(t, f.OwnerID, ev[0].ActorUserID)
	assert.Equal(t, viewertest.OrgID, ev[0].OrgID)

	// Again: nothing changes, nothing is recorded.
	got, err = setChat(svc, f.Owner, chat, org)
	require.NoError(t, err)
	assert.False(t, got.Changed)
	assert.Len(t, events(t, f, core.ShareKindChat, chat), 1)

	// Now a viewer reads it, and still may neither share nor clear it.
	_, err = setChat(svc, f.Viewer, chat, org)
	requireRefusal(t, err, connect.CodePermissionDenied, ReasonOwnerOnly)
	_, err = setChat(svc, f.Viewer, chat, private)
	requireRefusal(t, err, connect.CodePermissionDenied, ReasonOrgAdminRequired)
	// An admin may not share what is not theirs either...
	_, err = setChat(svc, f.Admin, chat, org)
	requireRefusal(t, err, connect.CodePermissionDenied, ReasonOwnerOnly)
	// ...nor clear it through an agent's credential.
	_, err = setChat(svc, f.ContextAs(f.AdminID, auth.CredentialAccessToken), chat, private)
	requireRefusal(t, err, connect.CodeNotFound, "")
	// An outsider gets nothing.
	_, err = setChat(svc, f.Outsider, chat, private)
	requireRefusal(t, err, connect.CodeNotFound, "")
	assert.Len(t, events(t, f, core.ShareKindChat, chat), 1, "refusals record nothing")

	// The admin clears it: recorded as an admin's, and the admin reads it
	// no longer.
	got, err = setChat(svc, f.Admin, chat, private)
	require.NoError(t, err)
	assert.True(t, got.Changed)
	assert.Nil(t, got.Chat, "the admin cannot read a private chat")
	ev = events(t, f, core.ShareKindChat, chat)
	require.Len(t, ev, 2)
	assert.Equal(t, core.ShareActionAdminUnshare, ev[0].Action)
	assert.Equal(t, f.AdminID, ev[0].ActorUserID)
	assert.Equal(t, f.OwnerID, ev[0].OwnerUserID)

	// The owner shares again and clears it with an agent's credential.
	_, err = setChat(svc, f.Owner, chat, org)
	require.NoError(t, err)
	got, err = setChat(svc, f.ContextAs(f.OwnerID, auth.CredentialAccessToken), chat, private)
	require.NoError(t, err)
	assert.True(t, got.Changed)
	ev = events(t, f, core.ShareKindChat, chat)
	require.Len(t, ev, 4)
	assert.Equal(t, core.ShareActionUnshare, ev[0].Action)

	_, err = setChat(svc, f.Owner, chat, pb.ResourceVisibility_RESOURCE_VISIBILITY_UNSPECIFIED)
	requireRefusal(t, err, connect.CodeInvalidArgument, "")
	_, err = setChat(svc, f.Owner, "no-such-chat", org)
	requireRefusal(t, err, connect.CodeNotFound, "")
}

// A chat that belongs to no org has no one to be shared with.
func TestSetChatVisibilityNeedsAnOrg(t *testing.T) {
	svc, f := sharingFixture(t)
	now := time.Now().UTC()
	require.NoError(t, f.Repo.CreateChat(context.Background(), &db.Chat{
		ID: "orgless-" + f.OwnerID, UserID: f.OwnerID, ProjectID: f.ProjectID, Title: "no org",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := setChat(svc, f.Owner, "orgless-"+f.OwnerID, org)
	requireRefusal(t, err, connect.CodeFailedPrecondition, ReasonNoOrg)
}

// SetProjectVisibility follows the same rules; a viewer of the shared project
// is answered without its path.
func TestSetProjectVisibility(t *testing.T) {
	svc, f := sharingFixture(t)
	set := func(ctx context.Context, v pb.ResourceVisibility) (*pb.SetProjectVisibilityResponse, error) {
		resp, err := svc.SetProjectVisibility(ctx, connect.NewRequest(&pb.SetProjectVisibilityRequest{ProjectId: f.ProjectID, Visibility: v}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	// The fixture's project is shared.
	got, err := set(f.Owner, org)
	require.NoError(t, err)
	assert.False(t, got.Changed)
	_, err = set(f.Viewer, private)
	requireRefusal(t, err, connect.CodePermissionDenied, ReasonOrgAdminRequired)
	_, err = set(f.Outsider, private)
	requireRefusal(t, err, connect.CodeNotFound, "")
	got, err = set(f.Admin, private)
	require.NoError(t, err)
	assert.True(t, got.Changed)
	assert.Nil(t, got.Project)
	_, err = set(f.ContextAs(f.OwnerID, auth.CredentialAccessToken), org)
	requireRefusal(t, err, connect.CodePermissionDenied, auth.ReasonPersonRequired)
	got, err = set(f.Owner, org)
	require.NoError(t, err)
	assert.True(t, got.Changed)
	assert.Equal(t, "/home/owner/shared", got.Project.GetPath(), "the owner reads the path")
	ev := events(t, f, core.ShareKindProject, f.ProjectID)
	require.Len(t, ev, 2)
	assert.Equal(t, []core.ShareAction{core.ShareActionShare, core.ShareActionAdminUnshare}, []core.ShareAction{ev[0].Action, ev[1].Action})
}

// The shared lists: a member of the org sees what others shared there, in
// the viewer's shape, never their own; an outsider's org has none, and naming
// an org one is not in is refused; an agent's credential lists nothing.
func TestListShared(t *testing.T) {
	svc, f := sharingFixture(t)
	listChats := func(ctx context.Context, req *pb.ListSharedChatsRequest) ([]*pb.Chat, error) {
		resp, err := svc.ListSharedChats(ctx, connect.NewRequest(req))
		if err != nil {
			return nil, err
		}
		return resp.Msg.Chats, nil
	}
	chats, err := listChats(f.Viewer, &pb.ListSharedChatsRequest{})
	require.NoError(t, err)
	require.Len(t, chats, 1)
	assert.Equal(t, f.ChatID, chats[0].GetId())
	assert.True(t, chats[0].GetViewerAccess())
	assert.Equal(t, org, chats[0].GetVisibility())
	assert.Nil(t, chats[0].Selection)

	acme := viewertest.OrgID
	chats, err = listChats(f.Viewer, &pb.ListSharedChatsRequest{OrgId: &acme, ProjectId: &f.ProjectID})
	require.NoError(t, err)
	assert.Len(t, chats, 1)
	other := "another-project"
	chats, err = listChats(f.Viewer, &pb.ListSharedChatsRequest{ProjectId: &other})
	require.NoError(t, err)
	assert.Empty(t, chats)

	chats, err = listChats(f.Owner, &pb.ListSharedChatsRequest{})
	require.NoError(t, err)
	assert.Empty(t, chats, "the owner's own chats are in their own list")

	chats, err = listChats(f.Outsider, &pb.ListSharedChatsRequest{})
	require.NoError(t, err)
	assert.Empty(t, chats, "the outsider's org shares nothing")
	_, err = listChats(f.Outsider, &pb.ListSharedChatsRequest{OrgId: &acme})
	requireRefusal(t, err, connect.CodePermissionDenied, "not_org_member")
	_, err = listChats(f.ContextAs(f.ViewerID, auth.CredentialAccessToken), &pb.ListSharedChatsRequest{})
	requireRefusal(t, err, connect.CodePermissionDenied, auth.ReasonPersonRequired)

	projects, err := svc.ListSharedProjects(f.Viewer, connect.NewRequest(&pb.ListSharedProjectsRequest{}))
	require.NoError(t, err)
	require.Len(t, projects.Msg.Projects, 1)
	p := projects.Msg.Projects[0]
	assert.Equal(t, f.ProjectID, p.GetId())
	assert.True(t, p.GetViewerAccess())
	assert.Empty(t, p.GetPath(), "a viewer never reads the owner's path")
	projects, err = svc.ListSharedProjects(f.Owner, connect.NewRequest(&pb.ListSharedProjectsRequest{}))
	require.NoError(t, err)
	assert.Empty(t, projects.Msg.Projects)
	_, err = svc.ListSharedProjects(f.ContextAs(f.ViewerID, auth.CredentialAccessToken), connect.NewRequest(&pb.ListSharedProjectsRequest{}))
	requireRefusal(t, err, connect.CodePermissionDenied, auth.ReasonPersonRequired)
}

// With no control plane (no orgs) nothing is shared: the lists are empty.
func TestListSharedWithoutOrgs(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestSharing(t, WithSharingDeps(Deps{Repo: repo}))
	resp, err := svc.ListSharedChats(f.Viewer, connect.NewRequest(&pb.ListSharedChatsRequest{}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.Chats)
}
