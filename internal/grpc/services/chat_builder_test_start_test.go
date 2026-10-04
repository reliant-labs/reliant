// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/threads"
)

const builderTestDraftYAML = `name: bt-draft
entry: [echo]
nodes:
  - id: echo
    type: run
    command: "echo hi"
`

type builderTestFixture struct {
	repo      *db.Repo
	ctx       context.Context
	projectID string
	service   *ChatService
}

func newBuilderTestFixture(t *testing.T) *builderTestFixture {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID, _ := newInvariantTestProject(t, ctx, repo, true)
	temporal := &atomicityTestTemporalClient{}
	return &builderTestFixture{
		repo: repo, ctx: ctx, projectID: projectID,
		service: &ChatService{
			database: repo, threads: threads.NewService(repo),
			tempClient: temporal, runs: runs.NewService(repo, temporal, nil),
		},
	}
}

func (f *builderTestFixture) saveDraft(t *testing.T, owner, slug string, status db.WorkflowDraftStatus) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, f.repo.CreateWorkflowDraft(f.ctx, &db.WorkflowDraft{
		ID: uuid.NewString(), UserID: owner, Name: slug, Slug: slug,
		Definition: builderTestDraftYAML, Status: status, CreatedAt: now, UpdatedAt: now, Version: 1,
	}))
}

func (f *builderTestFixture) start(workflow string, builderTest *bool, chatID *string) (*connect.Response[reliantv1.StartChatResponse], error) {
	return f.service.StartChat(f.ctx, connect.NewRequest(&reliantv1.StartChatRequest{
		ProjectId:   f.projectID,
		Workflow:    workflow,
		BuilderTest: builderTest,
		ChatId:      chatID,
		Messages:    []*reliantv1.InputMessage{{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: "go"}},
	}))
}

func (f *builderTestFixture) launchKind(t *testing.T, chatID string) string {
	t.Helper()
	chat, err := f.repo.GetChat(f.ctx, chatID)
	require.NoError(t, err)
	return chat.LaunchKind
}

// A builder test run records launch kind builder.test, runs the saved draft
// even though it is not marked complete, and stays out of the sidebar.
func TestStartChat_BuilderTestRecordsBuilderTestKind(t *testing.T) {
	f := newBuilderTestFixture(t)
	f.saveDraft(t, "test-user", "bt-draft", db.WorkflowDraftStatusDraft)

	resp, err := f.start("bt-draft", proto.Bool(true), nil)
	require.NoError(t, err)
	chatID := resp.Msg.GetChat().GetId()

	assert.Equal(t, "builder.test", f.launchKind(t, chatID))
	event, err := f.repo.GetTriggerEventByChatID(f.ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventKindBuilderTest, event.Kind)
	assert.Equal(t, chatID, event.DedupeKey, "attended: deduped on the chat id, like chat.start")
	assert.Equal(t, "bt-draft", event.Payload["workflow"])

	chat, err := f.repo.GetChat(f.ctx, chatID)
	require.NoError(t, err)
	assert.False(t, chat.ListInSidebar)
}

// The same draft started the ordinary way is still refused: builder_test is
// the only door, and it does not make drafts runnable anywhere else.
func TestStartChat_DraftStillRefusedWithoutBuilderTest(t *testing.T) {
	f := newBuilderTestFixture(t)
	f.saveDraft(t, "test-user", "bt-draft", db.WorkflowDraftStatusDraft)

	for _, flag := range []*bool{nil, proto.Bool(false)} {
		_, err := f.start("bt-draft", flag, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is a draft")
	}
}

// An ordinary start is chat.start; there is no field through which a client
// can ask for schedule or agent.start_run.
func TestStartChat_ClientCannotClaimOtherLaunchKinds(t *testing.T) {
	f := newBuilderTestFixture(t)
	f.saveDraft(t, "test-user", "bt-draft", db.WorkflowDraftStatusComplete)

	for _, flag := range []*bool{nil, proto.Bool(false)} {
		resp, err := f.start("bt-draft", flag, nil)
		require.NoError(t, err)
		assert.Equal(t, "chat.start", f.launchKind(t, resp.Msg.GetChat().GetId()))
	}

	fields := (&reliantv1.StartChatRequest{}).ProtoReflect().Descriptor().Fields()
	for _, name := range []string{"launch_kind", "kind", "trigger_id", "dedupe_key"} {
		assert.Nil(t, fields.ByName(protoreflect.Name(name)), "StartChatRequest must not carry %q", name)
	}
}

func TestStartChat_BuilderTestRequiresAnOwnedSavedDraft(t *testing.T) {
	f := newBuilderTestFixture(t)
	f.saveDraft(t, "someone-else", "theirs", db.WorkflowDraftStatusComplete)
	f.saveDraft(t, "test-user", "mine", db.WorkflowDraftStatusComplete)

	cases := []struct {
		name     string
		workflow string
		chatID   *string
		want     connect.Code
	}{
		{"another user's draft", "theirs", nil, connect.CodeNotFound},
		{"no such draft", "nope", nil, connect.CodeNotFound},
		{"builtin workflow", "builtin://agent", nil, connect.CodeNotFound},
		{"no workflow", "", nil, connect.CodeInvalidArgument},
		{"existing chat", "mine", proto.String(uuid.NewString()), connect.CodeInvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.start(tc.workflow, proto.Bool(true), tc.chatID)
			require.Error(t, err)
			assert.Equal(t, tc.want, connect.CodeOf(err), err.Error())
		})
	}

	chats, err := f.repo.ListChats(f.ctx, db.ChatFilters{ProjectID: &f.projectID})
	require.NoError(t, err)
	assert.Empty(t, chats, "a rejected test run creates no chat")
}
