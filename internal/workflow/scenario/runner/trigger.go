// Copyright (c) 2025 Reliant Labs
package runner

import (
	"encoding/json"
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/workflow/celeval"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	"google.golang.org/protobuf/encoding/protojson"
)

// triggerInputs are the inputs a scenario's trigger firing starts the run
// with: the declared trigger's filter must pass the payload, and its inputs
// mapping is evaluated by the code a real firing uses (celeval.DryRunTrigger
// over triggerspec), so a scenario that fires through a trigger tests the
// mapping as well as the graph.
//
// The TriggerInfo is the event itself, which the run carries as `trigger`
// (trigger.payload, trigger.sender, trigger.kind) the way a fired run does, so
// a node that reads trigger.payload is scenario-testable. It has no event id:
// that is what would make the run a capacity-slot holder and a check
// reporter, which only a real launch is.
//
// The warnings name payload fields the run read that only the generated sample
// set (see sampleFilledWarnings).
func triggerInputs(wf *reliantv1.Workflow, t *scenario.TriggerFiring) (map[string]any, *runtime.TriggerInfo, []string, error) {
	var wt *reliantv1.WorkflowTrigger
	for _, candidate := range wf.GetTriggers() {
		if candidate.GetName() == t.Name {
			wt = candidate
			break
		}
	}
	if wt == nil {
		return nil, nil, nil, fmt.Errorf("trigger %q: the workflow declares no such trigger", t.Name)
	}
	run := celeval.DryRunTrigger(wt, wf.GetInputs(), t.Payload, t.Sender, &celeval.ActivationSample{Params: t.ActivationParams})
	if errs := run.Errors(); len(errs) > 0 {
		return nil, nil, nil, fmt.Errorf("firing trigger %q: %s", t.Name, strings.Join(errs, "; "))
	}
	if run.Matched == nil || !*run.Matched {
		return nil, nil, nil, fmt.Errorf("trigger %q: its filter rejects this payload, so the event would start no run", t.Name)
	}
	info := &runtime.TriggerInfo{Kind: run.Kind, TriggerID: "scenario-trigger", OccurredAt: "2026-01-01T00:00:00Z", Payload: run.Payload}
	if len(t.Sender) > 0 {
		str := func(k string) string { s, _ := t.Sender[k].(string); return s }
		verified, _ := t.Sender["verified"].(bool)
		info.Sender = &core.TriggerSender{Kind: core.TriggerSenderKind(str("kind")), ID: str("id"), DisplayName: str("display_name"), Verified: verified}
	}
	return run.Inputs, info, sampleFilledWarnings(wf, run), nil
}

// sampleFilledWarnings names each field of an integration trigger's payload
// that the workflow reads (its text names trigger.payload.<path>) but that
// only the generated sample set: the scenario left it out, the event's
// declared payload does not require it, and the sample filled it with a
// placeholder. A Slack app_mention scenario that sets no thread_ts reads
// data.thread_ts as "thread_ts" — a reply in a thread — though a top-level
// mention has none, so the run tests the branch the author did not mean.
//
// Only a warning: scenarios written against the filled sample keep passing.
func sampleFilledWarnings(wf *reliantv1.Workflow, run celeval.TriggerRun) []string {
	if len(run.SampleFilled) == 0 {
		return nil
	}
	text, err := protojson.Marshal(wf)
	if err != nil {
		return nil
	}
	var out []string
	for _, f := range run.SampleFilled {
		if !strings.Contains(string(text), "payload."+f.Path) {
			continue
		}
		value, _ := json.Marshal(f.Value)
		out = append(out, fmt.Sprintf("trigger %q: the workflow reads trigger.payload.%s, which this scenario's payload does not set, "+
			"so it holds the generated sample's %s; a real event may leave it out (the event's payload does not require it) — "+
			"set it in trigger.payload (\"\" or null for an absent value) so the run takes the branch you mean", run.Trigger, f.Path, value))
	}
	return out
}
