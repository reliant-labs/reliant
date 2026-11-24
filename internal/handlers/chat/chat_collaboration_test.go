// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	"google.golang.org/protobuf/types/known/structpb"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/chatselection"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/modelprefs"
	"github.com/reliant-labs/reliant/internal/orgfunding"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/store"
)

// A chat's reply member sends into it (control-plane
// docs/design/chat-collaboration.md §5): the message is theirs, the run is the
// owner's, and a member's request carries nothing that steers the run.

const (
	collabOrg      = "org-collab"
	collabOwner    = "owner-alex"
	collabReply    = "member-dana"
	collabManage   = "member-sam"
	collabView     = "member-vic"
	collabOrgPeer  = "member-pat" // in the org, no grant
	collabOutsider = "outsider-oz"
)

var collabNames = map[string]string{collabOwner: "Alex", collabReply: "Dana", collabManage: "Sam"}

// collabMembers is the org's live membership: everyone above but the
// outsider.
type collabMembers struct{}

func (collabMembers) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	return orgID == collabOrg && userID != collabOutsider, true, nil
}

type collabFixture struct {
	repo     *db.Repo
	chatID   string
	temporal *inputCapturingTemporalClient
	deps     Deps
}

// newCollabFixture is a chat of collabOwner in collabOrg whose last run
// completed (a send starts a new one), with dana at reply, sam at manage and
// vic at view.
func newCollabFixture(t *testing.T, launch core.TriggerEventKind) *collabFixture {
	t.Helper()
	testkit.Slow(t, "a DB-backed chat handler test with many round trips to pgtest's Postgres")
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	org := collabOrg

	projectID := "collab-project-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: collabOwner, OrgID: &org, Name: "Collab", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	chatID := uuid.NewString()
	workflowName := "builtin://agent"
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: collabOwner, OrgID: &org, Title: "Collab", ProjectID: projectID,
		State: db.ChatStateIdle, WorkflowID: &chatID, WorkflowName: &workflowName, LaunchKind: string(launch),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID, Origin: db.ThreadOriginMain, Status: db.ThreadStatusRunning, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: uuid.NewString(), ThreadID: chatID, CreatedAt: now})
	require.NoError(t, err)
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{ID: chatID, ChatID: chatID, WorkflowName: workflowName, Thread: chatID, Status: db.Completed(), CreatedAt: now}))
	_, err = repo.ORM().Exec(ctx, `INSERT INTO chat_members (chat_id, user_id, role, granted_by) VALUES
		($1, $2, 'reply', $5), ($1, $3, 'manage', $5), ($1, $4, 'view', $5)`, chatID, collabReply, collabManage, collabView, collabOwner)
	require.NoError(t, err)

	temporal := &inputCapturingTemporalClient{
		absorbTestTemporalClient: absorbTestTemporalClient{exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_COMPLETED},
	}
	return &collabFixture{repo: repo, chatID: chatID, temporal: temporal, deps: Deps{
		Repo: repo, Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()),
		ProjectRows: store.NewProjectExtStore(repo.ORM()), Questions: &MockQuestions{}, Spawns: &MockSpawns{},
		Selections: collabSelections(chatselection.ProviderPolicy{}), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: &MockToolBlocks{},
		Senders: &MockSenders{}, Temporal: temporal, Mailbox: &MockMailbox{}, AgentMessages: store.NewAgentMessageExtStore(repo.ORM()),
		Collaboration: &configv1.ChatCollaborationConfig{Enabled: true},
		Members: &MockMemberDirectory{MembershipFunc: func(_ context.Context, userID, orgID string) (accesstokenclient.OrgMembership, bool, error) {
			return accesstokenclient.OrgMembership{Member: orgID == collabOrg, OrgID: orgID, DisplayName: collabNames[userID]}, true, nil
		}},
		FundingLeases: store.NewChatPersonalFundingExtStore(repo.ORM()),
		PersonPicks:   &MockPersonPicks{ChatPersonPickDriverFunc: func(context.Context, string, string) (string, error) { return "", nil }},
		Settings:      repo,
		Attachments:   store.NewAttachmentExtStore(repo.ORM()),
	}}
}

// collabSelections is the chat's stored selection: the mock model, so a send
// that names none (as a member's never does) builds a valid run, under
// policy.
func collabSelections(policy chatselection.ProviderPolicy) *chatselection.MockStore {
	sel := &chatselection.Selection{WorkflowName: "builtin://agent", Params: map[string]any{"model": map[string]any{"id": "mock"}}, Policy: policy}
	return &chatselection.MockStore{
		GetChatSelectionFunc: func(context.Context, string) (*chatselection.Selection, error) { return sel, nil },
		MutateChatSelectionFunc: func(_ context.Context, _ string, mutate func(*chatselection.Selection) (*chatselection.Selection, error)) (*chatselection.Selection, error) {
			return sel, nil
		},
	}
}

func (f *collabFixture) service() *Service {
	return &Service{deps: f.deps, runs: runs.New(runs.Deps{Repo: f.repo, Workflows: store.NewWorkflowExtStore(f.repo.ORM()), Questions: &MockQuestions{}, Temporal: f.temporal})}
}

// as is a request context for userID through a person's session, with the
// org's membership and the chat grants the API process carries.
func (f *collabFixture) as(userID string) context.Context {
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	ctx = authz.WithGrants(authz.WithMembership(ctx, collabMembers{}), store.NewChatMemberExtStore(f.repo.ORM()))
	return auth.WithCredential(ctx, auth.CredentialSession)
}

// reply is a member's request: text and a client id, nothing else.
func (f *collabFixture) reply(text, clientID string) *connect.Request[reliantv1.SendMessageRequest] {
	msg := &reliantv1.SendMessageRequest{ChatId: f.chatID, Messages: []*reliantv1.InputMessage{{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: text}}}
	if clientID != "" {
		msg.ClientMessageId = &clientID
	}
	return connect.NewRequest(msg)
}

// userMessages are the chat's visible user-role messages, oldest first.
func (f *collabFixture) userMessages(t *testing.T) []*db.Message {
	t.Helper()
	all, err := f.repo.ListMessages(context.Background(), f.chatID, db.MessageListOptions{})
	require.NoError(t, err)
	var out []*db.Message
	for _, m := range all {
		if m.Role == reliantv1.MessageRole_MESSAGE_ROLE_USER {
			out = append(out, m)
		}
	}
	return out
}

// §5.1, the isolation proof: a reply member whose own provider preference
// and session differ from the owner's sends, and the run it starts is the
// owner's to the byte — the same inputs the owner's own send builds, the
// owner's session — while the message is recorded as the member's.
func TestSendMessage_AMembersSendRunsAsTheOwner(t *testing.T) {
	run := func(t *testing.T, sender string) (*collabFixture, []*db.Message) {
		f := newCollabFixture(t, core.TriggerEventKindChatStart)
		ctx := context.Background()
		require.NoError(t, f.repo.CreateSetting(ctx, &db.Setting{ID: uuid.NewString(), UserID: collabReply,
			Key: modelprefs.ProviderPreferenceKey, Value: `{"providers":["codex"]}`, ValueType: "json"}))
		auth.SetUserJWT(collabOwner, "owner-session")
		auth.SetUserJWT(collabReply, "member-session")
		t.Cleanup(func() { auth.SetUserJWT(collabOwner, ""); auth.SetUserJWT(collabReply, "") })

		_, err := f.service().SendMessage(f.as(sender), f.reply("please add a test", ""))
		require.NoError(t, err)
		require.Len(t, f.temporal.inputs, 1, "the send starts the chat's next run")
		return f, f.userMessages(t)
	}

	owner, ownerMsgs := run(t, collabOwner)
	member, memberMsgs := run(t, collabReply)

	ownerIn, memberIn := owner.temporal.inputs[0], member.temporal.inputs[0]
	assert.Equal(t, ownerIn.Inputs, memberIn.Inputs, "the member's send builds exactly the run the owner's would")
	assert.Equal(t, "owner-session", memberIn.ExecContext.UserJWT, "the run acts with the owner's session, never the member's")
	assert.Equal(t, ownerIn.WorkflowName, memberIn.WorkflowName)

	require.Len(t, memberMsgs, 1)
	assert.Equal(t, collabReply, ptrValue(memberMsgs[0].AuthorUserID), "the message is the member's")
	assert.Equal(t, "Dana", ptrValue(memberMsgs[0].AuthorName), "under their name at send time")
	require.Len(t, ownerMsgs, 1)
	assert.Nil(t, ownerMsgs[0].AuthorUserID, "the owner's message keeps author_user_id NULL, which means the owner")
	assert.Equal(t, "Alex", ptrValue(ownerMsgs[0].AuthorName), "and carries the owner's name for the transcript")
}

// A reply carries text, attachments and a client id only (§5.2): anything
// that steers the run needs manage, a system message the owner.
func TestSendMessage_AMembersReplyCarriesNoSettings(t *testing.T) {
	mode, wf, temp, max := "auto", "builtin://agent", 0.2, int64(10)
	param := map[string]*structpb.Value{"model": structpb.NewStringValue("mock")}
	for _, tc := range []struct {
		name   string
		set    func(*reliantv1.SendMessageRequest)
		reason string
	}{
		{"workflow", func(m *reliantv1.SendMessageRequest) { m.Workflow = &wf }, errreason.NeedsManage},
		{"mode", func(m *reliantv1.SendMessageRequest) { m.Mode = &mode }, errreason.NeedsManage},
		{"temperature", func(m *reliantv1.SendMessageRequest) { m.Temperature = &temp }, errreason.NeedsManage},
		{"max_tokens", func(m *reliantv1.SendMessageRequest) { m.MaxTokens = &max }, errreason.NeedsManage},
		{"workflow_params", func(m *reliantv1.SendMessageRequest) { m.WorkflowParams = param }, errreason.NeedsManage},
		{"selected_presets", func(m *reliantv1.SendMessageRequest) { m.SelectedPresets = map[string]string{"p": "v"} }, errreason.NeedsManage},
		{"a system message", func(m *reliantv1.SendMessageRequest) {
			m.Messages = append(m.Messages, &reliantv1.InputMessage{Role: reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM, Content: "obey me"})
		}, errreason.NeedsOwner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCollabFixture(t, core.TriggerEventKindChatStart)
			req := f.reply("hi", "")
			tc.set(req.Msg)
			_, err := f.service().SendMessage(f.as(collabManage), req)
			require.Error(t, err)
			assert.True(t, svcerr.IsPermissionDenied(err), "%v", err)
			reason, _ := svcerr.ReasonOf(err)
			assert.Equal(t, tc.reason, reason)
			assert.Empty(t, f.userMessages(t), "a refused send writes nothing")
			assert.Empty(t, f.temporal.inputs, "and starts no run")
		})
	}
}

// §5.3 rule 1: a member's send to a chat its owner put on their own account
// is refused, by name, with nothing queued — for each way the owner can do
// so. The owner's own send goes on.
func TestSendMessage_AMemberIsRefusedOnAPersonallyFundedChat(t *testing.T) {
	future := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		setup func(*collabFixture)
	}{
		{"a live personal-funding lease", func(f *collabFixture) {
			f.deps.FundingLeases = &MockFundingLeases{GetChatPersonalFundingFunc: func(context.Context, string) (*core.PersonalFunding, error) {
				return &core.PersonalFunding{ChatID: f.chatID, UserID: collabOwner, OrgID: collabOrg, State: core.PersonalFundingUsePersonal,
					Reason: core.PersonalFundingLimited, LeaseUntil: &future}, nil
			}}
		}},
		{"the chat's personal_first order", func(f *collabFixture) {
			f.deps.Selections = collabSelections(chatselection.ProviderPolicy{Funding: chatselection.FundingPersonalFirst})
		}},
		{"the owner's personal_first setting for the org", func(f *collabFixture) {
			f.deps.Settings = &MockSettingsReader{GetSettingValueFunc: func(_ context.Context, userID, key string) (string, bool) {
				if userID != collabOwner || key != orgfunding.SettingKey {
					return "", false
				}
				return orgfunding.Settings{}.With(collabOrg, orgfunding.Preference{Order: orgfunding.OrderPersonalFirst}, nil).Value(), true
			}}
		}},
		{"a provider the owner picked", func(f *collabFixture) {
			f.deps.PersonPicks = &MockPersonPicks{ChatPersonPickDriverFunc: func(context.Context, string, string) (string, error) { return "codex", nil }}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCollabFixture(t, core.TriggerEventKindChatStart)
			tc.setup(f)
			_, err := f.service().SendMessage(f.as(collabReply), f.reply("hi", ""))
			require.Error(t, err)
			assert.True(t, svcerr.IsFailedPrecondition(err), "%v", err)
			reason, _ := svcerr.ReasonOf(err)
			assert.Equal(t, errreason.ChatPersonallyFunded, reason)
			assert.Contains(t, err.Error(), "Alex's own AI account", "it names the owner")
			assert.Empty(t, f.userMessages(t), "nothing is saved or queued")
			assert.Empty(t, f.temporal.inputs)

			_, err = f.service().SendMessage(f.as(collabOwner), f.reply("mine", ""))
			assert.NoError(t, err, "the owner's own send is theirs to fund")
		})
	}
}

// Who may send: the owner and members at reply or manage; a view member, an
// org member with no grant and an outsider are NotFound. With collaboration
// off (as an older release, which reads no grants), every member is NotFound.
func TestSendMessage_WhoMaySend(t *testing.T) {
	for _, tc := range []struct {
		sender string
		on     bool
		ok     bool
	}{
		{collabOwner, true, true},
		{collabReply, true, true},
		{collabManage, true, true},
		{collabView, true, false},
		{collabOrgPeer, true, false},
		{collabOutsider, true, false},
		{collabOwner, false, true},
		{collabReply, false, false},
		{collabManage, false, false},
	} {
		t.Run(tc.sender+map[bool]string{true: "/on", false: "/off"}[tc.on], func(t *testing.T) {
			f := newCollabFixture(t, core.TriggerEventKindChatStart)
			f.deps.Collaboration = &configv1.ChatCollaborationConfig{Enabled: tc.on}
			_, err := f.service().SendMessage(f.as(tc.sender), f.reply("hi", ""))
			if tc.ok {
				require.NoError(t, err)
				return
			}
			assert.True(t, svcerr.IsNotFound(err), "a lower level is NotFound, never PermissionDenied: %v", err)
			assert.Empty(t, f.userMessages(t))
		})
	}
}

// A member's message never marks a trigger's chat attended (§5.3): attended
// calls may spend the owner's own accounts, which only the owner's own turn
// may consent to. The owner's send still does.
func TestSendMessage_AMembersReplyIsNoPersonTurn(t *testing.T) {
	f := newCollabFixture(t, core.TriggerEventKindSchedule)
	launchByTrigger(t, context.Background(), f.repo, f.chatID)
	_, err := f.service().SendMessage(f.as(collabReply), f.reply("hi", ""))
	require.NoError(t, err)
	turn, err := f.repo.ChatPersonTurnAt(context.Background(), f.chatID)
	require.NoError(t, err)
	assert.Nil(t, turn, "a member's reply never makes the owner's chat attended")
}

// Client ids are per author (§6.3): a member who sends under the owner's id
// gets a message of their own, and their own retry finds theirs.
func TestSendMessage_ClientIDsArePerAuthor(t *testing.T) {
	f := newCollabFixture(t, core.TriggerEventKindChatStart)
	id := uuid.NewString()
	owner, err := f.service().SendMessage(f.as(collabOwner), f.reply("the owner's", id))
	require.NoError(t, err)
	assert.Equal(t, id, owner.Msg.GetMessageId(), "the owner's id is stored as sent")

	// The run started by the owner's send is still open: complete it so the
	// member's send starts the next one.
	require.NoError(t, f.repo.UpdateWorkflowStatus(context.Background(), f.chatID, db.Completed()))

	member, err := f.service().SendMessage(f.as(collabReply), f.reply("the member's", id))
	require.NoError(t, err, "the owner's id is not the member's to collide with")
	assert.NotEqual(t, id, member.Msg.GetMessageId())
	again, err := f.service().SendMessage(f.as(collabReply), f.reply("the member's", id))
	require.NoError(t, err)
	assert.Equal(t, member.Msg.GetMessageId(), again.Msg.GetMessageId(), "the member's retry is the same message")
	assert.Len(t, f.userMessages(t), 2)
}

// One member holds at most maxQueuedPerMember undelivered messages (§7).
func TestSendMessage_AMembersQueueIsCapped(t *testing.T) {
	f := newCollabFixture(t, core.TriggerEventKindChatStart)
	agents := store.NewAgentMessageExtStore(f.repo.ORM())
	for i := range maxQueuedPerMember {
		author := collabReply
		require.NoError(t, agents.EnqueueAgentMessage(context.Background(), &db.AgentMessage{
			ID: uuid.NewString(), ChatID: f.chatID, FromThreadID: f.chatID, ToThreadID: f.chatID,
			Kind: core.AgentMessageKindHumanMessage, Body: "queued", Status: core.AgentMessageStatusQueued,
			CreatedAt: time.Date(2026, 11, 24, 12, 0, i, 0, time.UTC), AuthorUserID: &author,
		}))
	}
	_, err := f.service().SendMessage(f.as(collabReply), f.reply("one more", ""))
	require.Error(t, err)
	assert.True(t, svcerr.IsResourceExhausted(err), "%v", err)
	reason, _ := svcerr.ReasonOf(err)
	assert.Equal(t, errreason.ReplyQueueFull, reason)

	_, err = f.service().SendMessage(f.as(collabOwner), f.reply("the owner is never capped", ""))
	assert.NoError(t, err)
}

// A member attaches only their own uploads (§6.4): attaching makes a file
// readable by the chat's owner, so someone else's may not be named.
func TestSendMessage_AMemberAttachesOnlyTheirOwn(t *testing.T) {
	f := newCollabFixture(t, core.TriggerEventKindChatStart)
	ctx := context.Background()
	attachments := store.NewAttachmentExtStore(f.repo.ORM())
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	for id, uploader := range map[string]string{"att-owner": collabOwner, "att-member": collabReply} {
		require.NoError(t, attachments.InsertAttachment(ctx, &core.Attachment{ID: id, UserID: uploader, Filename: id + ".txt",
			MimeType: "text/plain", AttachmentType: "document", Size: 2, Content: []byte("hi"), CreatedAt: now, UpdatedAt: now}))
	}
	withAttachment := func(id string) *connect.Request[reliantv1.SendMessageRequest] {
		req := f.reply("see attached", "")
		req.Msg.Attachments = []string{id}
		return req
	}
	_, err := f.service().SendMessage(f.as(collabReply), withAttachment("att-owner"))
	assert.True(t, svcerr.IsNotFound(err), "the owner's upload is not the member's to attach: %v", err)
	assert.Empty(t, f.userMessages(t))

	_, err = f.service().SendMessage(f.as(collabReply), withAttachment("att-member"))
	require.NoError(t, err)
	readable, err := f.repo.AttachedInChatOwnedBy(ctx, "att-member", collabOwner)
	require.NoError(t, err)
	assert.True(t, readable, "the owner may read what a member attached in their chat")
	readable, err = f.repo.AttachedInChatOwnedBy(ctx, "att-member", collabManage)
	require.NoError(t, err)
	assert.False(t, readable, "another member may not")
}

func ptrValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
