// Copyright (c) 2026 Reliant Labs
package tools

import (
	"encoding/json"
	"testing"

	"github.com/invopop/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/rctx"
)

type coerceKVInner struct {
	Tags []string `json:"tags,omitempty"`
}

// coerceKVParams has one parameter of every shape coerceKVArrayMaps decides
// on: arrays (of strings, of {key,value}-shaped objects, behind a pointer),
// a map, and a struct holding an array.
type coerceKVParams struct {
	Formats    []string          `json:"formats,omitempty"`
	MaybeIDs   *[]string         `json:"maybe_ids,omitempty"`
	Pairs      []coerceKVPair    `json:"pairs,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	Nested     *coerceKVInner    `json:"nested,omitempty"`
	FreeformKV map[string]any    `json:"freeform,omitempty"`
}

type coerceKVPair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func coerceKVSchema() *jsonschema.Schema {
	reflector := jsonschema.Reflector{AllowAdditionalProperties: false, DoNotReference: true}
	return ResolveSchemaRefs(reflector.Reflect(&coerceKVParams{}))
}

func TestCoerceKVArrayMaps(t *testing.T) {
	schema := coerceKVSchema()
	for _, tc := range []struct {
		name   string
		schema *jsonschema.Schema
		in     string
		want   string
	}{
		// Arrays stay arrays.
		{"empty array for an array param", schema, `{"formats":[]}`, `{"formats":[]}`},
		{"empty array for a pointer-to-slice param", schema, `{"maybe_ids":[]}`, `{"maybe_ids":[]}`},
		{"non-empty array for an array param", schema, `{"formats":["A","B"]}`, `{"formats":["A","B"]}`},
		{"an array param whose items look like KV pairs", schema,
			`{"pairs":[{"key":"k","value":"v"}]}`, `{"pairs":[{"key":"k","value":"v"}]}`},

		// Maps accept the KV encoding, empty included.
		{"empty array for a map param", schema, `{"labels":[]}`, `{"labels":{}}`},
		{"KV array for a map param", schema,
			`{"labels":[{"key":"a","value":"1"},{"key":"b","value":"2"}]}`, `{"labels":{"a":"1","b":"2"}}`},
		{"KV array with nested values for a map param", schema,
			`{"freeform":[{"key":"list","value":[]},{"key":"obj","value":{"x":[1]}}]}`, `{"freeform":{"list":[],"obj":{"x":[1]}}}`},
		{"a non-KV array for a map param is left for validation", schema, `{"labels":["a"]}`, `{"labels":["a"]}`},

		// Nested values are never coerced: only top-level fields are.
		{"empty array inside an object param", schema, `{"nested":{"tags":[]}}`, `{"nested":{"tags":[]}}`},
		{"KV array inside an object param", schema,
			`{"nested":{"tags":[{"key":"k","value":"v"}]}}`, `{"nested":{"tags":[{"key":"k","value":"v"}]}}`},

		// A field no schema describes keeps the old behaviour, except that an
		// empty array is not taken for a map.
		{"empty array, no schema", nil, `{"x":[]}`, `{"x":[]}`},
		{"KV array, no schema", nil, `{"x":[{"key":"k","value":1}]}`, `{"x":{"k":1}}`},
		{"empty array, field the schema lacks", schema, `{"other":[]}`, `{"other":[]}`},
		{"KV array, field the schema lacks", schema, `{"other":[{"key":"k","value":true}]}`, `{"other":{"k":true}}`},
		{"array of objects without value, no schema", nil, `{"x":[{"key":"k"}]}`, `{"x":[{"key":"k"}]}`},

		// Mixed: each field decided on its own.
		{"array and map together", schema,
			`{"formats":[],"labels":[],"pairs":[]}`, `{"formats":[],"labels":{},"pairs":[]}`},
		{"not an object", schema, `[]`, `[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := coerceKVArrayMaps(tc.in, tc.schema)
			assert.JSONEq(t, tc.want, got)
		})
	}
}

// End to end: an empty list argument reaches the tool as an empty list, and
// passes the schema validation that the old {} failed.
type coerceKVTool struct{ got *coerceKVParams }

func (t *coerceKVTool) Name() string                                    { return "coerce_kv_test" }
func (t *coerceKVTool) Description() string                             { return "test" }
func (t *coerceKVTool) RequiresPermission(coerceKVParams) (bool, error) { return false, nil }
func (t *coerceKVTool) Execute(_ *rctx.ToolContext, p coerceKVParams) (ToolResponse, error) {
	*t.got = p
	return NewTextResponse("ok"), nil
}

func TestToolWrapper_EmptyArrayArgumentStaysAnArray(t *testing.T) {
	var got coerceKVParams
	tool := NewToolWrapper[coerceKVParams, ToolResponse](&coerceKVTool{got: &got})
	resp, err := tool.Run(rctx.NewToolContext(t.Context(), "chat", "thread", nil, nil), ToolCall{
		ID: "call-1", Name: "coerce_kv_test",
		Input: `{"formats":[],"maybe_ids":[],"labels":[{"key":"a","value":"1"}]}`,
	})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.NotNil(t, got.Formats)
	assert.Empty(t, got.Formats)
	require.NotNil(t, got.MaybeIDs, "an explicit empty list is not an omitted one")
	assert.Empty(t, *got.MaybeIDs)
	assert.Equal(t, map[string]string{"a": "1"}, got.Labels)

	b, _ := json.Marshal(got.Formats)
	assert.Equal(t, "[]", string(b))
}
