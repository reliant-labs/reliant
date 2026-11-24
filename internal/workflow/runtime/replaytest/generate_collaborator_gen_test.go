// Copyright (c) 2025 Reliant Labs
//
//go:build replayfixtures

package replaytest

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/store"
)

// The collaboration scenarios' org, and the chat member who replies.
const (
	collabOrgID      = "replayfix-org"
	collabMemberID   = "replayfix-member"
	collabMemberName = "Dana"
)

// collabMembership: everyone the scenario names is a live member of its org.
type collabMembership struct{}

func (collabMembership) IsMember(_ context.Context, _, orgID string) (bool, bool, error) {
	return orgID == collabOrgID, true, nil
}

// collabDirectory names the member; the owner has no name on record.
type collabDirectory struct{}

func (collabDirectory) Membership(_ context.Context, userID, orgID string) (accesstokenclient.OrgMembership, bool, error) {
	m := accesstokenclient.OrgMembership{Member: orgID == collabOrgID, OrgID: orgID}
	if userID == collabMemberID {
		m.DisplayName = collabMemberName
	}
	return m, true, nil
}

// collabContext is a request context in the scenario's org, as the API
// listener's edge makes it: membership, the chat grants, a person's session.
func collabContext(ctx context.Context, s *Stack) context.Context {
	ctx = authz.WithGrants(authz.WithMembership(ctx, collabMembership{}), store.NewChatMemberExtStore(s.Repo.ORM()))
	return auth.WithCredential(ctx, auth.CredentialSession)
}

// newCollabHarness is newHarness with the scenario project in an org and chat
// collaboration on.
func newCollabHarness(t *testing.T, llmScript *ScriptedLLM) *Harness {
	t.Helper()
	return buildHarness(t, llmScript, nil, collabOrgID)
}

// GrantReply makes the scenario's member a reply member of chatID.
func (h *Harness) GrantReply(chatID string) error {
	_, err := h.Stack.Repo.ORM().Exec(h.Ctx, `INSERT INTO chat_members (chat_id, user_id, role, granted_by) VALUES ($1, $2, 'reply', $3)`,
		chatID, collabMemberID, h.UserID)
	return err
}

// SendMessageAsMember is SendMessage by the scenario's reply member: text
// only, as a member's reply is.
func (h *Harness) SendMessageAsMember(chatID, content string) error {
	ctx := collabContext(context.WithValue(context.Background(), auth.UserIDContextKey, collabMemberID), h.Stack)
	_, err := h.ChatSvc.SendMessage(ctx, connect.NewRequest(&reliantv1.SendMessageRequest{
		ChatId:   chatID,
		Messages: []*reliantv1.InputMessage{{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: content}},
	}))
	return err
}

// TestGenerateFixture_CollaboratorMessage pins a run whose thread holds a chat
// member's message (control-plane docs/design/chat-collaboration.md §12): the
// owner starts the chat in an org, a member granted reply sends while the
// first turn is in flight, the message is queued with its author and drained
// as theirs, and the next CallLLM reads it tagged with the member's name.
// Authorship lives in Postgres and in the activities, never in a workflow
// input, so this history must replay with the same command sequence as a
// single-person chat's: it is the regression pin for any later change that
// lets authorship reach the workflow.
func TestGenerateFixture_CollaboratorMessage(t *testing.T) {
	const reply = "Also add a test for the empty case, please."
	var h *Harness
	chatIDs := make(chan string, 1)
	sent := make(chan error, 1)
	var tagged bool
	script := NewScriptedLLM(
		Turn{Text: "Working on the fix.", During: func() {
			id := <-chatIDs
			if err := h.GrantReply(id); err != nil {
				sent <- err
				return
			}
			sent <- h.SendMessageAsMember(id, reply)
		}},
		Turn{Text: "Fixed, and added the test."},
	)
	script.FailWhen(func(msgs []message.Message) error {
		for i := range msgs {
			for _, text := range msgs[i].TextContents() {
				if strings.Contains(text.Text, `<message from="`+collabMemberName+`"`) {
					tagged = true
				}
			}
		}
		return nil
	})
	h = newCollabHarness(t, script)

	created := h.StartChat("builtin://agent", "Fix the bug in the parser", map[string]any{"mode": "auto"})
	chatIDs <- created.Chat.Id
	workflowID := created.WorkflowId

	require.NoError(t, <-sent, "the member's reply must be accepted")
	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.Completed())
	assert.False(t, h.LLM.Exhausted())
	assert.True(t, tagged, "the second turn reads the member's message under their name")

	author, err := store.LatestHumanTurnAuthor(h.Ctx, h.Stack.Repo.ORM(), created.Chat.Id)
	require.NoError(t, err)
	assert.Equal(t, collabMemberID, author, "the drained message is the member's")

	h.ExportHistory(workflowID, "collaborator_message")
}
