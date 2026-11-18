// Copyright (c) 2025 Reliant Labs
package validation

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
	"github.com/reliant-labs/reliant/internal/workflowcheck"
)

// validateCheck checks the workflow's `check:` block (the GitHub check run
// the engine owns). The YAML parser enforces its shape; this covers what each
// field may say, for proto and builder callers too. check.output's CEL is
// validated with the graph's (validateCheckOutput).
func validateCheck(wf *reliantv1.Workflow, result *Result) {
	check := wf.GetCheck()
	if check == nil {
		return
	}
	if err := workflowcheck.ValidateName(check.GetName()); err != nil {
		result.AddError(CategoryStructure, nil, "check.name", err.Error())
	}
	for _, field := range []struct {
		path, value, suggestion string
		literal                 func(string) error
	}{
		{"check.repository", check.GetRepository(), "a template over `trigger`, e.g. {{ trigger.payload.attributes.repository }} (the default), or owner/name", workflowcheck.ValidateRepository},
		{"check.head_sha", check.GetHeadSha(), "a template over `trigger`, e.g. {{ trigger.payload.data.check_suite.head_sha }}; omit it for the event's head commit", workflowcheck.ValidateHeadSHA},
	} {
		if err := field.literal(field.value); err != nil {
			result.AddErrorWithSuggestion(CategoryStructure, nil, field.path, err.Error(), field.suggestion)
			continue
		}
		if err := triggerspec.CompileCheckTemplate(field.value); err != nil {
			result.AddErrorWithSuggestion(CategoryStructure, nil, field.path, err.Error(), field.suggestion)
		}
	}
	if _, err := workflowcheck.ParseTimeout(check.GetTimeout()); err != nil {
		result.AddErrorWithSuggestion(CategoryStructure, nil, "check.timeout", err.Error(), "a duration from 1m to 72h, e.g. 2h; omit it for 6h")
	}
	if output := check.GetOutput(); output != "" && !containsTemplate(output) {
		result.AddErrorWithSuggestion(CategoryStructure, nil, "check.output", "output is plain text, but it must evaluate to an object {title, summary, text?, annotations?}",
			"name the node output that builds it, e.g. {{ nodes.report.check }}")
	}
}

// validateCheckOutput validates check.output's CEL. It is evaluated when the
// run ends, in the scope of a declared output plus `run` (run.status), as a
// finally handler sees it.
func (s *workflowScope) validateCheckOutput(result *Result) {
	raw := s.wf.GetCheck().GetOutput()
	if raw == "" || !containsTemplate(raw) {
		return
	}
	env, typeCtx, err := s.env(siteNodeResolution, false, nil, nil)
	if err != nil {
		s.reportEnvError(err, result)
		return
	}
	expr := unwrapTemplate(raw)
	path := append(append([]string{}, s.basePath...), "check", "output")
	validateInputPropertyAccess(raw, path, typeCtx, result)
	validateCELExpressionWithCompilationAndSchema(raw, rewriteNodesAccess(expr, s.nodeIDs), path, env, s.schema, typeCtx, result)
}

// validateCheckCEL validates the root workflow's check.output in a scope of
// its own: the graph's nodes and types, with `run` declared.
func validateCheckCEL(wf *reliantv1.Workflow, typeCtx *WorkflowTypeContext, result *Result) {
	if wf.GetCheck().GetOutput() == "" {
		return
	}
	scope := newWorkflowScope(wf, []string{wf.GetName()}, typeCtx, nil)
	scope.root = true
	scope.run = true
	scope.validateCheckOutput(result)
}
