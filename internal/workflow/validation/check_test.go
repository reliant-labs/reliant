// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

const checkBase = `
name: checked
check: %s
entry: [report]
nodes:
  - id: report
    type: compute
    outputs:
      check: "{{ {'title': 'ok', 'summary': 'all good'} }}"
`

func checkSrc(check string) string { return strings.Replace(checkBase, "%s", check, 1) }

func fieldErrors(errs []*Error, field string) []*Error {
	var out []*Error
	for _, e := range errs {
		if e.Field == field || strings.HasPrefix(e.Field, field+".") || strings.Contains(strings.Join(e.Path, "."), field) {
			out = append(out, e)
		}
	}
	return out
}

func TestCheck_ValidDeclarations(t *testing.T) {
	for _, check := range []string{
		"reliant/ci",
		"{name: reliant/ci, timeout: 2h}",
		"{name: lint, repository: '{{ trigger.payload.attributes.repository }}', head_sha: '{{ trigger.payload.data.check_suite.head_sha }}'}",
		"{name: lint, repository: octo/hello, head_sha: 6dcb09b5b57875f334f61aebed695e2e4193db5e}",
		"{name: review, output: '{{ nodes.report.check }}'}",
		"{name: review, output: \"{{ {'title': 'Run ' + run.status, 'summary': nodes.report.check.summary} }}\"}",
	} {
		t.Run(check, func(t *testing.T) {
			errs := computeFindings(t, checkSrc(check))
			assert.Empty(t, fieldErrors(errs, "check"), "%v", messages(errs))
		})
	}
}

func TestCheck_InvalidDeclarations(t *testing.T) {
	for check, field := range map[string]string{
		"{name: '{{ inputs.name }}'}":                              "check.name",
		"{name: ci, timeout: 10s}":                                 "check.timeout",
		"{name: ci, timeout: 4d}":                                  "check.timeout",
		"{name: ci, head_sha: main}":                               "check.head_sha",
		"{name: ci, head_sha: '{{ trigger.payload. }}'}":           "check.head_sha",
		"{name: ci, head_sha: '{{ nodes.report.check }}'}":         "check.head_sha",
		"{name: ci, repository: reliant}":                          "check.repository",
		"{name: ci, output: 'all good'}":                           "check.output",
		"{name: ci, output: '{{ nodes.missing.check }}'}":          "check",
		"{name: ci, output: '{{ failure.error }}'}":                "check",
		"{name: ci, repository: '{{ inputs.repo }}', timeout: 1h}": "check.repository",
	} {
		t.Run(check, func(t *testing.T) {
			errs := computeFindings(t, checkSrc(check))
			assert.NotEmpty(t, fieldErrors(errs, field), "want a finding on %s: %v", field, messages(errs))
		})
	}
}

// A proto or builder caller meets the same rules as the YAML parser.
func TestCheck_ProtoCallersAreChecked(t *testing.T) {
	result := StaticAnalysis(&reliantv1.Workflow{Name: "checked", Check: &reliantv1.WorkflowCheck{}}, nil)
	require.NotEmpty(t, fieldErrors(result.Errors(), "check.name"))
}
