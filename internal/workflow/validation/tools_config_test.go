// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

// workflowWithToolBindings builds the smallest workflow that carries a
// tools_config.tools block, so these tests exercise the binding layer and not
// the structural one.
func workflowWithToolBindings(t *testing.T, toolBindings map[string]map[string]any) *reliantv1.Workflow {
	t.Helper()

	protoBindings := map[string]*structpb.Struct{}
	for toolName, params := range toolBindings {
		asStruct, err := structpb.NewStruct(params)
		if err != nil {
			t.Fatalf("building struct for %q: %v", toolName, err)
		}
		protoBindings[toolName] = asStruct
	}

	// Structurally complete on purpose. StaticAnalysis returns early when
	// structural validation fails, so a fixture missing `entry` or `model`
	// never reaches the binding layer — and every negative test below would
	// then pass for the wrong reason.
	return &reliantv1.Workflow{
		Name:  "tool-binding-fixture",
		Entry: []string{"generate"},
		Nodes: []*reliantv1.Node{{
			Id:   "generate",
			Type: "call_llm",
			Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
				Model: &reliantv1.CelModelSelector{
					Value: &reliantv1.CelModelSelector_Literal{
						Literal: &reliantv1.ModelSelector{Tags: []string{"flagship"}},
					},
				},
				ToolsConfig: &reliantv1.ToolsConfig{Tools: protoBindings},
			}},
		}},
	}
}

// bindingErrors returns only the tool-binding errors, so an unrelated
// complaint cannot be mistaken for the thing under test.
//
// It FAILS on any structural error rather than filtering it out. StaticAnalysis
// returns early when structure is bad, so a structurally broken fixture would
// produce zero binding errors — which is exactly what a passing negative test
// looks like if you only filter. This turns that into a loud failure.
func bindingErrors(t *testing.T, result *Result) []*Error {
	t.Helper()

	var found []*Error
	for _, err := range result.Errors() {
		if err.Category == CategoryToolBinding {
			found = append(found, err)
			continue
		}
		if err.Category == CategoryStructure {
			t.Fatalf("fixture is structurally invalid, so the binding layer never ran: %s", err.Message)
		}
	}
	return found
}

// THE NEGATIVE TEST. A workflow binding a parameter that the tool does not
// declare must fail validation. Before this layer existed the binding was
// accepted here and discarded at runtime — for a tool the filter never admits,
// discarded with no error at all.
func TestToolsConfigRejectsUnknownParameter(t *testing.T) {
	wf := workflowWithToolBindings(t, map[string]map[string]any{
		"generate_image": {"sizze": "1536x1024"},
	})

	errs := bindingErrors(t, StaticAnalysis(wf, nil))
	if len(errs) != 1 {
		t.Fatalf("expected exactly 1 tool-binding error, got %d: %+v", len(errs), errs)
	}

	message := errs[0].Message
	if !strings.Contains(message, "sizze") {
		t.Errorf("error should name the bad parameter, got: %s", message)
	}
	if !strings.Contains(message, "size") {
		t.Errorf("error should list the real parameters so the typo is obvious, got: %s", message)
	}
}

func TestToolsConfigRejectsUnknownTool(t *testing.T) {
	wf := workflowWithToolBindings(t, map[string]map[string]any{
		"generate_imagee": {"size": "1536x1024"},
	})

	errs := bindingErrors(t, StaticAnalysis(wf, nil))
	if len(errs) != 1 {
		t.Fatalf("expected exactly 1 tool-binding error, got %d: %+v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Message, "generate_imagee") {
		t.Errorf("error should name the unknown tool, got: %s", errs[0].Message)
	}
	if !strings.Contains(errs[0].Suggestion, "generate_image") {
		t.Errorf("expected a did-you-mean suggestion, got: %q", errs[0].Suggestion)
	}
}

// A parameter that EXISTS but is deliberately unbindable must be rejected with
// its reason, not with a spelling suggestion. Someone who wrote save_to
// spelled it perfectly; telling them to check the spelling wastes their time.
func TestToolsConfigRejectsUnbindableParameterWithReason(t *testing.T) {
	wf := workflowWithToolBindings(t, map[string]map[string]any{
		"generate_image": {"save_to": "assets/hero.png"},
	})

	errs := bindingErrors(t, StaticAnalysis(wf, nil))
	if len(errs) != 1 {
		t.Fatalf("expected exactly 1 tool-binding error, got %d: %+v", len(errs), errs)
	}
	message := errs[0].Message
	if !strings.Contains(message, "not bindable") {
		t.Errorf("error should say the parameter is unbindable, got: %s", message)
	}
	if !strings.Contains(message, "overwrite") {
		t.Errorf("error should carry the policy reason, got: %s", message)
	}
}

// The positive case. A valid binding — including `model`, which is bound by
// default and therefore absent from the model-visible schema — must pass.
// Without this the guard could "work" by rejecting everything.
func TestToolsConfigAcceptsValidBindings(t *testing.T) {
	wf := workflowWithToolBindings(t, map[string]map[string]any{
		"generate_image": {
			"model": map[string]any{"tags": []any{"image-gen"}, "providers": []any{"codex"}},
			"size":  "1536x1024",
		},
	})

	if errs := bindingErrors(t, StaticAnalysis(wf, nil)); len(errs) != 0 {
		t.Fatalf("valid bindings should pass, got: %+v", errs)
	}
}

// Zero configuration must stay valid. The invariant this whole feature rests
// on is that a tool works with no bindings at all.
func TestToolsConfigWithoutToolsBlockIsValid(t *testing.T) {
	wf := workflowWithToolBindings(t, nil)
	if errs := bindingErrors(t, StaticAnalysis(wf, nil)); len(errs) != 0 {
		t.Fatalf("absent tools block should pass, got: %+v", errs)
	}
}

// Every bad key is reported, not just the first. An author fixing a config
// one error per run is an author running validation six times.
func TestToolsConfigReportsEveryBadKey(t *testing.T) {
	wf := workflowWithToolBindings(t, map[string]map[string]any{
		"generate_image": {"sizze": "x", "qualty": "high"},
		"nonexistent":    {"whatever": "y"},
	})

	errs := bindingErrors(t, StaticAnalysis(wf, nil))
	if len(errs) != 3 {
		t.Fatalf("expected 3 tool-binding errors (2 params + 1 tool), got %d: %+v", len(errs), errs)
	}
}
