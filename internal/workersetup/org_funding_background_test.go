// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/accountlimits"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// A chat's background calls over the production wiring (the world of
// org_funding_wake_consent_test.go): the browser pass's chat titled itself on
// the member's own OpenAI key three times while the org's Anthropic key was
// limited and the member had agreed to nothing. A title, and a compaction,
// now read the chat's consent and never write it.

func (w *orgFundingWorld) backgroundRouting() *handlers.AccountRouting {
	w.t.Helper()
	recorder := accountlimits.NewRecorder(w.repo, nil)
	w.t.Cleanup(recorder.Close)
	return accountRouting(w.repo, recorder)
}

// backgroundActivityEnv is an activity environment of its own: every
// activity's method is named Execute, so the turn's would be replaced.
func backgroundActivityEnv() *testsuite.TestActivityEnvironment {
	var suite temporaltest.WorkflowTestSuite
	return suite.NewTestActivityEnvironment()
}

// title runs the chat's GenerateTitle activity once.
func (w *orgFundingWorld) title(first string) (string, error) {
	w.t.Helper()
	activity := handlers.NewGenerateTitleActivity(w.repo, nil).WithAccountRouting(w.backgroundRouting())
	env := backgroundActivityEnv()
	env.RegisterActivity(activity.Execute)
	val, err := env.ExecuteActivity(activity.Execute, handlers.GenerateTitleInput{ChatID: w.chatID, FirstMessage: first})
	if err != nil {
		return "", err
	}
	var out handlers.GenerateTitleOutput
	require.NoError(w.t, val.Get(&out))
	return out.Title, nil
}

// compact runs a forced compaction of the chat's root thread once.
func (w *orgFundingWorld) compact() (*handlers.CompactOutput, error) {
	w.t.Helper()
	activity := handlers.NewCompactActivity(w.repo, nil).WithAccountRouting(w.backgroundRouting())
	env := backgroundActivityEnv()
	env.RegisterActivity(activity.Execute)
	input := types.ActivityInput{
		Runtime: types.RuntimeContext{ChatID: w.chatID, SessionID: w.chatID, Thread: w.chatID},
		Node: &reliantv1.Node{Type: "compact", Args: &reliantv1.Node_Compact{Compact: &reliantv1.CompactArgs{
			Force: &reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: true}},
		}}},
	}
	val, err := env.ExecuteActivity(activity.Execute, input)
	if err != nil {
		return nil, err
	}
	var out handlers.CompactOutput
	require.NoError(w.t, val.Get(&out))
	return &out, nil
}

func TestOrgFunding_TitleOnALimitedOrgKeyNeverCallsThePersonsAccount(t *testing.T) {
	w, _, _, mine := limitedOrgChat(t)
	myOpenAI := db.SeedProviderAccount(t, w.repo, w.member, "openai", "sk-openai-member-own")
	w.orgKey("Team OpenAI", core.OrgAccessAdmins) // the member's org gives them no OpenAI key

	// The chat's turn asks, as in the pass; the member has not answered.
	_, err := w.step()
	requireWait(t, err)
	before := w.question()
	require.NotNil(t, before)

	first := "Rename the billing page and update its copy everywhere it appears"
	title, err := w.title(first)
	require.NoError(t, err, "a chat is never left untitled")
	assert.NotEmpty(t, title)
	assert.True(t, strings.HasPrefix(first, strings.TrimSuffix(title, "...")), "the deterministic title is the first message: %q", title)
	assert.NotContains(t, w.builtFor, mine.ID)
	assert.NotContains(t, w.builtFor, myOpenAI.ID)
	assert.Empty(t, w.builtFor, "no provider call at all")
	chat, err := w.repo.GetChat(w.ctx, w.chatID)
	require.NoError(t, err)
	assert.Equal(t, title, chat.Title)
	after := w.question()
	require.NotNil(t, after)
	assert.Equal(t, before.State, after.State, "the chat's question is untouched")
	assert.Equal(t, before.Reason, after.Reason)
	assert.True(t, before.UpdatedAt.Equal(after.UpdatedAt))
}

func TestOrgFunding_TitleWithConsentUsesThePersonsAccount(t *testing.T) {
	w, _, _, mine := limitedOrgChat(t)
	_, err := w.step()
	requireWait(t, err)
	_, err = w.repo.AnswerPersonalFunding(w.ctx, w.chatID, w.member, core.PersonalFundingUsePersonal, time.Now())
	require.NoError(t, err)
	before := w.question()

	// The edge driver answers no set_title, so the title itself fails; what
	// matters is that the request went to the member's account, with their
	// answer, and nothing about the answer changed.
	_, _ = w.title("Rename the billing page")
	assert.Contains(t, w.builtFor, mine.ID)
	after := w.question()
	require.NotNil(t, after)
	assert.Equal(t, before.State, after.State)
	assert.Equal(t, before.Announced, after.Announced, "the turn announces the answer, not the title")
}

func TestOrgFunding_CompactionWithoutConsentLeavesTheTurnToDecide(t *testing.T) {
	w, _, _, mine := limitedOrgChat(t)
	out, err := w.compact()
	require.NoError(t, err)
	assert.Contains(t, out.GetSkippedReason(), "consent")
	assert.NotContains(t, w.builtFor, mine.ID)
	assert.Nil(t, w.question(), "the compaction never asks")

	// The turn that follows decides: it asks and waits.
	_, err = w.step()
	requireWait(t, err)
	require.NotNil(t, w.question())
	assert.Equal(t, core.PersonalFundingAsked, w.question().State)
}

func TestOrgFunding_CompactionUnderTheChatsAnswerSummarizesOnIt(t *testing.T) {
	w, _, _, mine := limitedOrgChat(t)
	_, err := w.step()
	requireWait(t, err)
	_, err = w.repo.AnswerPersonalFunding(w.ctx, w.chatID, w.member, core.PersonalFundingUsePersonal, time.Now())
	require.NoError(t, err)

	out, err := w.compact()
	require.NoError(t, err)
	assert.Empty(t, out.GetSkippedReason())
	assert.Equal(t, []string{mine.ID}, w.builtFor)
	assert.False(t, w.question().Announced, "the turn announces the answer, not the compaction")
}
