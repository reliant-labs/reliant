// Copyright (c) 2025 Reliant Labs
// Tests for the reliant__ tool-name prefix and tool_choice pinning.
//
// The bug these exist to prevent is round-trip asymmetry: a name prefixed on
// the way out but not stripped on the way back in produces a tool call Reliant
// cannot execute, and a tool_choice that skips the prefix pins a function
// absent from the tools array.
package antigravity

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/tools"
)

func TestPrefixedToolName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "prefixes a bare name", in: "view_file", want: "reliant__view_file"},
		{name: "idempotent", in: "reliant__view_file", want: "reliant__view_file"},
		{name: "empty passes through", in: "", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := prefixedToolName(tc.in); got != tc.want {
				t.Errorf("prefixedToolName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestToolNameRoundTrip is the both-directions pin: every name that goes out
// prefixed must come back as the original.
func TestToolNameRoundTrip(t *testing.T) {
	for _, original := range []string{"view_file", "set_title", "run_command", ""} {
		if got := unprefixedToolName(prefixedToolName(original)); got != original {
			t.Errorf("round trip of %q produced %q", original, got)
		}
	}
}

func TestConvertToolsAppliesPrefix(t *testing.T) {
	converted := convertTools([]tools.Tool{tools.NewSetTitleTool()})
	if len(converted) != 1 || len(converted[0].FunctionDeclarations) != 1 {
		t.Fatalf("expected one tool with one declaration, got %+v", converted)
	}
	got := converted[0].FunctionDeclarations[0].Name
	want := reliantToolPrefix + tools.SetTitleToolName
	if got != want {
		t.Errorf("declared tool name = %q, want %q", got, want)
	}
}

func TestConvertToolsEmpty(t *testing.T) {
	if converted := convertTools(nil); converted != nil {
		t.Errorf("convertTools(nil) = %+v, want nil", converted)
	}
}

// TestToolChoiceUsesPrefixedName is the equivalent of
// anthropic/tool_choice_test.go:79 — the pin must name the SAME rewritten tool
// that appears in the tools array, or the request names a function that is not
// there.
func TestToolChoiceUsesPrefixedName(t *testing.T) {
	titleTool := tools.NewSetTitleTool()
	list := []tools.Tool{titleTool}

	converted := convertTools(list)
	config := buildToolConfig(tools.SetTitleToolName, list)

	if config == nil || config.FunctionCallingConfig == nil {
		t.Fatal("expected a tool config pinning the named tool")
	}
	if len(config.FunctionCallingConfig.AllowedFunctionNames) != 1 {
		t.Fatalf("allowedFunctionNames = %v, want exactly one", config.FunctionCallingConfig.AllowedFunctionNames)
	}
	pinned := config.FunctionCallingConfig.AllowedFunctionNames[0]

	declared := map[string]bool{}
	for _, decl := range converted[0].FunctionDeclarations {
		declared[decl.Name] = true
	}
	if !declared[pinned] {
		t.Fatalf("tool_choice pins %q but the tools array declares %v", pinned, declared)
	}
	if config.FunctionCallingConfig.Mode != "ANY" {
		t.Errorf("mode = %q, want ANY", config.FunctionCallingConfig.Mode)
	}
}

func TestToolChoiceUnpinnedAndAbsent(t *testing.T) {
	list := []tools.Tool{tools.NewSetTitleTool()}

	if config := buildToolConfig("", list); config != nil {
		t.Errorf("no force choice should produce no tool config, got %+v", config)
	}
	// A pin naming a tool that is not in the array is a provider error, so it
	// is dropped rather than sent.
	if config := buildToolConfig("not_a_real_tool", list); config != nil {
		t.Errorf("absent tool should produce no tool config, got %+v", config)
	}
	if config := buildToolConfig(tools.SetTitleToolName, nil); config != nil {
		t.Errorf("empty tool list should produce no tool config, got %+v", config)
	}
}
