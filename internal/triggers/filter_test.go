// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/webhook/github"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
)

func TestCompileFilterRejectsWhatCannotRun(t *testing.T) {
	cases := map[string]string{
		"syntax": "trigger.payload.action ==",
		// Statically a string. (A dyn read like trigger.payload.action cannot
		// be typed at compile time; see TestFilterThatYieldsANonBoolIsAnError.)
		"not a bool":        "'opened'",
		"unknown root":      "event.action == 'opened'",
		"template braces":   "{{ trigger.payload.action == 'opened' }}",
		"string comparison": "'a' + 1",
	}
	for name, expr := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := CompileFilter(expr)
			require.Error(t, err)
			var fe *FilterError
			require.ErrorAs(t, err, &fe, "a bad filter is the caller's error, reported as such")
		})
	}
}

func TestCompileFilterAcceptsTheTriggerRoot(t *testing.T) {
	for _, expr := range []string{
		"",
		"   ",
		"trigger.payload.action == 'opened'",
		"trigger.kind == 'integration' && trigger.payload.data.issue.number > 10",
		"has(trigger.payload.data.label) && trigger.payload.data.label in ['bug', 'p0']",
		"size(trigger.payload.body.items) > 0",
	} {
		_, err := CompileFilter(expr)
		assert.NoError(t, err, expr)
	}
}

// The "Only from" shapes, and every key of trigger.sender, compile against
// the trigger environment.
func TestCompileFilterAcceptsTheSender(t *testing.T) {
	for _, expr := range []string{
		`trigger.sender.verified && trigger.sender.id in ["U123"]`,
		`trigger.sender.id == "583231"`,
		`(trigger.payload.data.issue.number > 1) && trigger.sender.verified && trigger.sender.id in ["583231", "7"]`,
		`trigger.sender.kind == "email" && trigger.sender.display_name != ""`,
	} {
		_, err := CompileFilter(expr)
		assert.NoError(t, err, expr)
	}
}

// A sender filter evaluates against what the receiver recorded; an event
// with no sender (one recorded before senders existed) is the empty,
// unverified one, so an allowlist rejects it instead of erroring.
func TestSenderFilterMatchesOnlyAVerifiedAllowlistedSender(t *testing.T) {
	f, err := CompileFilter(`trigger.sender.verified && trigger.sender.id in ["U123"]`)
	require.NoError(t, err)
	for name, tc := range map[string]struct {
		sender *core.TriggerSender
		want   bool
	}{
		"allowlisted and verified": {&core.TriggerSender{Kind: core.TriggerSenderKindSlack, ID: "U123", Verified: true}, true},
		"allowlisted, unverified":  {&core.TriggerSender{Kind: core.TriggerSenderKindSMS, ID: "U123", Verified: false}, false},
		"someone else":             {&core.TriggerSender{Kind: core.TriggerSenderKindSlack, ID: "U999", Verified: true}, false},
		"no sender recorded":       {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			hit, err := f.Match(FilterInput{Kind: "integration", Sender: tc.sender})
			require.NoError(t, err)
			assert.Equal(t, tc.want, hit)
		})
	}

	byID, err := CompileFilter(`trigger.sender.id == "583231"`)
	require.NoError(t, err)
	hit, err := byID.Match(FilterInput{Kind: "integration", Sender: &core.TriggerSender{Kind: core.TriggerSenderKindGitHub, ID: "583231", DisplayName: "octocat", Verified: true}})
	require.NoError(t, err)
	assert.True(t, hit)
}

// gitHubSender is the trigger.sender the GitHub receiver records for an
// issue opened by login, whose GitHub user id is id.
func gitHubSender(t *testing.T, login string, id int64) *core.TriggerSender {
	t.Helper()
	body := fmt.Sprintf(`{"action":"opened","issue":{"number":1,"title":"t"},"repository":{"id":1,"full_name":"acme/app"},`+
		`"installation":{"id":2},"sender":{"login":%q,"id":%d,"type":"User"}}`, login, id)
	parsed, err := github.Parse("issues", []byte(body), time.Now())
	require.NoError(t, err)
	require.Len(t, parsed.Events, 1)
	return parsed.Events[0].Sender
}

// "Only from" on GitHub, end to end from what the receiver records: the list
// names a person by their GitHub user id, so it follows them through a rename
// and never follows their old login to whoever registers it next.
func TestGitHubOnlyFromFollowsTheUserIDNotTheLogin(t *testing.T) {
	only, err := CompileFilter(`trigger.sender.verified && trigger.sender.id in ["583231"]`)
	require.NoError(t, err)
	for name, tc := range map[string]struct {
		sender *core.TriggerSender
		want   bool
	}{
		"the person":                        {gitHubSender(t, "octocat", 583231), true},
		"the person, renamed":               {gitHubSender(t, "octo-renamed", 583231), true},
		"someone who claimed their login":   {gitHubSender(t, "octocat", 99999999), false},
		"someone with a lookalike login":    {gitHubSender(t, "OctoCat", 31337), false},
		"a login that happens to be the id": {gitHubSender(t, "583231", 4242), false},
	} {
		t.Run(name, func(t *testing.T) {
			hit, err := only.Match(FilterInput{Kind: "integration", Sender: tc.sender})
			require.NoError(t, err)
			assert.Equal(t, tc.want, hit)
		})
	}

	// A list of logins — what "Only from" wrote before ids — admits nobody,
	// not even the login's owner. That is why the migration that shipped with
	// this change cleared such lists and disabled their triggers instead of
	// leaving them to match nothing in silence.
	stale, err := CompileFilter(`trigger.sender.verified && trigger.sender.id in ["octocat"]`)
	require.NoError(t, err)
	hit, err := stale.Match(FilterInput{Kind: "integration", Sender: gitHubSender(t, "octocat", 583231)})
	require.NoError(t, err)
	assert.False(t, hit)
}

func TestFilterMatchEvaluatesOverThePayload(t *testing.T) {
	f, err := CompileFilter("trigger.payload.data.action == 'opened' && trigger.payload.data.issue.number >= 2")
	require.NoError(t, err)

	hit, err := f.Match(FilterInput{Kind: "integration", Payload: map[string]any{
		"data": map[string]any{"action": "opened", "issue": map[string]any{"number": float64(7)}},
	}})
	require.NoError(t, err)
	assert.True(t, hit)

	miss, err := f.Match(FilterInput{Kind: "integration", Payload: map[string]any{
		"data": map[string]any{"action": "closed", "issue": map[string]any{"number": float64(7)}},
	}})
	require.NoError(t, err)
	assert.False(t, miss)
}

func TestEmptyFilterMatchesEverything(t *testing.T) {
	f, err := CompileFilter("")
	require.NoError(t, err)
	hit, err := f.Match(FilterInput{Kind: "webhook"})
	require.NoError(t, err)
	assert.True(t, hit)
}

func TestFilterThatYieldsANonBoolIsAnError(t *testing.T) {
	f, err := CompileFilter("trigger.payload.action")
	require.NoError(t, err, "a dyn read type-checks; the result is checked at evaluation")
	_, err = f.Match(FilterInput{Kind: "webhook", Payload: map[string]any{"action": "opened"}})
	var fe *FilterError
	require.ErrorAs(t, err, &fe)
}

// A filter that reads a key the payload lacks is a runtime error, not a
// silent false: "the field moved" must be visible on the firing, not
// indistinguishable from "this event was not for me".
func TestFilterOnAMissingKeyIsAnErrorNotAMiss(t *testing.T) {
	f, err := CompileFilter("trigger.payload.data.issue.number > 1")
	require.NoError(t, err)
	_, err = f.Match(FilterInput{Kind: "integration", Payload: map[string]any{"data": map[string]any{}}})
	require.Error(t, err)
}

// gitHubIssueWithLabels is trigger.payload.data as the GitHub receiver
// records an issue opened with these labels.
func gitHubIssueWithLabels(t *testing.T, labels ...string) FilterInput {
	t.Helper()
	ls := make([]string, len(labels))
	for i, l := range labels {
		ls[i] = fmt.Sprintf(`{"id":%d,"name":%q,"color":"ededed"}`, i+1, l)
	}
	body := `{"action":"opened","issue":{"number":7,"title":"t","labels":[` + strings.Join(ls, ",") + `]},` +
		`"repository":{"id":1,"full_name":"acme/app"},"installation":{"id":2},"sender":{"login":"octocat","id":1,"type":"User"}}`
	parsed, err := github.Parse("issues", []byte(body), time.Now())
	require.NoError(t, err)
	require.Len(t, parsed.Events, 1)
	return FilterInput{Kind: "integration", Payload: map[string]any{"data": parsed.Events[0].Data}}
}

// The receiver records issue.labels as label NAMES. So the "skip wontfix"
// filter is a membership test, and the form docs used to show — reading
// l.name — fails on every issue that has a label.
func TestWontfixFilterOverARecordedGitHubIssue(t *testing.T) {
	fixed, err := CompileFilter("!('wontfix' in trigger.payload.data.issue.labels)")
	require.NoError(t, err)
	for name, tc := range map[string]struct {
		labels []string
		want   bool
	}{
		"no labels":        {nil, true},
		"other labels":     {[]string{"bug", "p1"}, true},
		"labelled wontfix": {[]string{"bug", "wontfix"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			hit, err := fixed.Match(gitHubIssueWithLabels(t, tc.labels...))
			require.NoError(t, err)
			assert.Equal(t, tc.want, hit)
		})
	}

	old, err := CompileFilter("!trigger.payload.data.issue.labels.exists(l, l.name == 'wontfix')")
	require.NoError(t, err, "untyped, it compiles: only the declared shape can tell")
	_, err = old.Match(gitHubIssueWithLabels(t, "bug"))
	assert.Error(t, err, "a label is a string, so l.name cannot be read")
}

// triggerspec.Shape declares the `trigger` root closed. Every key the
// runtime puts on it must be declared there, or validation would reject a
// filter that runs.
func TestFilterRootMatchesTheDeclaredShape(t *testing.T) {
	shape, err := triggerspec.NewShape()
	require.NoError(t, err)
	root := FilterInput{
		Kind: "integration", TriggerID: "t", EventID: "e", OccurredAt: time.Now(),
		Payload: map[string]any{"data": map[string]any{}},
		Sender:  &core.TriggerSender{Kind: core.TriggerSenderKindGitHub, ID: "1", DisplayName: "octocat", Verified: true},
	}.Root()
	for key := range root {
		assert.Empty(t, shape.CheckFilter("has(trigger."+key+")"), "trigger.%s is not declared", key)
	}
	sender, ok := root["sender"].(map[string]any)
	require.True(t, ok)
	for key := range sender {
		assert.Empty(t, shape.CheckFilter("has(trigger.sender."+key+")"), "trigger.sender.%s is not declared", key)
	}
}
