// Copyright (c) 2025 Reliant Labs
package toolbindings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// fakeSettings is an in-memory SettingsReader. Deliberately NOT the postgres
// harness: internal/grpc/services-style tests t.Skip when DATABASE_URL is
// unset, and a package that reports "ok" having run zero assertions is worse
// than a package that fails.
type fakeSettings struct {
	rows map[string][]*db.Setting // userID -> rows
	err  error
	// pattern records what the last call asked for, so a test can prove the
	// read is a single prefix query rather than one lookup per tool.
	pattern string
}

func (f *fakeSettings) ListSettingsByKey(_ context.Context, userID, keyPattern string) ([]*db.Setting, error) {
	f.pattern = keyPattern
	if f.err != nil {
		return nil, f.err
	}
	return f.rows[userID], nil
}

func setting(key, value string) *db.Setting {
	return &db.Setting{Key: key, Value: value}
}

// stubParams is a two-parameter tool schema. Both params are open by default,
// so a test can watch one disappear as it gets bound.
type stubParams struct {
	Model  string `json:"model,omitempty"`
	Prompt string `json:"prompt" jsonschema:"required"`
}

type stubTool struct {
	name     string
	defaults tools.Bindings
}

func (s *stubTool) Name() string                                { return s.name }
func (s *stubTool) Description() string                         { return "stub" }
func (s *stubTool) RequiresPermission(stubParams) (bool, error) { return false, nil }
func (s *stubTool) DefaultBindings() tools.Bindings             { return s.defaults }
func (s *stubTool) Execute(*rctx.ToolContext, stubParams) (tools.ToolResponse, error) {
	return tools.NewTextResponse("ok"), nil
}

func newStubTool(name string, defaults tools.Bindings) tools.Tool {
	return tools.NewToolWrapper[stubParams, tools.ToolResponse](&stubTool{name: name, defaults: defaults})
}

// boundLiteral extracts the literal a tool has bound for a parameter, or fails.
func boundLiteral(t *testing.T, tool tools.Tool, param string) any {
	t.Helper()
	bindable, ok := tool.(tools.BindableTool)
	if !ok {
		t.Fatalf("tool %s is not bindable", tool.Name())
	}
	bound, present := bindable.Bindings()[param]
	if !present {
		t.Fatalf("tool %s has no binding for %q; has %v", tool.Name(), param, bindable.Bindings().Names())
	}
	return bound.Literal
}

// TestScopes_For_MostSpecificWinsPerParameter is the resolution order itself.
// Per PARAMETER, not per scope: a preset that binds only `model` must not
// discard a global binding of `prompt`.
func TestScopes_For_MostSpecificWinsPerParameter(t *testing.T) {
	scopes := Scopes{
		Global: ByTool{"t": {
			"model":  tools.LiteralBinding("global-model"),
			"prompt": tools.LiteralBinding("global-prompt"),
		}},
		Workflow: ByTool{"t": {
			"model": tools.LiteralBinding("workflow-model"),
		}},
		Preset: ByTool{"t": {
			"model": tools.LiteralBinding("preset-model"),
		}},
	}

	resolved := scopes.For("t")
	if got := resolved["model"].Literal; got != "preset-model" {
		t.Errorf("model: preset is the most specific scope, want %q, got %q", "preset-model", got)
	}
	if got := resolved["prompt"].Literal; got != "global-prompt" {
		t.Errorf("prompt: no later scope binds it, so global survives; want %q, got %q", "global-prompt", got)
	}
}

func TestScopes_For_WorkflowBeatsGlobal(t *testing.T) {
	scopes := Scopes{
		Global:   ByTool{"t": {"model": tools.LiteralBinding("global")}},
		Workflow: ByTool{"t": {"model": tools.LiteralBinding("workflow")}},
	}
	if got := scopes.For("t")["model"].Literal; got != "workflow" {
		t.Errorf("workflow YAML is more specific than a global setting; want %q, got %q", "workflow", got)
	}
}

// TestScopes_For_ScopesDoNotMutate guards the primitive Merge promises: a
// resolution must not leak into the scope maps, or the second tool in a list
// would inherit the first one's answer.
func TestScopes_For_ScopesDoNotMutate(t *testing.T) {
	global := ByTool{"t": {"model": tools.LiteralBinding("global")}}
	scopes := Scopes{
		Global: global,
		Preset: ByTool{"t": {"model": tools.LiteralBinding("preset")}},
	}
	_ = scopes.For("t")
	if got := global["t"]["model"].Literal; got != "global" {
		t.Errorf("resolution mutated the global scope; want %q, got %q", "global", got)
	}
}

// TestApply_ToolDefaultSurvivesWhenNoScopeBindsIt is the zero-configuration
// invariant. A tool declaring a default must keep it when nothing overrides.
func TestApply_ToolDefaultSurvivesWhenNoScopeBindsIt(t *testing.T) {
	tool := newStubTool("t", tools.Bindings{"model": tools.LiteralBinding("tool-default")})

	bound, problems := Apply([]tools.Tool{tool}, Scopes{})
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if got := boundLiteral(t, bound[0], "model"); got != "tool-default" {
		t.Errorf("want the tool's own default %q, got %q", "tool-default", got)
	}
}

// TestApply_GlobalOverridesToolDefault is the chain's first real link, and
// the one that answers the user's question: an account-wide preference beats
// what the tool ships with.
func TestApply_GlobalOverridesToolDefault(t *testing.T) {
	tool := newStubTool("t", tools.Bindings{"model": tools.LiteralBinding("tool-default")})

	bound, problems := Apply([]tools.Tool{tool}, Scopes{
		Global: ByTool{"t": {"model": tools.LiteralBinding("global-choice")}},
	})
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if got := boundLiteral(t, bound[0], "model"); got != "global-choice" {
		t.Errorf("global setting must beat the tool default; want %q, got %q", "global-choice", got)
	}
}

// TestApply_FullChain walks all four scopes at once.
func TestApply_FullChain(t *testing.T) {
	tool := newStubTool("t", tools.Bindings{"model": tools.LiteralBinding("tool-default")})

	for _, tc := range []struct {
		name   string
		scopes Scopes
		want   string
	}{
		{"tool default", Scopes{}, "tool-default"},
		{"global", Scopes{Global: ByTool{"t": {"model": tools.LiteralBinding("g")}}}, "g"},
		{"workflow", Scopes{
			Global:   ByTool{"t": {"model": tools.LiteralBinding("g")}},
			Workflow: ByTool{"t": {"model": tools.LiteralBinding("w")}},
		}, "w"},
		{"preset", Scopes{
			Global:   ByTool{"t": {"model": tools.LiteralBinding("g")}},
			Workflow: ByTool{"t": {"model": tools.LiteralBinding("w")}},
			Preset:   ByTool{"t": {"model": tools.LiteralBinding("p")}},
		}, "p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bound, problems := Apply([]tools.Tool{tool}, tc.scopes)
			if len(problems) != 0 {
				t.Fatalf("unexpected problems: %v", problems)
			}
			if got := boundLiteral(t, bound[0], "model"); got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

// TestApply_BoundParamLeavesTheModelFacingSchema is the access-control half:
// resolution is only meaningful if the resolved parameter actually disappears
// from what the LLM is shown.
func TestApply_BoundParamLeavesTheModelFacingSchema(t *testing.T) {
	tool := newStubTool("t", nil)

	if tool.ParamSchema().Properties == nil {
		t.Fatal("stub tool has no properties; the fixture is wrong")
	}
	if _, present := tool.ParamSchema().Properties.Get("model"); !present {
		t.Fatal("model should be visible before anything binds it")
	}

	bound, problems := Apply([]tools.Tool{tool}, Scopes{
		Global: ByTool{"t": {"model": tools.LiteralBinding("fixed")}},
	})
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if _, present := bound[0].ParamSchema().Properties.Get("model"); present {
		t.Error("a globally bound parameter must not appear in the LLM-visible schema")
	}
	if _, present := bound[0].ParamSchema().Properties.Get("prompt"); !present {
		t.Error("an unbound parameter must remain visible")
	}
}

// TestApply_DoesNotMutateTheInputTool matters because tool instances come from
// a shared registry factory: binding one call must not configure every later
// call in the process.
func TestApply_DoesNotMutateTheInputTool(t *testing.T) {
	tool := newStubTool("t", nil)

	_, _ = Apply([]tools.Tool{tool}, Scopes{
		Preset: ByTool{"t": {"model": tools.LiteralBinding("fixed")}},
	})

	if _, present := tool.ParamSchema().Properties.Get("model"); !present {
		t.Error("Apply mutated the tool it was given; the shared registry instance is now bound")
	}
}

// TestApply_UnknownParamIsReportedNotFatal: a stale global setting naming a
// parameter the tool no longer declares must not take the LLM call down.
func TestApply_UnknownParamIsReportedNotFatal(t *testing.T) {
	tool := newStubTool("t", nil)
	other := newStubTool("other", nil)

	bound, problems := Apply([]tools.Tool{tool, other}, Scopes{
		Global: ByTool{"t": {"no_such_param": tools.LiteralBinding(1)}},
	})

	if len(bound) != 2 {
		t.Fatalf("every tool must survive; want 2 tools, got %d", len(bound))
	}
	if len(problems) != 1 {
		t.Fatalf("want exactly one reported problem, got %v", problems)
	}
	if !errors.Is(problems[0], tools.ErrUnknownBoundParam) {
		t.Errorf("problem should identify the unknown parameter, got %v", problems[0])
	}
	// The surviving tool is the unbound original, still usable.
	if _, present := bound[0].ParamSchema().Properties.Get("prompt"); !present {
		t.Error("the tool must still be callable after a bad binding")
	}
}

// TestApply_UnbindableToolIsReportedNotFatal covers the MCP shape.
func TestApply_UnbindableToolIsReportedNotFatal(t *testing.T) {
	mcpish := tools.NewSchemaOnlyTool("remote", "remote tool", map[string]any{
		"type":       "object",
		"properties": map[string]any{"model": map[string]any{"type": "string"}},
	})

	bound, problems := Apply([]tools.Tool{mcpish}, Scopes{
		Global: ByTool{"remote": {"model": tools.LiteralBinding("x")}},
	})

	if len(bound) != 1 || bound[0] == nil {
		t.Fatalf("the tool must survive an unsupported binding, got %v", bound)
	}
	if len(problems) != 1 || !errors.Is(problems[0], tools.ErrBindingsUnsupported) {
		t.Fatalf("want one ErrBindingsUnsupported, got %v", problems)
	}
}

// TestLoadGlobal_ReadsEveryToolInOneQuery is the server-side read. It also
// pins the single-query shape: the existing model.tag_config design reads one
// key per tag from the frontend, and repeating that per tool on the hot path
// of every LLM call would be a fan-out we would have to undo later.
func TestLoadGlobal_ReadsEveryToolInOneQuery(t *testing.T) {
	reader := &fakeSettings{rows: map[string][]*db.Setting{
		"user-1": {
			setting(GlobalSettingKey("generate_image"),
				`{"model":{"literal":{"tags":["image-gen"],"providers":["codex"]}}}`),
			setting(GlobalSettingKey("preview_url"), `{"port":{"literal":8080}}`),
			setting("model.tag_config.flagship", `{"model_id":"x"}`),
		},
	}}

	loaded, err := LoadGlobal(context.Background(), reader, "user-1")
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if reader.pattern != GlobalSettingKeyPrefix+"%" {
		t.Errorf("want one prefix query %q, got %q", GlobalSettingKeyPrefix+"%", reader.pattern)
	}
	if len(loaded) != 2 {
		t.Fatalf("want bindings for 2 tools, got %d (%v)", len(loaded), loaded)
	}

	imageModel, present := loaded["generate_image"]["model"]
	if !present {
		t.Fatal("generate_image has no model binding")
	}
	selector, ok := imageModel.Literal.(map[string]any)
	if !ok {
		t.Fatalf("model literal should decode as an object, got %T", imageModel.Literal)
	}
	providers, _ := selector["providers"].([]any)
	if len(providers) != 1 || providers[0] != "codex" {
		t.Errorf("want providers [codex], got %v", providers)
	}
	if _, leaked := loaded["flagship"]; leaked {
		t.Error("a model.tag_config key must not be read as a tool binding")
	}
}

// TestLoadGlobal_OneBadRowDoesNotSinkTheRest: the settings table is
// user-writable and outlives the code that wrote it.
func TestLoadGlobal_OneBadRowDoesNotSinkTheRest(t *testing.T) {
	reader := &fakeSettings{rows: map[string][]*db.Setting{
		"user-1": {
			setting(GlobalSettingKey("broken"), `not json at all`),
			setting(GlobalSettingKey("generate_image"), `{"model":{"literal":"gpt-image-2"}}`),
		},
	}}

	loaded, err := LoadGlobal(context.Background(), reader, "user-1")
	if err == nil {
		t.Error("the malformed row should be reported")
	}
	if got := loaded["generate_image"]["model"].Literal; got != "gpt-image-2" {
		t.Errorf("the good row must still load; want %q, got %v", "gpt-image-2", got)
	}
	if _, present := loaded["broken"]; present {
		t.Error("the malformed row must not produce bindings")
	}
}

func TestLoadGlobal_NoRowsIsNotAnError(t *testing.T) {
	reader := &fakeSettings{}
	loaded, err := LoadGlobal(context.Background(), reader, "user-1")
	if err != nil {
		t.Fatalf("an unconfigured user is the normal case: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("want no bindings, got %v", loaded)
	}
}

func TestLoadGlobal_QueryFailureIsReported(t *testing.T) {
	reader := &fakeSettings{err: fmt.Errorf("db down")}
	if _, err := LoadGlobal(context.Background(), reader, "user-1"); err == nil {
		t.Fatal("a failed settings read must be reported, not silently treated as unconfigured")
	}
}

// TestEncodeDecodeBindings_RoundTrip pins the stored shape, which is the
// contract the settings UI writes against.
func TestEncodeDecodeBindings_RoundTrip(t *testing.T) {
	original := tools.Bindings{
		"model":   tools.LiteralBinding(map[string]any{"tags": []any{"image-gen"}}),
		"save_to": tools.ExprBinding(`"assets/" + nodes.plan.slug`),
	}

	encoded, err := EncodeBindings(original)
	if err != nil {
		t.Fatalf("EncodeBindings: %v", err)
	}

	// The shape must be the plain JSON a TypeScript caller would write by
	// hand, not a Go-specific envelope.
	var raw map[string]map[string]any
	if err := json.Unmarshal([]byte(encoded), &raw); err != nil {
		t.Fatalf("encoded value is not a plain object: %v (%s)", err, encoded)
	}
	if _, present := raw["model"]["literal"]; !present {
		t.Errorf("a literal binding should serialize under \"literal\", got %s", encoded)
	}
	if raw["save_to"]["expr"] != `"assets/" + nodes.plan.slug` {
		t.Errorf("an expression binding should serialize under \"expr\", got %s", encoded)
	}

	decoded, err := DecodeBindings(encoded)
	if err != nil {
		t.Fatalf("DecodeBindings: %v", err)
	}
	if len(decoded) != 2 {
		t.Fatalf("round trip lost bindings: %v", decoded)
	}
	if !decoded["save_to"].IsExpr() {
		t.Error("the expression binding did not survive the round trip")
	}
}

// TestDecodeBindings_EmptyValueClearsRatherThanErrors is how the UI removes a
// preference without deleting the row.
func TestDecodeBindings_EmptyValueClearsRatherThanErrors(t *testing.T) {
	for _, value := range []string{"", "   ", "{}", "null"} {
		decoded, err := DecodeBindings(value)
		if err != nil {
			t.Errorf("DecodeBindings(%q) should clear, not error: %v", value, err)
		}
		if len(decoded) != 0 {
			t.Errorf("DecodeBindings(%q) = %v, want none", value, decoded)
		}
	}
}

// TestDecodeBindings_DropsEmptyEntries: a UI that writes {"model":{}} when the
// user picks "Automatic" means "no preference", not "bind model to nothing" —
// which would otherwise hide the parameter from the model for no reason.
func TestDecodeBindings_DropsEmptyEntries(t *testing.T) {
	decoded, err := DecodeBindings(`{"model":{},"save_to":{"literal":"x"}}`)
	if err != nil {
		t.Fatalf("DecodeBindings: %v", err)
	}
	if _, present := decoded["model"]; present {
		t.Error("an entry with neither literal nor expr is not a binding")
	}
	if decoded["save_to"].Literal != "x" {
		t.Errorf("the real binding was lost: %v", decoded)
	}
}

func TestToolNameFromGlobalSettingKey(t *testing.T) {
	for _, tc := range []struct {
		key      string
		wantName string
		wantOK   bool
	}{
		{GlobalSettingKey("generate_image"), "generate_image", true},
		{"model.tag_config.flagship", "", false},
		{GlobalSettingKeyPrefix, "", false},
	} {
		name, ok := ToolNameFromGlobalSettingKey(tc.key)
		if name != tc.wantName || ok != tc.wantOK {
			t.Errorf("ToolNameFromGlobalSettingKey(%q) = (%q, %v), want (%q, %v)",
				tc.key, name, ok, tc.wantName, tc.wantOK)
		}
	}
}

func TestScopes_ToolNames(t *testing.T) {
	scopes := Scopes{
		Global:   ByTool{"b": {"x": tools.LiteralBinding(1)}},
		Workflow: ByTool{"a": {"x": tools.LiteralBinding(1)}, "empty": {}},
		Preset:   ByTool{"b": {"y": tools.LiteralBinding(1)}},
	}
	got := scopes.ToolNames()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("want [a b], got %v", got)
	}
}
