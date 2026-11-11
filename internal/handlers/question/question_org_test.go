// Copyright (c) 2025 Reliant Labs
package question

import (
	"context"
	"sort"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
)

// memberOf is a Membership in which the caller is a live member of exactly
// the named orgs, and whose default org is "home".
type memberOf map[string]bool

func (m memberOf) IsMember(_ context.Context, _, orgID string) (bool, bool, error) {
	return m[orgID], true, nil
}

func (memberOf) DefaultOrg(context.Context, string) (string, error) { return "home", nil }

// ListPendingQuestions with no chat lists only questions in chats the caller
// may still read: none of a removed member's left org, and none of the other
// org of a credential bound to one.
func TestListPendingQuestions_LeavesOutQuestionsOfAnOrgTheCallerMayNotRead(t *testing.T) {
	org := func(s string) *string { return &s }
	chatOrgs := map[string]*string{"c-acme": org("acme"), "c-home": org("home")}
	repo := &MockRepository{
		ListAnswerablePendingQuestionsByUserFunc: func(context.Context, string) ([]*db.Question, error) {
			return []*db.Question{
				{ID: "q-acme-1", ChatID: "c-acme"},
				{ID: "q-home", ChatID: "c-home"},
				{ID: "q-acme-2", ChatID: "c-acme"},
			}, nil
		},
		GetChatRowFunc: func(_ context.Context, id string) (*db.ChatRow, error) {
			return &db.ChatRow{ID: id, UserID: "test-user", OrgID: chatOrgs[id]}, nil
		},
	}
	svc := NewTestQuestion(t, WithQuestionDeps(Deps{Repo: repo}))
	ids := func(ctx context.Context) []string {
		t.Helper()
		resp, err := svc.ListPendingQuestions(ctx, connect.NewRequest(&reliantv1.ListPendingQuestionsRequest{}))
		require.NoError(t, err)
		var out []string
		for _, q := range resp.Msg.GetQuestions() {
			out = append(out, q.GetQuestionId())
		}
		sort.Strings(out)
		return out
	}

	removed := authz.WithMembership(AuthedContext(t), memberOf{"home": true})
	require.Equal(t, []string{"q-home"}, ids(removed), "a removed member of acme")

	both := authz.WithMembership(AuthedContext(t), memberOf{"home": true, "acme": true})
	require.Equal(t, []string{"q-acme-1", "q-acme-2", "q-home"}, ids(both), "a member of both")
	require.Equal(t, []string{"q-home"}, ids(authz.WithCredentialOrg(both, "home")), "a credential bound to home")
}
