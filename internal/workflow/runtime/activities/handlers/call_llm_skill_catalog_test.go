package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	cfgpkg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

const privateSkillPath = "PRIVATE_SKILL_NAME_40e5b620"
const forgeSkillBodyCanary = "PRIVATE_FORGE_SKILL_BODY_40e5b620"

// Exercise the actual handler -> driver boundary with a shared config snapshot.
// Both exclusions are necessary: neither tools:[] nor skills:[] hides the
// catalog by itself. These synthetic canaries never leave the process.
func TestCallLLM_ExcludeSkillCatalogProviderBoundary(t *testing.T) {
	cfg := &cfgpkg.Config{
		GlobalMemoryMD: ambientGlobalCanary, ProjectMemoryMD: ambientProjectCanary,
		RepoMemories: map[string]string{"private": ambientRepoCanary},
		Skills: []cfgpkg.StoredSkill{
			{Name: privateSkillPath, SkillPath: privateSkillPath, Description: skillCatalogCanary, Body: skillBodyCanary},
			{Name: forgeStartHereSkill, SkillPath: forgeStartHereSkill, Scope: "forge", Body: forgeSkillBodyCanary},
		},
	}
	for _, tc := range []struct {
		name                    string
		exclude, preload, retry bool
	}{
		{name: "default catalog unchanged"},
		{name: "default explicit and forge preload unchanged", preload: true},
		{name: "both exclusions", exclude: true},
		{name: "both exclusions survive activity retry", exclude: true, retry: true},
		{name: "shared snapshot still usable after exclusion", preload: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewIdempotencyTestHelper(t)
			defer h.Cleanup()
			ctx := context.Background()
			project := h.CreateTestProject(ctx, uuid.NewString(), uuid.NewString())
			chat := h.CreateTestChat(ctx, uuid.NewString(), project.ID, project.UserID)
			_, err := h.sqlDB.ExecContext(ctx, "UPDATE chats SET no_machine = true WHERE id = $1", chat.ID)
			require.NoError(t, err)
			h.CreateTestUserMessageWithText(ctx, chat.ID, chat.ID, approvedBriefCanary)
			driver := &ambientCaptureDriver{failFirst: tc.retry}
			handler := NewCallLLMActivity(h.Repo(), nil, nil, ambientConfigProvider{cfg}, func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
				return driver, nil
			}, nil)
			input := callLLMInput(chat.ID, chat.ID, "mock-model")
			args := input.Node.GetCallLlm()
			args.ExcludeAmbientMemory = true
			args.ExcludeSkillCatalog = tc.exclude
			args.ToolsConfig = emptyAmbientTools()
			args.Skills = &reliantv1.CelStringList{Value: &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{}}}
			if tc.preload {
				args.Skills.GetLiteral().Values = []string{privateSkillPath}
			}
			if tc.retry {
				var suite temporaltest.WorkflowTestSuite
				env := suite.NewTestWorkflowEnvironment()
				env.RegisterWorkflow(callLLMWithRetries)
				env.RegisterActivityWithOptions(handler.Execute, activity.RegisterOptions{Name: "CallLLM"})
				env.ExecuteWorkflow(callLLMWithRetries, input)
				require.NoError(t, env.GetWorkflowError())
			} else {
				var out CallLLMOutput
				require.NoError(t, h.ExecuteActivity(handler.Execute, input, &out))
			}
			want := 1
			if tc.retry {
				want = 2
			}
			require.Len(t, driver.sent, want)
			for _, req := range driver.sent {
				text := req.text(t)
				require.Contains(t, text, approvedBriefCanary)
				require.Contains(t, text, "This run has no machine")
				require.Empty(t, req.Tools)
				for _, canary := range []string{ambientGlobalCanary, ambientProjectCanary, ambientRepoCanary} {
					require.NotContains(t, text, canary)
				}
				for _, canary := range []string{privateSkillPath, skillCatalogCanary} {
					if tc.exclude {
						require.NotContains(t, text, canary)
					} else {
						require.Contains(t, text, canary)
					}
				}
				for _, canary := range []string{skillBodyCanary, forgeSkillBodyCanary} {
					if tc.preload {
						require.Contains(t, text, canary)
					} else {
						require.NotContains(t, text, canary)
					}
				}
			}
			if tc.retry {
				require.Equal(t, driver.sent[0], driver.sent[1])
			}
			require.Len(t, cfg.Skills, 2, "call-local filtering must not mutate the shared snapshot")
		})
	}
}

func TestCallLLM_ExcludeSkillCatalogRejectsExplicitPreload(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, uuid.NewString(), uuid.NewString())
	chat := h.CreateTestChat(ctx, uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessageWithText(ctx, chat.ID, chat.ID, approvedBriefCanary)
	driver := &ambientCaptureDriver{}
	handler := NewCallLLMActivity(h.Repo(), nil, nil, ambientConfigProvider{&cfgpkg.Config{}}, func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		t.Error("conflicting policy must be rejected before driver resolution")
		return driver, nil
	}, nil)
	input := callLLMInput(chat.ID, chat.ID, "mock-model")
	args := input.Node.GetCallLlm()
	args.ExcludeSkillCatalog = true
	args.Skills = &reliantv1.CelStringList{Value: &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{Values: []string{privateSkillPath}}}}
	var out CallLLMOutput
	require.ErrorContains(t, h.ExecuteActivity(handler.Execute, input, &out), "exclude_skill_catalog cannot be combined with nonempty skills")
	require.Empty(t, driver.sent)
}
