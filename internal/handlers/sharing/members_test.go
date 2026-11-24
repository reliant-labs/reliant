package sharing

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// Chat members (control-plane docs/design/chat-collaboration.md §3, PR C2),
// end to end over the database: the fixture's Viewer and Admin are live
// members of the org, the Outsider is not.

func membersFixture(t *testing.T) (on, off *Service, f *viewertest.Fixture) {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f = viewertest.New(t, repo)
	deps := realDeps(repo, f.Membership)
	off = NewTestSharing(t, WithSharingDeps(deps))
	deps.Collaboration = &configv1.ChatCollaborationConfig{Enabled: true}
	on = NewTestSharing(t, WithSharingDeps(deps))
	return on, off, f
}

const (
	view   = pb.ChatMemberRole_CHAT_MEMBER_ROLE_VIEW
	reply  = pb.ChatMemberRole_CHAT_MEMBER_ROLE_REPLY
	manage = pb.ChatMemberRole_CHAT_MEMBER_ROLE_MANAGE
)

func setMember(svc *Service, ctx context.Context, chatID, userID string, role pb.ChatMemberRole) (*pb.SetChatMemberResponse, error) {
	resp, err := svc.SetChatMember(ctx, connect.NewRequest(&pb.SetChatMemberRequest{ChatId: chatID, UserId: userID, Role: role}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func removeMember(svc *Service, ctx context.Context, chatID, userID string) (*pb.RemoveChatMemberResponse, error) {
	resp, err := svc.RemoveChatMember(ctx, connect.NewRequest(&pb.RemoveChatMemberRequest{ChatId: chatID, UserId: userID}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func listMembers(svc *Service, ctx context.Context, chatID string) (*pb.ListChatMembersResponse, error) {
	resp, err := svc.ListChatMembers(ctx, connect.NewRequest(&pb.ListChatMembersRequest{ChatId: chatID}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func listed(resp *pb.ListChatMembersResponse) map[string]pb.ChatMemberRole {
	out := map[string]pb.ChatMemberRole{}
	for _, m := range resp.GetMembers() {
		out[m.GetUserId()] = m.GetRole()
	}
	return out
}

// readsAs is how userID reads chatID now, at a site that serves viewers.
func readsAs(t *testing.T, f *viewertest.Fixture, ctx context.Context, userID, chatID string) (authz.Access, error) {
	t.Helper()
	chat, err := f.Repo.GetChat(context.Background(), chatID)
	require.NoError(t, err)
	return authz.ReadChatAccess(authz.WithViewerReads(ctx), userID, chat)
}

// Who may grant, change and remove, what each level may see of the list, and
// what each change records: the owner and a manager grant, as a person, to a
// live member of the org who is not the owner, up to manage; a grant at any
// level reads the private chat and writes nothing; every change is exactly
// one share event, and a no-op none.
func TestChatMembers(t *testing.T) {
	svc, _, f := membersFixture(t)
	chat := f.PrivateChatID

	// Before any grant the Viewer cannot even see the private chat.
	_, err := listMembers(svc, f.Viewer, chat)
	requireRefusal(t, err, connect.CodeNotFound, "")

	// The owner grants view: the Viewer now reads the private chat, as a
	// viewer, and may change nothing.
	got, err := setMember(svc, f.Owner, chat, f.ViewerID, view)
	require.NoError(t, err)
	assert.True(t, got.Changed)
	assert.Equal(t, view, got.Member.GetRole())
	assert.Equal(t, f.OwnerID, got.Member.GetGrantedBy())
	got, err = setMember(svc, f.Owner, chat, f.ViewerID, view)
	require.NoError(t, err)
	assert.False(t, got.Changed, "granting the level held changes nothing")
	require.Len(t, events(t, f, core.ShareKindChat, chat), 1, "a no-op records nothing")

	access, err := readsAs(t, f, f.Viewer, f.ViewerID, chat)
	require.NoError(t, err)
	assert.Equal(t, authz.AccessViewer, access)
	row, err := f.Repo.GetChat(context.Background(), chat)
	require.NoError(t, err)
	assert.False(t, authz.CanOwnChat(f.Viewer, f.ViewerID, row), "a view member never writes")
	assert.False(t, authz.CanReplyChat(f.Viewer, f.ViewerID, row), "a view member never replies")
	_, err = setChat(svc, f.Viewer, chat, org)
	requireRefusal(t, err, connect.CodePermissionDenied, ReasonOwnerOnly)

	l, err := listMembers(svc, f.Viewer, chat)
	require.NoError(t, err)
	assert.Equal(t, f.OwnerID, l.OwnerUserId)
	assert.EqualValues(t, 1, l.MemberCount)
	assert.Empty(t, l.Members, "a view member sees only the count")
	assert.Equal(t, view, l.CallerRole)

	_, err = setMember(svc, f.Viewer, chat, f.AdminID, view)
	requireRefusal(t, err, connect.CodePermissionDenied, ReasonNeedsManage)

	// At reply: sees the reply and manage members, still cannot grant.
	got, err = setMember(svc, f.Owner, chat, f.ViewerID, reply)
	require.NoError(t, err)
	assert.True(t, got.Changed)
	l, err = listMembers(svc, f.Viewer, chat)
	require.NoError(t, err)
	assert.Equal(t, map[string]pb.ChatMemberRole{f.ViewerID: reply}, listed(l))
	assert.Equal(t, reply, l.CallerRole)
	_, err = setMember(svc, f.Viewer, chat, f.AdminID, view)
	requireRefusal(t, err, connect.CodePermissionDenied, ReasonNeedsManage)
	_, err = setMember(svc, f.Viewer, chat, f.ViewerID, manage)
	requireRefusal(t, err, connect.CodePermissionDenied, ReasonNeedsManage)
	assert.False(t, authz.CanOwnChat(f.Viewer, f.ViewerID, row), "every write still asks for the owner")

	// At manage: grants and removes anyone but the owner, never above manage.
	_, err = setMember(svc, f.Owner, chat, f.ViewerID, manage)
	require.NoError(t, err)
	got, err = setMember(svc, f.Viewer, chat, f.AdminID, view)
	require.NoError(t, err)
	assert.True(t, got.Changed)
	assert.Equal(t, f.ViewerID, got.Member.GetGrantedBy())
	l, err = listMembers(svc, f.Viewer, chat)
	require.NoError(t, err)
	assert.Equal(t, map[string]pb.ChatMemberRole{f.ViewerID: manage, f.AdminID: view}, listed(l))
	_, err = setMember(svc, f.Viewer, chat, f.OwnerID, view)
	requireRefusal(t, err, connect.CodeFailedPrecondition, ReasonOwnerNotMember)
	_, err = removeMember(svc, f.Viewer, chat, f.OwnerID)
	requireRefusal(t, err, connect.CodeFailedPrecondition, ReasonOwnerNotMember)
	_, err = setMember(svc, f.Viewer, chat, f.OutsiderID, view)
	requireRefusal(t, err, connect.CodeFailedPrecondition, ReasonNotOrgMember)
	_, err = setMember(svc, f.Owner, chat, f.OutsiderID, view)
	requireRefusal(t, err, connect.CodeFailedPrecondition, ReasonNotOrgMember)
	_, err = setMember(svc, f.Owner, chat, f.ViewerID, pb.ChatMemberRole_CHAT_MEMBER_ROLE_UNSPECIFIED)
	requireRefusal(t, err, connect.CodeInvalidArgument, "")

	// The owner sees everyone; a view member who was granted sees the count.
	l, err = listMembers(svc, f.Owner, chat)
	require.NoError(t, err)
	assert.Equal(t, map[string]pb.ChatMemberRole{f.ViewerID: manage, f.AdminID: view}, listed(l))
	assert.Equal(t, pb.ChatMemberRole_CHAT_MEMBER_ROLE_UNSPECIFIED, l.CallerRole)
	l, err = listMembers(svc, f.Admin, chat)
	require.NoError(t, err)
	assert.Empty(t, l.Members)
	assert.EqualValues(t, 2, l.MemberCount)

	// A member may leave; anyone who cannot read it is told nothing; the
	// owner acts only as a person.
	left, err := removeMember(svc, f.Admin, chat, f.AdminID)
	require.NoError(t, err)
	assert.True(t, left.Changed)
	left, err = removeMember(svc, f.Admin, chat, f.AdminID)
	requireRefusal(t, err, connect.CodeNotFound, "")
	assert.Nil(t, left)
	_, err = setMember(svc, f.Outsider, chat, f.AdminID, view)
	requireRefusal(t, err, connect.CodeNotFound, "")
	_, err = listMembers(svc, f.Outsider, chat)
	requireRefusal(t, err, connect.CodeNotFound, "")
	_, err = setMember(svc, f.ContextAs(f.OwnerID, auth.CredentialAccessToken), chat, f.AdminID, view)
	requireRefusal(t, err, connect.CodePermissionDenied, auth.ReasonPersonRequired)
	_, err = setMember(svc, f.Owner, "no-such-chat", f.AdminID, view)
	requireRefusal(t, err, connect.CodeNotFound, "")

	// Every change, one event each, newest first; refusals recorded nothing.
	ev := events(t, f, core.ShareKindChat, chat)
	var actions []core.ShareAction
	for _, e := range ev {
		actions = append(actions, e.Action)
	}
	assert.Equal(t, []core.ShareAction{
		core.ShareActionRevoke,     // the Admin left
		core.ShareActionGrant,      // the manager granted the Admin view
		core.ShareActionRoleChange, // reply -> manage
		core.ShareActionRoleChange, // view -> reply
		core.ShareActionGrant,      // the owner granted the Viewer view
	}, actions)
	assert.Equal(t, f.AdminID, ev[0].ActorUserID)
	assert.Equal(t, f.AdminID, ev[0].TargetUserID)
	assert.Equal(t, f.ViewerID, ev[1].ActorUserID)
	assert.Equal(t, core.ChatMemberView, ev[1].Role)
	for _, e := range ev {
		assert.Equal(t, f.OwnerID, e.OwnerUserID)
		assert.Equal(t, viewertest.OrgID, e.OrgID)
	}
}

// Granting is off until chat_collaboration.enabled; removing never is, so a
// grant made while it was on can always be taken back, and listing reads
// what exists.
func TestChatMembersGate(t *testing.T) {
	on, off, f := membersFixture(t)
	chat := f.PrivateChatID
	_, err := setMember(off, f.Owner, chat, f.ViewerID, view)
	requireRefusal(t, err, connect.CodeFailedPrecondition, ReasonCollaborationDisabled)
	assert.Empty(t, events(t, f, core.ShareKindChat, chat))

	_, err = setMember(on, f.Owner, chat, f.ViewerID, reply)
	require.NoError(t, err)
	l, err := listMembers(off, f.Owner, chat)
	require.NoError(t, err)
	assert.Equal(t, map[string]pb.ChatMemberRole{f.ViewerID: reply}, listed(l))
	got, err := removeMember(off, f.Owner, chat, f.ViewerID)
	require.NoError(t, err)
	assert.True(t, got.Changed)
	_, err = listMembers(off, f.Viewer, chat)
	requireRefusal(t, err, connect.CodeNotFound, "")
}

// A chat that belongs to no org has no one to grant it to; an org-wide viewer
// of a shared chat sees its count and reads it as a viewer.
func TestChatMembersNeedAnOrg(t *testing.T) {
	svc, _, f := membersFixture(t)
	at := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	require.NoError(t, f.Repo.CreateProject(context.Background(), &db.Project{
		ID: "no-org-project-" + f.OwnerID, UserID: f.OwnerID, Name: "No org", Path: "/home/owner/no-org",
		CreatedAt: at, UpdatedAt: at, LastActive: at,
	}))
	require.NoError(t, f.Repo.CreateChat(context.Background(), &db.Chat{
		ID: "no-org-" + f.OwnerID, UserID: f.OwnerID, ProjectID: "no-org-project-" + f.OwnerID, Title: "No org",
		CreatedAt: at, UpdatedAt: at, LastActive: at,
	}))
	noOrg, err := f.Repo.GetChat(context.Background(), "no-org-"+f.OwnerID)
	require.NoError(t, err)
	require.Nil(t, noOrg.OrgID, "the chat must belong to no org")
	_, err = setMember(svc, f.Owner, "no-org-"+f.OwnerID, f.ViewerID, view)
	requireRefusal(t, err, connect.CodeFailedPrecondition, ReasonNoOrg)
	_, err = removeMember(svc, f.Owner, "no-org-"+f.OwnerID, f.ViewerID)
	requireRefusal(t, err, connect.CodeFailedPrecondition, ReasonNoOrg)

	l, err := listMembers(svc, f.Viewer, f.ChatID)
	require.NoError(t, err)
	assert.Empty(t, l.Members)
	assert.EqualValues(t, 0, l.MemberCount)
	assert.Equal(t, view, l.CallerRole)
}
