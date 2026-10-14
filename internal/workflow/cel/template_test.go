package wfcel

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScanTemplate_Holes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		in    string
		exprs []string
	}{
		{"no template", "just a string", nil},
		{"empty", "", nil},
		{"single brace is text", "a { b } c", nil},
		{"simple", "{{nodes.foo.bar}}", []string{"nodes.foo.bar"}},
		{"padded", "{{   inputs.x \t}}", []string{"inputs.x"}},
		{"surrounding text", "prefix {{expr}} suffix", []string{"expr"}},
		{"several", "{{a}} and {{ {b: c} }}", []string{"a", "{b: c}"}},
		{"map literal", "{{[{a: 1}]}}", []string{"[{a: 1}]"}},
		{"ternary with maps", "{{x ? {a: 1} : {b: 2}}}", []string{"x ? {a: 1} : {b: 2}"}},
		{"map ends the expression", "{{ {'a': 1}}}", []string{"{'a': 1}"}},
		{"multiline", "{{[\n  {a: 1}\n]}}", []string{"[\n  {a: 1}\n]"}},

		// A brace inside a string literal is data.
		{"open brace in single quotes", "{{ x.startsWith('{') }}", []string{"x.startsWith('{')"}},
		{"open brace in double quotes", `{{ x.startsWith("{") }}`, []string{`x.startsWith("{")`}},
		{"close brace in a string", "{{ x + '}' }}", []string{"x + '}'"}},
		{"closing pair inside a string", "{{ x.contains('}}') }} tail", []string{"x.contains('}}')"}},
		{"opening pair inside a string", "{{ x + '{{' }}", []string{"x + '{{'"}},
		{"both quote kinds", `{{ x + "'" + '"' }}`, []string{`x + "'" + '"'`}},
		{"unbalanced brace beside a map", "{{ [{'a': '{'}] }}", []string{"[{'a': '{'}]"}},
		{"backtick identifier", "{{ m.`a}b` }}", []string{"m.`a}b`"}},

		// Escapes.
		{"escaped quote", `{{ x + 'it\'s}' }}`, []string{`x + 'it\'s}'`}},
		{"escaped backslash then quote closes", `{{ x + '\\' }} {{ y }}`, []string{`x + '\\'`, "y"}},
		{"raw string ignores backslash", `{{ x + r'\' }}`, []string{`x + r'\'`}},
		{"raw uppercase", `{{ x + R"\" }}`, []string{`x + R"\"`}},
		{"identifier ending in r is not raw", `{{ bar'x' }}`, []string{`bar'x'`}},

		// Newlines are whitespace, in a folded or literal scalar alike.
		{"newline inside braces", "{{ inputs.x\n  + 'y' }}", []string{"inputs.x\n  + 'y'"}},
		{"newline after open", "{{\n  inputs.x\n}}\n", []string{"inputs.x"}},
		{"folded join", "{{ [\n 'a',\n 'b'\n].join('-') }}", []string{"[\n 'a',\n 'b'\n].join('-')"}},

		// Triple quotes may span lines and hold quotes.
		{"triple quoted", "{{ '''a\n'}}'b''' }}", []string{"'''a\n'}}'b'''"}},
		{"triple double", "{{ \"\"\"x \"}} y\"\"\" }}", []string{"\"\"\"x \"}} y\"\"\""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ScanTemplate(tc.in)
			require.NoError(t, err)
			var exprs []string
			for _, m := range got {
				exprs = append(exprs, m.Expr)
			}
			assert.Equal(t, tc.exprs, exprs)
		})
	}
}

func TestScanTemplate_Offsets(t *testing.T) {
	t.Parallel()
	in := "ab {{ x }} cd {{y}}"
	got, err := ScanTemplate(in)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, TemplateMatch{Full: "{{ x }}", Expr: "x", Start: 3, End: 10, ExprStart: 6}, got[0])
	assert.Equal(t, "{{ x }}", in[got[0].Start:got[0].End])
	assert.Equal(t, "x", in[got[0].ExprStart:got[0].ExprStart+1])
	assert.Equal(t, TemplateMatch{Full: "{{y}}", Expr: "y", Start: 14, End: 19, ExprStart: 16}, got[1])
}

func TestScanTemplate_Malformed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		in           string
		line, column int
		contains     string
	}{
		{"never closed", "hello {{ incomplete", 1, 7, `no closing "}}"`},
		{"closed by a single brace", "{{ x }", 1, 1, `no closing "}}"`},
		{"string eats the close", "{{ x.startsWith('}}) }}", 1, 17, "unterminated string literal"},
		{"string cut off by a newline", "{{ 'abc\n }}", 1, 4, "not closed before the end of the line"},
		{"unterminated string at end", "{{ 'abc", 1, 4, "unterminated string literal"},
		{"unterminated triple", "{{ '''abc }}", 1, 4, "unterminated string literal"},
		{"unterminated backtick", "{{ a.`b }}", 1, 6, "unterminated `quoted identifier`"},
		{"second hole is bad, position is on line 3", "ok {{ a }}\nmore\n   {{ b ", 3, 4, `no closing "}}"`},
		{"unbalanced map brace hides the close", "{{ [{'a': 1] }}", 1, 1, `no closing "}}"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ScanTemplate(tc.in)
			require.Error(t, err)
			var synErr *TemplateSyntaxError
			require.True(t, errors.As(err, &synErr), "want *TemplateSyntaxError, got %T", err)
			assert.Equal(t, tc.line, synErr.Line, err.Error())
			assert.Equal(t, tc.column, synErr.Column, err.Error())
			assert.Contains(t, synErr.Message, tc.contains)
		})
	}
}

func TestRawCELTemplateDelimiter(t *testing.T) {
	t.Parallel()
	_, found := RawCELTemplateDelimiter("nodes.x.stdout.startsWith('{{')")
	assert.False(t, found, "{{ inside a string is data")
	off, found := RawCELTemplateDelimiter("{{ a == b }}")
	assert.True(t, found)
	assert.Equal(t, 0, off)
	off, found = RawCELTemplateDelimiter("a == '}' && {{ b }}")
	assert.True(t, found)
	assert.Equal(t, 12, off)
	_, found = RawCELTemplateDelimiter("a == 'unterminated")
	assert.False(t, found)
}

func TestEvaluateTemplate_MalformedIsAnErrorNotLiteralText(t *testing.T) {
	t.Parallel()
	ctx := &NodeResolutionContext{Inputs: map[string]interface{}{"x": "{abc"}}
	for _, in := range []string{"{{ inputs.x ", "pre {{ inputs.x.startsWith('}}) }} post", "{{ 'open }}"} {
		v, err := EvaluateTemplate(in, ctx)
		require.Error(t, err, "%q", in)
		assert.Nil(t, v)
		var synErr *TemplateSyntaxError
		assert.True(t, errors.As(err, &synErr), "%q: %v", in, err)
	}
}

func TestEvaluateTemplate_BracesInStringLiteralsEvaluate(t *testing.T) {
	t.Parallel()
	ctx := &NodeResolutionContext{Inputs: map[string]interface{}{"x": "{abc", "y": "a}}b"}}

	v, err := EvaluateTemplate("{{ inputs.x.startsWith('{') }}", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, v, "was returned as the literal template source")

	v, err = EvaluateTemplate("x={{ inputs.x.startsWith('{') }}", ctx)
	require.NoError(t, err)
	assert.Equal(t, "x=true", v)

	v, err = EvaluateTemplate("{{ inputs.y.contains('}}') }}", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, v)

	v, err = EvaluateTemplate("{{ inputs.x + '}' }}", ctx)
	require.NoError(t, err)
	assert.Equal(t, "{abc}", v)
}

func TestEvaluateTemplate_NewlinesInsideBracesEvaluate(t *testing.T) {
	t.Parallel()
	ctx := &NodeResolutionContext{Inputs: map[string]interface{}{"x": "v"}}

	v, err := EvaluateTemplate("{{ inputs.x\n  + '!' }}", ctx)
	require.NoError(t, err)
	assert.Equal(t, "v!", v)

	v, err = EvaluateTemplate("a {{ [\n inputs.x,\n 'z'\n].join('-') }} b\n", ctx)
	require.NoError(t, err)
	assert.Equal(t, "a v-z b", v)
}

func TestTemplateExpressions_MalformedIsAnError(t *testing.T) {
	t.Parallel()
	exprs, err := TemplateExpressions("{{ a }} {{ b", false)
	require.Error(t, err)
	assert.Nil(t, exprs)

	exprs, err = TemplateExpressions("plain", true)
	require.NoError(t, err)
	assert.Equal(t, []string{"plain"}, exprs)

	exprs, err = TemplateExpressions("x.startsWith('{') && y", true)
	require.NoError(t, err)
	assert.Equal(t, []string{"x.startsWith('{') && y"}, exprs, "raw CEL with a brace in a string has no {{")
}
