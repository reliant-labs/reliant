// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		"has(trigger.payload.data.label) && trigger.payload.data.label.name in ['bug', 'p0']",
		"size(trigger.payload.body.items) > 0",
	} {
		_, err := CompileFilter(expr)
		assert.NoError(t, err, expr)
	}
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
