package validation

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

func makeParallelLoopWorkflow(loopArgs *reliantv1.LoopArgs) *reliantv1.Workflow {
	return &reliantv1.Workflow{
		Name:  "test-parallel",
		Entry: []string{"my_loop"},
		Nodes: []*reliantv1.Node{
			{
				Id:   "my_loop",
				Type: "loop",
				Args: &reliantv1.Node_Loop{
					Loop: loopArgs,
				},
			},
		},
	}
}

func hasStructuralError(result *Result, substring string) bool {
	for _, e := range result.ByCategory(CategoryStructure) {
		// Check both message and field, since the substring might be in either
		if strings.Contains(e.Message, substring) || strings.Contains(e.Field, substring) {
			return true
		}
	}
	return false
}

func TestParallelLoopRequiresItems(t *testing.T) {
	wf := makeParallelLoopWorkflow(&reliantv1.LoopArgs{
		Parallel: &reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: true}},
		Ref:      &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://agent"}},
	})
	result := StaticAnalysis(wf, nil)
	if !hasStructuralError(result, "items") {
		t.Errorf("expected error about missing items, got errors: %v", result.Errors())
	}
}

func TestParallelLoopDisallowsWhile(t *testing.T) {
	wf := makeParallelLoopWorkflow(&reliantv1.LoopArgs{
		Parallel: &reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: true}},
		Items:    &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "{{inputs.components}}"}},
		While:    &reliantv1.DirectCelBool{Expr: "iter.iteration < 5"},
		Ref:      &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://agent"}},
	})
	result := StaticAnalysis(wf, nil)
	if !hasStructuralError(result, "while") {
		t.Errorf("expected error about while not allowed, got errors: %v", result.Errors())
	}
}

func TestParallelLoopDisallowsYield(t *testing.T) {
	wf := makeParallelLoopWorkflow(&reliantv1.LoopArgs{
		Parallel: &reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: true}},
		Items:    &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "{{inputs.components}}"}},
		Yield:    "inputs.yield",
		Ref:      &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://agent"}},
	})
	result := StaticAnalysis(wf, nil)
	if !hasStructuralError(result, "yield") {
		t.Errorf("expected error about yield not allowed, got errors: %v", result.Errors())
	}
}

func TestParallelLoopValidOnFailure(t *testing.T) {
	for _, valid := range []string{"continue", "fail_fast", "fail_all"} {
		wf := makeParallelLoopWorkflow(&reliantv1.LoopArgs{
			Parallel:  &reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: true}},
			Items:     &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "{{inputs.components}}"}},
			OnFailure: valid,
			Ref:       &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://agent"}},
		})
		result := StaticAnalysis(wf, nil)
		if hasStructuralError(result, "on_failure") {
			t.Errorf("on_failure=%q should be valid, got error", valid)
		}
	}
}

func TestParallelLoopInvalidOnFailure(t *testing.T) {
	wf := makeParallelLoopWorkflow(&reliantv1.LoopArgs{
		Parallel:  &reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: true}},
		Items:     &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "{{inputs.components}}"}},
		OnFailure: "invalid_policy",
		Ref:       &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://agent"}},
	})
	result := StaticAnalysis(wf, nil)
	if !hasStructuralError(result, "on_failure") {
		t.Errorf("expected error about invalid on_failure, got errors: %v", result.Errors())
	}
}

func TestSequentialLoopStillRequiresWhile(t *testing.T) {
	wf := makeParallelLoopWorkflow(&reliantv1.LoopArgs{
		// No parallel flag = sequential
		Ref: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://agent"}},
	})
	result := StaticAnalysis(wf, nil)
	if !hasStructuralError(result, "while") {
		t.Errorf("expected error about missing while, got errors: %v", result.Errors())
	}
}

func TestValidParallelLoopPasses(t *testing.T) {
	wf := makeParallelLoopWorkflow(&reliantv1.LoopArgs{
		Parallel: &reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: true}},
		Items:    &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "{{inputs.components}}"}},
		Ref:      &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://agent"}},
	})
	result := StaticAnalysis(wf, nil)
	structErrors := result.ByCategory(CategoryStructure)
	if len(structErrors) > 0 {
		for _, e := range structErrors {
			t.Errorf("unexpected structural error: %s", e.Message)
		}
	}
}
