// Copyright (c) 2026 Reliant Labs
package wfobserve

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/telemetry"
)

func TestRow_SmallResultIsStoredWhole(t *testing.T) {
	t.Parallel()
	row := Result{
		Attributes: map[string]interface{}{"trigger": "schedule"},
		Events:     []Event{{Name: "decision", Attributes: map[string]interface{}{"value": "no_cut"}}},
		Errors:     []Error{{Kind: telemetry.ObserveErrorEval, Message: "attributes.x: no such key"}},
	}.Row()
	assert.JSONEq(t, `{"trigger":"schedule"}`, row.Attributes)
	assert.JSONEq(t, `[{"name":"decision","attributes":{"value":"no_cut"}}]`, row.Events)
	assert.JSONEq(t, `["attributes.x: no such key"]`, row.Errors)
	assert.False(t, row.Truncated)

	empty := Result{}.Row()
	assert.Equal(t, "{}", empty.Attributes)
	assert.Equal(t, "[]", empty.Events)
	assert.Equal(t, "[]", empty.Errors)
}

// A row over MaxRowBytes is cut: long strings first, then trailing events.
func TestRow_LargeResultIsCutToTheRowCap(t *testing.T) {
	t.Parallel()
	attrs := map[string]interface{}{}
	for i := 0; i < 32; i++ {
		attrs[fmt.Sprintf("k%02d", i)] = Storable(strings.Repeat("x", 600))
	}
	var events []Event
	for i := 0; i < 16; i++ {
		ev := Event{Name: "decision", Attributes: map[string]interface{}{}}
		for j := 0; j < 16; j++ {
			ev.Attributes[fmt.Sprintf("a%02d", j)] = fmt.Sprintf("value-%02d-%02d-%s", i, j, strings.Repeat("y", 90))
		}
		events = append(events, ev)
	}
	row := Result{Attributes: attrs, Events: events}.Row()
	assert.True(t, row.Truncated)
	assert.LessOrEqual(t, len(row.Attributes)+len(row.Events), MaxRowBytes)

	var gotAttrs map[string]string
	require.NoError(t, json.Unmarshal([]byte(row.Attributes), &gotAttrs))
	assert.Len(t, gotAttrs, 32, "every attribute is kept, cut")
	var gotEvents []Event
	require.NoError(t, json.Unmarshal([]byte(row.Events), &gotEvents))
	assert.NotEmpty(t, gotEvents)
	assert.Less(t, len(gotEvents), 16, "trailing events were dropped")
	assert.True(t, strings.HasPrefix(gotEvents[0].Attributes["a00"].(string), "value-00-00-"), "leading events are kept")
}
