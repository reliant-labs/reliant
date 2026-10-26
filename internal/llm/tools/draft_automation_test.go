// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/rctx"
)

func scheduleDraft() DraftAutomationParams {
	return DraftAutomationParams{
		When:    DraftAutomationWhen{Kind: "schedule", Cron: []string{"  0 9  * * 1-5 "}},
		Prompt:  " Summarise yesterday's failed CI runs. ",
		Summary: "Every weekday at 9:00, summarise failed CI",
	}
}

func TestBuildAutomationDraft_Schedule(t *testing.T) {
	t.Parallel()
	draft, err := BuildAutomationDraft(scheduleDraft(), DraftOptions{Timezone: "Europe/Paris"})
	require.NoError(t, err)
	def := draft.GetDefinition()
	assert.Equal(t, "Summarise yesterday's failed CI runs.", def.GetMessage())
	assert.Equal(t, []string{"0 9 * * 1-5"}, def.GetSchedule().GetCron())
	assert.Equal(t, "Europe/Paris", def.GetSchedule().GetTimezone(), "an unnamed zone is the caller's")
	assert.Empty(t, def.GetWorkflow(), "empty is the user's default agent")
	// Where it runs and as whom are the person's choices, never the draft's.
	assert.Empty(t, def.GetProjectId())
	assert.Empty(t, def.GetDaemonId())
	assert.False(t, def.GetNoMachine())
	assert.Nil(t, def.Enabled)
	assert.Nil(t, def.ConnectionId)
	assert.Equal(t, reliantv1.ActingIdentity_ACTING_IDENTITY_UNSPECIFIED, def.GetActingIdentity())
}

func TestBuildAutomationDraft_LeavesAnUnknownZoneForTheDialog(t *testing.T) {
	t.Parallel()
	draft, err := BuildAutomationDraft(scheduleDraft(), DraftOptions{})
	require.NoError(t, err)
	assert.Empty(t, draft.GetDefinition().GetSchedule().GetTimezone())
}

func TestBuildAutomationDraft_FixedWorkflowWins(t *testing.T) {
	t.Parallel()
	p := scheduleDraft()
	p.Workflow = "something-else"
	draft, err := BuildAutomationDraft(p, DraftOptions{Workflow: "ci-digest"})
	require.NoError(t, err)
	assert.Equal(t, "ci-digest", draft.GetDefinition().GetWorkflow())
}

func TestBuildAutomationDraft_Kinds(t *testing.T) {
	t.Parallel()
	base := func(w DraftAutomationWhen) DraftAutomationParams {
		return DraftAutomationParams{When: w, Prompt: "Review it.", Summary: "On new PRs, review"}
	}

	d, err := BuildAutomationDraft(base(DraftAutomationWhen{Kind: "integration", Integration: "github", Events: []string{"pull_request.opened", " "}, Filter: "trigger.payload.data.pull_request.draft == false"}), DraftOptions{})
	require.NoError(t, err)
	assert.Equal(t, "github", d.GetDefinition().GetIntegration().GetIntegration())
	assert.Equal(t, []string{"pull_request.opened"}, d.GetDefinition().GetIntegration().GetEvents())
	assert.Equal(t, "trigger.payload.data.pull_request.draft == false", d.GetDefinition().GetFilter())

	d, err = BuildAutomationDraft(base(DraftAutomationWhen{Kind: "webhook"}), DraftOptions{})
	require.NoError(t, err)
	assert.NotNil(t, d.GetDefinition().GetWebhook())

	d, err = BuildAutomationDraft(base(DraftAutomationWhen{Kind: "workflow_event", Outcomes: []string{"failed"}}), DraftOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"failed"}, d.GetDefinition().GetWorkflowEvent().GetOutcomes())
}

func TestBuildAutomationDraft_Refusals(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*DraftAutomationParams){
		"no prompt":            func(p *DraftAutomationParams) { p.Prompt = " " },
		"no summary":           func(p *DraftAutomationParams) { p.Summary = "" },
		"bad cron":             func(p *DraftAutomationParams) { p.When.Cron = []string{"every day at 9"} },
		"empty schedule":       func(p *DraftAutomationParams) { p.When.Cron = nil },
		"short interval":       func(p *DraftAutomationParams) { p.When.Cron = nil; p.When.Interval = "5s" },
		"unknown zone":         func(p *DraftAutomationParams) { p.When.Timezone = "Mars/Olympus" },
		"filter on a schedule": func(p *DraftAutomationParams) { p.When.Filter = "true" },
		"unknown kind":         func(p *DraftAutomationParams) { p.When.Kind = "email" },
		"integration without id": func(p *DraftAutomationParams) {
			p.When = DraftAutomationWhen{Kind: "integration", Events: []string{"x"}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := scheduleDraft()
			mutate(&p)
			_, err := BuildAutomationDraft(p, DraftOptions{})
			assert.Error(t, err)
		})
	}
}

func TestBuildAutomationDraft_TrimsNameAndAssumptions(t *testing.T) {
	t.Parallel()
	p := scheduleDraft()
	p.Name = "  Morning   CI   digest "
	p.Assumptions = []string{"a", " ", "b", "c", "d", "e", "f"}
	d, err := BuildAutomationDraft(p, DraftOptions{})
	require.NoError(t, err)
	assert.Equal(t, "Morning CI digest", d.GetDefinition().GetName())
	assert.Equal(t, []string{"a", "b", "c", "d", "e"}, d.GetAssumptions())
}

// The chat card reads the draft from the result metadata, as protojson.
func TestDraftAutomationTool_ReturnsTheDraftAsMetadata(t *testing.T) {
	t.Parallel()
	tool := &draftAutomationTool{}
	resp, err := tool.Execute(&rctx.ToolContext{}, scheduleDraft())
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "nothing is created or turned on")

	var meta DraftAutomationMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	draft := &reliantv1.AutomationDraft{}
	require.NoError(t, protojson.Unmarshal(meta.Draft, draft))
	assert.Equal(t, "Every weekday at 9:00, summarise failed CI", draft.GetSummary())
	assert.Equal(t, []string{"0 9 * * 1-5"}, draft.GetDefinition().GetSchedule().GetCron())

	bad := scheduleDraft()
	bad.When.Cron = []string{"nope"}
	resp, err = tool.Execute(&rctx.ToolContext{}, bad)
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Empty(t, resp.Metadata)
}
