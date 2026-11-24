// Copyright (c) 2025 Reliant Labs
package question

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// A chat's reply and manage members answer its questions (control-plane
// docs/design/chat-collaboration.md §2.3, §6.1, §7): only with collaboration
// on, under the funding rule a member's send meets, recorded as theirs, and of
// two people answering one question exactly one wins.

const qOrg = "org-q"

// qGrants is the chat's grants: dana at reply, vic at view.
type qGrants struct{}

func (qGrants) ChatMemberRole(_ context.Context, _, userID string) (core.ChatMemberRole, error) {
	return map[string]core.ChatMemberRole{"dana": core.ChatMemberReply, "vic": core.ChatMemberView}[userID], nil
}

// qGate is the chat service's reply gate: refuse names who is refused.
type qGate struct{ refuse error }

func (g qGate) MemberReplyRefusal(_ context.Context, chat *db.Chat, sender string) error {
	if sender == chat.UserID {
		return nil
	}
	return g.refuse
}

func (qGate) ReplyAuthor(_ context.Context, chat *db.Chat, sender string) db.MessageAuthor {
	if sender == chat.UserID {
		return db.MessageAuthor{Name: "Alex"}
	}
	return db.MessageAuthor{UserID: sender, Name: "Dana"}
}

type qResolve struct {
	id         string
	answeredBy *string
}

func qContext(t *testing.T, userID string) context.Context {
	t.Helper()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	ctx = authz.WithGrants(authz.WithMembership(ctx, memberOf{qOrg: true}), qGrants{})
	return auth.WithCredential(ctx, auth.CredentialSession)
}

// newQService answers a pending ask_user question in alex's chat of qOrg,
// recording every resolve and every message the answer saves (with the author
// its context carries). resolved is what ResolveQuestionBy reports.
func newQService(t *testing.T, on bool, gate ReplyGate, resolved bool) (*Service, *[]qResolve, *[]db.MessageAuthor) {
	t.Helper()
	org := qOrg
	metadata := `{"type":"ask_user"}`
	var resolves []qResolve
	var authors []db.MessageAuthor
	repo := &MockRepository{
		GetQuestionByIDFunc: func(context.Context, string) (*db.Question, error) {
			return &db.Question{ID: "q1", ChatID: "c1", ThreadID: "c1", WorkflowID: "c1", Status: db.QuestionStatusPending, Metadata: &metadata}, nil
		},
		GetChatFunc: func(context.Context, string) (*db.Chat, error) {
			return &db.Chat{ID: "c1", UserID: "alex", OrgID: &org}, nil
		},
		ResolveQuestionByFunc: func(_ context.Context, id string, _ *string, answeredBy *string) (bool, error) {
			resolves = append(resolves, qResolve{id: id, answeredBy: answeredBy})
			return resolved, nil
		},
		EmitQuestionUpdateFunc: func(context.Context, string, db.QuestionUpdate) error { return nil },
		SaveMessageToThreadFunc: func(ctx context.Context, _, _ string, _ int32, _ string, _ *string, _ []string, _ *int32) (*db.Message, error) {
			a, _ := db.MessageAuthorFrom(ctx)
			authors = append(authors, a)
			return &db.Message{ID: "m"}, nil
		},
	}
	deps := Deps{Repo: repo, Signals: &MockSignaler{SignalWithRecoveryFunc: func(context.Context, string, string, interface{}, map[string]interface{}) error { return nil }}}
	if on {
		deps.Collaboration = &configv1.ChatCollaborationConfig{Enabled: true}
	}
	if gate != nil {
		deps.Replies = gate
	}
	return NewTestQuestion(t, WithQuestionDeps(deps)), &resolves, &authors
}

func answer(t *testing.T, svc *Service, userID string) error {
	t.Helper()
	data, _ := json.Marshal(map[string]string{"reply": "yes, go on"})
	reply := string(data)
	_, err := svc.ResolveQuestion(qContext(t, userID), connect.NewRequest(&reliantv1.ResolveQuestionRequest{
		QuestionId: "q1", Action: "reply", ResponseData: &reply,
	}))
	return err
}

func TestResolveQuestion_AReplyMemberAnswersAsThemselves(t *testing.T) {
	svc, resolves, authors := newQService(t, true, qGate{}, true)
	require.NoError(t, answer(t, svc, "dana"))
	require.Len(t, *resolves, 1)
	require.NotNil(t, (*resolves)[0].answeredBy, "the question records who answered")
	assert.Equal(t, "dana", *(*resolves)[0].answeredBy)
	require.Len(t, *authors, 1, "the ask_user reply is saved as a message")
	assert.Equal(t, db.MessageAuthor{UserID: "dana", Name: "Dana"}, (*authors)[0], "under the member's name")

	svc, resolves, authors = newQService(t, true, qGate{}, true)
	require.NoError(t, answer(t, svc, "alex"))
	assert.Nil(t, (*resolves)[0].answeredBy, "the owner's answer keeps answered_by NULL, which means the owner")
	assert.Equal(t, db.MessageAuthor{Name: "Alex"}, (*authors)[0])
}

func TestResolveQuestion_WhoMayAnswer(t *testing.T) {
	for _, tc := range []struct {
		user string
		on   bool
		ok   bool
	}{
		{"alex", true, true},
		{"dana", true, true},
		{"vic", true, false},   // view
		{"pat", true, false},   // in the org, no grant
		{"alex", false, true},  // collaboration off: the owner alone,
		{"dana", false, false}, // as before, and as an older release
	} {
		svc, resolves, _ := newQService(t, tc.on, qGate{}, true)
		err := answer(t, svc, tc.user)
		if tc.ok {
			assert.NoError(t, err, "%s on=%v", tc.user, tc.on)
			continue
		}
		assert.True(t, svcerr.IsNotFound(err), "%s on=%v: a lower level is NotFound: %v", tc.user, tc.on, err)
		assert.Empty(t, *resolves, "nothing is resolved")
	}
}

// A member's answer drives the owner's run as a message does, so a chat on
// the owner's own account refuses it (§5.3), as it would their message.
func TestResolveQuestion_AMembersAnswerMeetsTheFundingRule(t *testing.T) {
	refused := errors.New("this chat runs on Alex's own AI account")
	svc, resolves, _ := newQService(t, true, qGate{refuse: refused}, true)
	assert.ErrorIs(t, answer(t, svc, "dana"), refused)
	assert.Empty(t, *resolves)

	svc, resolves, _ = newQService(t, true, nil, true)
	assert.True(t, svcerr.IsNotFound(answer(t, svc, "dana")), "with no gate wired, a member's answer is refused")
	assert.Empty(t, *resolves)
}

// Two people answering one question: the one whose answer lands first is the
// one recorded; the other is told it is already resolved, and nothing of
// theirs is saved or signalled.
func TestResolveQuestion_ALaterAnswerLoses(t *testing.T) {
	svc, resolves, authors := newQService(t, true, qGate{}, false)
	err := answer(t, svc, "dana")
	assert.True(t, svcerr.IsFailedPrecondition(err), "%v", err)
	assert.Len(t, *resolves, 1)
	assert.Empty(t, *authors, "the loser's reply is not saved")
}
