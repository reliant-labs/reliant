// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

// --- tools_config filter-list tags -----------------------------------------

// callLLMNodeWithFilter builds a call_llm node carrying the given
// preloaded_tools filter, for the tag-vocabulary layer.
func callLLMNodeWithFilter(id string, filter ...string) *reliantv1.Node {
	return &reliantv1.Node{
		Id:   id,
		Type: "call_llm",
		Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
			Model: &reliantv1.CelModelSelector{
				Value: &reliantv1.CelModelSelector_Literal{
					Literal: &reliantv1.ModelSelector{Tags: []string{"flagship"}},
				},
			},
			ToolsConfig: &reliantv1.ToolsConfig{
				PreloadedTools: &reliantv1.CelStringList{
					Value: &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{Values: filter}},
				},
			},
		}},
	}
}

// workflowWithFilter wraps filter nodes in a structurally complete workflow.
func workflowWithFilter(nodes ...*reliantv1.Node) *reliantv1.Workflow {
	return &reliantv1.Workflow{
		Name:  "tool-filter-fixture",
		Entry: []string{nodes[0].GetId()},
		Nodes: nodes,
	}
}

// tagWarnings returns only the tool-tag findings, so an unrelated complaint
// cannot be mistaken for the thing under test.
func tagWarnings(result *Result) []*Error {
	var found []*Error
	for _, w := range result.Warnings() {
		if w.Category == CategoryToolBinding {
			found = append(found, w)
		}
	}
	return found
}

// THE NEGATIVE TEST for the filter lists. A tag:X entry naming a tag that is
// not in the vocabulary selects NOTHING at run time — ExpandToolFilter finds
// no carriers and moves on — so the agent starts with a bundle smaller than
// its author believed. tag:default (renamed tag:coding:default) is exactly
// that mistake, and it used to pass validation silently.
func TestToolsConfigFilterRejectsUnknownTag(t *testing.T) {
	wf := workflowWithFilter(callLLMNodeWithFilter("call", "tag:coding:default", "tag:default"))

	result := StaticAnalysis(wf, nil)
	if result.HasErrors() {
		t.Fatalf("unknown tag should warn, not error: %s", result.Error())
	}
	warnings := tagWarnings(result)
	if len(warnings) != 1 {
		t.Fatalf("expected exactly 1 tag warning, got %d: %+v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0].Message, `tag "default"`) {
		t.Errorf("warning should name the unknown tag, got: %s", warnings[0].Message)
	}
	if !strings.Contains(warnings[0].Message, "coding:default") {
		t.Errorf("warning should list the vocabulary so the rename is obvious, got: %s", warnings[0].Message)
	}
}

// The positive case. Known tags — descriptive, curated, and the runtime-only
// tag:mcp, which matches no statically registered tool but is a legitimate
// name — must pass, alongside the entries that cannot be judged statically.
func TestToolsConfigFilterAcceptsKnownAndUnjudgeableEntries(t *testing.T) {
	wf := workflowWithFilter(callLLMNodeWithFilter("call",
		"tag:coding:default", // curated bundle
		"tag:file",           // descriptive
		"tag:mcp",            // runtime-only: no static carrier, still valid
		"view",               // plain name: may be an MCP tool at run time
		"mcp__serena__*",     // glob: matches anything
		"!tag:shell",         // exclusion of a known tag
		"!nonexistent",       // exclusion: excluding nothing is harmless
	))

	if warnings := tagWarnings(StaticAnalysis(wf, nil)); len(warnings) != 0 {
		t.Fatalf("known and unjudgeable entries should pass, got: %+v", warnings)
	}
}

// A filter supplied as one expression ("{{inputs.tools}}") names nothing
// until the run binds it, and is skipped wholesale.
func TestToolsConfigFilterSkipsExpressionList(t *testing.T) {
	node := callLLMNodeWithFilter("call")
	node.GetCallLlm().GetToolsConfig().PreloadedTools = &reliantv1.CelStringList{
		Value: &reliantv1.CelStringList_Expr{Expr: "inputs.tools"},
	}
	wf := workflowWithFilter(node)

	if warnings := tagWarnings(StaticAnalysis(wf, nil)); len(warnings) != 0 {
		t.Fatalf("expression filter should be skipped, got: %+v", warnings)
	}
}

// loadable_tools carries the same syntax and the same silent-drop failure
// (load_tool cannot reach a bundle that matches nothing), so it is checked
// too.
func TestToolsConfigFilterChecksLoadableTools(t *testing.T) {
	node := callLLMNodeWithFilter("call", "tag:coding:default")
	node.GetCallLlm().GetToolsConfig().LoadableTools = &reliantv1.CelStringList{
		Value: &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{Values: []string{"tag:defualt"}}},
	}
	wf := workflowWithFilter(node)

	warnings := tagWarnings(StaticAnalysis(wf, nil))
	if len(warnings) != 1 {
		t.Fatalf("expected exactly 1 tag warning on loadable_tools, got %d: %+v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0].Message, `tag "defualt"`) {
		t.Errorf("warning should name the misspelled tag, got: %s", warnings[0].Message)
	}
}

// The inline-body walk. A call_llm node inside a loop body used to be
// validated by nobody: the structural and CEL layers recurse with lenient
// rules and the cross-workflow layer recurses through compile contracts, but
// this layer stopped at the root graph — so a binding and a tag inside the
// body were accepted here and discarded at run time.
func TestToolsConfigBindingsReachedInsideInlineLoopBody(t *testing.T) {
	body := callLLMNodeWithFilter("call", "tag:default")
	body.GetCallLlm().GetToolsConfig().Tools = map[string]*structpb.Struct{
		"no_such_tool": mustStruct(t, map[string]any{"param": 1}),
	}
	wf := workflowWithFilter(&reliantv1.Node{
		Id:   "loop",
		Type: "loop",
		Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{
			While: &reliantv1.DirectCelBool{Expr: "true"},
			Inline: &reliantv1.Workflow{
				Name:  "loop-body",
				Entry: []string{"call"},
				Nodes: []*reliantv1.Node{body},
			},
		}},
	})

	result := StaticAnalysis(wf, nil)
	errs := bindingErrors(t, result)
	if len(errs) != 1 {
		t.Fatalf("expected exactly 1 tool-binding error inside the loop body, got %d: %+v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Message, "no_such_tool") {
		t.Errorf("error should name the unknown tool, got: %s", errs[0].Message)
	}
	// The path must say WHERE the node is, not just which node.
	pathStr := strings.Join(errs[0].Path, ".")
	if !strings.Contains(pathStr, "loop") || !strings.Contains(pathStr, "inline") {
		t.Errorf("error path should locate the node inside the loop body, got: %s", pathStr)
	}
	if warnings := tagWarnings(result); len(warnings) != 1 {
		t.Fatalf("expected exactly 1 tag warning inside the loop body, got %d: %+v", len(warnings), warnings)
	}
}

// mustStruct builds a structpb.Struct, failing the test on error.
func mustStruct(t *testing.T, m map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(m)
	if err != nil {
		t.Fatalf("building struct: %v", err)
	}
	return s
}
