// Copyright (c) 2025 Reliant Labs
package simulator

import (
	"fmt"
	"reflect"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ValidationResult contains the results of scenario validation.
type ValidationResult struct {
	Valid    bool     // True if no errors (warnings are ok)
	Errors   []string // Critical errors that prevent execution
	Warnings []string // Non-critical warnings (e.g., unknown output fields)
}

// AddError adds an error to the result and marks it as invalid.
func (r *ValidationResult) AddError(format string, args ...interface{}) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
	r.Valid = false
}

// AddWarning adds a warning to the result (doesn't affect validity).
func (r *ValidationResult) AddWarning(format string, args ...interface{}) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// String returns a formatted string of all errors and warnings.
func (r *ValidationResult) String() string {
	var parts []string
	if len(r.Errors) > 0 {
		parts = append(parts, "Errors:")
		for _, e := range r.Errors {
			parts = append(parts, "  - "+e)
		}
	}
	if len(r.Warnings) > 0 {
		parts = append(parts, "Warnings:")
		for _, w := range r.Warnings {
			parts = append(parts, "  - "+w)
		}
	}
	return strings.Join(parts, "\n")
}

// ValidateScenario validates a scenario against a workflow definition.
// Returns a ValidationResult with errors and warnings.
//
// Validations performed:
// 1. All event.Node references exist in the workflow (including qualified IDs)
// 2. All expect.Reached nodes exist in the workflow
// 3. All expect.NotReached nodes exist in the workflow
// 4. Scenario inputs match workflow input schema
// 5. Output fields match activity types (warnings only)
// 6. No mixing of black-box and internal events for the same workflow node
func ValidateScenario(scenario *Scenario, workflow *reliantv1.Workflow) *ValidationResult {
	result := &ValidationResult{Valid: true}

	// Collect all valid node IDs from the workflow
	validNodes := collectValidNodes(workflow, "")

	// Validate event node references
	for i, event := range scenario.Events {
		if event.Node != "" {
			if !isValidNodeRef(event.Node, validNodes) {
				result.AddError("event[%d]: unknown node %q (valid nodes: %s)",
					i, event.Node, formatNodeList(validNodes))
			} else {
				// Validate output fields against activity type (warnings only)
				validateOutputFields(result, event, workflow, i)
			}
		}
	}

	// Validate expect.Reached nodes
	if scenario.Expect != nil {
		for _, nodeRef := range scenario.Expect.Reached {
			if !isValidNodeRef(nodeRef, validNodes) {
				result.AddError("expect.reached: unknown node %q", nodeRef)
			}
		}

		// Validate expect.NotReached nodes
		for _, nodeRef := range scenario.Expect.NotReached {
			if !isValidNodeRef(nodeRef, validNodes) {
				result.AddError("expect.not_reached: unknown node %q", nodeRef)
			}
		}

		// Validate expect.Completed nodes
		for _, nodeRef := range scenario.Expect.Completed {
			if !isValidNodeRef(nodeRef, validNodes) {
				result.AddError("expect.completed: unknown node %q", nodeRef)
			}
		}

		// Validate expect.Skipped nodes
		for _, nodeRef := range scenario.Expect.Skipped {
			if !isValidNodeRef(nodeRef, validNodes) {
				result.AddError("expect.skipped: unknown node %q", nodeRef)
			}
		}

		// Validate expect.NodeOutputs references
		for nodeRef := range scenario.Expect.NodeOutputs {
			if !isValidNodeRef(nodeRef, validNodes) {
				result.AddError("expect.node_outputs: unknown node %q", nodeRef)
			}
		}
	}

	// Validate StartAt node
	if scenario.StartAt != "" {
		if !isValidNodeRef(scenario.StartAt, validNodes) {
			result.AddError("start_at: unknown node %q", scenario.StartAt)
		}
	}

	// Validate State node references
	for nodeRef := range scenario.State {
		if !isValidNodeRef(nodeRef, validNodes) {
			result.AddError("state: unknown node %q", nodeRef)
		}
	}

	// Validate scenario inputs against workflow input schema
	validateScenarioInputs(result, scenario.Inputs, workflow.GetInputs(), "")

	// Validate no mixing of black-box and internal events for same node
	validateMockingConsistency(result, scenario.Events)

	return result
}

// collectValidNodes recursively collects all valid node IDs from a workflow.
// The prefix is used for qualified IDs (e.g., "loop_id.inner_node_id").
func collectValidNodes(workflow *reliantv1.Workflow, prefix string) map[string]nodeInfo {
	nodes := make(map[string]nodeInfo)
	if workflow == nil {
		return nodes
	}

	for _, node := range workflow.GetNodes() {
		nodeID := node.GetId()
		nodeType := node.GetType()

		// Build qualified ID
		qualifiedID := nodeID
		if prefix != "" {
			qualifiedID = prefix + "." + nodeID
		}

		// Add the node with its type
		nodes[qualifiedID] = nodeInfo{
			nodeType:  nodeType,
			protoNode: node,
		}

		// For loop and workflow nodes with inline definitions, recurse
		if la := model.GetLoopArgs(node); la != nil {
			if la.GetInline() != nil {
				innerNodes := collectValidNodes(la.GetInline(), qualifiedID)
				for id, info := range innerNodes {
					nodes[id] = info
				}
			}
			if ref := model.CelStringRaw(la.GetRef()); ref != "" {
				nodes[ref] = nodeInfo{
					nodeType: "ref",
					isRef:    true,
				}
			}
		} else if swa := model.GetSubWorkflowArgs(node); swa != nil {
			if swa.GetInline() != nil {
				innerNodes := collectValidNodes(swa.GetInline(), qualifiedID)
				for id, info := range innerNodes {
					nodes[id] = info
				}
			}
			if ref := model.CelStringRaw(swa.GetRef()); ref != "" {
				nodes[ref] = nodeInfo{
					nodeType: "ref",
					isRef:    true,
				}
			}
		}
	}

	return nodes
}

// nodeInfo stores information about a node for validation.
type nodeInfo struct {
	nodeType  string
	protoNode *reliantv1.Node
	isRef     bool // True if this is a ref target, not an actual node
}

// isValidNodeRef checks if a node reference is valid.
// For qualified IDs (e.g., "impl_1.agent_loop.call_llm"), it validates as much
// as possible but allows internal paths through workflow-type nodes with refs
// since those reference external workflows we can't validate.
func isValidNodeRef(nodeRef string, validNodes map[string]nodeInfo) bool {
	// Direct match
	if _, ok := validNodes[nodeRef]; ok {
		return true
	}

	// For qualified IDs, check if we're traversing through a workflow node with a ref
	if strings.Contains(nodeRef, ".") {
		parts := strings.Split(nodeRef, ".")
		// Build up the path and check each segment
		for i := 1; i < len(parts); i++ {
			parentPath := strings.Join(parts[:i], ".")
			if info, ok := validNodes[parentPath]; ok && info.protoNode != nil {
				// If this is a workflow node with a ref, we can't validate deeper
				// Allow the reference since internal nodes are in an external workflow
				if swa := model.GetSubWorkflowArgs(info.protoNode); swa != nil && model.CelStringRaw(swa.GetRef()) != "" {
					return true
				}
				if la := model.GetLoopArgs(info.protoNode); la != nil && model.CelStringRaw(la.GetRef()) != "" {
					return true
				}
			}
		}
	}

	return false
}

// formatNodeList formats a list of valid nodes for error messages.
func formatNodeList(nodes map[string]nodeInfo) string {
	if len(nodes) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(nodes))
	for name, info := range nodes {
		if !info.isRef {
			names = append(names, name)
		}
	}
	if len(names) > 10 {
		return strings.Join(names[:10], ", ") + fmt.Sprintf(" ... and %d more", len(names)-10)
	}
	return strings.Join(names, ", ")
}

// validateOutputFields validates that output fields match the activity type.
// Uses the workflow CEL TypeRegistry to check field names.
// Adds warnings for unknown fields (not errors, since outputs are flexible).
func validateOutputFields(result *ValidationResult, event SimulatedEvent, workflow *reliantv1.Workflow, eventIndex int) {
	if len(event.Output) == 0 {
		return
	}

	// Get the node type for this event
	nodeType := getNodeType(event.Node, workflow)
	if nodeType == "" {
		return // Can't determine type, skip validation
	}

	// Look up the type in the registry
	registry := wfcel.NewTypeRegistry()
	fields := registry.OutputFieldsForNodeType(nodeType)
	if fields == nil {
		return // Type not registered, skip validation
	}

	// Build a set of valid field names
	validFieldNames := make(map[string]bool, len(fields))
	for _, f := range fields {
		validFieldNames[f.Name] = true
	}

	// Check each output field
	for fieldName := range event.Output {
		if !validFieldNames[fieldName] {
			names := make([]string, 0, len(fields))
			for _, f := range fields {
				names = append(names, f.Name)
			}
			result.AddWarning("event[%d]: output field %q not in %s type (valid: %s)",
				eventIndex, fieldName, nodeType, strings.Join(names, ", "))
		}
	}

	// Raw-output mode only: the map IS the activity result, so its value shapes
	// must match the activity's proto output. A typed event (llm_response,
	// tool_result) carries a shorthand payload that the engine converts, so its
	// `output:` is not an activity output and must not be checked here.
	if event.Type == "" {
		if md, ok := registry.OutputForNodeType(nodeType); ok {
			validateOutputValueTypes(result, event.Output, md, eventIndex, "")
		}
	}
}

// validateOutputValueTypes checks a raw mock output against the activity's proto
// output descriptor, recursively.
//
// This exists because a mock is only useful if it is a shape the real activity
// could actually produce. The corpus previously wrote call_llm's
// tool_calls[].input as a nested YAML mapping, but every LLM driver assigns it
// the provider's JSON arguments STRING (e.g. openai/driver.go's
// call.Function.Arguments), message.ToolCall.Input is a string, and
// ToolCallMsg.input is `string`. The fast simulator tolerated the map because it
// never reaches the activity layer; the Temporal-backed lane failed in
// save_message's convertToToolCalls. Catching it here means a bad mock is
// rejected where it is written, on BOTH lanes, instead of silently exercising a
// path the product cannot reach.
//
// Unknown field names stay warnings (handled above); a wrong VALUE SHAPE is an
// error, because it makes the scenario assert against a fiction.
func validateOutputValueTypes(
	result *ValidationResult,
	output map[string]interface{},
	md protoreflect.MessageDescriptor,
	eventIndex int,
	path string,
) {
	fields := md.Fields()
	for name, value := range output {
		fd := fields.ByName(protoreflect.Name(name))
		if fd == nil || value == nil {
			continue // unknown names are reported by the name check above
		}
		validateFieldValue(result, fd, value, eventIndex, joinFieldPath(path, name))
	}
}

// validateFieldValue checks one value against one proto field, handling
// repeated and map cardinality before the element type.
func validateFieldValue(
	result *ValidationResult,
	fd protoreflect.FieldDescriptor,
	value interface{},
	eventIndex int,
	path string,
) {
	if fd.IsMap() {
		return // map fields accept any YAML mapping
	}
	if fd.IsList() {
		// Reflect rather than asserting []interface{}: YAML decodes lists to
		// []interface{}, but Go-constructed scenarios (and test helpers) build
		// []map[string]interface{} for the same field, and both are valid.
		items, ok := asSlice(value)
		if !ok {
			result.AddError("event[%d]: output %s: expected a list, got %T", eventIndex, path, value)
			return
		}
		for i, item := range items {
			validateElementValue(result, fd, item, eventIndex, fmt.Sprintf("%s[%d]", path, i))
		}
		return
	}
	validateElementValue(result, fd, value, eventIndex, path)
}

// validateElementValue checks a single (non-repeated) value against a field's type.
func validateElementValue(
	result *ValidationResult,
	fd protoreflect.FieldDescriptor,
	value interface{},
	eventIndex int,
	path string,
) {
	if value == nil {
		return
	}
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		nested := fd.Message()
		// Well-known types (Struct, Value, Timestamp, ...) accept arbitrary
		// JSON shapes; checking them would produce false positives.
		if strings.HasPrefix(string(nested.FullName()), "google.protobuf.") {
			return
		}
		m, ok := asStringKeyedMap(value)
		if !ok {
			result.AddError("event[%d]: output %s: expected an object, got %T", eventIndex, path, value)
			return
		}
		validateOutputValueTypes(result, m, nested, eventIndex, path)
	case protoreflect.StringKind, protoreflect.BytesKind:
		if _, ok := value.(string); !ok {
			result.AddError(
				"event[%d]: output %s: expected string, got %T "+
					"(proto declares this field as string — encode structured values as a JSON string)",
				eventIndex, path, value)
		}
	case protoreflect.BoolKind:
		if _, ok := value.(bool); !ok {
			result.AddError("event[%d]: output %s: expected bool, got %T", eventIndex, path, value)
		}
	case protoreflect.EnumKind:
		switch value.(type) {
		case string, int, int32, int64, float64:
		default:
			result.AddError("event[%d]: output %s: expected enum name or number, got %T", eventIndex, path, value)
		}
	default: // numeric kinds
		switch value.(type) {
		case int, int32, int64, uint, uint32, uint64, float32, float64:
		default:
			result.AddError("event[%d]: output %s: expected number, got %T", eventIndex, path, value)
		}
	}
}

// asSlice normalizes any slice or array to []interface{}, so a mock built in Go
// with a concrete element type validates the same as one decoded from YAML.
// Strings and byte slices are deliberately not treated as sequences.
func asSlice(value interface{}) ([]interface{}, bool) {
	if items, ok := value.([]interface{}); ok {
		return items, true
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, false
	}
	if rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() == reflect.Uint8 {
		return nil, false
	}
	items := make([]interface{}, rv.Len())
	for i := range items {
		items[i] = rv.Index(i).Interface()
	}
	return items, true
}

// asStringKeyedMap normalizes a mapping to map[string]interface{}, covering
// Go-constructed mocks whose value type is concrete rather than interface{}.
func asStringKeyedMap(value interface{}) (map[string]interface{}, bool) {
	if m, ok := value.(map[string]interface{}); ok {
		return m, true
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Map || rv.Type().Key().Kind() != reflect.String {
		return nil, false
	}
	m := make(map[string]interface{}, rv.Len())
	for _, k := range rv.MapKeys() {
		m[k.String()] = rv.MapIndex(k).Interface()
	}
	return m, true
}

// joinFieldPath builds a dotted field path for error messages.
func joinFieldPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// getNodeType returns the node type for a qualified node ID.
func getNodeType(qualifiedID string, workflow *reliantv1.Workflow) string {
	if workflow == nil {
		return ""
	}

	parts := strings.Split(qualifiedID, ".")
	return findNodeType(parts, workflow)
}

// findNodeType recursively finds the node type for a path of IDs.
func findNodeType(path []string, workflow *reliantv1.Workflow) string {
	if len(path) == 0 || workflow == nil {
		return ""
	}

	// Find the node with this ID
	for _, node := range workflow.GetNodes() {
		if node.GetId() == path[0] {
			if len(path) == 1 {
				// This is the target node
				return node.GetType()
			}
			// Need to go deeper into inline workflow
			if la := model.GetLoopArgs(node); la != nil && la.GetInline() != nil {
				return findNodeType(path[1:], la.GetInline())
			}
			if swa := model.GetSubWorkflowArgs(node); swa != nil && swa.GetInline() != nil {
				return findNodeType(path[1:], swa.GetInline())
			}
			return "" // Can't traverse into ref nodes
		}
	}

	return ""
}

// validateScenarioInputs validates scenario inputs against workflow input schema.
func validateScenarioInputs(result *ValidationResult, inputs map[string]interface{}, schema map[string]*reliantv1.Input, prefix string) {
	if schema == nil {
		return
	}

	for name, value := range inputs {
		inputPath := name
		if prefix != "" {
			inputPath = prefix + "." + name
		}

		// Check if this input is defined in the schema
		input, ok := schema[name]
		if !ok {
			result.AddWarning("input %q is not defined in workflow schema", inputPath)
			continue
		}

		// For group inputs, recursively validate
		if input.GetType() == "group" {
			if gc := input.GetGroupInput(); gc != nil && gc.GetInputs() != nil {
				if nestedInputs, ok := value.(map[string]interface{}); ok {
					validateScenarioInputs(result, nestedInputs, gc.GetInputs(), inputPath)
				}
			}
			continue
		}

		// Basic type validation for common input types
		if value != nil {
			if err := validateInputValue(input, value); err != nil {
				result.AddError("input %q: %s", inputPath, err)
			}
		}
	}
}

// validateInputValue performs basic type checking for scenario inputs.
func validateInputValue(input *reliantv1.Input, value interface{}) error {
	switch input.GetType() {
	case "string", "message":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("expected string, got %T", value)
		}
	case "number":
		switch value.(type) {
		case float64, float32, int, int64, int32:
			// ok
		default:
			return fmt.Errorf("expected number, got %T", value)
		}
	case "integer":
		switch v := value.(type) {
		case int, int64, int32:
			// ok
		case float64:
			if v != float64(int(v)) {
				return fmt.Errorf("expected integer, got float %v", v)
			}
		default:
			return fmt.Errorf("expected integer, got %T", value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("expected boolean, got %T", value)
		}
	case "enum":
		if ec := input.GetEnumInput(); ec != nil {
			allowed := ec.GetEnumValues()
			if ec.GetMulti() {
				// Multi-select: value must be an array of strings
				var items []string
				switch v := value.(type) {
				case []string:
					items = v
				case []interface{}:
					for _, elem := range v {
						s, ok := elem.(string)
						if !ok {
							return fmt.Errorf("expected array of strings for multi-enum, got element %T", elem)
						}
						items = append(items, s)
					}
				default:
					return fmt.Errorf("expected array for multi-select enum, got %T", value)
				}
				for _, item := range items {
					found := false
					for _, a := range allowed {
						if a == item {
							found = true
							break
						}
					}
					if !found {
						return fmt.Errorf("%q is not in enum list %v", item, allowed)
					}
				}
			} else {
				// Single-select: value must be a string
				str, ok := value.(string)
				if !ok {
					return fmt.Errorf("expected string for enum, got %T", value)
				}
				found := false
				for _, v := range allowed {
					if v == str {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("%q is not in enum list %v", str, allowed)
				}
			}
		}
	}
	return nil
}

// ValidateScenarioNodes is a convenience function that only validates node references.
// Use this for quick validation when you don't need full validation.
func ValidateScenarioNodes(scenario *Scenario, workflow *reliantv1.Workflow) []string {
	result := ValidateScenario(scenario, workflow)
	return result.Errors
}

// WorkflowRefLoader resolves a workflow ref (e.g. "builtin://agent") to its
// definition. Declared here, at the consumer, so the analysis can report how
// much of a black-boxed sub-workflow went unexecuted.
type WorkflowRefLoader func(ref string) (*reliantv1.Workflow, error)

// AnalyzeFalsePasses inspects a COMPLETED simulation and reports the ways this
// run could have passed green without testing what the scenario author thinks
// it tested. These are warnings, never errors: both patterns are sometimes
// exactly what the author wants.
//
// Two detections, both of which are silent today:
//
//  1. Black-box sub-workflow. A ref-mode `workflow` node with no
//     internally-qualified events is replaced wholesale by a scenario literal.
//     The entire sub-workflow — every node in it — never runs.
//  2. Defaulted node router. An unmocked node router silently selects its first
//     candidate, so a routing test asserts a path the router was never asked to
//     choose.
//
// routerMocks must come from SnapshotRouterMocks taken BEFORE the run — see
// that function for why the scenario itself cannot be trusted afterwards.
func AnalyzeFalsePasses(scenario *Scenario, workflow *reliantv1.Workflow, exec *ExecutionDetails, loader WorkflowRefLoader, routerMocks map[string]bool) []string {
	if scenario == nil || workflow == nil || exec == nil {
		return nil
	}

	internalPrefixes := internalEventPrefixes(scenario.Events)
	blackBoxed := explicitBlackBoxNodes(scenario.Events)

	var warnings []string
	seen := make(map[string]bool, len(exec.NodesReached))
	for _, qualifiedID := range exec.NodesReached {
		if seen[qualifiedID] {
			continue // loop bodies revisit the same qualified id per iteration
		}
		seen[qualifiedID] = true

		node := findNodeByPath(strings.Split(qualifiedID, "."), workflow, loader)
		if node == nil {
			continue // inside a ref we cannot resolve; nothing to say about it
		}

		if w := blackBoxWorkflowWarning(qualifiedID, node, internalPrefixes, blackBoxed, loader); w != "" {
			warnings = append(warnings, w)
		}
		if w := defaultedRouterWarning(qualifiedID, node, routerMocks, exec.NodeOutputs[qualifiedID]); w != "" {
			warnings = append(warnings, w)
		}
	}

	return warnings
}

// blackBoxWorkflowWarning reports a ref-mode workflow node whose internals were
// never simulated. Inline sub-workflows always execute, so they are exempt.
//
// The warning is for ACCIDENTAL opacity. An author who wrote `black_box: true`
// against this node has said the sub-workflow's body is not what the scenario
// tests, so warning them anyway leaves no way to silence it — which is how a
// corpus learns to ignore its own warnings.
func blackBoxWorkflowWarning(qualifiedID string, node *reliantv1.Node, internalPrefixes, blackBoxed map[string]bool, loader WorkflowRefLoader) string {
	swa := model.GetSubWorkflowArgs(node)
	if swa == nil || swa.GetInline() != nil {
		return ""
	}
	ref := model.CelStringRaw(swa.GetRef())
	if ref == "" {
		return ""
	}
	if internalPrefixes[qualifiedID+"."] {
		return "" // scenario targets its internals — the sub-workflow really ran
	}
	if blackBoxed[qualifiedID] {
		return "" // the author opted into opacity for this node, deliberately
	}

	scope := "its nodes"
	example := qualifiedID + ".<inner_node>"
	if loader != nil {
		if sub, err := loader(ref); err == nil && sub != nil {
			if n := len(sub.GetNodes()); n > 0 {
				scope = fmt.Sprintf("all %d of its nodes", n)
				example = qualifiedID + "." + sub.GetNodes()[0].GetId()
			}
		}
	}

	return fmt.Sprintf(
		"black-box sub-workflow: node %q (ref %q) did NOT execute — its output came from the scenario, so %s were skipped. "+
			"To exercise it, mock its internals with qualified ids (e.g. `node: %s`).",
		qualifiedID, ref, scope, example)
}

// defaultedRouterWarning reports a node router that fell back to its first
// candidate because the scenario never supplied selected_node — meaning the
// routing decision under test was never actually made.
func defaultedRouterWarning(qualifiedID string, node *reliantv1.Node, routerMocks map[string]bool, output map[string]interface{}) string {
	if !model.IsNodeRouterMode(node) {
		return ""
	}
	candidates := model.GetRouterArgs(node).GetNodes()
	if len(candidates) == 0 {
		return ""
	}
	if routerMocks[qualifiedID] {
		return "" // the scenario made the routing decision itself
	}

	// The node router ran without a mocked selection, so the default fired.
	// Confirm against the recorded output so a node that never actually
	// dispatched stays silent.
	first := candidates[0].GetId()
	if selected, _ := output["selected_node"].(string); selected != first {
		return ""
	}

	names := make([]string, 0, len(candidates))
	for _, c := range candidates {
		names = append(names, c.GetId())
	}
	return fmt.Sprintf(
		"unmocked node router: %q defaulted to its first candidate %q — routing was NOT tested. Candidates: %s. "+
			"To test the decision, mock it (e.g. `- node: %s` / `output: {selected_node: %s}`).",
		qualifiedID, first, strings.Join(names, ", "), qualifiedID, names[len(names)-1])
}

// SnapshotRouterMocks records which nodes the scenario supplies a non-empty
// selected_node for. It MUST be called before the simulation runs.
//
// Raw-mode events hand their Output map to the simulator by reference, and the
// simulator writes its first-candidate default straight into that map. After a
// run, a defaulted router is therefore indistinguishable from a mocked one by
// inspecting the scenario — the scenario has been mutated to look mocked. This
// snapshot is the only reliable record of what the author actually wrote.
func SnapshotRouterMocks(events []SimulatedEvent) map[string]bool {
	mocked := make(map[string]bool)
	for _, event := range events {
		if event.Node == "" {
			continue
		}
		if selected, ok := event.Output["selected_node"].(string); ok && selected != "" {
			mocked[event.Node] = true
		}
	}
	return mocked
}

// explicitBlackBoxNodes returns the nodes the scenario opted out of body
// execution for with `black_box: true` — the same set buildBlackBoxed feeds the
// simulator, read here so deliberate opacity is silent while accidental opacity
// still warns.
func explicitBlackBoxNodes(events []SimulatedEvent) map[string]bool {
	opted := make(map[string]bool)
	for _, event := range events {
		if event.BlackBox && event.Node != "" {
			opted[event.Node] = true
		}
	}
	return opted
}

// internalEventPrefixes returns every "a.", "a.b." prefix implied by the
// scenario's qualified event node refs — the same prefixes the simulator uses
// to decide whether to descend into a referenced sub-workflow.
func internalEventPrefixes(events []SimulatedEvent) map[string]bool {
	prefixes := make(map[string]bool)
	for _, event := range events {
		if event.Node == "" || !strings.Contains(event.Node, ".") {
			continue
		}
		parts := strings.Split(event.Node, ".")
		for i := 1; i < len(parts); i++ {
			prefixes[strings.Join(parts[:i], ".")+"."] = true
		}
	}
	return prefixes
}

// findNodeByPath resolves a qualified node id to its proto node, descending
// through inline loop/workflow bodies and, when a loader is available, through
// referenced ones.
func findNodeByPath(path []string, workflow *reliantv1.Workflow, loader WorkflowRefLoader) *reliantv1.Node {
	if len(path) == 0 || workflow == nil {
		return nil
	}

	for _, node := range workflow.GetNodes() {
		if node.GetId() != path[0] {
			continue
		}
		if len(path) == 1 {
			return node
		}

		var inline *reliantv1.Workflow
		var ref string
		if la := model.GetLoopArgs(node); la != nil {
			inline, ref = la.GetInline(), model.CelStringRaw(la.GetRef())
		} else if swa := model.GetSubWorkflowArgs(node); swa != nil {
			inline, ref = swa.GetInline(), model.CelStringRaw(swa.GetRef())
		}
		if inline != nil {
			return findNodeByPath(path[1:], inline, loader)
		}
		if ref != "" && loader != nil {
			if sub, err := loader(ref); err == nil {
				return findNodeByPath(path[1:], sub, loader)
			}
		}
		return nil
	}

	return nil
}

// validateMockingConsistency checks that scenarios don't mix black-box and internal
// events for the same workflow node.
//
// For example, these events conflict:
//   - node: impl_1 (black-box: "here's what impl_1 returns")
//   - node: impl_1.agent_loop.call_llm (internal: "simulate impl_1's internals")
//
// You must choose one approach per workflow node.
func validateMockingConsistency(result *ValidationResult, events []SimulatedEvent) {
	// Collect all node references from events
	blackBoxNodes := make(map[string]bool) // Nodes targeted directly (black-box)
	internalNodes := make(map[string]bool) // Parent nodes of qualified IDs (internal)

	for _, event := range events {
		if event.Node == "" {
			continue // Sequential event, skip
		}

		if strings.Contains(event.Node, ".") {
			// This is a qualified ID (internal mocking)
			// Extract the top-level node ID
			parts := strings.SplitN(event.Node, ".", 2)
			topLevel := parts[0]
			internalNodes[topLevel] = true
		} else {
			// This is a direct node ID (black-box mocking)
			blackBoxNodes[event.Node] = true
		}
	}

	// Check for conflicts
	for node := range blackBoxNodes {
		if internalNodes[node] {
			result.AddError(
				"node %q has both black-box events (node: %q) and internal events (node: %q.*). "+
					"Choose one mocking approach per workflow node.",
				node, node, node,
			)
		}
	}
}
