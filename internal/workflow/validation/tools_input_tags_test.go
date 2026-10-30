package validation

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// The tag vocabulary is judged when a workflow is validated. The values a run
// starts with (ValidateInputs) only type-check: a renamed tag in a saved
// preset must not stop the run.
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
			input := &pb.Input{Type: "tools"}
			err := toolsInputTagError("tools", []any{tc.entry}, input)
			if (err != nil) != tc.invalid {
				t.Fatalf("entry %q: error=%v, want invalid=%v", tc.entry, err, tc.invalid)
			}
			if err := validateProtoInputType("tools", []any{tc.entry}, input, nil); err != nil {
				t.Fatalf("entry %q rejected at run time: %v", tc.entry, err)
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

// Authoring rejects an unknown tag; loading the same workflow to run it only
// warns, so a definition saved before a tag was renamed still starts.
func TestToolsInputTagIsAnErrorOnlyWhenAuthoring(t *testing.T) {
	wf := actionWorkflow("http/request@1", map[string]*structpb.Value{"url": structpb.NewStringValue("https://example.com")})
	wf.Inputs = map[string]*pb.Input{"tools": {Type: "tools", Config: &pb.Input_ToolsInput{ToolsInput: &pb.ToolsInputConfig{
		Default: structpb.NewListValue(&structpb.ListValue{Values: []*structpb.Value{structpb.NewStringValue("tag:default")}}),
	}}}}
	authored := StaticAnalysisWithOptions(wf, &ValidationOptions{Authoring: true})
	if len(authored.ByCategory(CategoryToolTag)) != 1 || !authored.HasErrors() {
		t.Fatalf("authoring accepted an unknown tag: %s", authored.Error())
	}
	run := StaticAnalysisWithOptions(wf, &ValidationOptions{})
	if run.HasErrors() || len(run.Warnings()) != 1 || run.Warnings()[0].Category != CategoryToolTag {
		t.Fatalf("a run must start with a warning only: errors=%v warnings=%v", run.Errors(), run.Warnings())
	}
}
