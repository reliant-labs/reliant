// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	"github.com/reliant-labs/reliant/internal/workflow/validation/valuetypestest"
)

// Typed integration inputs change nothing for a workflow that does not use
// them: every builtin validates to the same findings whichever resolver is
// supplied, none declares an integration input, and none marks an input
// required (which could now conflict with a default).
func TestBuiltinValidationIsIndependentOfIntegrationInputTypes(t *testing.T) {
	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	require.NoError(t, err)

	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			wf, err := parseBuiltin(name)
			require.NoError(t, err)
			for inputName, in := range wf.GetInputs() {
				assert.False(t, model.IsIntegrationInput(in), "builtin input %s unexpectedly uses an integration type", inputName)
			}

			findings := func(opts *validation.ValidationOptions) []string {
				opts.WorkflowLoader = builtinLoader
				res := validation.StaticAnalysisWithOptions(wf, opts)
				var out []string
				for _, f := range append(res.Errors(), res.Warnings()...) {
					out = append(out, fmt.Sprintf("%v %v", f.Severity, f.Error()))
				}
				sort.Strings(out)
				return out
			}
			assert.Equal(t, findings(&validation.ValidationOptions{}),
				findings(&validation.ValidationOptions{ValueTypes: valuetypestest.GitHub()}),
				"%s validates differently once integrations declare input types", name)
		})
		checked++
	}
	require.NotZero(t, checked, "no builtin workflows found")
}
