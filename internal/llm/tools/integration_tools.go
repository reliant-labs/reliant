// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/invopop/jsonschema"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// TagIntegration marks every tool that comes from an integration manifest, so
// a workflow can grant them as a group (`tag:integration`).
const TagIntegration ToolTag = "integration"

// ConnectionAvailable reports whether the run's owner can use the connection
// an integration needs. Integrations with connection type "none" never ask.
// Until the connections phase lands there is nothing to resolve a connection
// against, so the default is "no": a tool whose manifest needs a connection is
// absent rather than present-and-401ing.
var ConnectionAvailable = func(m *reliantv1.IntegrationManifest) bool {
	t := m.GetConnection().GetType()
	return t == "" || t == manifest.ConnectionNone
}

var (
	integrationRunnerOnce sync.Once
	integrationRunner     *httpaction.Runner
)

func sharedIntegrationRunner() *httpaction.Runner {
	integrationRunnerOnce.Do(func() { integrationRunner = httpaction.NewRunner(netguard.New()) })
	return integrationRunner
}

// integrationToolDefinitions turns every exposed manifest action whose
// connection is available into a registry entry. Placement comes from the
// manifest, which only curated manifests may set to server or any.
func integrationToolDefinitions() []ToolDefinition {
	cat, err := catalog.Builtin()
	if err != nil {
		return nil
	}
	var defs []ToolDefinition
	for _, m := range cat.Manifests() {
		if !ConnectionAvailable(m) {
			continue
		}
		for _, a := range m.GetActions() {
			if !a.GetTool().GetExpose() {
				continue
			}
			m, a := m, a
			defs = append(defs, ToolDefinition{
				Name:    manifest.ToolName(m, a),
				Factory: func(*ToolsFactory) Tool { return newIntegrationTool(m, a) },
				Tags:    []ToolTag{TagIntegration},
				RunsOn:  ToolLocation(a.GetPlacement()),
			})
		}
	}
	return defs
}

type integrationTool struct {
	manifest *reliantv1.IntegrationManifest
	action   *reliantv1.ActionSpec
	bindings Bindings
}

var _ BindableTool = (*integrationTool)(nil)

// WithBindings returns a copy with parameters bound. A bound parameter leaves
// the model's schema and is merged back in at call time, which is how a
// workflow pins, say, a request's URL so the agent cannot redirect it.
func (t *integrationTool) WithBindings(bindings Bindings) (Tool, error) {
	merged := t.bindings.Merge(bindings)
	for _, name := range merged.Names() {
		props := t.fullSchema().Properties
		if props == nil {
			return t, fmt.Errorf("%s: %w: %q", t.Name(), ErrUnknownBoundParam, name)
		}
		if _, ok := props.Get(name); !ok {
			return t, fmt.Errorf("%s: %w: %q", t.Name(), ErrUnknownBoundParam, name)
		}
	}
	clone := *t
	clone.bindings = merged
	return &clone, nil
}

func (t *integrationTool) Bindings() Bindings { return t.bindings }

func newIntegrationTool(m *reliantv1.IntegrationManifest, a *reliantv1.ActionSpec) Tool {
	return &integrationTool{manifest: m, action: a}
}

// fullSchema builds a fresh schema on every call: withoutBoundParams mutates
// what it is given, so a cached one would leak one workflow's bindings into
// every other user of the shared tool.
func (t *integrationTool) fullSchema() *jsonschema.Schema {
	if t.action.GetParams() != nil {
		if raw, err := json.Marshal(t.action.GetParams().AsMap()); err == nil {
			var s jsonschema.Schema
			if json.Unmarshal(raw, &s) == nil {
				return &s
			}
		}
	}
	return &jsonschema.Schema{Type: "object"}
}

func (t *integrationTool) Name() string { return manifest.ToolName(t.manifest, t.action) }

func (t *integrationTool) Description() string {
	if d := t.action.GetDescription(); d != "" {
		return d
	}
	return t.action.GetDisplayName()
}

func (t *integrationTool) ParamSchema() *jsonschema.Schema {
	return withoutBoundParams(t.fullSchema(), t.bindings)
}

// RequiresPermission asks for approval exactly when the manifest says the
// action changes external state.
func (t *integrationTool) RequiresPermission(_ *rctx.ToolContext, _ ToolCall) (bool, error) {
	return t.action.GetMutates(), nil
}

func (t *integrationTool) Run(rc *rctx.ToolContext, call ToolCall) (ToolResponse, error) {
	input := stripBoundKeys(call.Input, t.bindings)
	resolved, err := resolveBindings(t.bindings, nil, rc)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	if input, err = applyBindingsToInput(input, resolved); err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	var params map[string]any
	if input != "" {
		if err := json.Unmarshal([]byte(input), &params); err != nil {
			return NewTextErrorResponse(fmt.Sprintf("invalid parameters: %v", err)), nil
		}
	}
	ctx := rc.Context
	if ctx == nil {
		return NewTextErrorResponse("no execution context"), nil
	}
	result, err := sharedIntegrationRunner().Run(ctx, t.manifest, t.action, params)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	resp := NewTextResponse(result.Content)
	resp.IsError = result.IsError
	if result.Data != nil {
		resp = WithResponseMetadata(resp, result.Data)
	}
	return resp, nil
}
