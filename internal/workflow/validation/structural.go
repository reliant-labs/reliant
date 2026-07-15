// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// Valid JSON Schema types per the JSON Schema spec
var validJSONSchemaTypes = map[string]bool{
	"string":  true,
	"number":  true,
	"integer": true,
	"boolean": true,
	"array":   true,
	"object":  true,
	"null":    true,
}

// =============================================================================
// NAMING VALIDATION
// =============================================================================

// identifierPattern defines valid user-defined identifiers.
var identifierPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

// validateIdentifier checks if a name is a valid user-defined identifier.
func validateIdentifier(name string, context string) *validationError {
	if name == "" {
		return &validationError{
			path:    context,
			message: "identifier must not be empty",
		}
	}
	if len(name) > 0 && name[0] == '_' {
		return &validationError{
			path:    context,
			message: fmt.Sprintf("'%s' cannot start with '_' (reserved for system fields)", name),
		}
	}
	if !identifierPattern.MatchString(name) {
		return &validationError{
			path:    context,
			message: fmt.Sprintf("invalid %s '%s': must start with a letter and contain only letters, digits, or underscores", context, name),
		}
	}
	return nil
}

// validateOutputName validates an output name.
func validateOutputName(name string) *validationError {
	if name == "" {
		return &validationError{
			path:    "output name",
			message: "identifier must not be empty",
		}
	}
	if len(name) > 0 && name[0] == '_' {
		return &validationError{
			path:    "output name",
			message: fmt.Sprintf("'%s' cannot start with '_' (reserved for system fields like _iterations)", name),
		}
	}
	if !identifierPattern.MatchString(name) {
		return &validationError{
			path:    "output name",
			message: fmt.Sprintf("invalid output name '%s': must start with a letter and contain only letters, digits, or underscores", name),
		}
	}
	return nil
}

type validationError struct {
	path    string
	message string
}

// =============================================================================
// STRUCTURAL VALIDATION
// =============================================================================

// validateStructure performs all structural validation on the workflow.
func validateStructure(wf *reliantv1.Workflow, result *Result) {
	seenNodeIDs := make(map[string]int)
	nodeMap := make(map[string]*reliantv1.Node)

	name := wf.GetName()

	// === Basic Required Fields ===

	if name == "" {
		result.AddError(CategoryStructure, nil, "name", "workflow name is required")
	}

	entry := wf.GetEntry()
	if len(entry) == 0 {
		result.AddErrorWithSuggestion(CategoryStructure, []string{name}, "entry",
			"entry field is required",
			"specify the starting node(s): entry: [node1] or entry: [node1, node2]")
	}

	nodes := wf.GetNodes()
	if len(nodes) == 0 {
		result.AddError(CategoryStructure, []string{name}, "nodes",
			"workflow must have at least one node")
		return
	}

	// === Input Validation ===

	for inputName, input := range wf.GetInputs() {
		validateProtoInput(inputName, input, result)
	}

	// === Workflow-level Presets Validation ===

	if presets := wf.GetPresets(); presets != nil {
		if presets.GetTag() == "" && presets.GetDefault() == "" {
			result.AddError(CategoryStructure, []string{name, "presets"}, "",
				"presets block must have at least 'tag' or 'default' specified")
		}
	}

	// === Output Key Validation ===

	for key := range wf.GetOutputs() {
		if err := validateOutputName(key); err != nil {
			result.AddError(CategoryStructure, []string{name, "outputs"}, key, err.message)
		}
	}

	// === Node Validation ===

	for i, node := range nodes {
		nodeID := node.GetId()
		nodePath := []string{name, "nodes", fmt.Sprintf("[%d](%s)", i, nodeID)}

		if err := validateIdentifier(nodeID, "node ID"); err != nil {
			result.AddError(CategoryStructure, nodePath, "id", err.message)
		}

		if firstIdx, exists := seenNodeIDs[nodeID]; exists {
			result.AddError(CategoryStructure, nodePath, "id",
				fmt.Sprintf("duplicate node ID '%s' (first seen at nodes[%d])", nodeID, firstIdx))
		} else {
			seenNodeIDs[nodeID] = i
			nodeMap[nodeID] = node
		}

		// Every node must have a type
		if node.GetType() == "" {
			result.AddError(CategoryStructure, nodePath, "type", "node is missing a 'type' field")
		}

		// Per-node-type validation
		validateNodeArgs(node, nodePath, result)

		// Validate timeout format if specified
		validateNodeTimeout(node, nodePath, result)

		// Validate response_tool schema if present
		validateResponseToolSchema(node, nodePath, result)

		// Validate inline workflows
		validateInlineWorkflows(node, nodePath, result)
	}

	// === Router Node Routing References ===

	for i, node := range nodes {
		if node.GetType() != model.NodeTypeRouter {
			continue
		}
		args := node.GetRouter()
		if args == nil || len(args.GetNodes()) == 0 {
			continue
		}
		nodeID := node.GetId()
		nodePath := []string{name, "nodes", fmt.Sprintf("[%d](%s)", i, nodeID)}

		candidateIDs := make(map[string]bool, len(args.GetNodes()))
		for j, candidate := range args.GetNodes() {
			cid := candidate.GetId()
			if cid == "" {
				continue // already reported in validateNodeArgs
			}
			candidateIDs[cid] = true
			if cid == nodeID {
				result.AddError(CategoryStructure, nodePath, "nodes",
					fmt.Sprintf("router node candidate %d references itself ('%s')", j, cid))
			} else if _, exists := seenNodeIDs[cid]; !exists {
				result.AddError(CategoryStructure, nodePath, "nodes",
					fmt.Sprintf("router node candidate %d references unknown node '%s'", j, cid))
			}
		}

		if fb := args.GetFallback(); fb != "" {
			if !candidateIDs[fb] {
				result.AddError(CategoryStructure, nodePath, "fallback",
					fmt.Sprintf("fallback '%s' must be one of the candidate node IDs", fb))
			}
		}
	}

	// === Entry References Valid Nodes ===

	for _, entryID := range entry {
		if _, exists := seenNodeIDs[entryID]; !exists {
			result.AddError(CategoryStructure, []string{name}, "entry",
				fmt.Sprintf("references unknown node '%s'", entryID))
		}
	}

	// === Resume Node References a Valid Node ===
	// resume_node overrides the position checkpoint when a run resumes an
	// interrupted predecessor; it must name a top-level node in this workflow.

	if rn := wf.GetResumeNode(); rn != "" {
		if _, exists := seenNodeIDs[rn]; !exists {
			result.AddError(CategoryStructure, []string{name}, "resume_node",
				fmt.Sprintf("references unknown node '%s'", rn))
		}
	}

	// === transition_to Must Not Self-Reference ===
	// transition_to names a workflow ref the chat switches to when this
	// workflow's run completes. It must not reference this workflow itself (a
	// self-cycle would trap the chat forever). Loadability is checked in
	// cross-workflow validation, where the WorkflowLoader is available.

	if tt := wf.GetTransitionTo(); tt != "" {
		if tt == name || tt == "builtin://"+name {
			result.AddError(CategoryStructure, []string{name}, "transition_to",
				fmt.Sprintf("must not reference this workflow itself ('%s')", tt))
		}
	}

	// === Edge Validation ===

	for i, edge := range wf.GetEdges() {
		edgePath := []string{name, "edges", fmt.Sprintf("[%d]", i)}

		if _, exists := seenNodeIDs[edge.GetFrom()]; !exists {
			result.AddError(CategoryStructure, edgePath, "from",
				fmt.Sprintf("references unknown node '%s'", edge.GetFrom()))
		}

		cases := edge.GetCases()
		defaults := edge.GetDefault()
		if len(cases) == 0 && len(defaults) == 0 {
			result.AddError(CategoryStructure, edgePath, "cases",
				fmt.Sprintf("edge from '%s' has no cases or default - must have at least one target", edge.GetFrom()))
		}

		for j, c := range cases {
			if c.GetCondition() == "" {
				result.AddError(CategoryStructure, edgePath, fmt.Sprintf("cases[%d].condition", j),
					"all cases must have a condition; use 'default' for unconditional routing")
			}
			for _, to := range c.GetTo() {
				if _, exists := seenNodeIDs[to]; !exists {
					result.AddError(CategoryStructure, edgePath, fmt.Sprintf("cases[%d].to", j),
						fmt.Sprintf("references unknown node '%s'", to))
				}
			}
		}

		for _, to := range defaults {
			if _, exists := seenNodeIDs[to]; !exists {
				result.AddError(CategoryStructure, edgePath, "default",
					fmt.Sprintf("references unknown node '%s'", to))
			}
		}
	}

	// === Node Reachability ===

	validateNodeReachability(wf, nodeMap, result)

	// === Cycle Detection ===

	validateNoCycles(wf, result)
}

// validateNodeArgs validates node-type-specific args.
func validateNodeArgs(node *reliantv1.Node, nodePath []string, result *Result) {
	nodeType := node.GetType()

	switch nodeType {
	case model.NodeTypeCallLLM:
		args := node.GetCallLlm()
		if args == nil {
			result.AddError(CategoryStructure, nodePath, "args", "call_llm node missing args")
			return
		}
		if !model.CelModelSelectorIsSet(args.GetModel()) {
			result.AddError(CategoryStructure, nodePath, "model",
				"call_llm node requires a model - specify model field or reference inputs.model")
		} else if !model.CelModelSelectorIsExpr(args.GetModel()) {
			// Literal model - validate it has ID or tags
			ms := model.CelModelSelectorValue(args.GetModel())
			if ms != nil && ms.GetId() == "" && len(ms.GetTags()) == 0 {
				result.AddError(CategoryStructure, nodePath, "model",
					"model must have 'id' or 'tags' set")
			}
		}

	case model.NodeTypeRun:
		args := node.GetRun()
		if args == nil {
			result.AddError(CategoryStructure, nodePath, "args", "run node missing args")
			return
		}
		if !model.CelStringIsSet(args.GetCommand()) {
			result.AddError(CategoryStructure, nodePath, "command", "required")
		}

	case model.NodeTypeLoop:
		args := node.GetLoop()
		if args == nil {
			result.AddError(CategoryStructure, nodePath, "args", "loop node missing args")
			return
		}

		isParallel := model.CelBoolIsSet(args.GetParallel()) && model.CelBoolValue(args.GetParallel())
		hasItems := model.CelStringIsSet(args.GetItems())

		if isParallel {
			// Parallel loop: requires items, disallows while
			if !hasItems {
				result.AddError(CategoryStructure, nodePath, "items",
					"parallel loop requires 'items' (CEL expression evaluating to a list or map)")
			}
			if model.DirectCelIsSet(args.GetWhile()) {
				result.AddError(CategoryStructure, nodePath, "while",
					"parallel loop cannot use 'while' — iteration count is determined by 'items'")
			}
			// Validate on_failure enum
			if onFailure := args.GetOnFailure(); onFailure != "" {
				switch onFailure {
				case "continue", "fail_fast", "fail_all":
					// valid
				default:
					result.AddError(CategoryStructure, nodePath, "on_failure",
						fmt.Sprintf("invalid on_failure value '%s': must be 'continue', 'fail_fast', or 'fail_all'", onFailure))
				}
			}
		} else {
			// Sequential loop: requires while
			if !model.DirectCelIsSet(args.GetWhile()) {
				result.AddError(CategoryStructure, nodePath, "while", "required for sequential loop")
			}
		}

		// Sub-workflow: must have ref or inline
		if !model.CelStringIsSet(args.GetRef()) && args.GetInline() == nil {
			result.AddError(CategoryStructure, nodePath, "ref",
				"workflow/loop node requires either 'ref' or 'inline'")
		}
		// Sequential loops are passthrough — they always inherit the parent's thread.
		// Parallel loops have their own thread config (defaults to mode: new).

	case model.NodeTypeWorkflow:
		args := node.GetWorkflow()
		if args == nil {
			result.AddError(CategoryStructure, nodePath, "args", "workflow node missing args")
			return
		}
		// Sub-workflow: must have ref or inline
		if !model.CelStringIsSet(args.GetRef()) && args.GetInline() == nil {
			result.AddError(CategoryStructure, nodePath, "ref",
				"workflow/loop node requires either 'ref' or 'inline'")
		}

	case model.NodeTypeRouter:
		args := node.GetRouter()
		if args == nil {
			result.AddError(CategoryStructure, nodePath, "args", "router node missing args")
			return
		}

		hasWorkflows := len(args.GetWorkflows()) > 0
		hasNodes := len(args.GetNodes()) > 0

		// Mutually exclusive: exactly one of workflows or nodes must be set.
		if hasWorkflows && hasNodes {
			result.AddError(CategoryStructure, nodePath, "workflows",
				"router node cannot have both 'workflows' and 'nodes' — use exactly one")
		} else if !hasWorkflows && !hasNodes {
			result.AddError(CategoryStructure, nodePath, "workflows",
				"router node requires either 'workflows' or 'nodes' — at least one candidate must be specified")
		}

		// Workflow routing mode validation.
		for i, candidate := range args.GetWorkflows() {
			if candidate.GetRef() == "" {
				result.AddError(CategoryStructure, nodePath, "workflows",
					fmt.Sprintf("router candidate %d has empty ref", i))
			}
		}

		// Node routing mode validation.
		for i, candidate := range args.GetNodes() {
			if candidate.GetId() == "" {
				result.AddError(CategoryStructure, nodePath, "nodes",
					fmt.Sprintf("router node candidate %d has empty id", i))
			}
		}
	case model.NodeTypeJoin:
		// Only "all" and "any" are valid join conditions
		condExpr := model.ConditionExpr(node)
		condition := strings.TrimSpace(strings.ToLower(condExpr))
		if condition != "" && condition != "all" && condition != "any" {
			result.AddError(CategoryStructure, nodePath, "condition",
				fmt.Sprintf("join node condition must be 'all' or 'any', got '%s'", condExpr))
		}

	default:
		// Reject unknown node types — catches typos, the string "null" from
		// template resolution producing nil, and any other invalid values.
		if nodeType != "" && !model.IsKnownNodeType(nodeType) {
			known := model.KnownNodeTypes()
			sort.Strings(known)
			result.AddError(CategoryStructure, nodePath, "type",
				fmt.Sprintf("unknown node type %q; valid types are: %s", nodeType, strings.Join(known, ", ")))
		}
	}
}

// validateProtoInput validates a proto input definition.
func validateProtoInput(name string, input *reliantv1.Input, result *Result) {
	if input == nil {
		return
	}
	// Type must be set
	inputType := model.GetInputType(input)
	if inputType == "" {
		result.AddError(CategoryStructure, parsePath(fmt.Sprintf("inputs.%s", name)), "", "input type is required")
	}
}

// validateInlineWorkflows recursively validates inline workflows in nodes.
func validateInlineWorkflows(node *reliantv1.Node, basePath []string, result *Result) {
	if wfArgs := node.GetWorkflow(); wfArgs != nil && wfArgs.GetInline() != nil {
		validateInlineWorkflow(wfArgs.GetInline(), append(basePath, "inline"), result)
	}
	if loopArgs := node.GetLoop(); loopArgs != nil && loopArgs.GetInline() != nil {
		validateInlineWorkflow(loopArgs.GetInline(), append(basePath, "inline"), result)
	}
}

// validateInlineWorkflow validates an inline workflow with lenient rules.
func validateInlineWorkflow(wf *reliantv1.Workflow, basePath []string, result *Result) {
	seenNodeIDs := make(map[string]int)
	nodeMap := make(map[string]*reliantv1.Node)

	entry := wf.GetEntry()
	nodes := wf.GetNodes()

	if len(entry) == 0 {
		result.AddError(CategoryStructure, basePath, "entry", "entry field is required")
	}

	if len(nodes) == 0 {
		result.AddError(CategoryStructure, basePath, "nodes", "inline workflow must have at least one node")
		return
	}

	// Validate output names
	for key := range wf.GetOutputs() {
		if err := validateOutputName(key); err != nil {
			result.AddError(CategoryStructure, append(basePath, "outputs"), key, err.message)
		}
	}

	for i, node := range nodes {
		nodeID := node.GetId()
		nodePath := append(basePath, "nodes", fmt.Sprintf("[%d](%s)", i, nodeID))

		if firstIdx, exists := seenNodeIDs[nodeID]; exists {
			result.AddError(CategoryStructure, nodePath, "id",
				fmt.Sprintf("duplicate node ID '%s' (first seen at nodes[%d])", nodeID, firstIdx))
		} else {
			seenNodeIDs[nodeID] = i
			nodeMap[nodeID] = node
		}

		// Every node must have a type
		if node.GetType() == "" {
			result.AddError(CategoryStructure, nodePath, "type", "node is missing a 'type' field")
		}

		// Per-node-type validation
		validateNodeArgs(node, nodePath, result)

		validateInlineWorkflows(node, nodePath, result)
	}

	for _, entryID := range entry {
		if _, exists := seenNodeIDs[entryID]; !exists {
			result.AddError(CategoryStructure, basePath, "entry",
				fmt.Sprintf("references unknown node '%s'", entryID))
		}
	}

	for i, edge := range wf.GetEdges() {
		edgePath := append(basePath, "edges", fmt.Sprintf("[%d]", i))

		if _, exists := seenNodeIDs[edge.GetFrom()]; !exists {
			result.AddError(CategoryStructure, edgePath, "from",
				fmt.Sprintf("references unknown node '%s'", edge.GetFrom()))
		}

		for j, c := range edge.GetCases() {
			if c.GetCondition() == "" {
				result.AddError(CategoryStructure, edgePath, fmt.Sprintf("cases[%d].condition", j),
					"all cases must have a condition; use 'default' for unconditional routing")
			}
			for _, to := range c.GetTo() {
				if _, exists := seenNodeIDs[to]; !exists {
					result.AddError(CategoryStructure, edgePath, fmt.Sprintf("cases[%d].to", j),
						fmt.Sprintf("references unknown node '%s'", to))
				}
			}
		}

		for _, to := range edge.GetDefault() {
			if _, exists := seenNodeIDs[to]; !exists {
				result.AddError(CategoryStructure, edgePath, "default",
					fmt.Sprintf("references unknown node '%s'", to))
			}
		}
	}

	validateNodeReachability(wf, nodeMap, result)
	validateNoCycles(wf, result)
}

// =============================================================================
// NODE REACHABILITY
// =============================================================================

func validateNodeReachability(wf *reliantv1.Workflow, nodeMap map[string]*reliantv1.Node, result *Result) {
	nodes := wf.GetNodes()
	if len(nodes) == 0 {
		return
	}

	edgeTargets := make(map[string][]string)
	for _, edge := range wf.GetEdges() {
		for _, c := range edge.GetCases() {
			edgeTargets[edge.GetFrom()] = append(edgeTargets[edge.GetFrom()], c.GetTo()...)
		}
		if len(edge.GetDefault()) > 0 {
			edgeTargets[edge.GetFrom()] = append(edgeTargets[edge.GetFrom()], edge.GetDefault()...)
		}
	}

	// Router nodes with node-routing implicitly connect to their candidate node IDs.
	for _, node := range nodes {
		if node.GetType() != model.NodeTypeRouter {
			continue
		}
		args := node.GetRouter()
		if args == nil {
			continue
		}
		for _, candidate := range args.GetNodes() {
			if cid := candidate.GetId(); cid != "" {
				edgeTargets[node.GetId()] = append(edgeTargets[node.GetId()], cid)
			}
		}
	}

	reachable := make(map[string]bool)
	queue := append([]string{}, wf.GetEntry()...)

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if reachable[current] {
			continue
		}
		reachable[current] = true

		for _, target := range edgeTargets[current] {
			if !reachable[target] {
				queue = append(queue, target)
			}
		}
	}

	// Check each node
	for _, node := range nodes {
		nodeID := node.GetId()
		if !reachable[nodeID] {
			result.AddErrorWithSuggestion(CategoryStructure, []string{wf.GetName(), "nodes", nodeID}, "",
				fmt.Sprintf("node '%s' is unreachable (not connected from entry via edges)", nodeID),
				"add an edge to this node or include it in 'entry'")
		}
	}
}

// =============================================================================
// CYCLE DETECTION
// =============================================================================

func validateNoCycles(wf *reliantv1.Workflow, result *Result) {
	edgeTargets := make(map[string][]string)
	for _, edge := range wf.GetEdges() {
		for _, c := range edge.GetCases() {
			edgeTargets[edge.GetFrom()] = append(edgeTargets[edge.GetFrom()], c.GetTo()...)
		}
		if len(edge.GetDefault()) > 0 {
			edgeTargets[edge.GetFrom()] = append(edgeTargets[edge.GetFrom()], edge.GetDefault()...)
		}
	}

	// Include router node candidates as implicit edges.
	for _, node := range wf.GetNodes() {
		if node.GetType() != model.NodeTypeRouter {
			continue
		}
		if args := node.GetRouter(); args != nil {
			for _, candidate := range args.GetNodes() {
				if cid := candidate.GetId(); cid != "" {
					edgeTargets[node.GetId()] = append(edgeTargets[node.GetId()], cid)
				}
			}
		}
	}

	visited := make(map[string]bool)
	visiting := make(map[string]bool)
	var currentPath []string

	var dfs func(nodeID string) []string
	dfs = func(nodeID string) []string {
		if visited[nodeID] {
			return nil
		}
		if visiting[nodeID] {
			cycleStart := -1
			for i, n := range currentPath {
				if n == nodeID {
					cycleStart = i
					break
				}
			}
			if cycleStart >= 0 {
				cyclePath := append([]string{}, currentPath[cycleStart:]...)
				cyclePath = append(cyclePath, nodeID)
				return cyclePath
			}
			return []string{nodeID}
		}

		visiting[nodeID] = true
		currentPath = append(currentPath, nodeID)

		for _, target := range edgeTargets[nodeID] {
			if cycle := dfs(target); cycle != nil {
				return cycle
			}
		}

		currentPath = currentPath[:len(currentPath)-1]
		delete(visiting, nodeID)
		visited[nodeID] = true
		return nil
	}

	for _, entryID := range wf.GetEntry() {
		if cycle := dfs(entryID); cycle != nil {
			result.AddErrorWithSuggestion(CategoryStructure, []string{wf.GetName()}, "edges",
				fmt.Sprintf("cycle detected: %s", strings.Join(cycle, " -> ")),
				"remove one of the edges to break the cycle")
			return
		}
	}
}

// =============================================================================
// PARALLEL WRITE VALIDATION
// =============================================================================

func validateParallelWrites(wf *reliantv1.Workflow, result *Result) {
	if wf == nil {
		return
	}

	nodeMap := make(map[string]*reliantv1.Node)
	for _, node := range wf.GetNodes() {
		nodeMap[node.GetId()] = node
	}

	type parallelTargets struct {
		source  string
		targets []string
	}

	var parallelGroups []parallelTargets

	entryIDs := wf.GetEntry()
	if len(entryIDs) > 1 {
		parallelGroups = append(parallelGroups, parallelTargets{
			source:  "entry",
			targets: entryIDs,
		})
	}

	edgesBySource := make(map[string][]*reliantv1.Edge)
	for _, edge := range wf.GetEdges() {
		edgesBySource[edge.GetFrom()] = append(edgesBySource[edge.GetFrom()], edge)
	}

	for source, edges := range edgesBySource {
		if len(edges) > 1 {
			targets := make(map[string]bool)
			for _, edge := range edges {
				for _, c := range edge.GetCases() {
					for _, to := range c.GetTo() {
						targets[to] = true
					}
				}
				for _, to := range edge.GetDefault() {
					targets[to] = true
				}
			}
			if len(targets) > 1 {
				var targetList []string
				for t := range targets {
					targetList = append(targetList, t)
				}
				sort.Strings(targetList)
				parallelGroups = append(parallelGroups, parallelTargets{
					source:  source,
					targets: targetList,
				})
			}
		} else {
			edge := edges[0]
			for _, c := range edge.GetCases() {
				if len(c.GetTo()) > 1 {
					parallelGroups = append(parallelGroups, parallelTargets{
						source:  source,
						targets: c.GetTo(),
					})
				}
			}
			if len(edge.GetDefault()) > 1 {
				parallelGroups = append(parallelGroups, parallelTargets{
					source:  source,
					targets: edge.GetDefault(),
				})
			}
		}
	}

	for _, group := range parallelGroups {
		var inheritingWriters []string

		for _, nodeID := range group.targets {
			node, exists := nodeMap[nodeID]
			if !exists {
				continue
			}

			if !nodeWritesToThread(node) {
				continue
			}

			if nodeCreatesOwnThread(node) {
				continue
			}

			inheritingWriters = append(inheritingWriters, nodeID)
		}

		if len(inheritingWriters) > 1 {
			sort.Strings(inheritingWriters)
			result.AddErrorWithSuggestion(CategoryParallelWrite, []string{wf.GetName()}, "",
				fmt.Sprintf("parallel write conflict: nodes [%s] can execute simultaneously and write to the same thread (triggered from '%s')",
					strings.Join(inheritingWriters, ", "), group.source),
				"add thread config with mode: new or mode: fork, or restructure so these nodes don't run in parallel")
		}
	}
}

func nodeWritesToThread(node *reliantv1.Node) bool {
	if node.GetSaveMessage() != nil {
		return true
	}
	switch node.GetType() {
	case model.NodeTypeSaveMessage:
		return true
	case model.NodeTypeWorkflow, model.NodeTypeLoop, model.NodeTypeRouter:
		return true
	}
	return false
}

func nodeCreatesOwnThread(node *reliantv1.Node) bool {
	thread := model.NodeThreadConfig(node)
	if thread == nil {
		return false
	}
	mode := thread.GetMode()
	return mode == "new" || mode == "fork"
}

// =============================================================================
// HELPERS
// =============================================================================

func parsePath(path string) []string {
	if path == "" {
		return nil
	}
	return strings.Split(path, ".")
}

// =============================================================================
// TIMEOUT VALIDATION
// =============================================================================

func validateNodeTimeout(node *reliantv1.Node, nodePath []string, result *Result) {
	timeout := node.GetTimeout()
	if !model.CelStringIsSet(timeout) {
		return
	}

	// Skip CEL expressions - they'll be evaluated at runtime
	if model.CelStringIsExpr(timeout) {
		return
	}

	timeoutStr := model.CelStringValue(timeout)
	duration, err := time.ParseDuration(timeoutStr)
	if err != nil {
		result.AddErrorWithSuggestion(CategoryStructure, nodePath, "timeout",
			fmt.Sprintf("invalid duration format '%s': %v", timeoutStr, err),
			"use Go duration format: e.g., '5m', '1h30m', '30s', '500ms'")
		return
	}

	if duration < 0 {
		result.AddError(CategoryStructure, nodePath, "timeout",
			fmt.Sprintf("timeout cannot be negative: '%s'", timeoutStr))
	}
}

// =============================================================================
// RESPONSE TOOL SCHEMA VALIDATION
// =============================================================================

func validateResponseToolSchema(node *reliantv1.Node, nodePath []string, result *Result) {
	callLLM := node.GetCallLlm()
	if callLLM == nil {
		return
	}

	rt := callLLM.GetResponseTool()
	if rt == nil {
		return
	}

	rtPath := append(nodePath, "response_tool")

	// Validate response tool name is a valid identifier
	nameStr := model.CelStringRaw(rt.GetName())
	if nameStr != "" && !containsTemplate(nameStr) {
		if err := validateIdentifier(nameStr, "response tool name"); err != nil {
			result.AddError(CategoryStructure, rtPath, "name", err.message)
		}
	}

	// Validate schema if present
	if rt.GetSchema() != nil {
		schemaMap := rt.GetSchema().AsMap()
		if schemaMap != nil {
			schemaPath := append(rtPath, "schema")
			validateJSONSchema(schemaMap, schemaPath, result)
		}
	}
}

// validateJSONSchema validates a JSON Schema structure recursively.
func validateJSONSchema(schema map[string]interface{}, path []string, result *Result) {
	// Validate "type" field if present
	if typeVal, ok := schema["type"]; ok {
		switch t := typeVal.(type) {
		case string:
			if !validJSONSchemaTypes[t] {
				result.AddErrorWithSuggestion(CategoryStructure, path, "type",
					fmt.Sprintf("invalid JSON Schema type '%s'", t),
					"valid types: string, number, integer, boolean, array, object, null")
			}
		case []interface{}:
			for i, v := range t {
				if s, ok := v.(string); ok {
					if !validJSONSchemaTypes[s] {
						result.AddError(CategoryStructure, path, fmt.Sprintf("type[%d]", i),
							fmt.Sprintf("invalid JSON Schema type '%s'", s))
					}
				}
			}
		default:
			result.AddError(CategoryStructure, path, "type",
				"'type' must be a string or array of strings")
		}
	}

	// Validate "required" field if present
	if requiredVal, ok := schema["required"]; ok {
		switch r := requiredVal.(type) {
		case []interface{}:
			for i, v := range r {
				if _, ok := v.(string); !ok {
					result.AddError(CategoryStructure, path, fmt.Sprintf("required[%d]", i),
						"'required' array items must be strings")
				}
			}
		case bool, string, int, float64:
			result.AddErrorWithSuggestion(CategoryStructure, path, "required",
				fmt.Sprintf("'required' must be an array of strings, not %T", r),
				"use: required: [\"field1\", \"field2\"]")
		}
	}

	// Validate "properties" recursively
	if propsVal, ok := schema["properties"]; ok {
		if props, ok := propsVal.(map[string]interface{}); ok {
			for propName, propVal := range props {
				if propSchema, ok := propVal.(map[string]interface{}); ok {
					validateJSONSchema(propSchema, append(path, "properties", propName), result)
				}
			}
		}
	}

	// Validate "items" (for arrays)
	if itemsVal, ok := schema["items"]; ok {
		switch items := itemsVal.(type) {
		case map[string]interface{}:
			validateJSONSchema(items, append(path, "items"), result)
		case []interface{}:
			for i, item := range items {
				if itemSchema, ok := item.(map[string]interface{}); ok {
					validateJSONSchema(itemSchema, append(path, "items", fmt.Sprintf("[%d]", i)), result)
				}
			}
		case string:
			result.AddErrorWithSuggestion(CategoryStructure, path, "items",
				fmt.Sprintf("'items' must be a schema object, not a string '%s'", items),
				"use: items: { type: string }")
		}
	}

	// Validate "additionalProperties" if it's a schema
	if addPropsVal, ok := schema["additionalProperties"]; ok {
		if addProps, ok := addPropsVal.(map[string]interface{}); ok {
			validateJSONSchema(addProps, append(path, "additionalProperties"), result)
		}
	}
}
