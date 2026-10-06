// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
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

// needsConnection reports whether an integration's calls require a
// credential: it declares auth and does not make it optional.
func needsConnection(m *reliantv1.IntegrationManifest) bool {
	conn := m.GetConnection()
	return len(conn.GetAuth()) > 0 && !conn.GetAuthOptional()
}

var (
	gatedToolsOnce sync.Once
	gatedTools     map[string]*reliantv1.IntegrationManifest
)

// connectionGated maps every integration tool whose integration needs a
// connection to that integration's manifest.
func connectionGated() map[string]*reliantv1.IntegrationManifest {
	gatedToolsOnce.Do(func() {
		gatedTools = map[string]*reliantv1.IntegrationManifest{}
		cat, err := catalog.Builtin()
		if err != nil {
			return
		}
		for _, m := range cat.Manifests() {
			if !needsConnection(m) {
				continue
			}
			for _, a := range m.GetActions() {
				if a.GetTool().GetExpose() {
					gatedTools[manifest.ToolName(m, a)] = m
				}
			}
		}
	})
	return gatedTools
}

// ConnectionGatedIntegration reports the integration a tool authenticates
// through when that integration needs a connection, so the tool may be offered
// only to a run whose owner has one (UsableIntegrations). Every other tool,
// including an integration's that needs no credential (http__request), is not
// gated.
//
// The registry holds every exposed action regardless: availability is a
// property of the run's owner, not of the process, so it is decided per
// request by call_llm rather than baked into a registry built owner-blind
// (INTEGRATIONS.md §3.2: a tool that is present-and-401ing is worse than
// absent).
func ConnectionGatedIntegration(name string) (integrationID string, gated bool) {
	m, ok := connectionGated()[name]
	if !ok {
		return "", false
	}
	return m.GetId(), true
}

// connectionAvailability is the half of a credential source that can say,
// without resolving a credential for any call, which integrations a run's
// owner could authenticate now. Declared here, where it is consumed:
// connauth.Source implements it by walking the same resolution order its
// Credential uses (the owner's default saved connection, else a delegated
// authority), so a tool offered because of it is a tool whose call will find
// a credential.
type connectionAvailability interface {
	UsableIntegrations(ctx context.Context, runID string, integrations map[string]*reliantv1.ConnectionSpec) (map[string]bool, error)
}

// UsableIntegrations reports which of the named integrations the run's owner
// can authenticate now, asked of the credential source this factory's
// integration tools execute through. The run is identified exactly as an
// integration tool identifies it at call time (integrationRunID), so the
// answer is about the owner the call will act as.
//
// It fails closed: a factory with no credential source, or one that cannot
// answer, reports nothing usable; so does an integration that needs no
// connection (it is never gated). An error is returned for the caller to log,
// alongside whatever the source did establish.
func (f *ToolsFactory) UsableIntegrations(ctx context.Context, chatID, thread string, integrationIDs []string) (map[string]bool, error) {
	usable := map[string]bool{}
	checker, ok := f.integrationCredentials().(connectionAvailability)
	if !ok || len(integrationIDs) == 0 {
		return usable, nil
	}
	cat, err := catalog.Builtin()
	if err != nil {
		return usable, err
	}
	specs := make(map[string]*reliantv1.ConnectionSpec, len(integrationIDs))
	for _, m := range cat.Manifests() {
		for _, id := range integrationIDs {
			if m.GetId() == id && needsConnection(m) {
				specs[id] = m.GetConnection()
			}
		}
	}
	if len(specs) == 0 {
		return usable, nil
	}
	answer, err := checker.UsableIntegrations(ctx, integrationRunID(chatID, thread), specs)
	for id := range specs {
		if answer[id] {
			usable[id] = true
		}
	}
	return usable, err
}

// integrationRunID is the run an integration tool executes in: the workflow
// the tool runs under (its thread), falling back to the chat for runs whose
// id is the chat id. Shared by the call and by the availability check, so the
// two always ask about the same owner.
func integrationRunID(chatID, thread string) string {
	if thread != "" {
		return thread
	}
	return chatID
}

var (
	integrationRunnerOnce sync.Once
	integrationRunner     *httpaction.Runner
)

// UseIntegrationRunner swaps the runner integration tools execute through and
// returns a restore func. For tests that must trust an httptest TLS server.
func UseIntegrationRunner(r *httpaction.Runner) (restore func()) {
	integrationRunnerOnce.Do(func() {})
	prev := integrationRunner
	integrationRunner = r
	return func() { integrationRunner = prev }
}

func sharedIntegrationRunner() *httpaction.Runner {
	integrationRunnerOnce.Do(func() { integrationRunner = httpaction.NewRunner(netguard.New()) })
	return integrationRunner
}

// integrationToolDefinitions turns every exposed manifest action into a
// registry entry. Placement comes from the manifest, which only curated
// manifests may set to server or any. Whether a run may be OFFERED one whose
// integration needs a connection is decided per run's owner
// (ConnectionGatedIntegration), not here.
func integrationToolDefinitions() []ToolDefinition {
	cat, err := catalog.Builtin()
	if err != nil {
		return nil
	}
	var defs []ToolDefinition
	for _, m := range cat.Manifests() {
		for _, a := range m.GetActions() {
			if !a.GetTool().GetExpose() {
				continue
			}
			m, a := m, a
			defs = append(defs, ToolDefinition{
				Name:      manifest.ToolName(m, a),
				Factory:   func(f *ToolsFactory) Tool { return newIntegrationTool(m, a, f.integrationCredentials()) },
				Tags:      []ToolTag{TagIntegration},
				Placement: Placement(a.GetPlacement()),
			})
		}
	}
	return defs
}

type integrationTool struct {
	manifest *reliantv1.IntegrationManifest
	action   *reliantv1.ActionSpec
	bindings Bindings
	// credentials resolves the `connection` param for the run's owner. The
	// owner is read from the run record by the source, never taken from the
	// model's arguments.
	credentials httpaction.CredentialSource
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

func newIntegrationTool(m *reliantv1.IntegrationManifest, a *reliantv1.ActionSpec, credentials httpaction.CredentialSource) Tool {
	return &integrationTool{manifest: m, action: a, credentials: credentials}
}

func (f *ToolsFactory) integrationCredentials() httpaction.CredentialSource {
	if f == nil || f.opts == nil {
		return nil
	}
	return f.opts.IntegrationCredentials
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
	site := httpaction.CallSite{RunID: integrationRunID(rc.ChatID, rc.Thread), ToolCallID: call.ID}
	result, err := sharedIntegrationRunner().RunAuthenticated(ctx, t.manifest, t.action, params, t.credentials, site)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	resp := NewTextResponse(result.Content)
	resp.IsError = result.IsError
	if result.Data != nil || result.ConnectionID != "" {
		meta := map[string]any{}
		for k, v := range result.Data {
			meta[k] = v
		}
		if result.ConnectionID != "" {
			meta["connection_id"] = result.ConnectionID
		}
		resp = WithResponseMetadata(resp, meta)
	}
	return resp, nil
}
