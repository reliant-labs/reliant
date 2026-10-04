// Copyright (c) 2025 Reliant Labs
package commands

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

func TestParseTriggerPresets(t *testing.T) {
	got, err := parseTriggerPresets([]string{"fast", "reviewer=thorough"})
	require.NoError(t, err)
	// A bare name is the TOP-LEVEL preset, whose group key is the empty
	// string — the same shape an interactive start sends.
	assert.Equal(t, map[string]string{"": "fast", "reviewer": "thorough"}, got)

	_, err = parseTriggerPresets([]string{"reviewer="})
	assert.Error(t, err, "a group with no preset name is not a preset assignment")
}

// Params keep their JSON types where they have one, and fall back to the
// literal string — which is what `--param msg=hello` obviously means.
func TestParseTriggerParams(t *testing.T) {
	got, err := parseTriggerParams([]string{
		"depth=2",
		"strict=true",
		"msg=hello world",
		"tags=[\"a\",\"b\"]",
	})
	require.NoError(t, err)

	assert.Equal(t, float64(2), got["depth"].GetNumberValue())
	assert.True(t, got["strict"].GetBoolValue())
	assert.Equal(t, "hello world", got["msg"].GetStringValue())
	require.NotNil(t, got["tags"].GetListValue())
	assert.Len(t, got["tags"].GetListValue().GetValues(), 2)

	_, err = parseTriggerParams([]string{"novalue"})
	assert.Error(t, err, "a param without = is not a key/value pair")
}

func TestParseTriggerOverlap(t *testing.T) {
	for in, want := range map[string]reliantv1.TriggerOverlapPolicy{
		"":      reliantv1.TriggerOverlapPolicy_TRIGGER_OVERLAP_POLICY_UNSPECIFIED,
		"skip":  reliantv1.TriggerOverlapPolicy_TRIGGER_OVERLAP_POLICY_SKIP,
		"SKIP":  reliantv1.TriggerOverlapPolicy_TRIGGER_OVERLAP_POLICY_SKIP,
		"allow": reliantv1.TriggerOverlapPolicy_TRIGGER_OVERLAP_POLICY_ALLOW,
	} {
		got, err := parseTriggerOverlap(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	_, err := parseTriggerOverlap("queue")
	assert.Error(t, err)
}

// fakeTriggerClient resolves ids and names for resolveTrigger's tests.
type fakeTriggerClient struct {
	reliantv1connect.TriggerServiceClient
	byID []*reliantv1.Trigger
}

func (c *fakeTriggerClient) GetTrigger(
	_ context.Context,
	req *connect.Request[reliantv1.GetTriggerRequest],
) (*connect.Response[reliantv1.GetTriggerResponse], error) {
	for _, t := range c.byID {
		if t.GetId() == req.Msg.GetId() {
			return connect.NewResponse(&reliantv1.GetTriggerResponse{Trigger: t}), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, assertNotFound{})
}

func (c *fakeTriggerClient) ListTriggers(
	_ context.Context,
	_ *connect.Request[reliantv1.ListTriggersRequest],
) (*connect.Response[reliantv1.ListTriggersResponse], error) {
	return connect.NewResponse(&reliantv1.ListTriggersResponse{Triggers: c.byID}), nil
}

type assertNotFound struct{}

func (assertNotFound) Error() string { return "not found" }

func TestResolveTriggerByIDAndName(t *testing.T) {
	client := &fakeTriggerClient{byID: []*reliantv1.Trigger{
		{Id: "trigger-1", Name: "nightly", ProjectId: "project-a"},
		{Id: "trigger-2", Name: "weekly", ProjectId: "project-a"},
	}}

	got, err := resolveTrigger(context.Background(), client, "trigger-2")
	require.NoError(t, err)
	assert.Equal(t, "trigger-2", got.GetId())

	got, err = resolveTrigger(context.Background(), client, "nightly")
	require.NoError(t, err)
	assert.Equal(t, "trigger-1", got.GetId(), "a name must resolve through the list")

	_, err = resolveTrigger(context.Background(), client, "nope")
	assert.Error(t, err)
}

// Names are unique per PROJECT, so the same name can exist twice across
// projects. That must be reported, not guessed at.
func TestResolveTriggerReportsAnAmbiguousName(t *testing.T) {
	client := &fakeTriggerClient{byID: []*reliantv1.Trigger{
		{Id: "trigger-1", Name: "nightly", ProjectId: "project-a"},
		{Id: "trigger-2", Name: "nightly", ProjectId: "project-b"},
	}}

	_, err := resolveTrigger(context.Background(), client, "nightly")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "matches 2 triggers")
	assert.Contains(t, err.Error(), "trigger-1")
	assert.Contains(t, err.Error(), "trigger-2")
}

func TestScheduleSummaryShowsCronAndIntervalTogether(t *testing.T) {
	every := "30m"
	// Cron and interval are a UNION — the trigger fires at every time either
	// matches — so showing only one would misstate what it does.
	got := scheduleSummary(&reliantv1.ScheduleSource{
		Cron:     []string{"0 9 * * 1-5"},
		Interval: &every,
	})
	assert.Equal(t, "0 9 * * 1-5, every 30m", got)

	assert.Equal(t, "-", scheduleSummary(nil))
	assert.Equal(t, "-", scheduleSummary(&reliantv1.ScheduleSource{}))
}

// The create command must leave enabled UNSET unless the user said something,
// because the server reads nil as "true on create, unchanged on update".
func TestTriggerCreateLeavesEnabledUnsetByDefault(t *testing.T) {
	cmd := newTriggerCreateCmd()
	flags := &triggerDefinitionFlags{
		name:      "nightly",
		projectID: "project-a",
		message:   "Audit.",
		cron:      []string{"0 9 * * *"},
	}
	def, err := flags.definition(context.Background(), cmd, nil, false)
	require.NoError(t, err)
	assert.Nil(t, def.Enabled, "an unmentioned --disabled must leave enabled unset")

	flags.disabled = true
	def, err = flags.definition(context.Background(), cmd, nil, false)
	require.NoError(t, err)
	require.NotNil(t, def.Enabled)
	assert.False(t, *def.Enabled)
}

func TestTriggerDefinitionRequiresAPromptAndASchedule(t *testing.T) {
	cmd := newTriggerCreateCmd()

	noMessage := &triggerDefinitionFlags{name: "n", projectID: "p", cron: []string{"0 9 * * *"}}
	_, err := noMessage.definition(context.Background(), cmd, nil, false)
	assert.ErrorContains(t, err, "--message is required")

	noSchedule := &triggerDefinitionFlags{name: "n", projectID: "p", message: "go"}
	_, err = noSchedule.definition(context.Background(), cmd, nil, false)
	assert.ErrorContains(t, err, "--cron or --interval")
}

// The name falls back to the workflow, so `--workflow x --cron ...` needs no
// --name; with neither there is nothing to call the trigger.
func TestTriggerDefinitionNameFallsBackToTheWorkflow(t *testing.T) {
	cmd := newTriggerCreateCmd()

	flags := &triggerDefinitionFlags{
		projectID: "p", message: "go", cron: []string{"0 9 * * *"}, workflow: "builtin://agent",
	}
	def, err := flags.definition(context.Background(), cmd, nil, false)
	require.NoError(t, err)
	assert.Equal(t, "builtin://agent", def.GetName())

	flags.workflow = ""
	_, err = flags.definition(context.Background(), cmd, nil, false)
	assert.ErrorContains(t, err, "--name is required")
}

func TestTriggerCommandTreeIsRegistered(t *testing.T) {
	cmd := newTriggerCmd()
	var names []string
	for _, sub := range cmd.Commands() {
		names = append(names, sub.Name())
	}
	for _, want := range []string{
		"create", "list", "get", "update", "enable", "disable", "delete", "fire", "events",
	} {
		assert.Contains(t, names, want)
	}
}
