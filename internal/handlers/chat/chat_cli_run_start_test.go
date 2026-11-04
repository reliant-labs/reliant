// Copyright (c) 2025 Reliant Labs
package chat

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func (f *builderTestFixture) startCLI(req *reliantv1.StartChatRequest) (*connect.Response[reliantv1.StartChatResponse], error) {
	req.ProjectId = f.projectID
	req.CliRun = proto.Bool(true)
	if len(req.Messages) == 0 {
		req.Messages = []*reliantv1.InputMessage{{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: "inventory the machine\nand report"}}
	}
	return f.service.StartChat(f.ctx, connect.NewRequest(req))
}

// `reliant workflow run` records cli.run: deduped on the chat id like any
// attended start, listed under Runs, and kept out of the sidebar — nine
// command-line runs of one probe workflow once filled it.
func TestStartChat_CLIRunRecordsCLIRunKindOutOfTheSidebar(t *testing.T) {
	t.Parallel()

	f := newBuilderTestFixture(t)
	f.saveDraft(t, "test-user", "cicd-probe", db.WorkflowDraftStatusComplete)

	resp, err := f.startCLI(&reliantv1.StartChatRequest{Workflow: "cicd-probe"})
	require.NoError(t, err)
	chatID := resp.Msg.GetChat().GetId()

	assert.Equal(t, "cli.run", f.launchKind(t, chatID))
	event, err := f.repo.GetTriggerEventByChatID(f.ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventKindCLIRun, event.Kind)
	assert.Equal(t, chatID, event.DedupeKey, "attended: deduped on the chat id, like chat.start")

	chat, err := f.repo.GetChat(f.ctx, chatID)
	require.NoError(t, err)
	assert.False(t, chat.ListInSidebar)
	assert.Equal(t, "cicd-probe · inventory the machine", chat.Title,
		"named for the workflow and the message's first line, not generated")

	sidebar, err := f.repo.ListChats(f.ctx, db.ChatFilters{UserID: "test-user", ProjectID: &f.projectID, Limit: 100, SidebarOnly: true})
	require.NoError(t, err)
	for _, c := range sidebar {
		assert.NotEqual(t, chatID, c.ID, "a command-line run is not a sidebar chat")
	}
	runs, _, err := f.repo.ListRuns(f.ctx, db.RunListFilters{UserID: "test-user", Limit: 50})
	require.NoError(t, err)
	listed := false
	for _, r := range runs {
		listed = listed || r.ChatID == chatID
	}
	assert.True(t, listed, "a command-line run is listed under Runs")
}

// A title the caller gives is kept as given.
func TestStartChat_CLIRunKeepsAnExplicitTitle(t *testing.T) {
	t.Parallel()

	f := newBuilderTestFixture(t)
	f.saveDraft(t, "test-user", "cicd-probe", db.WorkflowDraftStatusComplete)

	resp, err := f.startCLI(&reliantv1.StartChatRequest{Workflow: "cicd-probe", Title: proto.String("probe: kube access")})
	require.NoError(t, err)
	chat, err := f.repo.GetChat(f.ctx, resp.Msg.GetChat().GetId())
	require.NoError(t, err)
	assert.Equal(t, "probe: kube access", chat.Title)
}

// cli_run starts a new run: not a branch's first send, and not also a builder
// test. A refused start creates no chat.
func TestStartChat_CLIRunRefusesAChatIDOrBuilderTest(t *testing.T) {
	t.Parallel()

	f := newBuilderTestFixture(t)
	f.saveDraft(t, "test-user", "cicd-probe", db.WorkflowDraftStatusComplete)

	for name, req := range map[string]*reliantv1.StartChatRequest{
		"chat_id":      {Workflow: "cicd-probe", ChatId: proto.String(uuid.NewString())},
		"builder_test": {Workflow: "cicd-probe", BuilderTest: proto.Bool(true)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.startCLI(req)
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), err.Error())
		})
	}

	chats, err := f.repo.ListChats(f.ctx, db.ChatFilters{ProjectID: &f.projectID})
	require.NoError(t, err)
	assert.Empty(t, chats)
}
