// Copyright (c) 2025 Reliant Labs
package runner

import (
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/celeval"
	"github.com/reliant-labs/reliant/internal/workflow/scenario"
)

// triggerInputs are the inputs a scenario's trigger firing starts the run
// with: the declared trigger's filter must pass the payload, and its inputs
// mapping is evaluated by the code a real firing uses (celeval.DryRunTrigger
// over triggerspec), so a scenario that fires through a trigger tests the
// mapping as well as the graph.
func triggerInputs(wf *reliantv1.Workflow, t *scenario.TriggerFiring) (map[string]any, error) {
	var wt *reliantv1.WorkflowTrigger
	for _, candidate := range wf.GetTriggers() {
		if candidate.GetName() == t.Name {
			wt = candidate
			break
		}
	}
	if wt == nil {
		return nil, fmt.Errorf("trigger %q: the workflow declares no such trigger", t.Name)
	}
	run := celeval.DryRunTrigger(wt, wf.GetInputs(), t.Payload, t.Sender, &celeval.ActivationSample{Params: t.ActivationParams})
	if errs := run.Errors(); len(errs) > 0 {
		return nil, fmt.Errorf("firing trigger %q: %s", t.Name, strings.Join(errs, "; "))
	}
	if run.Matched == nil || !*run.Matched {
		return nil, fmt.Errorf("trigger %q: its filter rejects this payload, so the event would start no run", t.Name)
	}
	return run.Inputs, nil
}
