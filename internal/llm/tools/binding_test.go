// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"testing"

	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bindingParams is a stand-in tool parameter struct with one required open
// param, one optional open param, and one param that configuration will bind.
type bindingParams struct {
	Query    string `json:"query" jsonschema:"required,description=what to look for"`
	Limit    int    `json:"limit,omitempty" jsonschema:"description=max results"`
	Endpoint string `json:"endpoint,omitempty" jsonschema:"description=service to query"`
}

// bindingTool records the params it was executed with, so a test can assert on
// the values that actually reached the tool rather than on the JSON in flight.
type bindingTool struct {
	name     string
	got      bindingParams
	calls    int
	defaults Bindings
	execErr  error
}

func (t *bindingTool) Name() string        { return t.name }
func (t *bindingTool) Description() string { return "test tool" }

func (t *bindingTool) RequiresPermission(params bindingParams) (bool, error) {
	return params.Endpoint == "prod", nil
}

func (t *bindingTool) Execute(_ *rctx.ToolContext, params bindingParams) (ToolResponse, error) {
	t.calls++
	t.got = params
	if t.execErr != nil {
		return ToolResponse{}, t.execErr
	}
	return NewTextResponse("done"), nil
}

// defaultBindingTool additionally declares defaults, exercising the
// DefaultBindingsProvider seam.
type defaultBindingTool struct{ bindingTool }

func (t *defaultBindingTool) DefaultBindings() Bindings { return t.defaults }

func newBindingWrapper(t *testing.T) (*ToolWrapper[bindingParams, ToolResponse], *bindingTool) {
	t.Helper()
	inner := &bindingTool{name: "binding_test_tool"}
	return NewToolWrapper[bindingParams, ToolResponse](inner), inner
}

func schemaPropertyNames(t *testing.T, tool Tool) []string {
	t.Helper()
	schema := tool.ParamSchema()
	require.NotNil(t, schema)
	if schema.Properties == nil {
		return nil
	}
	var names []string
	for pair := schema.Properties.Oldest(); pair != nil; pair = pair.Next() {
		names = append(names, pair.Key)
	}
	return names
}

func runBindingTool(t *testing.T, tool Tool, input string) ToolResponse {
	t.Helper()
	resp, err := tool.Run(createTestContext(t, "binding-chat"), ToolCall{ID: "call-1", Input: input})
	require.NoError(t, err)
	return resp
}

// TestBinding_BoundParamIsAbsentFromModelSchema is the core claim: binding a
// parameter removes it from the schema the model sees. That absence IS the
// access control — there is no separate permission check keeping the model out
// of a bound parameter.
func TestBinding_BoundParamIsAbsentFromModelSchema(t *testing.T) {
	wrapper, _ := newBindingWrapper(t)

	assert.ElementsMatch(t, []string{"query", "limit", "endpoint"}, schemaPropertyNames(t, wrapper),
		"with zero bindings every parameter must be visible")

	bound, err := BindTool(wrapper, Bindings{"endpoint": LiteralBinding("prod")})
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"query", "limit"}, schemaPropertyNames(t, bound),
		"a bound parameter must not appear in the model-facing schema")

	assert.ElementsMatch(t, []string{"query", "limit", "endpoint"}, schemaPropertyNames(t, wrapper),
		"binding must not mutate the shared tool instance")
}

// TestBinding_BoundParamIsRemovedFromRequired covers the half that is easy to
// forget. A parameter left in `required` but absent from `properties` asks the
// model for something it cannot see: strict providers reject the schema
// outright, lenient ones invent a value.
func TestBinding_BoundParamIsRemovedFromRequired(t *testing.T) {
	wrapper, _ := newBindingWrapper(t)
	assert.Equal(t, []string{"query"}, wrapper.ParamSchema().Required)

	bound, err := BindTool(wrapper, Bindings{"query": LiteralBinding("fixed question")})
	require.NoError(t, err)

	schema := bound.ParamSchema()
	assert.NotContains(t, schemaPropertyNames(t, bound), "query")
	assert.Empty(t, schema.Required,
		"binding the only required parameter must leave the required list empty, not stale")
}

// TestBinding_BoundValueWinsOverModelSuppliedValue pins the precedence rule. A
// model that somehow emits a bound parameter — from a cached schema, a
// hallucination, or a prompt-injected instruction — must not override the
// human who fixed it.
func TestBinding_BoundValueWinsOverModelSuppliedValue(t *testing.T) {
	wrapper, inner := newBindingWrapper(t)
	bound, err := BindTool(wrapper, Bindings{"endpoint": LiteralBinding("prod")})
	require.NoError(t, err)

	resp := runBindingTool(t, bound, `{"query":"widgets","endpoint":"attacker-controlled"}`)
	require.False(t, resp.IsError, "a hallucinated bound param must be ignored, not fatal: %s", resp.Content)

	require.Equal(t, 1, inner.calls)
	assert.Equal(t, "prod", inner.got.Endpoint, "the bound value must win over the model's")
	assert.Equal(t, "widgets", inner.got.Query, "open parameters must still come from the model")
}

// TestBinding_BoundValueReachesTheToolWhenModelOmitsIt is the ordinary path:
// the model never sees the parameter, so it never sends one, and the tool must
// still receive the bound value.
func TestBinding_BoundValueReachesTheToolWhenModelOmitsIt(t *testing.T) {
	wrapper, inner := newBindingWrapper(t)
	bound, err := BindTool(wrapper, Bindings{
		"endpoint": LiteralBinding("staging"),
		"limit":    LiteralBinding(7),
	})
	require.NoError(t, err)

	resp := runBindingTool(t, bound, `{"query":"widgets"}`)
	require.False(t, resp.IsError, "unexpected error: %s", resp.Content)

	assert.Equal(t, "staging", inner.got.Endpoint)
	assert.Equal(t, 7, inner.got.Limit)
	assert.Equal(t, "widgets", inner.got.Query)
}

// TestBinding_ZeroBindingsChangesNothing is the invariant that makes this
// mechanism safe to add: an unconfigured tool must behave exactly as it did
// before bindings existed.
func TestBinding_ZeroBindingsChangesNothing(t *testing.T) {
	wrapper, inner := newBindingWrapper(t)

	schema := wrapper.ParamSchema()
	assert.ElementsMatch(t, []string{"query", "limit", "endpoint"}, schemaPropertyNames(t, wrapper))
	assert.Equal(t, []string{"query"}, schema.Required)

	resp := runBindingTool(t, wrapper, `{"query":"widgets","limit":3,"endpoint":"dev"}`)
	require.False(t, resp.IsError, "unexpected error: %s", resp.Content)
	assert.Equal(t, bindingParams{Query: "widgets", Limit: 3, Endpoint: "dev"}, inner.got)

	// An unknown parameter is still rejected — binding must not have widened
	// what the model may send.
	unknown := runBindingTool(t, wrapper, `{"query":"widgets","nope":1}`)
	assert.True(t, unknown.IsError, "an unbound tool must still reject unknown fields")

	// And BindTool with nothing to bind is a no-op that returns the same tool.
	same, err := BindTool(wrapper, nil)
	require.NoError(t, err)
	assert.Same(t, Tool(wrapper), same)
}

// TestBinding_PermissionSeesBoundValues checks that the permission decision is
// made against the parameters the tool will really run with. A binding that
// escalates a call must be visible to the permission layer, not merely to
// Execute.
func TestBinding_PermissionSeesBoundValues(t *testing.T) {
	wrapper, _ := newBindingWrapper(t)
	ctx := createTestContext(t, "binding-chat")

	needs, err := wrapper.RequiresPermission(ctx, ToolCall{Input: `{"query":"q"}`})
	require.NoError(t, err)
	assert.False(t, needs, "the unbound default endpoint is not prod")

	bound, err := BindTool(wrapper, Bindings{"endpoint": LiteralBinding("prod")})
	require.NoError(t, err)

	needs, err = bound.RequiresPermission(ctx, ToolCall{Input: `{"query":"q"}`})
	require.NoError(t, err)
	assert.True(t, needs, "the permission check must see the bound value the tool will run with")
}

// TestBinding_DeclaredDefaultsApplyAndAreOverridable covers the defaulting
// seam. A tool declares its own bindings; configuration layers over them.
func TestBinding_DeclaredDefaultsApplyAndAreOverridable(t *testing.T) {
	inner := &defaultBindingTool{bindingTool: bindingTool{
		name:     "default_binding_tool",
		defaults: Bindings{"endpoint": LiteralBinding("default-endpoint")},
	}}
	wrapper := NewToolWrapper[bindingParams, ToolResponse](inner)

	assert.NotContains(t, schemaPropertyNames(t, wrapper), "endpoint",
		"a declared default binding must hide the parameter with no configuration at all")

	resp := runBindingTool(t, wrapper, `{"query":"q"}`)
	require.False(t, resp.IsError, "unexpected error: %s", resp.Content)
	assert.Equal(t, "default-endpoint", inner.got.Endpoint)

	overridden, err := BindTool(wrapper, Bindings{"endpoint": LiteralBinding("configured")})
	require.NoError(t, err)
	resp = runBindingTool(t, overridden, `{"query":"q"}`)
	require.False(t, resp.IsError, "unexpected error: %s", resp.Content)
	assert.Equal(t, "configured", inner.got.Endpoint, "configuration must layer over the tool's default")

	assert.NotContains(t, schemaPropertyNames(t, overridden), "endpoint",
		"rebinding must keep the parameter hidden")
}

// TestBinding_UnknownParamIsRejected: a binding that names a parameter the tool
// does not have would silently do nothing. Reporting it is the difference
// between a typo the human can fix and a setting they believe is in effect.
func TestBinding_UnknownParamIsRejected(t *testing.T) {
	wrapper, _ := newBindingWrapper(t)

	_, err := BindTool(wrapper, Bindings{"endpiont": LiteralBinding("prod")})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownBoundParam)
	assert.Contains(t, err.Error(), "endpiont")
}

// stubResolver evaluates an expression binding by looking it up in a table.
type stubResolver struct {
	values map[string]any
	err    error
	calls  []string
}

func (r *stubResolver) ResolveBinding(_ *rctx.ToolContext, expr string) (any, error) {
	r.calls = append(r.calls, expr)
	if r.err != nil {
		return nil, r.err
	}
	value, ok := r.values[expr]
	if !ok {
		return nil, fmt.Errorf("no value for %q", expr)
	}
	return value, nil
}

// TestBinding_ExpressionValuesResolveAtCallTime pins that a bound value need
// not be a constant. `save_to: "assets/{{nodes.plan.slug}}.png"` is fixed from
// the model's point of view while still being computed per call.
func TestBinding_ExpressionValuesResolveAtCallTime(t *testing.T) {
	wrapper, inner := newBindingWrapper(t)
	configured, err := wrapper.WithBindings(Bindings{"endpoint": ExprBinding("nodes.plan.endpoint")})
	require.NoError(t, err)

	assert.NotContains(t, schemaPropertyNames(t, configured), "endpoint",
		"an expression binding hides the parameter exactly like a literal one")

	resolver := &stubResolver{values: map[string]any{"nodes.plan.endpoint": "resolved-at-runtime"}}
	withResolver := configured.(*ToolWrapper[bindingParams, ToolResponse]).WithBindingResolver(resolver)

	resp := runBindingTool(t, withResolver, `{"query":"q"}`)
	require.False(t, resp.IsError, "unexpected error: %s", resp.Content)
	assert.Equal(t, []string{"nodes.plan.endpoint"}, resolver.calls)
	assert.Equal(t, "resolved-at-runtime", inner.got.Endpoint)
}

// TestBinding_ExpressionWithoutResolverIsALoudError: silently substituting a
// zero value for an unresolvable expression would run the tool against a
// configuration the human never asked for.
func TestBinding_ExpressionWithoutResolverIsALoudError(t *testing.T) {
	wrapper, inner := newBindingWrapper(t)
	configured, err := BindTool(wrapper, Bindings{"endpoint": ExprBinding("nodes.plan.endpoint")})
	require.NoError(t, err)

	resp := runBindingTool(t, configured, `{"query":"q"}`)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "no binding resolver is configured")
	assert.Equal(t, 0, inner.calls, "the tool must not run with an unresolved binding")
}

// TestBinding_MergePrecedence pins the layering rule the scope-resolution task
// builds on: later layers win, and neither input is mutated.
func TestBinding_MergePrecedence(t *testing.T) {
	base := Bindings{"a": LiteralBinding(1), "b": LiteralBinding(2)}
	over := Bindings{"b": LiteralBinding(20), "c": LiteralBinding(30)}

	merged := base.Merge(over)
	assert.Equal(t, LiteralBinding(1), merged["a"])
	assert.Equal(t, LiteralBinding(20), merged["b"], "the more specific layer wins")
	assert.Equal(t, LiteralBinding(30), merged["c"])

	assert.Equal(t, LiteralBinding(2), base["b"], "Merge must not mutate the receiver")
	assert.Len(t, over, 2, "Merge must not mutate its argument")

	assert.Nil(t, Bindings(nil).Merge(nil))
	assert.Equal(t, []string{"a", "b", "c"}, merged.Names())
}

// TestBinding_NonBindableToolsDegradeGracefully covers the three hand-rolled
// Tool implementations. MCP tools in particular get their schema from a remote
// server, so there is no Go struct to filter — binding must report that rather
// than pretend to have worked, and the tool must keep working either way.
func TestBinding_NonBindableToolsDegradeGracefully(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"path": map[string]any{"type": "string"}},
		"required":   []any{"path"},
	}
	mcpAdapter, err := NewMCPToolAdapter("remote", mcp.Tool{
		Name:        "read_remote",
		Description: "a tool whose schema lives on another machine",
		InputSchema: schema,
	})
	require.NoError(t, err)

	cases := map[string]Tool{
		"ResponseTool": NewResponseTool(ResponseToolDefinition{
			Name: "submit", Description: "post the answer", Schema: schema,
		}),
		"SchemaOnlyTool": NewSchemaOnlyTool("provider_tool", "provider-shaped", schema),
		"MCPToolAdapter": mcpAdapter,
	}

	for name, tool := range cases {
		t.Run(name, func(t *testing.T) {
			// Zero bindings: untouched, and still the same instance.
			same, err := BindTool(tool, nil)
			require.NoError(t, err)
			assert.Same(t, tool, same)

			// Its schema still works and still carries its own properties.
			before := tool.ParamSchema()
			require.NotNil(t, before)

			// A real binding is refused, loudly, and the tool comes back
			// intact so a caller that continues anyway still has one.
			returned, err := BindTool(tool, Bindings{"path": LiteralBinding("/tmp")})
			assert.ErrorIs(t, err, ErrBindingsUnsupported)
			assert.Same(t, tool, returned)

			after := tool.ParamSchema()
			require.NotNil(t, after)
			assert.Equal(t, before.Required, after.Required,
				"a refused binding must not have altered the tool's schema")
		})
	}
}
