package provider_account_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	provider_account "github.com/reliant-labs/reliant/internal/handlers/provider_account"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/orgfunding"
)

// The chat's question before it moves from its org's accounts to the person's
// own (control-plane docs/design/org-provider-accounts.md §10.2).

type wakeRecorder struct{ users []string }

func (w *wakeRecorder) WakeUserChats(_ context.Context, userID, _ string) int {
	w.users = append(w.users, userID)
	return 1
}

func asPerson(user string) context.Context {
	return auth.WithCredential(as(user), auth.CredentialSession)
}

func TestPersonalFundingQuestion_ReadAndAnswer(t *testing.T) {
	e := newEnv(t)
	waker := &wakeRecorder{}
	e.svc = provider_account.NewTestProviderAccount(t, provider_account.WithProviderAccountDeps(provider_account.Deps{
		Store: e.repo, ChatWaker: waker,
		ValidateKey: func(context.Context, models.Family, string) (bool, string) { return true, "ok" },
	}))
	const user = "u-funding"
	chat, _ := seedChat(t, e.repo, user)
	reset := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	_, err := e.repo.AskPersonalFunding(as(user), core.PersonalFunding{ChatID: chat, UserID: user, OrgID: "org-acme",
		Reason: core.PersonalFundingLimited, Model: "claude-5.5-opus", PersonalProvider: "claude",
		PersonalAccountLabel: "me@example.com", OrgResetAt: &reset}, time.Now())
	require.NoError(t, err)

	got, err := e.svc.GetPersonalFundingQuestion(as(user), connect.NewRequest(&pb.GetPersonalFundingQuestionRequest{ChatId: chat}))
	require.NoError(t, err)
	q := got.Msg.GetQuestion()
	require.NotNil(t, q)
	assert.Equal(t, pb.PersonalFundingQuestion_REASON_LIMITED, q.Reason)
	assert.Equal(t, pb.PersonalFundingQuestion_STATE_ASKED, q.State)
	assert.Equal(t, "org-acme", q.OrgId)
	assert.Equal(t, reset.Format(time.RFC3339), q.GetOrgResetAt())

	_, err = e.svc.GetPersonalFundingQuestion(as("someone-else"), connect.NewRequest(&pb.GetPersonalFundingQuestionRequest{ChatId: chat}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "another user's chat")

	answer := func(ctx context.Context, choice pb.ResolvePersonalFundingChoiceRequest_Choice, remember bool) (*pb.ResolvePersonalFundingChoiceResponse, error) {
		resp, err := e.svc.ResolvePersonalFundingChoice(ctx, connect.NewRequest(&pb.ResolvePersonalFundingChoiceRequest{
			ChatId: chat, Choice: choice, Remember: remember}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}

	// Person-only: a CLI login or access token cannot spend the person's money.
	_, err = answer(as(user), pb.ResolvePersonalFundingChoiceRequest_CHOICE_USE_PERSONAL, false)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	_, err = answer(auth.WithCredential(as(user), auth.CredentialAccessToken), pb.ResolvePersonalFundingChoiceRequest_CHOICE_USE_PERSONAL, false)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	// The chat's owner only.
	_, err = answer(asPerson("someone-else"), pb.ResolvePersonalFundingChoiceRequest_CHOICE_USE_PERSONAL, false)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = answer(asPerson(user), pb.ResolvePersonalFundingChoiceRequest_CHOICE_UNSPECIFIED, false)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Empty(t, waker.users, "nothing refused woke anything")

	// Keep waiting, remembered: the setting says wait for the org.
	res, err := answer(asPerson(user), pb.ResolvePersonalFundingChoiceRequest_CHOICE_KEEP_WAITING, true)
	require.NoError(t, err)
	assert.Equal(t, pb.PersonalFundingQuestion_STATE_KEEP_WAITING, res.Question.State)
	v, _ := e.repo.GetSettingValue(as(user), user, orgfunding.SettingKey)
	assert.Equal(t, orgfunding.OnOrgLimitWait, orgfunding.Parse(v).For("org-acme").OnOrgLimit)

	// Use my account, remembered: the lease lasts until the org's reset, and the
	// chat is woken to act on it.
	res, err = answer(asPerson(user), pb.ResolvePersonalFundingChoiceRequest_CHOICE_USE_PERSONAL, true)
	require.NoError(t, err)
	assert.Equal(t, pb.PersonalFundingQuestion_STATE_USE_PERSONAL, res.Question.State)
	row, err := e.repo.GetChatPersonalFunding(as(user), chat)
	require.NoError(t, err)
	require.NotNil(t, row.LeaseUntil)
	assert.True(t, row.LeaseUntil.Equal(reset))
	v, _ = e.repo.GetSettingValue(as(user), user, orgfunding.SettingKey)
	assert.Equal(t, orgfunding.OnOrgLimitUsePersonal, orgfunding.Parse(v).For("org-acme").OnOrgLimit)
	assert.Equal(t, []string{user, user}, waker.users)

	// Answered: nothing is open to answer again.
	_, err = answer(asPerson(user), pb.ResolvePersonalFundingChoiceRequest_CHOICE_KEEP_WAITING, false)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestPersonalFundingQuestion_NoneIsUnset(t *testing.T) {
	e := newEnv(t)
	const user = "u-funding-none"
	chat, _ := seedChat(t, e.repo, user)
	got, err := e.svc.GetPersonalFundingQuestion(as(user), connect.NewRequest(&pb.GetPersonalFundingQuestionRequest{ChatId: chat}))
	require.NoError(t, err)
	assert.Nil(t, got.Msg.Question)
}
