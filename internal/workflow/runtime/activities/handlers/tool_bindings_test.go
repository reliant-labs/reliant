// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/toolbindings"
)

// settingsOnlyRepo is a db.Repository that implements only the settings read
// the binding resolver performs. Embedding the interface means any other
// method panics, which is the assertion: resolving bindings must not reach for
// anything else, and no database is required to prove it.
//
// This is not the postgres harness on purpose. A package that reports "ok"
// having t.Skip'd every case is indistinguishable from one that passed.
type settingsOnlyRepo struct {
	db.Repository
	rows []*db.Setting
	err  error
	// asked records the pattern the resolver queried with.
	asked string
}

func (r *settingsOnlyRepo) ListSettingsByKey(_ context.Context, _ string, keyPattern string) ([]*db.Setting, error) {
	r.asked = keyPattern
	if r.err != nil {
		return nil, r.err
	}
	return r.rows, nil
}

// bindableStubParams mirrors the shape that matters: one parameter a human can
// fix, one the model always fills in.
type bindableStubParams struct {
	Model  string `json:"model,omitempty"`
	Prompt string `json:"prompt" jsonschema:"required"`
}

type bindableStub struct {
	name     string
	defaults tools.Bindings
}

func (b *bindableStub) Name() string                                        { return b.name }
func (b *bindableStub) Description() string                                 { return "stub" }
func (b *bindableStub) RequiresPermission(bindableStubParams) (bool, error) { return false, nil }
func (b *bindableStub) DefaultBindings() tools.Bindings                     { return b.defaults }
func (b *bindableStub) Execute(*rctx.ToolContext, bindableStubParams) (tools.ToolResponse, error) {
	return tools.NewTextResponse("ok"), nil
}

func newBindableStub(name string, defaults tools.Bindings) tools.Tool {
	return tools.NewToolWrapper[bindableStubParams, tools.ToolResponse](
		&bindableStub{name: name, defaults: defaults})
}

func effectiveBinding(t *testing.T, tool tools.Tool, param string) any {
	t.Helper()
	bindable, ok := tool.(tools.BindableTool)
	if !ok {
		t.Fatalf("%s is not bindable", tool.Name())
	}
	return bindable.Bindings()[param].Literal
}

// TestCallLLMActivity_ResolveToolBindingScopes_ReadsGlobalSettingsServerSide is
// the point of the whole global surface: the preference is read HERE, in the
// activity, from the repository — not in the browser beside the component that
// wrote it. The existing model.tag_config.* keys are frontend-only, so a
// server-run tool never sees them; this asserts tool bindings do not repeat
// that.
func TestCallLLMActivity_ResolveToolBindingScopes_ReadsGlobalSettingsServerSide(t *testing.T) {
	repo := &settingsOnlyRepo{rows: []*db.Setting{{
		Key:   toolbindings.GlobalSettingKey("generate_image"),
		Value: `{"model":{"literal":{"tags":["image-gen"],"providers":["codex"]}}}`,
	}}}
	activityUnderTest := &CallLLMActivity{repo: repo}

	scopes := activityUnderTest.resolveToolBindingScopes(context.Background(), "user-1", nil)

	if repo.asked != toolbindings.GlobalSettingKeyPrefix+"%" {
		t.Errorf("global bindings should be read in one prefix query; asked for %q", repo.asked)
	}
	resolved := scopes.For("generate_image")
	if len(resolved) != 1 {
		t.Fatalf("want one bound parameter, got %v", resolved.Names())
	}
	selector, ok := resolved["model"].Literal.(map[string]any)
	if !ok {
		t.Fatalf("model literal should decode as an object, got %T", resolved["model"].Literal)
	}
	providers, _ := selector["providers"].([]any)
	if len(providers) != 1 || providers[0] != "codex" {
		t.Errorf("want the stored provider preference [codex], got %v", providers)
	}
}

// TestCallLLMActivity_ResolveToolBindingScopes_SettingsFailureIsNotFatal: a
// settings read that fails must degrade to tool defaults, not take the turn
// down. Losing a preference is recoverable; losing the LLM call is not.
func TestCallLLMActivity_ResolveToolBindingScopes_SettingsFailureIsNotFatal(t *testing.T) {
	repo := &settingsOnlyRepo{err: fmt.Errorf("settings unavailable")}
	activityUnderTest := &CallLLMActivity{repo: repo}

	scopes := activityUnderTest.resolveToolBindingScopes(context.Background(), "user-1", nil)

	if len(scopes.ToolNames()) != 0 {
		t.Errorf("a failed read should yield no bindings, got %v", scopes.ToolNames())
	}

	tool := newBindableStub("generate_image", tools.Bindings{
		"model": tools.LiteralBinding("tool-default"),
	})
	bound := applyToolBindings(context.Background(), []tools.Tool{tool}, scopes)
	if len(bound) != 1 {
		t.Fatalf("the tool list must survive; got %d tools", len(bound))
	}
	if got := effectiveBinding(t, bound[0], "model"); got != "tool-default" {
		t.Errorf("want the tool's own default after a failed read, got %v", got)
	}
}

// TestApplyToolBindings_GlobalPreferenceReachesTheTool is the end-to-end shape
// of what a user configures in Settings: a stored provider preference replaces
// the tool's shipped default, and the parameter stays invisible to the model.
func TestApplyToolBindings_GlobalPreferenceReachesTheTool(t *testing.T) {
	repo := &settingsOnlyRepo{rows: []*db.Setting{{
		Key:   toolbindings.GlobalSettingKey("generate_image"),
		Value: `{"model":{"literal":"gpt-image-2"}}`,
	}}}
	activityUnderTest := &CallLLMActivity{repo: repo}
	ctx := context.Background()

	tool := newBindableStub("generate_image", tools.Bindings{
		"model": tools.LiteralBinding("tool-default"),
	})
	if _, visible := tool.ParamSchema().Properties.Get("prompt"); !visible {
		t.Fatal("the fixture should expose prompt to the model")
	}

	scopes := activityUnderTest.resolveToolBindingScopes(ctx, "user-1", nil)
	bound := applyToolBindings(ctx, []tools.Tool{tool}, scopes)

	if got := effectiveBinding(t, bound[0], "model"); got != "gpt-image-2" {
		t.Errorf("the global preference should win over the tool default; got %v", got)
	}
	if _, visible := bound[0].ParamSchema().Properties.Get("model"); visible {
		t.Error("a bound parameter must not appear in the schema the model is shown")
	}
	if _, visible := bound[0].ParamSchema().Properties.Get("prompt"); !visible {
		t.Error("an open parameter must remain visible")
	}
}

// TestApplyToolBindings_UnconfiguredToolIsUntouched is the zero-configuration
// invariant at the wiring layer: a tool nobody has configured comes back
// exactly as it went in.
func TestApplyToolBindings_UnconfiguredToolIsUntouched(t *testing.T) {
	tool := newBindableStub("some_tool", nil)
	bound := applyToolBindings(context.Background(), []tools.Tool{tool}, toolbindings.Scopes{})

	if len(bound) != 1 || bound[0] != tool {
		t.Fatalf("an unconfigured tool should pass through untouched, got %v", bound)
	}
	if _, visible := bound[0].ParamSchema().Properties.Get("model"); !visible {
		t.Error("nothing bound model, so the model must still see it")
	}
}
