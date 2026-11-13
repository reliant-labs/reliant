// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// The scope of an LLM call (org-provider-accounts §4.1) is read off the chat
// row at the activity edge and reaches every account read the call makes.
// Every trigger predates the unattended-funding stamp (P11), so an unattended
// run may still draw on every source (UnattendedAnySource).

func TestChatScope_ReadFromTheChatRow(t *testing.T) {
	for _, tc := range []struct {
		name string
		chat *db.Chat
		want drivers.Scope
	}{
		{"a chat with no org (pre-org backlog) is personal",
			&db.Chat{UserID: "u1", LaunchKind: string(core.TriggerEventKindChatStart)},
			drivers.PersonalScope("u1")},
		{"a person's chat in an org is attended",
			&db.Chat{UserID: "u1", OrgID: strPtr("o1"), LaunchKind: string(core.TriggerEventKindChatStart)},
			drivers.Scope{UserID: "u1", OrgID: "o1", Attended: true}},
		{"a scheduled trigger's run is unattended",
			&db.Chat{UserID: "u1", OrgID: strPtr("o1"), LaunchKind: string(core.TriggerEventKindSchedule), TriggerID: strPtr("t1")},
			drivers.Scope{UserID: "u1", OrgID: "o1", Attended: false, UnattendedAnySource: true}},
		{"a webhook trigger's run is unattended",
			&db.Chat{UserID: "u1", OrgID: strPtr("o1"), LaunchKind: string(core.TriggerEventKindWebhook), TriggerID: strPtr("t1")},
			drivers.Scope{UserID: "u1", OrgID: "o1", Attended: false, UnattendedAnySource: true}},
		{"Run now of a workflow is attended",
			&db.Chat{UserID: "u1", OrgID: strPtr("o1"), LaunchKind: string(core.TriggerEventKindManualRun)},
			drivers.Scope{UserID: "u1", OrgID: "o1", Attended: true}},
		{"an unattended kind with no stored trigger is attended",
			&db.Chat{UserID: "u1", OrgID: strPtr("o1"), LaunchKind: string(core.TriggerEventKindSchedule)},
			drivers.Scope{UserID: "u1", OrgID: "o1", Attended: true}},
		{"a chat that predates launch events is attended",
			&db.Chat{UserID: "u1", OrgID: strPtr("o1")},
			drivers.Scope{UserID: "u1", OrgID: "o1", Attended: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, chatScope(tc.chat))
			spec := llmCallSpec{UserID: tc.chat.UserID, OrgID: chatOrgID(tc.chat), Attended: chatAttended(tc.chat),
				UnattendedAnySource: chatUnattendedAnySource(tc.chat)}
			assert.Equal(t, tc.want, spec.scope(), "a spec built from the row scopes as chatScope does")
		})
	}
	t.Run("no chat is the person's own", func(t *testing.T) {
		assert.Equal(t, "", chatOrgID(nil))
		assert.True(t, chatAttended(nil))
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
	_, _, _ = resolveCompactionModelForCall(context.Background(), nil, llmCallSpec{UserID: chat.UserID, OrgID: chatOrgID(chat), Attended: chatAttended(chat)})
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
