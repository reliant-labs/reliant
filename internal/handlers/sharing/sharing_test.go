package sharing

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
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
	ev, err := store.ListShareEvents(context.Background(), f.Repo.ORM(), kind, id)
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
	now := viewertest.SeededAt
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
	// Several projects are the union of project_id and project_ids.
	chats, err = listChats(f.Viewer, &pb.ListSharedChatsRequest{ProjectIds: []string{other, f.ProjectID}})
	require.NoError(t, err)
	assert.Len(t, chats, 1)
	chats, err = listChats(f.Viewer, &pb.ListSharedChatsRequest{ProjectId: &f.ProjectID, ProjectIds: []string{other}})
	require.NoError(t, err)
	assert.Len(t, chats, 1)
	chats, err = listChats(f.Viewer, &pb.ListSharedChatsRequest{ProjectIds: []string{other}})
	require.NoError(t, err)
	assert.Empty(t, chats, "a project with nothing shared narrows to nothing, never to every project")
	// An unset project_id lists every project; a present-but-empty one
	// lists nothing, as it did before project_ids existed. With project_ids
	// set, an empty project_id adds nothing to the list.
	empty := ""
	chats, err = listChats(f.Viewer, &pb.ListSharedChatsRequest{})
	require.NoError(t, err)
	assert.Len(t, chats, 1, "project_id unset")
	chats, err = listChats(f.Viewer, &pb.ListSharedChatsRequest{ProjectId: &empty})
	require.NoError(t, err)
	assert.Empty(t, chats, "project_id present and empty never broadens to every project")
	chats, err = listChats(f.Viewer, &pb.ListSharedChatsRequest{ProjectId: &empty, ProjectIds: []string{f.ProjectID}})
	require.NoError(t, err)
	assert.Len(t, chats, 1, "an empty project_id with project_ids is just project_ids")
	chats, err = listChats(f.Viewer, &pb.ListSharedChatsRequest{ProjectId: &empty, ProjectIds: []string{other}})
	require.NoError(t, err)
	assert.Empty(t, chats)
	_, err = listChats(f.Viewer, &pb.ListSharedChatsRequest{ProjectIds: make([]string, core.MaxListProjectIDs+1)})
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), "too many ids")
	_, err = listChats(f.Viewer, &pb.ListSharedChatsRequest{ProjectIds: []string{f.ProjectID, ""}})
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), "an empty id")

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

// ListSharedChats with app_name is the shared half of an app's chat list,
// and each chat carries its app tag in the viewer's shape.
func TestListSharedChatsByApp(t *testing.T) {
	svc, f := sharingFixture(t)
	_, err := f.Repo.ORM().Bun().NewRaw(`UPDATE chats SET app_name = 'storefront' WHERE id = ?`, f.ChatID).Exec(context.Background())
	require.NoError(t, err)
	list := func(app *string) []*pb.Chat {
		t.Helper()
		resp, err := svc.ListSharedChats(f.Viewer, connect.NewRequest(&pb.ListSharedChatsRequest{AppName: app}))
		require.NoError(t, err)
		return resp.Msg.Chats
	}
	storefront, blog, empty := "storefront", "blog", ""
	chats := list(&storefront)
	require.Len(t, chats, 1)
	assert.Equal(t, f.ChatID, chats[0].GetId())
	assert.Equal(t, "storefront", chats[0].GetAppName())
	assert.True(t, chats[0].GetViewerAccess())
	assert.Empty(t, list(&blog), "another app's list holds none of it")
	assert.Len(t, list(&empty), 1, "an empty app_name is unset")
	assert.Len(t, list(nil), 1)
}

// seedDraft writes a saved workflow of userID in org (nil: none), private.
func seedDraft(t *testing.T, f *viewertest.Fixture, id, userID string, org *string) {
	t.Helper()
	require.NoError(t, f.Repo.CreateWorkflowDraft(context.Background(), &db.WorkflowDraft{
		ID: id, UserID: userID, OrgID: org, Name: "Deploy " + id, Slug: "deploy-" + id,
		Definition: "name: deploy-" + id + "\nentry: run\n", Status: core.WorkflowDraftStatusComplete,
		SourcePath: ptrTo("/home/owner/.reliant/workflows/deploy.yaml"),
		CreatedAt:  viewertest.SeededAt, UpdatedAt: viewertest.SeededAt,
	}))
}

func ptrTo(s string) *string { return &s }

func setDraft(svc *Service, ctx context.Context, id string, v pb.ResourceVisibility) (*pb.SetWorkflowDraftSharingResponse, error) {
	resp, err := svc.SetWorkflowDraftSharing(ctx, connect.NewRequest(&pb.SetWorkflowDraftSharingRequest{WorkflowId: id, Visibility: v}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// SetWorkflowDraftSharing follows SetChatVisibility's rules for a saved
// workflow (org-sharing.md §8.5): only the owner shares, as a person, and
// only one in an org; the owner clears with any credential; an org admin
// clears a member's share but never sets one; anyone else is NOT_FOUND. Each
// change writes one share event; a no-op writes none.
func TestSetWorkflowDraftSharing(t *testing.T) {
	svc, f := sharingFixture(t)
	acme := viewertest.OrgID
	wf := "wf-" + f.OwnerID
	seedDraft(t, f, wf, f.OwnerID, &acme)

	// Private: nobody but the owner sees it.
	_, err := setDraft(svc, f.Viewer, wf, private)
	requireRefusal(t, err, connect.CodeNotFound, "")
	_, err = setDraft(svc, f.Admin, wf, private)
	requireRefusal(t, err, connect.CodeNotFound, "")
	_, err = setDraft(svc, f.Outsider, wf, org)
	requireRefusal(t, err, connect.CodeNotFound, "")
	// An agent's credential cannot share it.
	_, err = setDraft(svc, f.ContextAs(f.OwnerID, auth.CredentialAccessToken), wf, org)
	requireRefusal(t, err, connect.CodePermissionDenied, auth.ReasonPersonRequired)
	assert.Empty(t, events(t, f, core.ShareKindWorkflow, wf))

	// The owner shares, as a person, and reads it as the owner.
	got, err := setDraft(svc, f.Owner, wf, org)
	require.NoError(t, err)
	assert.True(t, got.Changed)
	assert.Equal(t, org, got.Workflow.GetVisibility())
	assert.False(t, got.Workflow.GetViewerAccess())
	assert.Equal(t, "deploy-"+wf, got.Workflow.GetSlug())
	assert.NotEmpty(t, got.Workflow.GetSharedAt())
	ev := events(t, f, core.ShareKindWorkflow, wf)
	require.Len(t, ev, 1)
	assert.Equal(t, core.ShareActionShare, ev[0].Action)
	assert.Equal(t, viewertest.OrgID, ev[0].OrgID)

	// Again: nothing changes, nothing is recorded.
	got, err = setDraft(svc, f.Owner, wf, org)
	require.NoError(t, err)
	assert.False(t, got.Changed)
	assert.Len(t, events(t, f, core.ShareKindWorkflow, wf), 1)

	// A viewer reads it and may neither share nor clear it; an admin may not
	// share it, nor clear it through an agent's credential; an outsider gets
	// nothing.
	_, err = setDraft(svc, f.Viewer, wf, org)
	requireRefusal(t, err, connect.CodePermissionDenied, ReasonOwnerOnly)
	_, err = setDraft(svc, f.Viewer, wf, private)
	requireRefusal(t, err, connect.CodePermissionDenied, ReasonOrgAdminRequired)
	_, err = setDraft(svc, f.Admin, wf, org)
	requireRefusal(t, err, connect.CodePermissionDenied, ReasonOwnerOnly)
	_, err = setDraft(svc, f.ContextAs(f.AdminID, auth.CredentialAccessToken), wf, private)
	requireRefusal(t, err, connect.CodeNotFound, "")
	_, err = setDraft(svc, f.Outsider, wf, private)
	requireRefusal(t, err, connect.CodeNotFound, "")
	assert.Len(t, events(t, f, core.ShareKindWorkflow, wf), 1, "refusals record nothing")

	// The admin clears it, recorded as an admin's, and reads it no longer.
	got, err = setDraft(svc, f.Admin, wf, private)
	require.NoError(t, err)
	assert.True(t, got.Changed)
	assert.Nil(t, got.Workflow, "the admin cannot read a private saved workflow")
	ev = events(t, f, core.ShareKindWorkflow, wf)
	require.Len(t, ev, 2)
	assert.Equal(t, core.ShareActionAdminUnshare, ev[0].Action)
	assert.Equal(t, f.AdminID, ev[0].ActorUserID)
	assert.Equal(t, f.OwnerID, ev[0].OwnerUserID)

	// The owner shares again and clears it with an agent's credential.
	_, err = setDraft(svc, f.Owner, wf, org)
	require.NoError(t, err)
	got, err = setDraft(svc, f.ContextAs(f.OwnerID, auth.CredentialAccessToken), wf, private)
	require.NoError(t, err)
	assert.True(t, got.Changed)
	assert.Equal(t, private, got.Workflow.GetVisibility())
	assert.Empty(t, got.Workflow.GetSharedAt())
	ev = events(t, f, core.ShareKindWorkflow, wf)
	require.Len(t, ev, 4)
	assert.Equal(t, core.ShareActionUnshare, ev[0].Action)

	_, err = setDraft(svc, f.Owner, wf, pb.ResourceVisibility_RESOURCE_VISIBILITY_UNSPECIFIED)
	requireRefusal(t, err, connect.CodeInvalidArgument, "")
	_, err = setDraft(svc, f.Owner, "no-such-workflow", org)
	requireRefusal(t, err, connect.CodeNotFound, "")

	// Sharing it widens no other workflow read: a launch or a lookup by slug
	// decides with CanReadWorkflow, which stays the owner's.
	_, err = setDraft(svc, f.Owner, wf, org)
	require.NoError(t, err)
	shared, err := f.Repo.GetWorkflowDraft(context.Background(), wf)
	require.NoError(t, err)
	assert.Equal(t, core.VisibilityOrg, shared.Visibility)
	assert.False(t, authz.CanReadWorkflow(authz.WithViewerReads(f.Viewer), f.ViewerID, shared))
	assert.True(t, authz.CanReadWorkflow(f.Owner, f.OwnerID, shared))
}

// A saved workflow that belongs to no org has no one to be shared with.
func TestSetWorkflowDraftSharingNeedsAnOrg(t *testing.T) {
	svc, f := sharingFixture(t)
	wf := "orgless-wf-" + f.OwnerID
	seedDraft(t, f, wf, f.OwnerID, nil)
	_, err := setDraft(svc, f.Owner, wf, org)
	requireRefusal(t, err, connect.CodeFailedPrecondition, ReasonNoOrg)
}

// The shared library: a member of the org lists what others shared there,
// most recently shared first, in the shared shape (no source path), never
// their own, a private one, a hidden one or another org's; an outsider's org
// lists none, and naming an org one is not in is refused; an agent's
// credential lists nothing.
func TestListSharedWorkflowDrafts(t *testing.T) {
	svc, f := sharingFixture(t)
	acme, globex := viewertest.OrgID, viewertest.OtherOrgID
	first, second := "wf-first-"+f.OwnerID, "wf-second-"+f.OwnerID
	privateWF, hidden := "wf-private-"+f.OwnerID, "wf-hidden-"+f.OwnerID
	viewerOwn, other := "wf-viewer-"+f.ViewerID, "wf-globex-"+f.OutsiderID
	for _, id := range []string{first, second, privateWF, hidden} {
		seedDraft(t, f, id, f.OwnerID, &acme)
	}
	seedDraft(t, f, viewerOwn, f.ViewerID, &acme)
	seedDraft(t, f, other, f.OutsiderID, &globex)
	for _, s := range []struct {
		ctx context.Context
		id  string
	}{{f.Owner, first}, {f.Owner, second}, {f.Owner, hidden}, {f.Viewer, viewerOwn}, {f.Outsider, other}} {
		_, err := setDraft(svc, s.ctx, s.id, org)
		require.NoError(t, err, s.id)
	}
	_, err := f.Repo.SetWorkflowDraftHidden(f.Owner, hidden, true)
	require.NoError(t, err)
	// first was shared earlier than second.
	_, err = f.Repo.ORM().Bun().NewRaw(`UPDATE workflow_drafts SET shared_at = shared_at - interval '1 hour' WHERE id = ?`, first).Exec(context.Background())
	require.NoError(t, err)

	list := func(ctx context.Context, req *pb.ListSharedWorkflowDraftsRequest) ([]*pb.SharedWorkflowDraft, error) {
		resp, err := svc.ListSharedWorkflowDrafts(ctx, connect.NewRequest(req))
		if err != nil {
			return nil, err
		}
		return resp.Msg.Workflows, nil
	}
	got, err := list(f.Viewer, &pb.ListSharedWorkflowDraftsRequest{})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, []string{second, first}, []string{got[0].GetId(), got[1].GetId()}, "most recently shared first")
	w := got[0]
	assert.True(t, w.GetViewerAccess())
	assert.Equal(t, org, w.GetVisibility())
	assert.Equal(t, f.OwnerID, w.GetUserId())
	assert.Equal(t, acme, w.GetOrgId())
	assert.Equal(t, pb.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE, w.GetStatus())
	assert.Equal(t, "name: deploy-"+second+"\nentry: run\n", w.GetDefinition(), "the team reads the definition to fork it")
	assert.NotEmpty(t, w.GetSharedAt())

	got, err = list(f.Viewer, &pb.ListSharedWorkflowDraftsRequest{OrgId: &acme, Limit: 1, Offset: 1})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, first, got[0].GetId())

	got, err = list(f.Owner, &pb.ListSharedWorkflowDraftsRequest{})
	require.NoError(t, err)
	require.Len(t, got, 1, "the owner's own are in their own library; the viewer's shared one is listed")
	assert.Equal(t, viewerOwn, got[0].GetId())

	got, err = list(f.Outsider, &pb.ListSharedWorkflowDraftsRequest{})
	require.NoError(t, err)
	assert.Empty(t, got, "the outsider's org shares only their own")
	_, err = list(f.Outsider, &pb.ListSharedWorkflowDraftsRequest{OrgId: &acme})
	requireRefusal(t, err, connect.CodePermissionDenied, "not_org_member")
	_, err = list(f.ContextAs(f.ViewerID, auth.CredentialAccessToken), &pb.ListSharedWorkflowDraftsRequest{})
	requireRefusal(t, err, connect.CodePermissionDenied, auth.ReasonPersonRequired)
}
