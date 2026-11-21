// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func credentialFindings(errs []*Error) []*Error {
	var out []*Error
	for _, e := range errs {
		if e.Category == CategoryCredentials {
			out = append(out, e)
		}
	}
	return out
}

func runNodeWithCredentials(value string) *reliantv1.Workflow {
	return &reliantv1.Workflow{
		Name:  "creds",
		Entry: []string{"build"},
		Nodes: []*reliantv1.Node{{
			Id:   "build",
			Type: "run",
			Args: &reliantv1.Node_Run{Run: &reliantv1.RunArgs{
				Command:     &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "make"}},
				Credentials: value,
			}},
		}},
	}
}

// A credentials: value the daemon refuses can never run, so an author is told
// at validate time (an error); a workflow already saved with one still starts
// (a warning), failing only the step, as before.
func TestCredentialsTypoIsAnAuthoringError(t *testing.T) {
	wf := runNodeWithCredentials("nope")
	authoring := credentialFindings(StaticAnalysisWithOptions(wf, &ValidationOptions{Authoring: true}).Errors())
	if len(authoring) != 1 || !strings.Contains(authoring[0].Message, `"nope"`) {
		t.Fatalf("authoring: want one credentials error naming the value, got %+v", authoring)
	}

	running := StaticAnalysisWithOptions(wf, &ValidationOptions{})
	if errs := credentialFindings(running.Errors()); len(errs) != 0 {
		t.Fatalf("a run must still start: %+v", errs)
	}
	if warns := credentialFindings(running.Warnings()); len(warns) != 1 {
		t.Fatalf("want one credentials warning at run time, got %+v", warns)
	}
}

func TestCredentialsAcceptedValuesAndExpressions(t *testing.T) {
	for _, v := range []string{"none", "inherit", "NONE", "{{ inputs.creds }}"} {
		res := StaticAnalysisWithOptions(runNodeWithCredentials(v), &ValidationOptions{Authoring: true})
		if errs := credentialFindings(res.Errors()); len(errs) != 0 {
			t.Errorf("%q: %+v", v, errs)
		}
	}
}

func TestShellCredentialsBindingTypoIsReported(t *testing.T) {
	wf := workflowWithToolBindings(t, map[string]map[string]any{"shell": {"credentials": "nope"}})
	errs := credentialFindings(StaticAnalysisWithOptions(wf, &ValidationOptions{Authoring: true}).Errors())
	if len(errs) != 1 {
		t.Fatalf("want one credentials error on the shell binding, got %+v", errs)
	}
	ok := workflowWithToolBindings(t, map[string]map[string]any{"shell": {"credentials": "none"}})
	if errs := credentialFindings(StaticAnalysisWithOptions(ok, &ValidationOptions{Authoring: true}).Errors()); len(errs) != 0 {
		t.Fatalf("credentials: none is valid: %+v", errs)
	}
}
