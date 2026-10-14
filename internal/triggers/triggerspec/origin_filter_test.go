// Copyright (c) 2025 Reliant Labs
package triggerspec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func originRoot(sameWorkflow bool, confidence, triggerName string) map[string]any {
	return map[string]any{"origin": map[string]any{
		"caused_by_reliant": confidence != "none", "confidence": confidence, "run_id": "r", "workflow": "w",
		"trigger_name": triggerName, "same_workflow": sameWorkflow, "actor": "app", "lineage_depth": int64(0),
	}}
}

func TestTheDefaultIgnoresOnlyAConfidentOwnEcho(t *testing.T) {
	f, err := CompileOriginFilter("")
	require.NoError(t, err)
	assert.Equal(t, DefaultOriginFilter, f.Expr(), "an empty origin_filter IS the default, written out")

	for _, tc := range []struct {
		name         string
		sameWorkflow bool
		confidence   string
		allowed      bool
	}{
		{"own echo, exact", true, "exact", false},
		{"own echo, intent", true, "intent", false},
		{"own workflow but only an app match", true, "app", true},
		{"own workflow but only a claim", true, "claimed", true},
		{"another workflow, exact", false, "exact", true},
		{"nobody's", false, "none", true},
	} {
		got, err := f.Allow(Scope{Trigger: originRoot(tc.sameWorkflow, tc.confidence, "")})
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.allowed, got, tc.name)
	}
}

func TestTrueReceivesEverything(t *testing.T) {
	f, err := CompileOriginFilter("true")
	require.NoError(t, err)
	got, err := f.Allow(Scope{Trigger: originRoot(true, "exact", "")})
	require.NoError(t, err)
	assert.True(t, got)
}

func TestACustomExpressionAllowsOneDeliberateRetrigger(t *testing.T) {
	f, err := CompileOriginFilter("!trigger.origin.same_workflow || trigger.origin.trigger_name == 'responder-push'")
	require.NoError(t, err)
	for _, tc := range []struct {
		same    bool
		trigger string
		allowed bool
	}{
		{true, "responder-push", true},
		{true, "other", false},
		{false, "other", true},
	} {
		got, err := f.Allow(Scope{Trigger: originRoot(tc.same, "exact", tc.trigger)})
		require.NoError(t, err)
		assert.Equal(t, tc.allowed, got, "same=%v trigger=%s", tc.same, tc.trigger)
	}
}

func TestCompileRejectsWhatIsNotABoolOverTrigger(t *testing.T) {
	for _, bad := range []string{"trigger.origin.same_workflow &&", "'yes'", "{{ true }}", "unknown_root.x", "1 + 1"} {
		_, err := CompileOriginFilter(bad)
		assert.Error(t, err, bad)
	}
	_, err := CompileOriginFilter("{{ true }}")
	assert.ErrorContains(t, err, "origin_filter", "the error names the field the author wrote")
}

func TestOriginIsTypedInTheShapeSoATypoIsCaughtAtValidation(t *testing.T) {
	shape, err := NewShape(nil)
	require.NoError(t, err)
	assert.Empty(t, shape.CheckFilter(DefaultOriginFilter), "the default is valid by construction")
	assert.Empty(t, shape.CheckFilter("trigger.origin.lineage_depth < 3 && trigger.origin.actor == 'app'"))
	assert.NotEmpty(t, shape.CheckFilter("trigger.origin.same_workfow"), "a misspelled field is an error, not a silent false")
	assert.NotEmpty(t, shape.CheckFilter("trigger.origin.same_workflow == 'yes'"), "and a wrong type is too")
	assert.NotEmpty(t, shape.CheckFilter("trigger.origin.confidence"), "an origin_filter must be a bool")
}
