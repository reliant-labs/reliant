// Copyright (c) 2025 Reliant Labs
package geminiwire

import (
	"encoding/json"
	"testing"

	"github.com/invopop/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
)

// parse decodes a JSON Schema literal the way a tool's schema arrives: through
// encoding/json, so numbers are float64 and lists are []any.
func parse(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(s), &m))
	return m
}

// asJSON renders a normalized schema for comparison against a literal.
func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func TestNormalizeJSONSchema(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "type array with null becomes nullable",
			in:   `{"type":["string","null"],"description":"d"}`,
			want: `{"description":"d","nullable":true,"type":"string"}`,
		},
		{
			name: "anyOf with a null branch hoists the other branch",
			in:   `{"anyOf":[{"type":"integer","minimum":1},{"type":"null"}],"description":"count"}`,
			want: `{"description":"count","minimum":1,"nullable":true,"type":"integer"}`,
		},
		{
			name: "anyOf of two real types is kept",
			in:   `{"anyOf":[{"type":"string"},{"type":"array","items":{"type":"string"}}]}`,
			want: `{"anyOf":[{"type":"string"},{"items":{"type":"string"},"type":"array"}]}`,
		},
		{
			name: "oneOf becomes anyOf",
			in:   `{"oneOf":[{"type":"string"},{"type":"number"}]}`,
			want: `{"anyOf":[{"type":"string"},{"type":"number"}]}`,
		},
		{
			name: "several concrete types split into branches with their own keywords",
			in:   `{"type":["array","object","null"],"items":{"type":"string"},"properties":{"a":{"type":"string"}},"required":["a"]}`,
			want: `{"anyOf":[{"items":{"type":"string"},"type":"array"},{"properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}],"nullable":true}`,
		},
		{
			name: "only documented formats survive",
			in: `{"type":"object","properties":{
				"when":{"type":"string","format":"date-time"},
				"url":{"type":"string","format":"uri"},
				"n":{"type":"integer","format":"int64"},
				"bad":{"type":"integer","format":"float"},
				"f":{"type":"number","format":"double"}}}`,
			want: `{"properties":{"bad":{"type":"integer"},"f":{"format":"double","type":"number"},"n":{"format":"int64","type":"integer"},"url":{"type":"string"},"when":{"format":"date-time","type":"string"}},"type":"object"}`,
		},
		{
			name: "enum entries become strings; type stays; null entry is nullable",
			in:   `{"type":"integer","enum":[1,2,2,null]}`,
			want: `{"enum":["1","2"],"nullable":true,"type":"integer"}`,
		},
		{
			name: "const becomes a one-entry enum and infers string",
			in:   `{"const":"fixed"}`,
			want: `{"enum":["fixed"],"type":"string"}`,
		},
		{
			name: "required is trimmed to declared properties",
			in:   `{"type":"object","properties":{"a":{"type":"string"}},"required":["a","ghost","a"]}`,
			want: `{"properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}`,
		},
		{
			name: "required with no properties is dropped (MCP servers emit this)",
			in:   `{"type":"object","required":["x"]}`,
			want: `{"type":"object"}`,
		},
		{
			name: "unsupported keywords are dropped",
			in:   `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"x","type":"object","additionalProperties":false,"properties":{"s":{"type":"string","examples":["a"],"not":{"const":"b"},"maxLength":10,"pattern":"^a"}}}`,
			want: `{"properties":{"s":{"maxLength":10,"pattern":"^a","type":"string"}},"type":"object"}`,
		},
		{
			name: "$ref to $defs is inlined; sibling description wins",
			in:   `{"type":"object","properties":{"p":{"$ref":"#/$defs/Point","description":"where"}},"$defs":{"Point":{"type":"object","description":"a point","properties":{"x":{"type":"number"}},"required":["x"]}}}`,
			want: `{"properties":{"p":{"description":"where","properties":{"x":{"type":"number"}},"required":["x"],"type":"object"}},"type":"object"}`,
		},
		{
			name: "$ref to definitions (draft-07) is inlined",
			in:   `{"type":"object","properties":{"c":{"$ref":"#/definitions/Color"}},"definitions":{"Color":{"type":"string","enum":["red","blue"]}}}`,
			want: `{"properties":{"c":{"enum":["red","blue"],"type":"string"}},"type":"object"}`,
		},
		{
			name: "recursive $ref is cut to an object",
			in:   `{"type":"object","properties":{"root":{"$ref":"#/$defs/Node"}},"$defs":{"Node":{"type":"object","properties":{"name":{"type":"string"},"child":{"$ref":"#/$defs/Node","description":"next"}}}}}`,
			want: `{"properties":{"root":{"properties":{"child":{"description":"next","type":"object"},"name":{"type":"string"}},"type":"object"}},"type":"object"}`,
		},
		{
			name: "unresolvable $ref leaves an untyped node",
			in:   `{"type":"object","properties":{"x":{"$ref":"https://example.com/other.json","description":"remote"}}}`,
			want: `{"properties":{"x":{"description":"remote"}},"type":"object"}`,
		},
		{
			name: "allOf is merged into its parent",
			in:   `{"allOf":[{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},{"properties":{"b":{"type":"boolean"}},"required":["b"]}]}`,
			want: `{"properties":{"a":{"type":"string"},"b":{"type":"boolean"}},"required":["a","b"],"type":"object"}`,
		},
		{
			name: "missing type is inferred from properties and items",
			in:   `{"properties":{"list":{"items":{"type":"string"}}}}`,
			want: `{"properties":{"list":{"items":{"type":"string"},"type":"array"}},"type":"object"}`,
		},
		{
			name: "tuple items become a union",
			in:   `{"type":"array","items":[{"type":"string"},{"type":"integer"}]}`,
			want: `{"items":{"anyOf":[{"type":"string"},{"type":"integer"}]},"type":"array"}`,
		},
		{
			name: "prefixItems of one becomes items",
			in:   `{"type":"array","prefixItems":[{"type":"string"}]}`,
			want: `{"items":{"type":"string"},"type":"array"}`,
		},
		{
			name: "numeric exclusive bounds become inclusive bounds; boolean form keeps the bound",
			in:   `{"type":"object","properties":{"a":{"type":"number","exclusiveMinimum":0},"b":{"type":"number","minimum":1,"exclusiveMinimum":true,"maximum":9,"exclusiveMaximum":true}}}`,
			want: `{"properties":{"a":{"minimum":0,"type":"number"},"b":{"maximum":9,"minimum":1,"type":"number"}},"type":"object"}`,
		},
		{
			name: "false property schema is dropped; true is untyped",
			in:   `{"type":"object","properties":{"never":false,"any":true}}`,
			want: `{"properties":{"any":{}},"type":"object"}`,
		},
		{
			name: "explicit OpenAPI nullable is kept",
			in:   `{"type":"string","nullable":true}`,
			want: `{"nullable":true,"type":"string"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := parse(t, tc.in)
			before := asJSON(t, in)
			got := NormalizeJSONSchema(in)
			assert.JSONEq(t, tc.want, asJSON(t, got))
			assert.Equal(t, before, asJSON(t, in), "the input must not be modified")
		})
	}
}

// A model graph that references itself through many paths must not expand
// exponentially: the budget cuts it and the conversion still terminates.
func TestNormalizeJSONSchema_RefBudget(t *testing.T) {
	t.Parallel()
	defs := map[string]any{}
	for i := 0; i < 20; i++ {
		next := "#/$defs/N" + string(rune('A'+i+1))
		defs["N"+string(rune('A'+i))] = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"left":  map[string]any{"$ref": next},
				"right": map[string]any{"$ref": next},
			},
		}
	}
	defs["N"+string(rune('A'+20))] = map[string]any{"type": "string"}
	schema := map[string]any{"$ref": "#/$defs/NA", "$defs": defs}
	got := NormalizeJSONSchema(schema)
	assert.Equal(t, "object", got["type"])
}

func TestParametersSchema(t *testing.T) {
	t.Parallel()

	t.Run("converts to genai with uppercase types and nullable", func(t *testing.T) {
		t.Parallel()
		got := ParametersSchema(parse(t, `{"type":"object","properties":{
			"path":{"type":"string","description":"file"},
			"limit":{"type":["integer","null"],"minimum":1},
			"mode":{"anyOf":[{"type":"string","enum":["a","b"]},{"type":"array","items":{"type":"string"}}]}
		},"required":["path","ghost"]}`))
		want := &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"path":  {Type: genai.TypeString, Description: "file"},
				"limit": {Type: genai.TypeInteger, Nullable: genai.Ptr(true), Minimum: genai.Ptr(1.0)},
				"mode": {AnyOf: []*genai.Schema{
					{Type: genai.TypeString, Enum: []string{"a", "b"}},
					{Type: genai.TypeArray, Items: &genai.Schema{Type: genai.TypeString}},
				}},
			},
			Required: []string{"path"},
		}
		assert.Equal(t, want, got)
	})

	t.Run("an untyped root becomes an object", func(t *testing.T) {
		t.Parallel()
		got := ParametersSchema(parse(t, `{"properties":{"a":{"type":"boolean"}}}`))
		assert.Equal(t, genai.TypeObject, got.Type)
		assert.Equal(t, genai.TypeBoolean, got.Properties["a"].Type)
	})

	t.Run("an empty schema is an empty object", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, &genai.Schema{Type: genai.TypeObject}, ParametersSchema(nil))
	})

	t.Run("a non-object root is replaced, not sent", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, &genai.Schema{Type: genai.TypeObject}, ParametersSchema(parse(t, `{"type":"string"}`)))
	})
}

type typedTool struct{ schema *jsonschema.Schema }

func (t typedTool) ParamSchema() *jsonschema.Schema { return t.schema }

type rawTool struct {
	typedTool
	raw map[string]any
}

func (t rawTool) ParamSchemaMap() map[string]any { return t.raw }

// ToolParameters prefers a tool's raw map (MCP tools), whose type arrays the
// typed model cannot hold, and falls back to the typed schema.
func TestToolParameters(t *testing.T) {
	t.Parallel()

	typed := &jsonschema.Schema{Type: "object", Properties: jsonschema.NewProperties()}
	typed.Properties.Set("q", &jsonschema.Schema{Type: "string", Format: "email"})
	typed.Required = []string{"q"}
	got := ToolParameters(typedTool{schema: typed})
	assert.Equal(t, &genai.Schema{
		Type:       genai.TypeObject,
		Properties: map[string]*genai.Schema{"q": {Type: genai.TypeString}},
		Required:   []string{"q"},
	}, got)

	raw := rawTool{
		typedTool: typedTool{schema: &jsonschema.Schema{Type: "object"}},
		raw:       parse(t, `{"type":"object","properties":{"n":{"type":["number","null"]}}}`),
	}
	got = ToolParameters(raw)
	require.Contains(t, got.Properties, "n")
	assert.Equal(t, &genai.Schema{Type: genai.TypeNumber, Nullable: genai.Ptr(true)}, got.Properties["n"])

	assert.Equal(t, &genai.Schema{Type: genai.TypeObject}, ToolParameters(typedTool{}))
}
