// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// The scope of an LLM call (org-provider-accounts §4.1) is decided at the
// activity edge, from the chat row, its agent.start_run lineage and whether a
// person has taken a turn in it, and reaches every account read the call
// makes. Every trigger predates the unattended-funding stamp (P11), so an
// unattended run may still draw on every source (UnattendedAnySource).

// attendanceStore is an in-memory AttendanceStore.
type attendanceStore struct {
	chats   map[string]*db.Chat
	parents map[string]string // agent.start_run chat -> parent_chat_id
	turns   map[string]time.Time
	err     error // returned by every read when set
}

func (s *attendanceStore) GetChat(_ context.Context, id string) (*db.Chat, error) {
	if s.err != nil {
		return nil, s.err
	}
	if c, ok := s.chats[id]; ok {
		return c, nil
	}
	return nil, errors.New("chat not found")
}

func (s *attendanceStore) GetTriggerEventByChat(_ context.Context, kind core.TriggerEventKind, chatID string) (*core.TriggerEvent, error) {
	if s.err != nil {
		return nil, s.err
	}
	parent, ok := s.parents[chatID]
	if !ok || kind != core.TriggerEventKindAgentStartRun {
		return nil, core.ErrTriggerEventNotFound
	}
	return &core.TriggerEvent{Kind: kind, ChatID: &chatID, Payload: map[string]any{"parent_chat_id": parent}}, nil
}

func (s *attendanceStore) ChatPersonTurnAt(_ context.Context, chatID string) (*time.Time, error) {
	if s.err != nil {
		return nil, s.err
	}
	if at, ok := s.turns[chatID]; ok {
		return &at, nil
	}
	return nil, nil
}

func chatOf(id, kind string, trigger *string) *db.Chat {
	return &db.Chat{ID: id, UserID: "u1", OrgID: strPtr("o1"), LaunchKind: kind, TriggerID: trigger}
}

func TestChatAttendance(t *testing.T) {
	scheduled := string(core.TriggerEventKindSchedule)
	started := string(core.TriggerEventKindAgentStartRun)
	for _, tc := range []struct {
		name  string
		chat  *db.Chat
		store *attendanceStore
		want  attendanceVerdict
	}{
		{"a person's chat is attended", chatOf("c", string(core.TriggerEventKindChatStart), nil), nil, verdictAttended},
		{"Run now of a workflow is attended", chatOf("c", string(core.TriggerEventKindManualRun), nil), nil, verdictAttended},
		{"a chat that predates launch events is attended", chatOf("c", "", nil), nil, verdictAttended},
		{"an unattended kind with no stored trigger is attended", chatOf("c", scheduled, nil), nil, verdictAttended},
		{"a scheduled trigger's run is unattended", chatOf("c", scheduled, strPtr("t1")), &attendanceStore{}, verdictUnattended},
		{"a webhook trigger's run is unattended",
			chatOf("c", string(core.TriggerEventKindWebhook), strPtr("t1")), &attendanceStore{}, verdictUnattended},
		{"I2: a trigger's chat a person took a turn in is attended",
			chatOf("c", scheduled, strPtr("t1")), &attendanceStore{turns: map[string]time.Time{"c": time.Now()}}, verdictAttended},
		{"I1: a run a person's chat started is attended",
			chatOf("child", started, nil), &attendanceStore{
				chats:   map[string]*db.Chat{"root": chatOf("root", string(core.TriggerEventKindChatStart), nil)},
				parents: map[string]string{"child": "root"},
			}, verdictAttended},
		{"I1: a run a trigger's run started is unattended, at any depth",
			chatOf("grandchild", started, nil), &attendanceStore{
				chats: map[string]*db.Chat{
					"child": chatOf("child", started, nil),
					"root":  chatOf("root", scheduled, strPtr("t1")),
				},
				parents: map[string]string{"grandchild": "child", "child": "root"},
			}, verdictUnattended},
		{"I1+I2: a sub-agent of a trigger's chat a person took a turn in is attended",
			chatOf("child", started, nil), &attendanceStore{
				chats:   map[string]*db.Chat{"root": chatOf("root", scheduled, strPtr("t1"))},
				parents: map[string]string{"child": "root"},
				turns:   map[string]time.Time{"root": time.Now()},
			}, verdictAttended},
		{"an agent-started run with no launch event fails closed",
			chatOf("child", started, nil), &attendanceStore{}, verdictUnknown},
		{"an agent-started run whose parent is gone fails closed",
			chatOf("child", started, nil), &attendanceStore{parents: map[string]string{"child": "gone"}}, verdictUnknown},
		{"a lineage cycle fails closed",
			chatOf("a", started, nil), &attendanceStore{
				chats:   map[string]*db.Chat{"a": chatOf("a", started, nil), "b": chatOf("b", started, nil)},
				parents: map[string]string{"a": "b", "b": "a"},
			}, verdictUnknown},
		{"a failed read fails closed",
			chatOf("c", scheduled, strPtr("t1")), &attendanceStore{err: errors.New("db down")}, verdictUnknown},
		{"no store fails closed", chatOf("c", scheduled, strPtr("t1")), nil, verdictUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var store AttendanceStore
			if tc.store != nil {
				store = tc.store
			}
			assert.Equal(t, tc.want, chatAttended(context.Background(), store, tc.chat))
			who := chatAttendance(context.Background(), store, tc.chat)
			assert.Equal(t, tc.want == verdictAttended, who.attended)
			assert.Equal(t, tc.want == verdictUnattended, who.anySource,
				"a resolved automation keeps every source until P11's stamp (I4); an undecided run gets the org's only")
		})
	}
	t.Run("no chat is the person's own", func(t *testing.T) {
		assert.Equal(t, "", chatOrgID(nil))
		assert.True(t, chatAttendance(context.Background(), nil, nil).attended)
	})
}

func TestLLMCallSpecScope(t *testing.T) {
	assert.Equal(t, drivers.PersonalScope("u1"), llmCallSpec{UserID: "u1"}.scope(), "no org: the personal scope, attended")
	assert.Equal(t, drivers.PersonalScope("u1"), llmCallSpec{UserID: "u1", Attended: false}.scope(), "no org stays attended")
	assert.Equal(t, drivers.Scope{UserID: "u1", OrgID: "o1"}, llmCallSpec{UserID: "u1", OrgID: "o1"}.scope())
}

// scopeRecorder records the scope of every account read of a call.
type scopeRecorder struct {
	mu     sync.Mutex
	scopes []drivers.Scope
}

func (r *scopeRecorder) record(s drivers.Scope) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scopes = append(r.scopes, s)
}

func (r *scopeRecorder) all() []drivers.Scope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]drivers.Scope(nil), r.scopes...)
}

func recordLookups(t *testing.T, avail models.AvailableDrivers) *scopeRecorder {
	t.Helper()
	rec := &scopeRecorder{}
	orig := lookupAvailableDrivers
	lookupAvailableDrivers = func(_ context.Context, s drivers.Scope) (models.AvailableDrivers, error) {
		rec.record(s)
		return avail, nil
	}
	t.Cleanup(func() { lookupAvailableDrivers = orig })
	return rec
}

// The registry's candidates and the default-account fallback read the same
// scope: the fallback can never pick from a different set than resolution.
func TestResolveLLMCall_EveryAccountReadTakesTheSpecsScope(t *testing.T) {
	want := drivers.Scope{UserID: "u1", OrgID: "o1", Attended: false}
	lookups := recordLookups(t, driversWith(apiKeyAccount("a", "alice@acme.com", 0)))
	captured := &capturedDriverOptions{}
	orig := drivers.GetDriver
	drivers.GetDriver = captureDriverOptionsResolver(captured)
	t.Cleanup(func() { drivers.GetDriver = orig })

	_, err := resolveLLMCall(context.Background(), nil, llmCallSpec{
		UserID: "u1", OrgID: "o1", Attended: false, SessionID: "s",
		Selector: models.ModelSelector{ID: "claude-4.5-haiku@anthropic"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, lookups.all())
	for _, s := range lookups.all() {
		assert.Equal(t, want, s, "resolution reads the run's scope")
	}
	assert.Equal(t, want, captured.Scope, "the default-account fallback reads the same scope")
}

// Compaction picks its summarizer from the thread's scope.
func TestResolveCompactionModel_ReadsTheSpecsScope(t *testing.T) {
	lookups := recordLookups(t, driversWith(apiKeyAccount("a", "alice@acme.com", 0)))
	chat := &db.Chat{UserID: "u1", OrgID: strPtr("o1"), LaunchKind: string(core.TriggerEventKindSchedule), TriggerID: strPtr("t1")}
	_, _, _ = resolveCompactionModelForCall(context.Background(), nil, llmCallSpec{UserID: chat.UserID, OrgID: chatOrgID(chat),
		Attended: chatAttendance(context.Background(), &attendanceStore{}, chat).attended})
	assert.Equal(t, []drivers.Scope{{UserID: "u1", OrgID: "o1", Attended: false}}, lookups.all())
}

// A chat's title is resolved in the chat's scope; a title for no chat in the
// person's own.
func TestGenerateTitle_ResolvesInTheChatsScope(t *testing.T) {
	lookups := recordLookups(t, driversWith(apiKeyAccount("a", "alice@acme.com", 0)))
	orig := drivers.GetDriver
	drivers.GetDriver = captureDriverOptionsResolver(&capturedDriverOptions{})
	t.Cleanup(func() { drivers.GetDriver = orig })
	activity := NewGenerateTitleActivity(nil, nil)

	chat := &db.Chat{ID: "c1", UserID: "u1", OrgID: strPtr("o1"), LaunchKind: string(core.TriggerEventKindChatStart)}
	_, _ = activity.generateTitleForChat(context.Background(), chat, "rename the billing page")
	require.NotEmpty(t, lookups.all())
	assert.Equal(t, drivers.Scope{UserID: "u1", OrgID: "o1", Attended: true}, lookups.all()[0])

	lookups.mu.Lock()
	lookups.scopes = nil
	lookups.mu.Unlock()
	_, _ = activity.generateTitle(context.Background(), "u1", "rename the billing page")
	require.NotEmpty(t, lookups.all())
	assert.Equal(t, drivers.PersonalScope("u1"), lookups.all()[0])
}

// The failover's alternative is chosen from the scope the refused call was
// routed over.
func TestFallbackModel_ReadsTheCallsScope(t *testing.T) {
	lookups := recordLookups(t, models.AvailableDrivers{})
	want := drivers.Scope{UserID: "u1", OrgID: "o1", Attended: true}
	(&AccountRouting{}).fallbackModel(context.Background(), usageLimitContext{
		Scope: want, Driver: "claude", Policy: types.FailoverPolicy{FallbackModel: "gpt-5.5@codex"},
	})
	assert.Equal(t, []drivers.Scope{want}, lookups.all())
}

// personalOnlyStore is an AvailabilityStore holding one of the person's own
// API keys and nothing of any org's.
type personalOnlyStore struct{}

func (personalOnlyStore) ListProviderAccountsWithSecrets(context.Context, string) ([]core.AccountWithSecrets, error) {
	return []core.AccountWithSecrets{{
		ProviderAccount: core.ProviderAccount{ID: "mine", UserID: "u1", OwnerKind: core.AccountOwnerUser, Provider: "anthropic", AuthKind: core.AuthKindAPIKey},
		Secrets:         core.AccountSecrets{Secret: "sk-mine"},
	}}, nil
}
func (personalOnlyStore) ListAccountLimits(context.Context, string) (map[string]core.LimitState, error) {
	return nil, nil
}
func (personalOnlyStore) ListOwnedProviderAccountsWithSecrets(context.Context, core.AccountOwner) ([]core.AccountWithSecrets, error) {
	return nil, nil
}
func (personalOnlyStore) ListOwnedAccountLimits(context.Context, core.AccountOwner) (map[string]core.LimitState, error) {
	return nil, nil
}
func (personalOnlyStore) ListMemberAccountGrants(context.Context, string, string) (map[string]time.Time, error) {
	return nil, nil
}

// What each verdict lets a call spend, through the loader every scope goes
// through: an undecided run never reaches the person's own account; a
// resolved automation (no stamp yet, I4) and an attended run do.
func TestChatAttendance_UndecidedRunNeverSpendsThePersonsOwnAccount(t *testing.T) {
	trigger := chatOf("c", string(core.TriggerEventKindSchedule), strPtr("t1"))
	for _, tc := range []struct {
		name     string
		store    AttendanceStore
		personal bool
	}{
		{"undecided: the turn read failed", &attendanceStore{err: errors.New("db down")}, false},
		{"undecided: no store", nil, false},
		{"resolved automation, before P11's stamp", &attendanceStore{}, true},
		{"attended: the person took a turn", &attendanceStore{turns: map[string]time.Time{"c": time.Now()}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			who := chatAttendance(context.Background(), tc.store, trigger)
			scope := llmCallSpec{UserID: "u1", OrgID: "o1", Attended: who.attended, UnattendedAnySource: who.anySource}.scope()
			avail, err := drivers.BuildAvailableDrivers(context.Background(), personalOnlyStore{}, scope)
			require.NoError(t, err)
			if tc.personal {
				assert.Len(t, avail.Accounts["anthropic"], 1, "the person's own account serves")
			} else {
				assert.Empty(t, avail.Accounts["anthropic"], "the person's own account is refused")
				assert.True(t, scope.OrgFundedOnly())
			}
		})
	}
}
