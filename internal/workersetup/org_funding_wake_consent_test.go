// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/accountlimits"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/orgfunding"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// The browser pass's suspected consent bypass (reliant#2723), replayed over the
// production wiring: stored org and personal accounts -> the real loader (org
// membership, access, grants, pause, limits) -> the real router over the real
// store -> the real CallLLMActivity, with the funding question in the real
// chat_personal_funding table. Only the network edge (the provider) and
// control-plane's membership answer are faked.
//
// What the pass did, from its logs: a member's first turn in an org chat met
// the org's only usable Anthropic key ("Backup", open to selected people,
// granted to the member) already rate-limited, so no call was made; the chat
// asked, and the member answered Keep waiting. The owner then changed Backup's
// access to owners and admins (the other org key, "Team", was paused), which
// wakes the org's waiting chats. The retried step found the org giving the
// member nothing and ran on the member's own key: no question, no notice.

// orgMembership answers control-plane's membership question from a table.
type orgMembership map[string]accesstokenclient.OrgMembership // by "<org>/<user>"

func (m orgMembership) Membership(_ context.Context, userID, orgID string) (accesstokenclient.OrgMembership, bool, error) {
	got, ok := m[orgID+"/"+userID]
	if !ok {
		return accesstokenclient.OrgMembership{OrgID: orgID}, true, nil
	}
	return got, true, nil
}

func (orgMembership) DefaultOrg(context.Context, string) (string, error) { return "", nil }

// orgFundingWorld is one org chat of a member over the production wiring.
type orgFundingWorld struct {
	t        *testing.T
	ctx      context.Context
	repo     *db.Repo
	org      string
	member   string
	chatID   string
	builtFor []string
	activity *handlers.CallLLMActivity
	env      *testsuite.TestActivityEnvironment
	input    types.ActivityInput
}

func newOrgFundingWorld(t *testing.T) *orgFundingWorld {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	drivers.InitializeAPIKeyProvider(repo)
	prevClient := drivers.AccountAvailabilityClient
	drivers.AccountAvailabilityClient = func(_ context.Context, id models.DriverID, _ models.DriverConfig) (registry.Client, error) {
		return noCatalog{name: string(id)}, nil
	}
	t.Cleanup(func() { drivers.AccountAvailabilityClient = prevClient })

	w := &orgFundingWorld{t: t, ctx: ctx, repo: repo, org: "org-" + uuid.NewString(), member: "member-" + uuid.NewString()}
	drivers.InitializeOrgMembership(orgMembership{w.org + "/" + w.member: {
		OrgID: w.org, Member: true, Role: "member", MemberSince: time.Now().Add(-24 * time.Hour),
	}})
	t.Cleanup(func() { drivers.InitializeOrgMembership(nil) })

	// The network edge: every account the routing builds a driver for, and
	// the provider's default account, is recorded and answers without a call.
	prevFor := accountDriverFor
	accountDriverFor = func(ctx context.Context, userID string, cfg models.DriverConfig, m models.Model, opts ...llm.DriverOption) (llm.Driver, error) {
		w.builtFor = append(w.builtFor, cfg.AccountID)
		return &edgeDriver{apiKey: cfg.APIKey}, nil
	}
	t.Cleanup(func() { accountDriverFor = prevFor })
	prevDefault := drivers.GetDriver
	drivers.GetDriver = func(context.Context, drivers.Scope, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		w.builtFor = append(w.builtFor, "default-account")
		return nil, errors.New("the provider's default account was reached")
	}
	t.Cleanup(func() { drivers.GetDriver = prevDefault })

	recorder := accountlimits.NewRecorder(repo, nil)
	t.Cleanup(recorder.Close)
	routing := accountRouting(repo, recorder)
	require.NotNil(t, routing.DriverFor)

	// A chat of the org with the member's first message: its first turn.
	projectID := "project-" + uuid.NewString()
	w.chatID = "chat-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{ID: projectID, UserID: w.member, Name: "p", Path: t.TempDir(), OrgID: &w.org}))
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: w.chatID, ProjectID: projectID, UserID: w.member, OrgID: &w.org}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: w.chatID, ChatID: w.chatID})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: w.chatID + ":" + w.chatID + ":0", ThreadID: w.chatID})
	require.NoError(t, err)
	_, err = repo.SaveMessageToThread(ctx, w.chatID, w.chatID, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), "hello", nil, nil, nil)
	require.NoError(t, err)

	var suite temporaltest.WorkflowTestSuite
	w.env = suite.NewTestActivityEnvironment()
	w.activity = handlers.NewCallLLMActivity(repo, nil, nil, emptyConfig{}, nil, nil).WithAccountRouting(routing)
	w.env.RegisterActivity(w.activity.Execute)
	w.input = types.ActivityInput{
		Runtime: types.RuntimeContext{ChatID: w.chatID, Thread: w.chatID},
		Node: &reliantv1.Node{Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
			Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Literal{
				Literal: &reliantv1.ModelSelector{Id: "claude-5.5-opus"}}},
		}}},
	}
	return w
}

// orgKey adds one of the org's own Anthropic API keys with access.
func (w *orgFundingWorld) orgKey(name, access string) core.ProviderAccount {
	w.t.Helper()
	owner := core.OrgAccountOwner(w.org)
	secret := "sk-ant-api03-" + name + "-" + uuid.NewString()
	acct, _, err := w.repo.UpsertProviderAccount(w.ctx, core.AccountUpsert{
		Owner: owner, OrgAccess: access, CreatedByUserID: "owner", Provider: "anthropic", AuthKind: core.AuthKindAPIKey,
		IdentityKey: core.APIKeyIdentityKey(owner.Key(), "anthropic", secret), KeyHint: core.KeyHint(secret),
		Secrets: core.AccountSecrets{Secret: secret},
	})
	require.NoError(w.t, err)
	acct, err = w.repo.PatchOwnedProviderAccount(w.ctx, owner, acct.ID, core.AccountPatch{Label: &name})
	require.NoError(w.t, err)
	return acct
}

func (w *orgFundingWorld) pause(acct core.ProviderAccount, paused bool) {
	w.t.Helper()
	_, err := w.repo.PatchOwnedProviderAccount(w.ctx, core.OrgAccountOwner(w.org), acct.ID, core.AccountPatch{Paused: &paused})
	require.NoError(w.t, err)
}

func (w *orgFundingWorld) access(acct core.ProviderAccount, access string) {
	w.t.Helper()
	_, err := w.repo.SetOrgAccountAccess(w.ctx, w.org, acct.ID, access)
	require.NoError(w.t, err)
}

// step runs the chat's call_llm step once: the first attempt, or the retry a
// capacity wake (or an answer, or a probe) sends.
func (w *orgFundingWorld) step() (*reliantv1.CallLLMOutput, error) {
	w.t.Helper()
	val, err := w.env.ExecuteActivity(w.activity.Execute, w.input)
	if err != nil {
		return nil, err
	}
	var out reliantv1.CallLLMOutput
	require.NoError(w.t, val.Get(&out))
	return &out, nil
}

// say posts the member's next message: the next step is a turn start.
func (w *orgFundingWorld) say(text string) {
	w.t.Helper()
	_, err := w.repo.SaveMessageToThread(w.ctx, w.chatID, w.chatID, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), text, nil, nil, nil)
	require.NoError(w.t, err)
}

// onOrgLimit sets the member's "When the org's accounts can't serve a chat".
func (w *orgFundingWorld) onOrgLimit(v orgfunding.OnOrgLimit) {
	w.t.Helper()
	require.NoError(w.t, w.repo.CreateSetting(w.ctx, &db.Setting{ID: uuid.NewString(), UserID: w.member, Key: orgfunding.SettingKey,
		Value: orgfunding.Settings{}.With(w.org, orgfunding.Preference{OnOrgLimit: v}, nil).Value(), ValueType: "string"}))
}

// endLease ends the chat's use_personal answer, as its reset passing does.
func (w *orgFundingWorld) endLease() {
	w.t.Helper()
	q := w.question()
	require.NotNil(w.t, q)
	require.Equal(w.t, core.PersonalFundingUsePersonal, q.State)
	ended := time.Now().Add(-time.Minute)
	q.LeaseUntil = &ended
	require.NoError(w.t, w.repo.GrantPersonalFunding(w.ctx, *q, time.Now()))
	require.False(w.t, w.question().LeaseActive(time.Now(), ""))
}

// limitedOrgChat is the pass's setup: Team (everyone) paused, Backup (selected,
// granted to the member) limited for an hour, and the member's own key.
func limitedOrgChat(t *testing.T) (w *orgFundingWorld, team, backup, mine core.ProviderAccount) {
	t.Helper()
	w = newOrgFundingWorld(t)
	team = w.orgKey("Team Anthropic", core.OrgAccessMembers)
	w.pause(team, true)
	backup = w.orgKey("Backup Anthropic EU", core.OrgAccessSelected)
	require.NoError(t, w.repo.SetOrgAccountGrants(w.ctx, w.org, backup.ID, []string{w.member}, "owner"))
	reset := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, w.repo.BenchAccount(w.ctx, core.OrgAccountOwner(w.org), backup.ID, reset, "five_hour", false))
	mine = db.SeedProviderAccount(t, w.repo, w.member, "anthropic", "sk-ant-api03-member-own-"+uuid.NewString())
	return w, team, backup, mine
}

func (w *orgFundingWorld) question() *core.PersonalFunding {
	w.t.Helper()
	q, err := w.repo.GetChatPersonalFunding(w.ctx, w.chatID)
	require.NoError(w.t, err)
	return q
}

// requireWait asserts a held step: a provider usage limit the workflow waits
// out, never a failed turn.
func requireWait(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err, "the step waits")
	assert.Contains(t, err.Error(), "RELIANT_PROVIDER_USAGE_LIMIT", "as a usage-limit wait, not a failure: %v", err)
}

func TestOrgFunding_KeepWaitingThenAccessWithdrawnNeverSpendsThePersonsAccount(t *testing.T) {
	w, team, backup, mine := limitedOrgChat(t)

	// The first turn: the org's key is limited, the member's own could serve.
	// The chat waits and asks.
	_, err := w.step()
	requireWait(t, err)
	q := w.question()
	require.NotNil(t, q, "the chat asks")
	assert.Equal(t, core.PersonalFundingAsked, q.State)
	assert.Equal(t, core.PersonalFundingLimited, q.Reason)

	// Keep waiting. The answer wakes the chat; it waits on.
	_, err = w.repo.AnswerPersonalFunding(w.ctx, w.chatID, w.member, core.PersonalFundingKeepWaiting, time.Now())
	require.NoError(t, err)
	_, err = w.step()
	requireWait(t, err)
	assert.Equal(t, core.PersonalFundingKeepWaiting, w.question().State)

	// The owner narrows Backup to owners and admins (Team stays paused): the
	// org now gives the member nothing. The access change wakes the chat.
	w.access(backup, core.OrgAccessAdmins)
	_, err = w.step()
	requireWait(t, err)
	assert.NotContains(t, w.builtFor, mine.ID, "no driver on the member's own account")
	assert.Empty(t, w.builtFor, "no provider call at all")
	q = w.question()
	require.NotNil(t, q)
	assert.Equal(t, core.PersonalFundingAsked, q.State, "the withdrawn grant is asked about (§10.1 case 2)")
	assert.Equal(t, core.PersonalFundingUnusable, q.Reason)

	// Every later wake waits on the open question, and asks once.
	_, err = w.step()
	requireWait(t, err)
	assert.Empty(t, w.builtFor)

	// The owner resumes Team: the chat goes on, on the org's key, and the
	// question is over.
	w.pause(team, false)
	out, err := w.step()
	require.NoError(t, err)
	assert.Equal(t, team.ID, out.GetProviderAccountId())
	assert.Equal(t, []string{team.ID}, w.builtFor)
	assert.Nil(t, w.question())
}

// The same withdrawal under "Wait" (the person's setting): the chat never asks,
// so it has no question row. The thread held on the org's limited key carries
// that key as its provenance, so losing it is case 2: still a wait, no call.
func TestOrgFunding_WaitSettingThenAccessWithdrawnWaits(t *testing.T) {
	w, _, backup, mine := limitedOrgChat(t)
	w.onOrgLimit(orgfunding.OnOrgLimitWait)

	_, err := w.step()
	requireWait(t, err)
	assert.Nil(t, w.question(), "wait never asks")

	w.access(backup, core.OrgAccessAdmins)
	_, err = w.step()
	requireWait(t, err)
	assert.NotContains(t, w.builtFor, mine.ID)
	assert.Empty(t, w.builtFor)
	assert.Nil(t, w.question(), "and still does not ask")
}

// The sibling path: the member said Use my account; the lease ran on their key
// until the org's reset; then the org withdrew its accounts. The next turn is
// no longer covered by the lease, and the org gives nothing now: it asks
// (case 2), it does not go on on the member's key.
func TestOrgFunding_LeaseEndedThenAccessWithdrawnAsksAgain(t *testing.T) {
	w, _, backup, mine := limitedOrgChat(t)

	_, err := w.step()
	requireWait(t, err)
	_, err = w.repo.AnswerPersonalFunding(w.ctx, w.chatID, w.member, core.PersonalFundingUsePersonal, time.Now())
	require.NoError(t, err)
	out, err := w.step()
	require.NoError(t, err)
	assert.Equal(t, mine.ID, out.GetProviderAccountId(), "the answer's lease runs on the member's key")

	w.endLease()
	w.access(backup, core.OrgAccessAdmins)
	w.say("and now?")
	_, err = w.step()
	requireWait(t, err)
	assert.Equal(t, []string{mine.ID}, w.builtFor, "no call after the lease ended")
	q := w.question()
	require.NotNil(t, q)
	assert.Equal(t, core.PersonalFundingAsked, q.State)
	assert.Equal(t, core.PersonalFundingUnusable, q.Reason)
}

// Ruled out: a stale lease. The lease ended but the org's key is still
// limited: the turn waits and asks anew, rather than going on.
func TestOrgFunding_LeaseEndedWhileTheOrgIsStillLimitedAsksAgain(t *testing.T) {
	w, _, _, mine := limitedOrgChat(t)

	_, err := w.step()
	requireWait(t, err)
	_, err = w.repo.AnswerPersonalFunding(w.ctx, w.chatID, w.member, core.PersonalFundingUsePersonal, time.Now())
	require.NoError(t, err)
	_, err = w.step()
	require.NoError(t, err)

	w.endLease()
	w.say("and now?")
	_, err = w.step()
	requireWait(t, err)
	assert.Equal(t, []string{mine.ID}, w.builtFor)
	q := w.question()
	require.NotNil(t, q)
	assert.Equal(t, core.PersonalFundingAsked, q.State)
	assert.Equal(t, core.PersonalFundingLimited, q.Reason)
}

// Ruled out: two org keys of different providers. The org opens a key that
// serves other models (OpenAI) while its Anthropic key is limited; the wake it
// sends does not move a Claude chat that is waiting onto the member's key.
func TestOrgFunding_AnOrgKeyForOtherModelsDoesNotEndTheWait(t *testing.T) {
	w, _, _, mine := limitedOrgChat(t)

	_, err := w.step()
	requireWait(t, err)
	_, err = w.repo.AnswerPersonalFunding(w.ctx, w.chatID, w.member, core.PersonalFundingKeepWaiting, time.Now())
	require.NoError(t, err)

	owner := core.OrgAccountOwner(w.org)
	secret := "sk-openai-" + uuid.NewString()
	_, _, err = w.repo.UpsertProviderAccount(w.ctx, core.AccountUpsert{
		Owner: owner, OrgAccess: core.OrgAccessMembers, CreatedByUserID: "owner", Provider: "openai", AuthKind: core.AuthKindAPIKey,
		IdentityKey: core.APIKeyIdentityKey(owner.Key(), "openai", secret), KeyHint: core.KeyHint(secret),
		Secrets: core.AccountSecrets{Secret: secret},
	})
	require.NoError(t, err)
	_, err = w.step()
	requireWait(t, err)
	assert.NotContains(t, w.builtFor, mine.ID)
	assert.Equal(t, core.PersonalFundingKeepWaiting, w.question().State, "the answer stands")
}

// Ruled out: the turn-start upgrade. The member's answered lease runs on their
// key; the org resumes a key that serves; once the lease is over, the next
// turn start moves to the org's key, says so, and the question is over.
// Nothing in the upgrade moves a thread the other way.
func TestOrgFunding_TurnStartUpgradeMovesBackToTheOrg(t *testing.T) {
	w, team, _, mine := limitedOrgChat(t)

	_, err := w.step()
	requireWait(t, err)
	_, err = w.repo.AnswerPersonalFunding(w.ctx, w.chatID, w.member, core.PersonalFundingUsePersonal, time.Now())
	require.NoError(t, err)
	out, err := w.step()
	require.NoError(t, err)
	assert.Equal(t, mine.ID, out.GetProviderAccountId())

	w.pause(team, false)
	w.endLease()
	w.say("and now?")
	out, err = w.step()
	require.NoError(t, err)
	assert.Equal(t, team.ID, out.GetProviderAccountId())
	assert.Equal(t, []string{mine.ID, team.ID}, w.builtFor)
	assert.Nil(t, w.question())
}
