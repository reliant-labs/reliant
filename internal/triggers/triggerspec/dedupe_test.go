// Copyright (c) 2025 Reliant Labs
package triggerspec

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDedupeWindow(t *testing.T) {
	d, err := ParseDedupeWindow("")
	require.NoError(t, err)
	assert.Equal(t, time.Hour, d, "the default window is explicit: one hour")

	d, err = ParseDedupeWindow("4h")
	require.NoError(t, err)
	assert.Equal(t, 4*time.Hour, d)

	for _, bad := range []string{"soon", "25h", "0s", "-5m", "500ms"} {
		_, err := ParseDedupeWindow(bad)
		require.Error(t, err, bad)
	}
}

func TestCompileDedupe(t *testing.T) {
	require.NoError(t, CompileDedupe("", ""))
	require.NoError(t, CompileDedupe("{{ trigger.payload.body.groupKey }}:{{ trigger.payload.body.status }}", "4h"))

	for key, want := range map[string]string{
		"constant":                       "no {{ }} expression",
		"{{ trigger.payload.body.x":      `no closing "}}"`,
		"{{ trigger.payload.body.x + }}": "Syntax error",
		"{{ inputs.x }}":                 "undeclared reference",
	} {
		err := CompileDedupe(key, "")
		require.Error(t, err, key)
		assert.Contains(t, err.Error(), want, key)
	}
	assert.Contains(t, CompileDedupe("", "4h").Error(), "needs a dedupe_key")
	assert.Error(t, CompileDedupe("{{ trigger.kind }}", "forever"))
}

func TestRenderDedupeKey(t *testing.T) {
	root := map[string]any{"payload": map[string]any{"body": map[string]any{"groupKey": `{}:{alertname="X"}`, "status": "firing"}}}

	got, err := RenderDedupeKey("{{ trigger.payload.body.groupKey }}:{{ trigger.payload.body.status }}", root)
	require.NoError(t, err)
	assert.Equal(t, `{}:{alertname="X"}:firing`, got, "braces in the payload are data")

	_, err = RenderDedupeKey("{{ trigger.payload.body.missing }}", root)
	require.Error(t, err, "a key the payload lacks is an error, not an empty key")

	_, err = RenderDedupeKey("{{ '' }}", root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rendered empty")
}

func TestDedupeToken_IsStableWithinAWindowAndNewAfterIt(t *testing.T) {
	win := time.Hour
	t0 := time.Date(2026, 10, 14, 10, 5, 0, 0, time.UTC)

	a := DedupeToken("g:firing", win, t0)
	assert.Equal(t, a, DedupeToken("g:firing", win, t0.Add(40*time.Minute)), "same key, same bucket")
	assert.NotEqual(t, a, DedupeToken("g:firing", win, t0.Add(56*time.Minute)), "the bucket ended at 11:00")
	assert.NotEqual(t, a, DedupeToken("g:resolved", win, t0), "a different key is a different event")
	assert.Equal(t, a, DedupeToken("g:firing", win, t0.In(time.FixedZone("x", 3600*5))), "UTC-aligned whatever the zone")

	long := make([]byte, 100000)
	assert.Less(t, len(DedupeToken(string(long), win, t0)), 80, "the token is bounded")
}
