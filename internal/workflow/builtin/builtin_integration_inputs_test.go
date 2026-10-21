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
// them: every builtin that declares none validates to the same findings
// whichever resolver is supplied. The Autopilot pack is the builtins that do
// declare them (github.repository, github.user, github.label); those are held
// to a stricter bar, no findings at all, by TestAutopilotPackValidatesCleanWithTypedInputs.
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
			for _, in := range wf.GetInputs() {
				if model.IsIntegrationInput(in) {
					t.Skip("declares integration inputs; see TestAutopilotPackValidatesCleanWithTypedInputs")
				}
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

// The Autopilot pack declares typed inputs and scopes its triggers with
// `match: {repository: {input: repos}}`. Resolved against the real embedded
// catalog (ValueTypes nil), each of these validates with no error and no
// warning, which includes the lint that flags a filter scoping by hand.
func TestAutopilotPackValidatesCleanWithTypedInputs(t *testing.T) {
	for _, name := range []string{
		"autopilot-pr-review.yaml", "autopilot-review-responder.yaml",
		"autopilot-issue-worker.yaml", "autopilot-alert-triage.yaml",
	} {
		t.Run(name, func(t *testing.T) {
			wf, err := parseBuiltin(name)
			require.NoError(t, err)
			res := validation.StaticAnalysisWithOptions(wf, &validation.ValidationOptions{WorkflowLoader: builtinLoader})
			for _, f := range append(res.Errors(), res.Warnings()...) {
				assert.Failf(t, "finding", "%s: %v %v", name, f.Severity, f.Error())
			}
		})
	}
}

// Every pack workflow that scopes by repository declares repos as a required,
// non-empty github.repository list, and no trigger filter still compares the
// event's repository by hand.
func TestAutopilotRepositoryScopeIsDeclaredNotFiltered(t *testing.T) {
	for _, name := range []string{
		"autopilot-pr-review.yaml", "autopilot-review-responder.yaml", "autopilot-issue-worker.yaml",
	} {
		t.Run(name, func(t *testing.T) {
			wf, err := parseBuiltin(name)
			require.NoError(t, err)
			repos := wf.GetInputs()["repos"]
			require.NotNil(t, repos)
			assert.Equal(t, "github.repository", repos.GetType())
			assert.True(t, model.IsInputRequired(repos))
			assert.True(t, repos.GetIntegrationInput().GetMulti())
			assert.EqualValues(t, 1, repos.GetIntegrationInput().GetMinItems())

			require.NotEmpty(t, wf.GetTriggers())
			for _, wt := range wf.GetTriggers() {
				assert.Equal(t, map[string]string{"repository": "repos"}, wt.GetIntegration().GetMatchInput(), "trigger %s", wt.GetName())
				assert.NotContains(t, wt.GetFilter(), "attributes.repository", "trigger %s", wt.GetName())
			}
		})
	}
}
