// Copyright (c) 2025 Reliant Labs
package tools

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// allParserNodeTypes lists every node type the YAML parser accepts, mirroring
// the constants in internal/workflow/model/constants.go. Adding a node type
// there without registering it in internal/workflow/reference makes get_schema
// fail on a type the product genuinely supports — this list is what catches it.
var allParserNodeTypes = []string{
	model.NodeTypeAskQuestion,
	model.NodeTypeCallLLM,
	model.NodeTypeExecuteTools,
	model.NodeTypeCompact,
	model.NodeTypeApproval,
	model.NodeTypeSaveMessage,
	model.NodeTypeCreateWorktree,
	model.NodeTypeRun,
	model.NodeTypeWorkflow,
	model.NodeTypeLoop,
	model.NodeTypeJoin,
	model.NodeTypeRouter,
}

func TestGetSchemaResolvesEveryParserNodeType(t *testing.T) {
	for _, nodeType := range allParserNodeTypes {
		t.Run(nodeType, func(t *testing.T) {
			doc, err := getNodeTypeDoc(nodeType)
			if err != nil {
				t.Fatalf("get_schema cannot resolve node type %q, which the parser accepts: %v", nodeType, err)
			}
			if !strings.Contains(doc, nodeType) {
				t.Errorf("doc for %q does not mention the type name:\n%s", nodeType, doc)
			}
		})
	}
}

// getSchemaDescriptionExamples are the type names get_schema's own description
// offers as examples. An agent's first call is almost always one of these, so
// every one has to resolve.
var getSchemaDescriptionExamples = []string{
	"call_llm",
	"ThreadConfig",
	"CallLLMOutput",
	"Workflow",
	"Edge",
}

func TestGetSchemaResolvesItsOwnDescriptionExamples(t *testing.T) {
	tool := &getSchemaTool{}

	for _, name := range getSchemaDescriptionExamples {
		if !strings.Contains(tool.Description(), name) {
			t.Errorf("example %q is no longer in the tool description; update getSchemaDescriptionExamples", name)
		}
	}

	// EdgeCase is named under "Top-level" in the description body rather than
	// the EXAMPLES block, but it is advertised just the same.
	for _, name := range append(getSchemaDescriptionExamples, "EdgeCase") {
		t.Run(name, func(t *testing.T) {
			resp, err := tool.Execute(nil, GetSchemaParams{Name: name})
			if err != nil {
				t.Fatalf("Execute(%q) returned error: %v", name, err)
			}
			if resp.IsError {
				t.Fatalf("get_schema advertises %q but cannot resolve it:\n%s", name, resp.Content)
			}
			if !strings.Contains(resp.Content, name) {
				t.Errorf("response for %q does not mention it:\n%s", name, resp.Content)
			}
		})
	}
}
