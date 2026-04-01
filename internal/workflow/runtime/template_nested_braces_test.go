package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExtractTemplateExpressions tests the balanced brace parser that replaced
// the regex-based template extraction. This correctly handles nested braces in
// CEL expressions like map/object literals.
func TestExtractTemplateExpressions(t *testing.T) {
	testCases := []struct {
		name     string
		template string
		expected []templateMatch
	}{
		{
			name:     "simple expression",
			template: "{{nodes.foo.bar}}",
			expected: []templateMatch{
				{full: "{{nodes.foo.bar}}", expr: "nodes.foo.bar", start: 0, end: 17},
			},
		},
		{
			name:     "map literal",
			template: "{{[{a: 1}]}}",
			expected: []templateMatch{
				{full: "{{[{a: 1}]}}", expr: "[{a: 1}]", start: 0, end: 12},
			},
		},
		{
			name:     "ternary with maps",
			template: "{{x ? {a: 1} : {b: 2}}}",
			expected: []templateMatch{
				{full: "{{x ? {a: 1} : {b: 2}}}", expr: "x ? {a: 1} : {b: 2}", start: 0, end: 23},
			},
		},
		{
			name:     "multiline with map",
			template: "{{[\n  {a: 1}\n]}}",
			expected: []templateMatch{
				{full: "{{[\n  {a: 1}\n]}}", expr: "[\n  {a: 1}\n]", start: 0, end: 16},
			},
		},
		{
			name:     "map with multiple properties",
			template: "{{ results.map(r, { tool_call_id: r.id, name: r.name }) }}",
			expected: []templateMatch{
				{full: "{{ results.map(r, { tool_call_id: r.id, name: r.name }) }}", expr: "results.map(r, { tool_call_id: r.id, name: r.name })", start: 0, end: 58},
			},
		},
		{
			name:     "multiple templates",
			template: "{{a}} and {{ {b: c} }}",
			expected: []templateMatch{
				{full: "{{a}}", expr: "a", start: 0, end: 5},
				{full: "{{ {b: c} }}", expr: "{b: c}", start: 10, end: 22},
			},
		},
		{
			name:     "template with surrounding text",
			template: "prefix {{expr}} suffix",
			expected: []templateMatch{
				{full: "{{expr}}", expr: "expr", start: 7, end: 15},
			},
		},
		{
			name:     "no template",
			template: "just a string",
			expected: []templateMatch{},
		},
		{
			name:     "empty string",
			template: "",
			expected: []templateMatch{},
		},
		{
			name:     "unmatched opening braces",
			template: "{{ incomplete",
			expected: []templateMatch{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			matches := extractTemplateExpressions(tc.template)
			require.Equal(t, len(tc.expected), len(matches), "number of matches")
			for i, expected := range tc.expected {
				assert.Equal(t, expected.full, matches[i].full, "match %d full", i)
				assert.Equal(t, expected.expr, matches[i].expr, "match %d expr", i)
				assert.Equal(t, expected.start, matches[i].start, "match %d start", i)
				assert.Equal(t, expected.end, matches[i].end, "match %d end", i)
			}
		})
	}
}

// TestTemplateEvaluationWithNestedBraces verifies that templates with nested braces
// are correctly extracted and the expressions can be accessed.
// The key fix is that the parser now correctly handles nested braces in templates.
func TestTemplateEvaluationWithNestedBraces(t *testing.T) {
	t.Run("template with nested braces is recognized", func(t *testing.T) {
		// This template has nested braces in a map literal
		// Before the fix, this would not be recognized as a template
		template := "{{data.map(d, {'id': d.id, 'result': d.value})}}"

		// Extract the template
		matches := extractTemplateExpressions(template)

		// Should find exactly one template expression
		require.Len(t, matches, 1, "should extract the template")
		assert.Equal(t, template, matches[0].full, "should match the full template")
		assert.Equal(t, "data.map(d, {'id': d.id, 'result': d.value})", matches[0].expr, "should extract the expression correctly")
	})

	t.Run("ternary with nested object literals is recognized", func(t *testing.T) {
		// Before the fix, the parser would stop at the first '}' and fail to extract correctly
		template := "{{ use_a ? {'type': 'a', 'value': 1} : {'type': 'b', 'value': 2} }}"

		matches := extractTemplateExpressions(template)

		require.Len(t, matches, 1)
		assert.Equal(t, "use_a ? {'type': 'a', 'value': 1} : {'type': 'b', 'value': 2}", matches[0].expr)
	})

	t.Run("nested braces with map literals", func(t *testing.T) {
		// This tests template EXTRACTION with nested braces.
		// Note: For valid CEL, map keys must be quoted: {"tool_call_id": r.tool_call_id}
		// This test only verifies the balanced brace parser works, not CEL compilation.
		template := `{{ results.map(r, r.filtered ? { "tool_call_id": r.tool_call_id, "name": r.name } : r) }}`

		matches := extractTemplateExpressions(template)

		require.Len(t, matches, 1, "should extract the template")
		assert.Contains(t, matches[0].expr, `"tool_call_id": r.tool_call_id`, "should preserve the object literal syntax")
		assert.Equal(t, `results.map(r, r.filtered ? { "tool_call_id": r.tool_call_id, "name": r.name } : r)`, matches[0].expr)
	})
}
