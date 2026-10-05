package handlers

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func modelValueArgs(ms *reliantv1.ModelSelector) *reliantv1.CallLLMArgs {
	return &reliantv1.CallLLMArgs{Model: &reliantv1.CelModelSelector{
		Value: &reliantv1.CelModelSelector_Literal{Literal: ms},
	}}
}

func f64(v float64) *float64 { return &v }
func i32(v int32) *int32     { return &v }

func TestExplicitSamplingOverrides_ModelValue(t *testing.T) {
	args := modelValueArgs(&reliantv1.ModelSelector{
		Tags: []string{"flagship"}, ThinkingLevel: "low", Temperature: f64(0),
	})
	temp, level := explicitSamplingOverrides(args)
	if level != "low" {
		t.Errorf("thinking level = %q, want low", level)
	}
	if temp == nil || *temp != 0 {
		t.Errorf("temperature = %v, want pointer to 0", temp)
	}
}

func TestExplicitSamplingOverrides_UnsetModelValueLeavesDefaults(t *testing.T) {
	temp, level := explicitSamplingOverrides(modelValueArgs(&reliantv1.ModelSelector{Tags: []string{"flagship"}}))
	if temp != nil || level != "" {
		t.Errorf("got temp=%v level=%q, want nil/empty", temp, level)
	}
}

func TestExplicitSamplingOverrides_NodeArgPinWins(t *testing.T) {
	args := modelValueArgs(&reliantv1.ModelSelector{ThinkingLevel: "low", Temperature: f64(0.9)})
	args.ThinkingLevel = &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "high"}}
	args.Temperature = &reliantv1.CelDouble{Value: &reliantv1.CelDouble_Literal{Literal: 0}}
	temp, level := explicitSamplingOverrides(args)
	if level != "high" {
		t.Errorf("thinking level = %q, want pinned high", level)
	}
	if temp == nil || *temp != 0 {
		t.Errorf("temperature = %v, want pinned 0", temp)
	}
}

func TestCompactionThreshold_ModelValueAndArgPrecedence(t *testing.T) {
	args := modelValueArgs(&reliantv1.ModelSelector{CompactionThreshold: i32(42000)})
	if got := explicitCompactionThresholdArg(args); got != 42000 || !compactionThresholdIsSet(args) {
		t.Errorf("model value threshold = %d (set=%v), want 42000", got, compactionThresholdIsSet(args))
	}
	args.CompactionThreshold = celIntLiteral(7000)
	if got := explicitCompactionThresholdArg(args); got != 7000 {
		t.Errorf("node arg threshold = %d, want 7000", got)
	}
	if compactionThresholdIsSet(modelValueArgs(&reliantv1.ModelSelector{CompactionThreshold: i32(0)})) {
		t.Error("model value threshold 0 must mean derive-from-model")
	}
}
