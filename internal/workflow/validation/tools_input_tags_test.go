package validation

import (
	"strings"
	"testing"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

func TestToolsInputTagVocabulary(t *testing.T) {
	for _, tc := range []struct {
		name, entry string
		invalid     bool
	}{
		{"renamed", "tag:default", true},
		{"known", "tag:coding:default", false},
		{"runtime MCP", "mcp__server__tool", false},
		{"glob", "mcp__*", false},
		{"exclusion", "!tag:unknown", false},
		{"expression", "{{inputs.tools}}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProtoInputType("tools", []any{tc.entry}, &pb.Input{Type: "tools"}, nil)
			if (err != nil) != tc.invalid {
				t.Fatalf("entry %q: error=%v, want invalid=%v", tc.entry, err, tc.invalid)
			}
		})
	}
}

func TestToolsLiteralChildArgTagVocabulary(t *testing.T) {
	result := &Result{}
	validateProtoInputsAgainstSchema([]string{"nodes", "child"}, map[string]interface{}{
		"tools": []interface{}{"tag:default"},
	}, nil, map[string]*pb.Input{"tools": {Type: "tools"}}, nil, nil, nil, result)
	if !result.HasErrors() || !strings.Contains(result.Error(), "tool tag \"default\"") {
		t.Fatalf("unknown literal child tools arg was accepted: %s", result.Error())
	}
}

func TestToolsInputDefaultInsideInlineWorkflow(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(`name: test
entry: nested
nodes:
  - id: nested
    type: workflow
    inline:
      inputs:
        tools:
          type: tools
          default: [tag:default]
      nodes: []
`))
	if err != nil {
		t.Fatal(err)
	}
	result := &Result{}
	validateToolsConfigBindings(wf, result)
	if !result.HasErrors() || !strings.Contains(result.Error(), "nodes.nested.inline.inputs.tools") {
		t.Fatalf("inline tools default was not reached: %s", result.Error())
	}
}

func TestToolsInputDefaultTagVocabulary(t *testing.T) {
	for _, tc := range []struct{ name, declaration string }{
		{"root", "  tools:\n    type: tools\n    default: [tag:default]\n"},
		{"group", "  implementer:\n    type: group\n    inputs:\n      tools:\n        type: tools\n        default: [tag:default]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf, err := wfyaml.ParseWorkflow([]byte("name: test\ninputs:\n" + tc.declaration + "nodes: []\n"))
			if err != nil {
				t.Fatal(err)
			}
			result := &Result{}
			validateToolsConfigBindings(wf, result)
			if !result.HasErrors() || !strings.Contains(result.Error(), "tool tag \"default\"") {
				t.Fatalf("unknown tools default was accepted: %s", result.Error())
			}
		})
	}
}
