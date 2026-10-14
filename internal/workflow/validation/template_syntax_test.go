// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A template that cannot be read is a validation error with a field path and a
// position. It never validates clean and falls back to literal text at run
// time (the Autopilot prototype shipped two such workflows).

func syntaxErrors(t *testing.T, src string) []*Error {
	t.Helper()
	errs, _ := gapFindings(t, src)
	var out []*Error
	for _, e := range errs {
		if e.Category == CategoryCELSyntax {
			out = append(out, e)
		}
	}
	return out
}

func yamlWithPrompt(field string) string {
	return `
name: syn
inputs:
  topic: {type: string, default: "x"}
entry: [a]
nodes:
  - id: a
    type: call_llm
    args:
` + field + `
`
}

func TestTemplateSyntax_UnclosedBraceIsAnErrorWithAPosition(t *testing.T) {
	errs := syntaxErrors(t, yamlWithPrompt(`      system_prompt: "hello {{ inputs.topic"`))
	require.Len(t, errs, 1, "%v", messages(errs))
	e := errs[0]
	assert.Contains(t, e.Message, `no closing "}}"`)
	assert.Contains(t, e.Message, "line 1, column 7")
	assertErrorPath(t, []string{"nodes", "[0]", "system_prompt"}, e.Path)
	assert.Equal(t, "syn", e.Path[0])
}

// The unbalanced-brace case from the prototype: braces in a STRING are data and
// the template is evaluated, not left as literal text.
func TestTemplateSyntax_BraceInStringLiteralIsValid(t *testing.T) {
	for _, field := range []string{
		`      system_prompt: "{{ inputs.topic.startsWith('{') }}"`,
		`      system_prompt: "{{ inputs.topic.contains('}}') ? 'a' : 'b' }}"`,
		`      system_prompt: "x {{ inputs.topic + '}' }} y"`,
	} {
		assert.Empty(t, syntaxErrors(t, yamlWithPrompt(field)), field)
	}
}

// The other prototype case: a newline inside {{ }} in a folded scalar.
func TestTemplateSyntax_NewlineInsideBracesInFoldedScalarIsValid(t *testing.T) {
	errs, _ := gapFindings(t, yamlWithPrompt(`      system_prompt: >-
        a {{ inputs.topic
          + 'z' }} b`))
	for _, e := range errs {
		assert.NotEqual(t, CategoryCELSyntax, e.Category, e.Message)
	}
}

// A real syntax error inside a well-formed hole is reported at its position in
// the YAML value, including through a folded scalar.
func TestTemplateSyntax_ExpressionThatDoesNotParse(t *testing.T) {
	errs := syntaxErrors(t, yamlWithPrompt(`      system_prompt: "q {{ inputs.topic ? 1 }} q"`))
	require.Len(t, errs, 1, "%v", messages(errs))
	assert.Contains(t, errs[0].Message, "CEL syntax error at line 1, column")
	assertErrorPath(t, []string{"nodes", "[0]", "system_prompt"}, errs[0].Path)
}

func TestTemplateSyntax_ErrorOnALaterLineReportsThatLine(t *testing.T) {
	errs := syntaxErrors(t, yamlWithPrompt(`      system_prompt: |
        first line
        second {{ inputs.topic
           + }} end`))
	require.Len(t, errs, 1, "%v", messages(errs))
	assert.Contains(t, errs[0].Message, "line 3, column 5", errs[0].Message)
}

func TestTemplateSyntax_NullCoalesceOperatorIsReportedOnlyByTheSyntaxPass(t *testing.T) {
	errs, _ := gapFindings(t, yamlWithPrompt(`      system_prompt: "{{ inputs.topic ?? 'd' }}"`))
	require.NotEmpty(t, errs)
	for _, e := range errs {
		assert.Equal(t, CategoryCELSyntax, e.Category, "the compile pass must not re-report a syntax error: %s", e.Message)
		assert.Contains(t, e.Message, "CEL syntax error at line 1, column")
	}
}

func TestTemplateSyntax_EmptyExpression(t *testing.T) {
	errs := syntaxErrors(t, yamlWithPrompt(`      system_prompt: "a {{   }} b"`))
	require.Len(t, errs, 1, "%v", messages(errs))
	assert.Contains(t, errs[0].Message, "empty expression")
}

// Every kind of template-bearing field is covered, not only node args.
func TestTemplateSyntax_CoversEveryTemplateSite(t *testing.T) {
	src := `
name: sites
inputs:
  topic: {type: string, default: "x"}
entry: [a]
outputs:
  out: "{{ nodes.a.response_text"
nodes:
  - id: a
    type: call_llm
    args:
      system_prompt: "ok"
    save_message:
      role: assistant
      content: "sm {{ output.response_text"
  - id: b
    type: workflow
    thread:
      mode: new
      inject:
        role: user
        content: "inj {{ inputs.topic"
    args:
      v: "arg {{ inputs.topic"
    inline:
      entry: [c]
      nodes:
        - id: c
          type: run
          args:
            command: "echo {{ inputs.topic"
  - id: d
    type: loop
    while: "iter.iteration < 1 &&"
    items: "{{ [1, 2"
    inline:
      entry: [e]
      nodes:
        - id: e
          type: save_message
          args:
            role: assistant
            content: "e {{ iter.item"
edges:
  - from: a
    default: b
  - from: b
    default: d
`
	errs := syntaxErrors(t, src)
	got := map[string]bool{}
	for _, e := range errs {
		got[strings.Join(e.Path, ".")] = true
	}
	for _, want := range []string{
		"sites.outputs.out",
		"sites.nodes.[0](a).save_message.content",
		"sites.nodes.[1](b).thread.inject.content",
		"sites.nodes.[1](b).args.v",
		"sites.nodes.[1](b).inline.nodes.[0](c).command",
		"sites.nodes.[2](d).while",
		"sites.nodes.[2](d).items",
		"sites.nodes.[2](d).inline.nodes.[0](e).content",
	} {
		assert.True(t, got[want], "missing a syntax error at %s; got %v", want, keys(got))
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestTemplateSyntax_RawCELFieldWithBraceInStringIsNotATemplate(t *testing.T) {
	src := `
name: rawbrace
entry: [a]
nodes:
  - id: a
    type: run
    args:
      command: "echo hi"
  - id: b
    type: save_message
    condition: "nodes.a.stdout.startsWith('{') && nodes.a.stdout.contains('{{')"
    args:
      role: assistant
      content: "ok"
edges:
  - from: a
    default: b
`
	for _, e := range syntaxErrors(t, src) {
		t.Errorf("unexpected syntax error: %s %v", e.Message, e.Path)
	}
	errs, _ := gapFindings(t, src)
	for _, e := range errs {
		assert.NotContains(t, e.Message, "remove the {{ }}", "a {{ inside a string literal is data")
	}
}
