package wfcel

import (
	"testing"
)

func evalMerge(t *testing.T, expr string, inputs map[string]interface{}) interface{} {
	t.Helper()
	out, err := EvaluateValue(expr, &EdgeEvalContext{Inputs: inputs})
	if err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
	return out
}

func TestMergeFunction(t *testing.T) {
	model := map[string]interface{}{"tags": []interface{}{"flagship"}, "thinking_level": "low", "temperature": 0.3}
	got := evalMerge(t, "merge(inputs.model, {'thinking_level': 'high'})", map[string]interface{}{"model": model}).(map[string]interface{})
	if got["thinking_level"] != "high" || got["temperature"] != 0.3 {
		t.Fatalf("merge result: %v", got)
	}
	if model["thinking_level"] != "low" {
		t.Fatal("merge mutated its input")
	}
	ms, err := toModelSelector(got, "t")
	if err != nil || ms.GetThinkingLevel() != "high" || ms.GetTemperature() != 0.3 || len(ms.Tags) != 1 {
		t.Fatalf("selector: %v %v", ms, err)
	}
	// null sides
	if m := evalMerge(t, "merge(null, {'a': 1})", map[string]interface{}{}).(map[string]interface{}); m["a"] == nil {
		t.Fatalf("null base: %v", m)
	}
	if _, err := EvaluateValue("merge('x', {'a': 1})", &EdgeEvalContext{Inputs: map[string]interface{}{}}); err == nil {
		t.Fatal("non-map must error")
	}
}
