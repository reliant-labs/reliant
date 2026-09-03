// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests lock in HARD input validation after removing the runtime-injected
// input weakening. Previously the validator pre-seeded 7 engine-injected names
// (including preview_url_template) as untyped placeholder fields, so any
// `inputs.<injected>` reference passed compile-time validation — and a separate
// has()-guard subsystem existed to paper over the fact several were only
// conditionally injected. That whole layer is gone. Now:
//
//   - Only chat_id / workflow_id / unique_activity_id are declared, as TYPED
//     reserved system keywords (always injected by the runtime), so referencing
//     them validates cleanly.
//   - Every other undeclared inputs.<name> reference is a hard validation ERROR
//     again — including preview_url_template and the conditional/internal engine
//     values (session_daemon_id, project_path, spawned_by), which are deliberately
//     NOT CEL-referenceable.
//
// buildInjectWorkflow wraps a single CEL reference in a thread.inject.content so it
// flows through ValidateCELWithCompilation exactly like a real handoff node would.
func buildInjectWorkflow(t *testing.T, name, declaredInputs, expr string) string {
	t.Helper()
	inputsBlock := ""
	if declaredInputs != "" {
		inputsBlock = "inputs:\n" + declaredInputs
	}
	return `
name: ` + name + `
entry: [step1]
` + inputsBlock + `
nodes:
  - id: step1
    type: workflow
    ref: builtin://agent
    thread:
      inject:
        role: user
        content: "` + expr + `"
`
}

// validateInject parses the wrapped workflow and runs CEL compilation validation,
// returning the resulting errors.
func validateInject(t *testing.T, yaml string) []*Error {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(yaml))
	require.NoError(t, err)
	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)
	return result.Errors()
}

// TestReservedInputs_PreviewURLTemplateNowErrors is the direct regression guard:
// the exact expression the removed weakening used to accept — inputs.preview_url_template,
// with no such declared input — must now be a hard validation error.
func TestReservedInputs_PreviewURLTemplateNowErrors(t *testing.T) {
	t.Parallel()
	yaml := buildInjectWorkflow(t, "preview-url-undeclared", "", "{{ inputs.preview_url_template }}")
	errs := validateInject(t, yaml)
	require.NotEmpty(t, errs,
		"inputs.preview_url_template is no longer a runtime-injected keyword; an undeclared reference must be a hard error")
	assertMentionsInput(t, errs, "preview_url_template")
}

// TestReservedInputs_ArbitraryUndeclaredErrors proves the validator is not merely
// blocking one name — any undeclared input reference fails.
func TestReservedInputs_ArbitraryUndeclaredErrors(t *testing.T) {
	t.Parallel()
	yaml := buildInjectWorkflow(t, "arbitrary-undeclared", "", "{{ inputs.totally_made_up_thing }}")
	errs := validateInject(t, yaml)
	require.NotEmpty(t, errs, "an arbitrary undeclared input reference must be a hard error")
	assertMentionsInput(t, errs, "totally_made_up_thing")
}

// TestReservedInputs_ConditionalEngineValuesNotReferenceable proves I did NOT
// re-add the conditionally-injected / internal engine values as CEL keywords.
// session_daemon_id, project_path, and spawned_by may be absent at runtime, so a
// template reference would be a foot-gun — it must fail static validation.
func TestReservedInputs_ConditionalEngineValuesNotReferenceable(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"session_daemon_id", "project_path", "spawned_by"} {
		t.Run(name, func(t *testing.T) {
			yaml := buildInjectWorkflow(t, "conditional-"+name, "", "{{ inputs."+name+" }}")
			errs := validateInject(t, yaml)
			require.NotEmpty(t, errs,
				"inputs.%s is a conditional/internal engine value and must NOT be CEL-referenceable", name)
			assertMentionsInput(t, errs, name)
		})
	}
}

// TestReservedInputs_SystemKeywordsValidateCleanly proves the always-injected trio
// is declared as typed reserved keywords, so referencing them is allowed.
func TestReservedInputs_SystemKeywordsValidateCleanly(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"chat_id", "workflow_id", "unique_activity_id"} {
		t.Run(name, func(t *testing.T) {
			yaml := buildInjectWorkflow(t, "reserved-"+name, "", "{{ inputs."+name+" }}")
			errs := validateInject(t, yaml)
			for _, e := range errs {
				assert.NotContains(t, e.Message, name,
					"reserved system keyword inputs.%s should validate without error, got: %s", name, e.Message)
			}
		})
	}
}

// TestReservedInputs_TypoOfSystemKeywordErrors proves the reserved keywords are a
// closed, typed set — not a wildcard that swallows near-misses. A typo of a
// reserved name must still fail (and ideally suggest the correct name).
func TestReservedInputs_TypoOfSystemKeywordErrors(t *testing.T) {
	t.Parallel()
	yaml := buildInjectWorkflow(t, "reserved-typo", "", "{{ inputs.chat_idd }}")
	errs := validateInject(t, yaml)
	require.NotEmpty(t, errs, "a typo of a reserved keyword (chat_idd) must still be a hard error")
	assertMentionsInput(t, errs, "chat_idd")
}

// TestReservedInputs_DeclaredInputStillValidates is the sanity floor: a properly
// declared input is referenceable, so the hardening didn't over-rotate into
// rejecting legitimate inputs.
func TestReservedInputs_DeclaredInputStillValidates(t *testing.T) {
	t.Parallel()
	declared := "  topic:\n    type: string\n"
	yaml := buildInjectWorkflow(t, "declared-ok", declared, "{{ inputs.topic }}")
	errs := validateInject(t, yaml)
	for _, e := range errs {
		assert.NotContains(t, e.Message, "topic",
			"a declared string input should validate cleanly, got: %s", e.Message)
	}
}

// assertMentionsInput asserts that at least one error references the given input
// name (via an undeclared-reference / undefined-field / unknown-input style
// message), without pinning the exact category — the point is that validation
// FAILED and pointed at this name.
func assertMentionsInput(t *testing.T, errs []*Error, name string) {
	t.Helper()
	for _, e := range errs {
		if strings.Contains(e.Message, name) {
			return
		}
	}
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, e.Message)
	}
	t.Fatalf("expected a validation error mentioning %q; got: %s", name, strings.Join(msgs, " | "))
}
