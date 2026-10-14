package wfcel

import "testing"

func TestTrimPrefixSuffix(t *testing.T) {
	nodes := map[string]interface{}{"run": map[string]interface{}{"stdout": "Error: boom\n"}}
	tests := []struct{ expr, want string }{
		{`nodes.run.stdout.trimPrefix("Error: ")`, "boom\n"},
		{`nodes.run.stdout.trimSuffix("\n")`, "Error: boom"},
		{`"abc".trimPrefix("x")`, "abc"},
		{`"abc".trimSuffix("x")`, "abc"},
		{`"abc".trimPrefix("")`, "abc"},
		{`"abc".trimPrefix("abc")`, ""},
		{`"abcabc".trimSuffix("abc")`, "abc"},
		{`"aaa".trimPrefix("a")`, "aa"},
		{`"refs/heads/main".trimPrefix("refs/heads/")`, "main"},
		{`"v1.2".trimPrefix("v").trimSuffix(".2")`, "1"},
	}
	for _, tc := range tests {
		got, err := EvaluateValue(tc.expr, &EdgeEvalContext{Nodes: nodes, Inputs: map[string]interface{}{}})
		if err != nil {
			t.Errorf("%s: %v", tc.expr, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s = %q, want %q", tc.expr, got, tc.want)
		}
	}
	for _, expr := range []string{`1.trimPrefix("a")`, `"a".trimPrefix(1)`} {
		if _, err := EvaluateValue(expr, &EdgeEvalContext{Inputs: map[string]interface{}{}}); err == nil {
			t.Errorf("%s: expected a compile error", expr)
		}
	}
}
