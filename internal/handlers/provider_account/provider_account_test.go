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
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	provider_account "github.com/reliant-labs/reliant/internal/handlers/provider_account"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// These tests run the service over a real database: ownership, ordering and the legacy-table
// mirror are properties of the store and the handler together, and a fake store would only
// restate the handler's own assumptions.

type env struct {
	t     *testing.T
	repo  *db.Repo
	svc   *provider_account.Service
	valid func(provider, key string) (bool, string)
	keys  []string // keys the validator saw
}

func newEnv(t *testing.T) *env {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	e := &env{t: t, repo: repo, valid: func(string, string) (bool, string) { return true, "ok" }}
	e.svc = provider_account.NewTestProviderAccount(t, provider_account.WithProviderAccountDeps(provider_account.Deps{
		Store: repo,
		ValidateKey: func(_ context.Context, p models.Family, key string) (bool, string) {
			e.keys = append(e.keys, key)
			return e.valid(string(p), key)
		},
	}))
	return e
}

func as(user string) context.Context {
	return context.WithValue(context.Background(), auth.UserIDContextKey, user)
}

func (e *env) addKey(user, provider, key string) *pb.AddProviderAPIKeyResponse {
	e.t.Helper()
	resp, err := e.svc.AddProviderAPIKey(as(user), connect.NewRequest(&pb.AddProviderAPIKeyRequest{Provider: provider, ApiKey: key}))
	require.NoError(e.t, err)
	return resp.Msg
}

func (e *env) oauthAccount(user, provider, id string) core.ProviderAccount {
	e.t.Helper()
	a, _, err := e.repo.UpsertProviderAccount(as(user), core.AccountUpsert{
		UserID: user, Provider: provider, AuthKind: core.AuthKindOAuth, IdentityKey: id, Email: id + "@example.com",
		Secrets: core.AccountSecrets{Secret: "tok-" + id, Refresh: "ref-" + id},
	})
	require.NoError(e.t, err)
	return a
}

func (e *env) list(user string) []*pb.ProviderAccount {
	e.t.Helper()
	resp, err := e.svc.ListProviderAccounts(as(user), connect.NewRequest(&pb.ListProviderAccountsRequest{}))
	require.NoError(e.t, err)
	return resp.Msg.Accounts
}

func TestAddProviderAPIKeyValidatesAndDedupes(t *testing.T) {
	e := newEnv(t)
	const user = "u-keys"

	first := e.addKey(user, "openai", "sk-first-1111")
	assert.True(t, first.Created)
	require.NotNil(t, first.Account.MaskedKey)
	assert.Equal(t, "…1111", *first.Account.MaskedKey)
	assert.Equal(t, pb.ProviderAuthKind_PROVIDER_AUTH_KIND_API_KEY, first.Account.AuthKind)

	// The same key again is the same account.
	again := e.addKey(user, "openai", "sk-first-1111")
	assert.False(t, again.Created)
	assert.Equal(t, first.Account.Id, again.Account.Id)
	assert.Len(t, e.list(user), 1)

	// A second key is a second account behind the first; a label names it.
	e.valid = func(string, string) (bool, string) { return true, "ok" }
	resp, err := e.svc.AddProviderAPIKey(as(user), connect.NewRequest(&pb.AddProviderAPIKeyRequest{
		Provider: "openai", ApiKey: "sk-second-2222", Label: ptr("work")}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.Created)
	assert.Equal(t, "work", resp.Msg.Account.Label)
	accounts := e.list(user)
	require.Len(t, accounts, 2)
	assert.EqualValues(t, 0, accounts[0].Priority)
	assert.EqualValues(t, 1, accounts[1].Priority)

	// The default (first) key is the one the drivers use.
	withSecrets, err := e.repo.ListProviderAccountsWithSecrets(as(user), user)
	require.NoError(t, err)
	require.Len(t, withSecrets, 2)
	assert.Equal(t, "sk-first-1111", withSecrets[0].Secrets.Secret)
	assert.Equal(t, "sk-second-2222", withSecrets[1].Secrets.Secret)

	assert.Len(t, e.keys, 3, "every add validates, including the duplicate")
}

func TestAddProviderAPIKeyRejectsBadInput(t *testing.T) {
	e := newEnv(t)
	call := func(provider, key string) error {
		_, err := e.svc.AddProviderAPIKey(as("u-bad"), connect.NewRequest(&pb.AddProviderAPIKeyRequest{Provider: provider, ApiKey: key}))
		return err
	}
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(call("claude", "sk-x")), "subscription providers sign in, they take no key")
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(call("reliant", "sk-x")), "the managed key is minted, never pasted")
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(call("nonsense", "sk-x")))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(call("openai", "   ")))

	e.valid = func(string, string) (bool, string) { return false, "Invalid API key" }
	err := call("openai", "sk-bad")
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "Invalid API key")
	assert.Empty(t, e.list("u-bad"), "a key the provider rejects is never stored")
}

func TestDeleteProviderAccountReDensifies(t *testing.T) {
	e := newEnv(t)
	const user = "u-del"
	a := e.addKey(user, "openai", "sk-a-1111").Account
	b := e.addKey(user, "openai", "sk-b-2222").Account
	c := e.addKey(user, "openai", "sk-c-3333").Account

	_, err := e.svc.DeleteProviderAccount(as(user), connect.NewRequest(&pb.DeleteProviderAccountRequest{AccountId: b.Id}))
	require.NoError(t, err)
	accounts := e.list(user)
	require.Len(t, accounts, 2)
	assert.Equal(t, a.Id, accounts[0].Id)
	assert.Equal(t, c.Id, accounts[1].Id)
	assert.EqualValues(t, 1, accounts[1].Priority, "priorities re-densify after a delete")

	// Deleting the default promotes the next key to the default.
	_, err = e.svc.DeleteProviderAccount(as(user), connect.NewRequest(&pb.DeleteProviderAccountRequest{AccountId: a.Id}))
	require.NoError(t, err)
	withSecrets, err := e.repo.ListProviderAccountsWithSecrets(as(user), user)
	require.NoError(t, err)
	require.Len(t, withSecrets, 1)
	assert.Equal(t, "sk-c-3333", withSecrets[0].Secrets.Secret)
	assert.EqualValues(t, 0, withSecrets[0].Priority)

	// Deleting the last one disconnects the provider.
	_, err = e.svc.DeleteProviderAccount(as(user), connect.NewRequest(&pb.DeleteProviderAccountRequest{AccountId: c.Id}))
	require.NoError(t, err)
	assert.Empty(t, e.list(user))
}

func TestAnotherUsersAccountIsNotFoundNeverPermissionDenied(t *testing.T) {
	e := newEnv(t)
	acct := e.oauthAccount("owner", "claude", "alice")

	notFound := func(name string, err error) {
		t.Helper()
		require.Error(t, err, name)
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err), name)
	}
	_, err := e.svc.UpdateProviderAccount(as("intruder"), connect.NewRequest(&pb.UpdateProviderAccountRequest{AccountId: acct.ID, Paused: ptr(true)}))
	notFound("update", err)
	_, err = e.svc.DeleteProviderAccount(as("intruder"), connect.NewRequest(&pb.DeleteProviderAccountRequest{AccountId: acct.ID}))
	notFound("delete", err)
	_, err = e.svc.UpdateProviderAccount(as("intruder"), connect.NewRequest(&pb.UpdateProviderAccountRequest{AccountId: "no-such-id", Paused: ptr(true)}))
	notFound("missing", err)
	_, err = e.svc.RefreshProviderAccountUsage(as("intruder"), connect.NewRequest(&pb.RefreshProviderAccountUsageRequest{AccountId: acct.ID}))
	assert.Error(t, err)

	assert.Empty(t, e.list("intruder"))
	require.Len(t, e.list("owner"), 1)
	assert.Nil(t, e.list("owner")[0].LimitedUntil)
	assert.Equal(t, pb.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_AVAILABLE, e.list("owner")[0].Status, "the intruder changed nothing")
}

func TestUpdateRenamesPausesAndRestoresTheEmailLabel(t *testing.T) {
	e := newEnv(t)
	const user = "u-upd"
	acct := e.oauthAccount(user, "claude", "alice")
	update := func(req *pb.UpdateProviderAccountRequest) *pb.ProviderAccount {
		t.Helper()
		req.AccountId = acct.ID
		resp, err := e.svc.UpdateProviderAccount(as(user), connect.NewRequest(req))
		require.NoError(t, err)
		return resp.Msg.Account
	}
	assert.Equal(t, "alice@example.com", e.list(user)[0].Label, "an account is labelled by its detected email")

	assert.Equal(t, "Work Max", update(&pb.UpdateProviderAccountRequest{Label: ptr("  Work Max ")}).Label)
	assert.Equal(t, "alice@example.com", update(&pb.UpdateProviderAccountRequest{Label: ptr("")}).Label, "an empty label restores the email")

	assert.Equal(t, pb.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_PAUSED, update(&pb.UpdateProviderAccountRequest{Paused: ptr(true)}).Status)
	assert.Equal(t, pb.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_AVAILABLE, update(&pb.UpdateProviderAccountRequest{Paused: ptr(false)}).Status)

	_, err := e.svc.UpdateProviderAccount(as(user), connect.NewRequest(&pb.UpdateProviderAccountRequest{AccountId: acct.ID, Label: ptr(string(make([]byte, 200)))}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = e.svc.UpdateProviderAccount(as(user), connect.NewRequest(&pb.UpdateProviderAccountRequest{}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestReorderMustBeAPermutationOfTheProvidersAccounts(t *testing.T) {
	e := newEnv(t)
	const user = "u-reorder"
	a := e.oauthAccount(user, "claude", "alice")
	b := e.oauthAccount(user, "claude", "bob")
	other := e.oauthAccount(user, "codex", "carol")

	resp, err := e.svc.ReorderProviderAccounts(as(user), connect.NewRequest(&pb.ReorderProviderAccountsRequest{
		Provider: "claude", AccountIds: []string{b.ID, a.ID}}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Accounts, 2, "the response lists only that provider")
	assert.Equal(t, b.ID, resp.Msg.Accounts[0].Id)
	assert.EqualValues(t, 0, resp.Msg.Accounts[0].Priority)

	for name, ids := range map[string][]string{
		"missing":   {a.ID},
		"duplicate": {a.ID, a.ID},
		"foreign":   {a.ID, other.ID},
		"unknown":   {a.ID, "nope"},
	} {
		_, err := e.svc.ReorderProviderAccounts(as(user), connect.NewRequest(&pb.ReorderProviderAccountsRequest{Provider: "claude", AccountIds: ids}))
		require.Error(t, err, name)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), name)
	}
	_, err = e.svc.ReorderProviderAccounts(as("someone-else"), connect.NewRequest(&pb.ReorderProviderAccountsRequest{
		Provider: "claude", AccountIds: []string{a.ID, b.ID}}))
	assert.Error(t, err, "another user's order is not theirs to set")
}

func TestListShowsStatusAndStoredWindows(t *testing.T) {
	e := newEnv(t)
	const user = "u-list"
	acct := e.oauthAccount(user, "claude", "alice")
	used := 0.82
	reset := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, e.repo.UpsertAccountLimitSnapshot(as(user), core.LimitState{
		AccountID: acct.ID, UserID: user, Status: "allowed", Source: "headers", ObservedAt: time.Now(),
		Windows: []core.LimitWindow{{Name: "5h", UsedFraction: &used, ResetsAt: reset, LengthS: 5 * 3600}},
	}))

	got := e.list(user)
	require.Len(t, got, 1)
	require.Len(t, got[0].Windows, 1)
	assert.Equal(t, "5-hour", got[0].Windows[0].Label)
	assert.InDelta(t, 0.82, got[0].Windows[0].GetUsedFraction(), 1e-9)
	assert.Equal(t, pb.UsageSource_USAGE_SOURCE_HEADERS, got[0].UsageSource)

	require.NoError(t, e.repo.BenchAccount(as(user), user, acct.ID, time.Now().Add(time.Hour), "5h", false))
	assert.Equal(t, pb.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_LIMITED, e.list(user)[0].Status)
	assert.NotNil(t, e.list(user)[0].LimitedUntil)
}

func TestGetChatProviderAccount(t *testing.T) {
	e := newEnv(t)
	const user = "u-chat"
	ctx := as(user)
	claude := e.oauthAccount(user, "claude", "alice")
	codex := e.oauthAccount(user, "codex", "carol")

	chat, root := seedChat(t, e.repo, user)
	get := func(chatID string, who string) (*pb.GetChatProviderAccountResponse, error) {
		resp, err := e.svc.GetChatProviderAccount(as(who), connect.NewRequest(&pb.GetChatProviderAccountRequest{ChatId: chatID}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}

	// Before the first model call there is no assignment.
	res, err := get(chat, user)
	require.NoError(t, err)
	assert.Nil(t, res.Account)

	// The root thread's newest assignment is the current provider's account.
	_, err = e.repo.SetThreadAssignment(ctx, core.Assignment{ThreadID: root, Provider: "claude", AccountID: claude.ID, AssignedAt: time.Now().Add(-time.Hour), Reason: "initial"}, "")
	require.NoError(t, err)
	res, err = get(chat, user)
	require.NoError(t, err)
	require.NotNil(t, res.Account)
	assert.Equal(t, claude.ID, res.Account.Id)

	// After a cross-provider fallback the codex account is current; the claude one stays remembered.
	_, err = e.repo.SetThreadAssignment(ctx, core.Assignment{ThreadID: root, Provider: "codex", AccountID: codex.ID, AssignedAt: time.Now(), Reason: "limited"}, "")
	require.NoError(t, err)
	res, err = get(chat, user)
	require.NoError(t, err)
	assert.Equal(t, codex.ID, res.Account.Id)

	// Removing the account leaves the other provider's assignment as the answer.
	require.NoError(t, e.repo.DeleteProviderAccount(ctx, user, codex.ID))
	res, err = get(chat, user)
	require.NoError(t, err)
	assert.Equal(t, claude.ID, res.Account.Id)

	// A chat that is another user's, or absent, is NotFound; an empty id is invalid.
	_, err = get(chat, "intruder")
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = get("no-such-chat", user)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = get("", user)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// A chat that fell back to Codex and came back runs on Claude again, though its newest sticky
// assignment is still the Codex one: the chip follows the account that served the last reply.
func TestGetChatProviderAccountFollowsTheAccountThatServedTheLastReply(t *testing.T) {
	e := newEnv(t)
	const user = "u-chip"
	ctx := as(user)
	claude := e.oauthAccount(user, "claude", "alice")
	codex := e.oauthAccount(user, "codex", "carol")
	chat, root := seedChat(t, e.repo, user)

	cw, err := e.repo.CreateContextWindow(ctx, &db.ContextWindow{ID: "cw-" + chat, ThreadID: root, Sequence: 1, CreatedAt: time.Now().UTC()})
	require.NoError(t, err)
	reply := func(id, accountID string) {
		t.Helper()
		_, err := e.repo.SaveMessageAtomic(ctx, db.AtomicMessageWrite{
			MessageID: id, ChatID: chat, ThreadID: root, ContextWindowID: cw.ID,
			Role: pb.MessageRole_MESSAGE_ROLE_ASSISTANT, CreatedAt: time.Now().UTC(), ProviderAccountID: ptr(accountID),
			ChatUpdateData: func(_, _, _ int64) (string, error) { return "{}", nil },
		})
		require.NoError(t, err)
	}
	chipAccount := func() string {
		t.Helper()
		resp, err := e.svc.GetChatProviderAccount(ctx, connect.NewRequest(&pb.GetChatProviderAccountRequest{ChatId: chat}))
		require.NoError(t, err)
		return resp.Msg.GetAccount().GetId()
	}

	_, err = e.repo.SetThreadAssignment(ctx, core.Assignment{ThreadID: root, Provider: "claude", AccountID: claude.ID, AssignedAt: time.Now().Add(-2 * time.Hour), Reason: "initial"}, "")
	require.NoError(t, err)
	reply("m-claude-1", claude.ID)
	assert.Equal(t, claude.ID, chipAccount())

	_, err = e.repo.SetThreadAssignment(ctx, core.Assignment{ThreadID: root, Provider: "codex", AccountID: codex.ID, AssignedAt: time.Now().Add(-time.Hour), Reason: "limited"}, "")
	require.NoError(t, err)
	reply("m-codex-1", codex.ID)
	assert.Equal(t, codex.ID, chipAccount(), "on the fallback provider")

	// Claude resets and the chat returns: the sticky Claude assignment is reused, so the Codex
	// one stays the newest, but the reply came from Claude.
	reply("m-claude-2", claude.ID)
	assert.Equal(t, claude.ID, chipAccount(), "back on Claude")

	// The serving account was deleted: fall back to the newest sticky assignment still standing.
	require.NoError(t, e.repo.DeleteProviderAccount(ctx, user, claude.ID))
	assert.Equal(t, codex.ID, chipAccount())
}

func ptr[T any](v T) *T { return &v }

// seedChat creates a chat for user with the root thread every real chat has (its id is the chat's).
func seedChat(t *testing.T, repo *db.Repo, user string) (chatID, rootThreadID string) {
	t.Helper()
	ctx := as(user)
	now := time.Now().UTC()
	chatID = "chat-" + user
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "t", ProjectID: "test-project", UserID: user,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	th, err := repo.CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	return chatID, th.ID
}

// wakes records the users whose blocked chats an account change woke.
type wakes struct{ users []string }

func (w *wakes) WakeUserChats(_ context.Context, userID, _ string) int {
	w.users = append(w.users, userID)
	return 1
}

// An account that adds capacity — a new key, an un-paused account — wakes the
// user's chats blocked on a provider, so each goes on as a whole without a
// message to every thread. Taking capacity away (a pause) wakes nothing.
func TestAccountThatAddsCapacityWakesTheUsersBlockedChats(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	w := &wakes{}
	svc := provider_account.NewTestProviderAccount(t, provider_account.WithProviderAccountDeps(provider_account.Deps{
		Store:       repo,
		ValidateKey: func(context.Context, models.Family, string) (bool, string) { return true, "ok" },
		ChatWaker:   w,
	}))
	const user = "u-capacity"

	added, err := svc.AddProviderAPIKey(as(user), connect.NewRequest(&pb.AddProviderAPIKeyRequest{Provider: "anthropic", ApiKey: "sk-ant-new-1234"}))
	require.NoError(t, err)
	assert.Equal(t, []string{user}, w.users, "a new key wakes the user's blocked chats")

	id := added.Msg.Account.Id
	_, err = svc.UpdateProviderAccount(as(user), connect.NewRequest(&pb.UpdateProviderAccountRequest{AccountId: id, Paused: ptr(true)}))
	require.NoError(t, err)
	assert.Len(t, w.users, 1, "pausing an account adds no capacity, so nothing is woken")

	_, err = svc.UpdateProviderAccount(as(user), connect.NewRequest(&pb.UpdateProviderAccountRequest{AccountId: id, Paused: ptr(false)}))
	require.NoError(t, err)
	assert.Equal(t, []string{user, user}, w.users, "un-pausing it does")

	_, err = svc.UpdateProviderAccount(as(user), connect.NewRequest(&pb.UpdateProviderAccountRequest{AccountId: id, Label: ptr("work")}))
	require.NoError(t, err)
	assert.Len(t, w.users, 2, "a rename changes no capacity")
}
