// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"sort"

	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// The workflow's half of the tool capability set (research/TOOL_CAPABILITIES.md).
//
// call_llm resolves one set per turn and records it in its output. This file
// is everything the workflow does with it: hand the set to the execute_tools
// that runs that turn's calls, apply it to the two tool families that run
// workflow-side (spawn, ask_user), and keep the per-thread load_tool grants
// that feed the next call_llm. All of it is computed from recorded activity
// results, so a replay re-derives it exactly.
//
// The set is read as the proto, not as the tools package's type, because the
// tools package imports this one.

// Names of the tools this file reasons about, spelled out for the same reason.
const (
	spawnToolName   = "spawn"
	askUserToolName = "ask_user"
)

// executeToolsActivityName is the registered name of the execute_tools node's activity.
const executeToolsActivityName = "ExecuteTools"

// capabilitiesResolved reports whether caps is a set call_llm actually
// resolved. A resolved set always carries a tier; an output from before the
// set existed decodes, once normalized, to a zero-valued message instead.
func capabilitiesResolved(caps *reliantv1.ToolCapabilities) bool {
	return caps != nil && caps.GetPermission() != ""
}

func capabilitiesOffer(caps *reliantv1.ToolCapabilities, name string) bool {
	for _, offered := range caps.GetOffered() {
		if offered == name {
			return true
		}
	}
	return false
}

func capabilitiesAllowPreset(caps *reliantv1.ToolCapabilities, preset string) bool {
	for _, allowed := range caps.GetSpawnPresets() {
		if allowed == preset {
			return true
		}
	}
	return false
}

// capabilitiesFromNodeOutput reads the set a call_llm node recorded, or nil.
func capabilitiesFromNodeOutput(output interface{}) *reliantv1.ToolCapabilities {
	outputMap, ok := output.(map[string]interface{})
	if !ok {
		return nil
	}
	raw, ok := outputMap["capabilities"].(map[string]interface{})
	if !ok {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	caps := &reliantv1.ToolCapabilities{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(encoded, caps); err != nil {
		return nil
	}
	if !capabilitiesResolved(caps) {
		return nil
	}
	return caps
}

// upstreamToolCapabilities finds the capability set of the call_llm turn that
// produced an execute_tools batch: the node its tool_calls expression names
// (`nodes.X.tool_calls`), else whichever call_llm output in this scope carried
// those tool-call ids — which covers an expression that filters or reshapes
// the list. nil when neither recorded a set: a run from before the set
// existed, or tool calls that never came from an LLM.
func (e *StepExecutor) upstreamToolCapabilities(node *reliantv1.Node, resolved []*reliantv1.ToolCallMsg) *reliantv1.ToolCapabilities {
	if source := extractToolCallsSourceNode(model.CelStringRaw(model.GetExecuteToolsArgs(node).GetToolCalls())); source != "" {
		if caps := capabilitiesFromNodeOutput(e.nodeOutputs[source]); caps != nil {
			return caps
		}
	}
	if e.workflow == nil || len(resolved) == 0 {
		return nil
	}
	ids := make(map[string]bool, len(resolved))
	for _, tc := range resolved {
		ids[tc.GetId()] = true
	}
	// Walk the definition, not the outputs map, so the order is fixed.
	for _, candidate := range e.workflow.GetNodes() {
		if model.NodeType(candidate) != model.NodeTypeCallLLM {
			continue
		}
		output, ok := e.nodeOutputs[model.NodeID(candidate)].(map[string]interface{})
		if !ok || !outputCarriesToolCall(output, ids) {
			continue
		}
		if caps := capabilitiesFromNodeOutput(output); caps != nil {
			return caps
		}
	}
	return nil
}

func outputCarriesToolCall(output map[string]interface{}, ids map[string]bool) bool {
	calls, _ := output["tool_calls"].([]interface{})
	for _, call := range calls {
		if callMap, ok := call.(map[string]interface{}); ok {
			if id, _ := callMap["id"].(string); ids[id] {
				return true
			}
		}
	}
	return false
}

// withCapabilitiesApplied moves the workflow-side calls the turn's set does
// not allow — a spawn that was not offered or names a preset the spawn tool
// was not offered with, an ask_user that was not offered — into the regular
// batch. The ExecuteTools activity then refuses them like any other call, so
// there is one refusal path and one FAILED row, instead of a second copy of
// the check here.
//
// Gated on the DATA: a batch whose call_llm recorded no set (every history
// from before the set existed) is split exactly as before, which is what
// keeps those histories replaying the same commands.
func withCapabilitiesApplied(split protoToolCallSplit, caps *reliantv1.ToolCapabilities) protoToolCallSplit {
	if !capabilitiesResolved(caps) {
		return split
	}
	applied := protoToolCallSplit{regularToolCalls: split.regularToolCalls}
	for _, tc := range split.spawnToolCalls {
		if spawnAllowed(tc, caps) {
			applied.spawnToolCalls = append(applied.spawnToolCalls, tc)
		} else {
			applied.regularToolCalls = append(applied.regularToolCalls, tc)
		}
	}
	for _, tc := range split.askUserToolCalls {
		if capabilitiesOffer(caps, askUserToolName) {
			applied.askUserToolCalls = append(applied.askUserToolCalls, tc)
		} else {
			applied.regularToolCalls = append(applied.regularToolCalls, tc)
		}
	}
	return applied
}

// spawnAllowed reports whether a spawn call may be dispatched under caps. A
// call whose input does not parse is left to the dispatcher, which already
// reports that to the model.
func spawnAllowed(tc *reliantv1.ToolCallMsg, caps *reliantv1.ToolCapabilities) bool {
	if !capabilitiesOffer(caps, spawnToolName) {
		return false
	}
	parsed, err := parseSpawnToolInput(tc.GetInput())
	if err != nil || parsed.preset == "" {
		return true
	}
	return capabilitiesAllowPreset(caps, parsed.preset)
}

// ── Per-thread load_tool grants ──────────────────────────────────────────

// recordToolGrants adds what a thread's execute_tools batch granted. Called
// from the completion of a recorded ExecuteTools result, so replay rebuilds
// the same map.
func (t *ChildWorkflowTracker) recordToolGrants(thread string, granted []string) {
	if t == nil || thread == "" || len(granted) == 0 {
		return
	}
	if t.toolGrants == nil {
		t.toolGrants = make(map[string][]string)
	}
	t.toolGrants[thread] = mergeSortedNames(t.toolGrants[thread], granted)
}

// toolGrantsFor is what a thread's next call_llm is handed as rtx.ToolGrants.
func (t *ChildWorkflowTracker) toolGrantsFor(thread string) []string {
	if t == nil || len(t.toolGrants[thread]) == 0 {
		return nil
	}
	return append([]string(nil), t.toolGrants[thread]...)
}

// toolGrantsHandoff is the grants a continue-as-new carries to the successor.
func (t *ChildWorkflowTracker) toolGrantsHandoff() map[string][]string {
	if t == nil || len(t.toolGrants) == 0 {
		return nil
	}
	handoff := make(map[string][]string, len(t.toolGrants))
	for thread, names := range t.toolGrants {
		handoff[thread] = append([]string(nil), names...)
	}
	return handoff
}

// seedToolGrants installs a predecessor's grants at the start of a run.
func (t *ChildWorkflowTracker) seedToolGrants(grants map[string][]string) {
	for thread, names := range grants {
		t.recordToolGrants(thread, names)
	}
}

// grantedToolsFromOutput reads ExecuteToolsOutput.granted_tools from a
// normalized step output.
func grantedToolsFromOutput(output map[string]interface{}) []string {
	raw, _ := output["granted_tools"].([]interface{})
	granted := make([]string, 0, len(raw))
	for _, item := range raw {
		if name, ok := item.(string); ok && name != "" {
			granted = append(granted, name)
		}
	}
	return granted
}

func mergeSortedNames(existing, added []string) []string {
	set := make(map[string]bool, len(existing)+len(added))
	for _, name := range existing {
		set[name] = true
	}
	for _, name := range added {
		set[name] = true
	}
	merged := make([]string, 0, len(set))
	for name := range set {
		merged = append(merged, name)
	}
	sort.Strings(merged)
	return merged
}
