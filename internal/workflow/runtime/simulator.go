// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"sort"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// NodeState represents the execution state of a node in simulation.
type NodeState string

const (
	// NodeStateCompleted means the node executed successfully.
	NodeStateCompleted NodeState = "completed"
	// NodeStateSkipped means the node was scheduled but skipped due to a false condition.
	NodeStateSkipped NodeState = "skipped"
	// NodeStateError means the node failed with an error.
	NodeStateError NodeState = "error"
)

// WorkflowSimulator simulates workflow execution WITHOUT Temporal
// It's a lightweight event-driven engine for testing workflow logic
type WorkflowSimulator struct {
	protoWorkflow  *reliantv1.Workflow
	stateMachine   *SimplifiedStateMachine
	nodeOutputs    map[string]interface{}
	nodeStates     map[string]NodeState // Explicit state tracking for each node
	visitedSteps   []string             // All scheduled nodes (for backward compatibility)
	events         []*core.WorkflowEvent
	eventSequence  []string
	workflowInputs map[string]interface{}
	maxIterations  int
	iteration      int
	joinState      *JoinState // Track join node state

	// hasInternalEvents checks if there are scenario events with qualified IDs
	// starting with the given prefix (e.g., "impl_1." for "impl_1.agent_loop.call_llm")
	hasInternalEvents func(prefix string) bool

	// workflowLoader loads referenced workflows (e.g., builtin://agent)
	// If nil, workflow nodes are treated as black boxes
	workflowLoader SimWorkflowLoader

	// semantic contracts compiled from core; used to align simulator semantics
	compiledSemantics    *core.CompiledSemantics
	canonicalWorkflowRef string
}

// SimWorkflowLoader loads workflows by reference for simulation (e.g., "builtin://agent")
type SimWorkflowLoader func(ref string) (*reliantv1.Workflow, error)

// SimulatorConfig configures the simulator
type SimulatorConfig struct {
	WorkflowInputs map[string]interface{}
	MaxIterations  int // Prevent infinite loops

	// StartAt specifies a node to start execution from (for partial testing)
	// If empty, starts from the workflow's entry point
	StartAt string

	// InitialState pre-populates node outputs (for partial testing)
	// Key is node ID, value is the node's output
	// Use with StartAt to test from a specific point with known state
	InitialState map[string]map[string]interface{}

	// HasInternalEvents checks if there are scenario events with qualified IDs
	// starting with the given prefix. Used for internal node mocking of workflow nodes.
	// If nil, workflow nodes are always treated as black boxes.
	HasInternalEvents func(prefix string) bool

	// WorkflowLoader loads referenced workflows. If nil, workflow nodes are black boxes.
	WorkflowLoader SimWorkflowLoader

	// CanonicalWorkflowRef is the core semantic identity used for workflow.name.
	// Inline sub-workflows inherit this identity.
	CanonicalWorkflowRef string
}

// NewWorkflowSimulator creates a new workflow simulator from a proto workflow.
func NewWorkflowSimulator(protoWf *reliantv1.Workflow, config SimulatorConfig) *WorkflowSimulator {
	// Default max iterations
	if config.MaxIterations == 0 {
		config.MaxIterations = 100
	}

	if config.WorkflowInputs == nil {
		config.WorkflowInputs = make(map[string]interface{})
	}

	// Pre-populate node outputs from InitialState
	nodeOutputs := make(map[string]interface{})
	for nodeID, outputs := range config.InitialState {
		nodeOutputs[nodeID] = outputs
	}

	// Determine initial event based on StartAt
	var initialEvent *core.WorkflowEvent
	if config.StartAt != "" {
		// Find a predecessor node that leads to StartAt
		predecessor := findPredecessorProto(protoWf, config.StartAt)
		if predecessor != "" {
			// Generate event as if predecessor completed
			var eventData map[string]interface{}
			if data, ok := nodeOutputs[predecessor].(map[string]interface{}); ok {
				eventData = data
			} else {
				eventData = make(map[string]interface{})
			}
			initialEvent = &core.WorkflowEvent{
				ID:         "sim-start",
				WorkflowID: "sim-workflow",
				StepID:     predecessor,
				Data:       eventData,
			}
		} else {
			initialEvent = &core.WorkflowEvent{
				ID:         "sim-start",
				WorkflowID: "sim-workflow",
				StepID:     "",
				Data:       config.WorkflowInputs,
			}
		}
	} else {
		initialEvent = &core.WorkflowEvent{
			ID:         "sim-start",
			WorkflowID: "sim-workflow",
			StepID:     "", // Empty StepID = workflow started
			Data:       config.WorkflowInputs,
		}
	}

	// Initialize join state using proto workflow
	joinState := NewJoinState()
	joinState.InitializeJoins(protoWf)

	canonicalWorkflowRef := strings.TrimSpace(config.CanonicalWorkflowRef)
	if canonicalWorkflowRef == "" {
		canonicalWorkflowRef = strings.TrimSpace(protoWf.GetName())
	}

	compileOptions := core.CompileOptions{CanonicalWorkflowRef: canonicalWorkflowRef}
	if config.WorkflowLoader != nil {
		compileOptions.WorkflowLoader = func(workflowRef string) (*reliantv1.Workflow, error) {
			return config.WorkflowLoader(workflowRef)
		}
	}

	compiledProgram, compileErr := core.Compile(protoWf, compileOptions)
	var compiledSemantics *core.CompiledSemantics
	if compileErr == nil {
		compiledSemantics = compiledProgram.Semantics
	}

	return &WorkflowSimulator{
		protoWorkflow:        protoWf,
		stateMachine:         NewSimplifiedStateMachine("sim-workflow", protoWf),
		nodeOutputs:          nodeOutputs,
		nodeStates:           make(map[string]NodeState),
		visitedSteps:         make([]string, 0),
		events:               []*core.WorkflowEvent{initialEvent},
		eventSequence:        []string{initialEvent.StepID},
		workflowInputs:       config.WorkflowInputs,
		maxIterations:        config.MaxIterations,
		iteration:            0,
		joinState:            joinState,
		hasInternalEvents:    config.HasInternalEvents,
		workflowLoader:       config.WorkflowLoader,
		compiledSemantics:    compiledSemantics,
		canonicalWorkflowRef: canonicalWorkflowRef,
	}
}

// findPredecessorProto finds a node that has an edge leading to targetNode using proto edges.
func findPredecessorProto(wf *reliantv1.Workflow, targetNode string) string {
	for _, edge := range wf.GetEdges() {
		for _, c := range edge.GetCases() {
			for _, to := range c.GetTo() {
				if to == targetNode {
					return edge.GetFrom()
				}
			}
		}
		for _, to := range edge.GetDefault() {
			if to == targetNode {
				return edge.GetFrom()
			}
		}
	}
	return ""
}

// StepMocker is a function that generates mock outputs for steps
type StepMocker func(stepID string, inputs map[string]interface{}) map[string]interface{}

// simLogger implements joinLogger for simulation
type simLogger struct{}

func (l *simLogger) Info(msg string, keyvals ...interface{}) {}

func (s *WorkflowSimulator) normalizeNodePath(nodePath string) string {
	if nodePath == "" {
		return ""
	}
	return strings.ReplaceAll(nodePath, ".", "/")
}

func (s *WorkflowSimulator) rootWorkflowIdentity() string {
	if s.canonicalWorkflowRef != "" {
		return s.canonicalWorkflowRef
	}
	return s.protoWorkflow.GetName()
}

func (s *WorkflowSimulator) subWorkflowContract(nodePath string) (core.SubWorkflowContract, bool) {
	if s.compiledSemantics == nil || len(s.compiledSemantics.SubWorkflows) == 0 {
		return core.SubWorkflowContract{}, false
	}
	contract, ok := s.compiledSemantics.SubWorkflows[s.normalizeNodePath(nodePath)]
	if !ok {
		return core.SubWorkflowContract{}, false
	}
	return contract, true
}

func (s *WorkflowSimulator) invocationMode(nodePath string, node *reliantv1.Node) core.InvocationMode {
	if contract, ok := s.subWorkflowContract(nodePath); ok {
		return contract.InvocationMode
	}
	if args := model.GetLoopArgs(node); args != nil && args.GetInline() != nil {
		return core.InvocationModeInline
	}
	if args := model.GetSubWorkflowArgs(node); args != nil && args.GetInline() != nil {
		return core.InvocationModeInline
	}
	return core.InvocationModeRef
}

func (s *WorkflowSimulator) workflowIdentityForNodePath(nodePath string) string {
	if contract, ok := s.subWorkflowContract(nodePath); ok && contract.WorkflowIdentity != "" {
		return contract.WorkflowIdentity
	}
	return s.rootWorkflowIdentity()
}

func mergeMaps(base map[string]interface{}, additions map[string]any) {
	for key, value := range additions {
		if nestedAdditions, ok := value.(map[string]any); ok {
			existing, hasExisting := base[key]
			if !hasExisting {
				nested := make(map[string]interface{}, len(nestedAdditions))
				base[key] = nested
				mergeMaps(nested, nestedAdditions)
				continue
			}
			existingMap, ok := existing.(map[string]interface{})
			if !ok {
				nested := make(map[string]interface{}, len(nestedAdditions))
				base[key] = nested
				mergeMaps(nested, nestedAdditions)
				continue
			}
			mergeMaps(existingMap, nestedAdditions)
			continue
		}
		base[key] = value
	}
}

func mergeMissingMaps(base map[string]interface{}, defaults map[string]any) {
	for key, value := range defaults {
		existing, hasExisting := base[key]
		nestedDefaults, nested := value.(map[string]any)
		if !hasExisting {
			if nested {
				nestedMap := make(map[string]interface{}, len(nestedDefaults))
				base[key] = nestedMap
				mergeMissingMaps(nestedMap, nestedDefaults)
				continue
			}
			base[key] = value
			continue
		}
		if !nested {
			continue
		}
		existingMap, ok := existing.(map[string]interface{})
		if !ok {
			continue
		}
		mergeMissingMaps(existingMap, nestedDefaults)
	}
}

func normalizeMockOutput(rawOutput map[string]interface{}, activityName string) map[string]interface{} {
	if rawOutput == nil {
		rawOutput = make(map[string]interface{})
	}

	defaults := schema.GetOutputDefaults(activityName)
	if defaults == nil {
		return rawOutput
	}

	normalized := make(map[string]interface{}, len(defaults)+len(rawOutput))
	for field, defaultValue := range defaults {
		normalized[field] = defaultValue
	}
	for field, value := range rawOutput {
		normalized[field] = value
	}

	return normalized
}

func evaluateLoopWhileStrict(
	whileExpr string,
	outputs map[string]interface{},
	iteration int,
	inputs map[string]interface{},
	nodes map[string]interface{},
	contextDescription string,
) (bool, error) {
	loopContext := &wfcel.LoopEvalContext{
		Outputs: outputs,
		Iter:    &model.IterContext{Iteration: iteration},
		Inputs:  inputs,
		Nodes:   nodes,
	}
	shouldContinue, err := wfcel.EvaluateBool(whileExpr, loopContext)
	if err != nil {
		return false, fmt.Errorf("evaluate while condition for %s: %w", contextDescription, err)
	}
	return shouldContinue, nil
}

func nodeActivityName(node *reliantv1.Node) string {
	if node == nil {
		return ""
	}
	if !isActivityType(node.GetType()) {
		return ""
	}
	return nodeTypeToActivityName(node.GetType())
}

func (s *WorkflowSimulator) assembleSubWorkflowInputs(nodePath string, node *reliantv1.Node) map[string]interface{} {
	mode := s.invocationMode(nodePath, node)
	if mode == core.InvocationModeInline {
		inherited := make(map[string]interface{}, len(s.workflowInputs))
		for key, value := range s.workflowInputs {
			inherited[key] = value
		}
		return inherited
	}

	assembled := make(map[string]interface{})
	contract, ok := s.subWorkflowContract(nodePath)
	if !ok {
		return assembled
	}
	if len(contract.Args) > 0 {
		mergeMaps(assembled, contract.Args)
	}
	if len(contract.DefaultInputs) > 0 {
		mergeMissingMaps(assembled, contract.DefaultInputs)
	}
	return assembled
}

// Run executes the workflow simulation
func (s *WorkflowSimulator) Run(mocker StepMocker) error {
	logger := &simLogger{}

	for {
		s.iteration++
		if s.iteration > s.maxIterations {
			return fmt.Errorf("infinite loop detected: exceeded %d iterations, visited steps: %v", s.maxIterations, s.visitedSteps)
		}

		// Process events through join nodes first
		s.events = processJoinEvents(s.events, s.joinState, s.protoWorkflow, "sim-workflow", "sim-chat", s.rootWorkflowIdentity(), s.nodeOutputs, logger, nil, time.Now())

		// Find triggered steps
		triggeredSteps, err := s.stateMachine.FindTriggeredNodes(s.events, s.nodeOutputs, s.workflowInputs)
		if err != nil {
			return fmt.Errorf("find triggered steps in simulator: %w", err)
		}

		// Node routing routers: if a router completion event didn't produce
		// edge-triggered nodes, dynamically dispatch to selected_node.
		// This mirrors workflow.go's node router dynamic dispatch logic.
		for _, evt := range s.events {
			if evt.StepID == "" {
				continue
			}
			routerNode := model.FindNode(s.protoWorkflow, evt.StepID)
			if routerNode == nil || !model.IsNodeRouterMode(routerNode) {
				continue
			}
			// Check if any triggered step came from this router's edge
			edgeTriggered := false
			for _, step := range triggeredSteps {
				if step.Event != nil && step.Event.StepID == evt.StepID {
					edgeTriggered = true
					break
				}
			}
			if edgeTriggered {
				continue
			}
			// No edges matched — dispatch to selected_node
			selectedNodeID, _ := evt.Data["selected_node"].(string)
			if selectedNodeID == "" {
				continue
			}
			targetNode := model.FindNode(s.protoWorkflow, selectedNodeID)
			if targetNode == nil {
				return fmt.Errorf("node router %s selected_node %q not found in workflow", evt.StepID, selectedNodeID)
			}
			triggeredSteps = append(triggeredSteps, &core.TriggeredNode{
				Node: targetNode,
				Event: &core.WorkflowEvent{
					ID:           fmt.Sprintf("node-router-dispatch-%s-%d", evt.StepID, s.iteration),
					WorkflowID:   "sim-workflow",
					ChatID:       "sim-chat",
					WorkflowName: s.rootWorkflowIdentity(),
					StepID:       evt.StepID,
					Data:         evt.Data,
				},
			})
		}

		// Clear events (they've been processed)
		s.events = nil

		// No steps triggered and no events = workflow complete
		if len(triggeredSteps) == 0 {
			return nil
		}

		// Track which nodes we've already processed this iteration to avoid duplicates
		// (can happen when multiple events trigger the same node via different edges)
		processedThisIteration := make(map[string]bool)

		// Execute each triggered step
		for _, triggered := range triggeredSteps {
			stepID := triggered.Node.GetId()

			// Skip if already processed this iteration (dedup)
			if processedThisIteration[stepID] {
				continue
			}
			processedThisIteration[stepID] = true

			// Skip join steps - they are handled by processJoinEvents
			// processJoinEvents records completions, checks satisfaction, and emits
			// synthetic join completion events. We just need to track visitedSteps.
			if triggered.Node.GetType() == model.NodeTypeJoin {
				// Only add to visited if join is satisfied (has output)
				if _, ok := s.nodeOutputs[stepID]; ok {
					s.visitedSteps = append(s.visitedSteps, stepID)
				}
				continue
			}

			// Handle loop steps specially
			if triggered.Node.GetType() == model.NodeTypeLoop {
				// Check if loop already has output from InitialState (state injection)
				// This allows start_at + state to skip loop execution
				var loopOutput map[string]interface{}
				if existingOutput, hasExisting := s.nodeOutputs[stepID]; hasExisting {
					// Use pre-populated state from InitialState - don't re-execute the loop.
					if outputMap, ok := existingOutput.(map[string]interface{}); ok {
						loopOutput = outputMap
					} else {
						loopOutput, err = s.executeLoop(stepID, triggered.Node, mocker)
						if err != nil {
							return fmt.Errorf("execute loop %s: %w", stepID, err)
						}
						s.nodeOutputs[stepID] = loopOutput
					}
				} else {
					loopOutput, err = s.executeLoop(stepID, triggered.Node, mocker)
					if err != nil {
						return fmt.Errorf("execute loop %s: %w", stepID, err)
					}
					s.nodeOutputs[stepID] = loopOutput
				}
				s.visitedSteps = append(s.visitedSteps, stepID)

				// Create loop completion event
				completionEvent := &core.WorkflowEvent{
					ID:           fmt.Sprintf("event-%s-%d", stepID, s.iteration),
					WorkflowID:   "sim-workflow",
					ChatID:       "sim-chat",
					WorkflowName: s.rootWorkflowIdentity(),
					StepID:       stepID,
					Data:         loopOutput,
				}
				s.events = append(s.events, completionEvent)
				s.eventSequence = append(s.eventSequence, completionEvent.StepID)
				continue
			}

			// Check node condition - skip if false
			if model.ConditionExpr(triggered.Node) != "" {
				workflowContext := map[string]interface{}{
					"id":     "sim-workflow",
					"name":   s.rootWorkflowIdentity(),
					"inputs": s.workflowInputs,
				}

				shouldExecute, err := evaluateNodeCondition(
					triggered.Node,
					s.nodeOutputs,
					s.workflowInputs,
					workflowContext,
				)
				if err != nil {
					return fmt.Errorf("node condition evaluation failed for %s: %w", stepID, err)
				}

				if !shouldExecute {
					// Mark as skipped (not visited - skipped nodes don't count as "reached")
					s.nodeStates[stepID] = NodeStateSkipped

					// Create skip output - skipped: true tells joins this source is done
					skippedOutput := model.SkippedOutputMap()

					// Store in outputs
					s.nodeOutputs[stepID] = skippedOutput

					// Create skip event
					skipEvent := &core.WorkflowEvent{
						ID:           fmt.Sprintf("skipped-%s-%d", stepID, s.iteration),
						WorkflowID:   "sim-workflow",
						ChatID:       "sim-chat",
						WorkflowName: s.rootWorkflowIdentity(),
						StepID:       stepID,
						Data:         skippedOutput,
					}
					s.events = append(s.events, skipEvent)
					s.eventSequence = append(s.eventSequence, skipEvent.StepID)
					continue
				}
			}

			// Check for workflow nodes that should be simulated as sub-workflows.
			// - Inline workflows are always simulated to preserve nested condition/skip semantics.
			// - Referenced workflows are simulated when scenarios target internal nodes via qualified IDs.
			if triggered.Node.GetType() == model.NodeTypeWorkflow && (s.invocationMode(stepID, triggered.Node) == core.InvocationModeInline || (s.hasInternalEvents != nil && s.hasInternalEvents(stepID+"."))) {
				workflowOutput, err := s.executeWorkflowNode(stepID, triggered.Node, mocker)
				if err != nil {
					return fmt.Errorf("failed to execute workflow node %s: %w", stepID, err)
				}

				s.visitedSteps = append(s.visitedSteps, stepID)
				s.nodeOutputs[stepID] = workflowOutput

				// Create completion event
				completionEvent := &core.WorkflowEvent{
					ID:           fmt.Sprintf("event-%s-%d", stepID, s.iteration),
					WorkflowID:   "sim-workflow",
					ChatID:       "sim-chat",
					WorkflowName: s.rootWorkflowIdentity(),
					StepID:       stepID,
					Data:         workflowOutput,
				}
				s.events = append(s.events, completionEvent)
				s.eventSequence = append(s.eventSequence, completionEvent.StepID)
				continue
			}

			s.visitedSteps = append(s.visitedSteps, stepID)

			// Evaluate node config using EXPLICIT NAMESPACES:
			// - workflow.*: Workflow context
			// - nodes.*: All step outputs
			evalResult, err := EvaluateNodeConfig(
				triggered.Node,
				s.nodeOutputs,            // All completed step outputs for nodes.* namespace
				"sim-workflow",           // workflowID
				s.rootWorkflowIdentity(), // workflowName
				s.workflowInputs,         // inputs
				nil,                      // Not in a loop
				nil,                      // loopOutputs
				nil,                      // No execContext in simulation
			)
			if err != nil {
				return fmt.Errorf("failed to evaluate config for node %s: %w", stepID, err)
			}
			evaluatedInputs, _ := model.NodeArgsAsMap(evalResult)

			// Generate mock output
			mockOutput := mocker(stepID, evaluatedInputs)

			// For node-routing routers: if the mocker didn't provide selected_node,
			// generate a default output picking the first candidate node.
			if model.IsNodeRouterMode(triggered.Node) {
				if _, hasSelectedNode := mockOutput["selected_node"]; !hasSelectedNode || mockOutput["selected_node"] == "" {
					args := model.GetRouterArgs(triggered.Node)
					if candidates := args.GetNodes(); len(candidates) > 0 {
						if mockOutput == nil {
							mockOutput = make(map[string]interface{})
						}
						mockOutput["selected_node"] = candidates[0].GetId()
						if _, hasReasoning := mockOutput["reasoning"]; !hasReasoning {
							mockOutput["reasoning"] = "simulator default: first candidate"
						}
					}
				}
			}

			normalizedOutput := normalizeMockOutput(mockOutput, nodeActivityName(triggered.Node))

			// Store step output
			s.nodeOutputs[stepID] = normalizedOutput

			// Create completion event
			completionEvent := &core.WorkflowEvent{
				ID:           fmt.Sprintf("event-%s-%d", stepID, s.iteration),
				WorkflowID:   "sim-workflow",
				ChatID:       "sim-chat",
				WorkflowName: s.rootWorkflowIdentity(),
				StepID:       stepID,
				Data:         normalizedOutput,
			}

			s.events = append(s.events, completionEvent)
			s.eventSequence = append(s.eventSequence, completionEvent.StepID)
		}
	}
}

// executeLoop executes a loop step and returns loop output.
// For inline loops, it recursively simulates each inner node individually,
// allowing scenario tests to target inner nodes with qualified IDs (e.g., "loop_id.inner_node_id").
// For referenced (external) loops, it falls back to black-box mocking since
// external workflows can't be loaded without Temporal.
func (s *WorkflowSimulator) executeLoop(nodePath string, protoNode *reliantv1.Node, mocker StepMocker) (map[string]interface{}, error) {
	// Check for parallel loop
	la := model.GetLoopArgs(protoNode)
	if model.CelBoolValue(la.GetParallel()) {
		return s.executeParallelLoop(nodePath, protoNode, mocker)
	}
	if s.invocationMode(nodePath, protoNode) == core.InvocationModeInline {
		return s.executeInlineLoop(nodePath, protoNode, mocker)
	}
	return s.executeRefLoop(nodePath, protoNode, mocker)
}

// executeParallelLoop simulates a parallel loop by running iterations sequentially
// but producing the parallel output format (results map).
// In simulation, there's no real parallelism — we just mock each iteration.
func (s *WorkflowSimulator) executeParallelLoop(nodePath string, protoNode *reliantv1.Node, mocker StepMocker) (map[string]interface{}, error) {
	la := model.GetLoopArgs(protoNode)
	itemsExpr := model.CelStringRaw(la.GetItems())

	if itemsExpr == "" {
		return nil, fmt.Errorf("parallel loop %s: items expression is empty", nodePath)
	}

	// Evaluate items expression
	evalCtx := &wfcel.EdgeEvalContext{
		Nodes:  s.nodeOutputs,
		Inputs: s.workflowInputs,
	}
	rawItems, err := wfcel.EvaluateTemplate(itemsExpr, evalCtx)
	if err != nil {
		return nil, fmt.Errorf("parallel loop %s: failed to evaluate items %q: %w", nodePath, itemsExpr, err)
	}

	items, err := s.parallelLoopItems(rawItems)
	if err != nil {
		return nil, fmt.Errorf("parallel loop %s: %w", nodePath, err)
	}

	results := make(map[string]interface{}, len(items))
	completed := 0

	mockID := model.CelStringRaw(la.GetRef())
	if contract, ok := s.subWorkflowContract(nodePath); ok && contract.WorkflowRef != "" {
		mockID = contract.WorkflowRef
	}
	if mockID == "" {
		mockID = nodePath
	}

	iterationKeys, err := s.parallelLoopKeys(items, la.GetKey())
	if err != nil {
		return nil, fmt.Errorf("parallel loop %s: %w", nodePath, err)
	}

	for i, item := range items {
		key := iterationKeys[i]
		iterItem := s.parallelLoopIterItem(item)

		// Build inputs for this iteration
		iterInputs := s.assembleSubWorkflowInputs(nodePath, protoNode)
		iterInputs["loop"] = map[string]interface{}{"iteration": i}
		iterInputs["iter"] = model.BuildParallelIterContext(i, iterItem, key)

		mockOutput := mocker(mockID, iterInputs)
		results[key] = mockOutput
		completed++
	}

	return model.ParallelLoopOutputToMap(len(items), results, completed, 0), nil
}

func (s *WorkflowSimulator) parallelLoopItems(rawItems interface{}) ([]interface{}, error) {
	switch v := rawItems.(type) {
	case []interface{}:
		return v, nil
	case map[string]interface{}:
		mapKeys := make([]string, 0, len(v))
		for key := range v {
			mapKeys = append(mapKeys, key)
		}
		sort.Strings(mapKeys)
		items := make([]interface{}, 0, len(v))
		for _, key := range mapKeys {
			items = append(items, map[string]interface{}{
				"_map_key":   key,
				"_map_value": v[key],
			})
		}
		return items, nil
	default:
		return nil, fmt.Errorf("items must be list or map, got %T", rawItems)
	}
}

func (s *WorkflowSimulator) parallelLoopKeys(items []interface{}, keyExpr string) ([]string, error) {
	keys := make([]string, len(items))
	seen := make(map[string]int, len(items))

	for i, item := range items {
		key := ""
		if keyExpr == "" {
			key = s.parallelLoopDefaultKey(i, item)
		} else {
			iterItem := s.parallelLoopIterItem(item)
			evalCtx := &wfcel.LoopEvalContext{
				Iter: &model.IterContext{
					Iteration: i,
					Index:     i,
					Item:      iterItem,
					Key:       s.parallelLoopDefaultKey(i, item),
				},
				Inputs: s.workflowInputs,
			}
			result, err := wfcel.EvaluateTemplate(keyExpr, evalCtx)
			if err != nil {
				return nil, fmt.Errorf("failed to evaluate key expression %q for iteration %d: %w", keyExpr, i, err)
			}
			key = fmt.Sprintf("%v", result)
		}

		if prevIndex, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate iteration key %q at indices %d and %d", key, prevIndex, i)
		}
		seen[key] = i
		keys[i] = key
	}

	return keys, nil
}

func (s *WorkflowSimulator) parallelLoopDefaultKey(index int, item interface{}) string {
	if itemMap, ok := item.(map[string]interface{}); ok {
		if mapKey, ok := itemMap["_map_key"].(string); ok {
			return mapKey
		}
	}
	if itemString, ok := item.(string); ok {
		return itemString
	}
	return fmt.Sprintf("%d", index)
}

func (s *WorkflowSimulator) parallelLoopIterItem(item interface{}) interface{} {
	if itemMap, ok := item.(map[string]interface{}); ok {
		if mapValue, hasMapValue := itemMap["_map_value"]; hasMapValue {
			return mapValue
		}
	}
	return item
}

// executeRefLoop handles external workflow reference loops with black-box mocking.
func (s *WorkflowSimulator) executeRefLoop(nodePath string, protoNode *reliantv1.Node, mocker StepMocker) (map[string]interface{}, error) {
	la := model.GetLoopArgs(protoNode)
	iterations := 0
	lastIterOutputs := make(map[string]interface{})
	whileExpr := model.DirectCelExpr(la.GetWhile())

	mockID := model.CelStringRaw(la.GetRef())
	if contract, ok := s.subWorkflowContract(nodePath); ok && contract.WorkflowRef != "" {
		mockID = contract.WorkflowRef
	}
	if mockID == "" {
		mockID = nodePath
	}

	const maxSimulationIterations = 1000

	for iterations < maxSimulationIterations {
		iterationInputs := s.assembleSubWorkflowInputs(nodePath, protoNode)
		iterationInputs["loop"] = map[string]interface{}{
			"iteration": iterations,
			"previous":  lastIterOutputs,
		}

		mockOutput := mocker(mockID, iterationInputs)
		lastIterOutputs = mockOutput
		iterations++

		shouldContinue, err := evaluateLoopWhileStrict(
			whileExpr,
			mockOutput,
			iterations,
			iterationInputs,
			s.nodeOutputs,
			fmt.Sprintf("loop %s", nodePath),
		)
		if err != nil {
			return nil, err
		}
		if !shouldContinue {
			break
		}
	}

	return model.LoopOutputToMap(iterations, lastIterOutputs), nil
}

// executeInlineLoop recursively simulates an inline loop's inner nodes individually.
// Each inner node is executed via the mocker with a qualified ID: "loopID.innerNodeID".
// Inner node visits and outputs are tracked in the parent simulator's state.
func (s *WorkflowSimulator) executeInlineLoop(nodePath string, protoNode *reliantv1.Node, mocker StepMocker) (map[string]interface{}, error) {
	la := model.GetLoopArgs(protoNode)
	subWorkflow := la.GetInline()
	if subWorkflow == nil {
		return s.executeRefLoop(nodePath, protoNode, mocker)
	}

	iterations := 0
	lastIterOutputs := make(map[string]interface{})
	whileExpr := model.DirectCelExpr(la.GetWhile())

	const maxSimulationIterations = 1000

	for iterations < maxSimulationIterations {
		iterOutputs, err := s.executeLoopIteration(nodePath, subWorkflow, protoNode, mocker, iterations, lastIterOutputs)
		if err != nil {
			return nil, err
		}
		lastIterOutputs = iterOutputs
		iterations++

		iterationInputs := s.assembleSubWorkflowInputs(nodePath, protoNode)
		shouldContinue, err := evaluateLoopWhileStrict(
			whileExpr,
			iterOutputs,
			iterations,
			iterationInputs,
			s.nodeOutputs,
			fmt.Sprintf("inline loop %s", nodePath),
		)
		if err != nil {
			return nil, err
		}
		if !shouldContinue {
			break
		}
	}

	return model.LoopOutputToMap(iterations, lastIterOutputs), nil
}

// executeLoopIteration runs all nodes in the sub-workflow for a single loop iteration.
// It mirrors the event-driven execution pattern of the main Run() loop.
// Inner nodes are mocked with qualified IDs: "loopID.innerNodeID".
func (s *WorkflowSimulator) executeLoopIteration(
	loopID string,
	subWorkflow *reliantv1.Workflow,
	loopNode *reliantv1.Node,
	mocker StepMocker,
	iteration int,
	prevIterOutputs map[string]interface{},
) (map[string]interface{}, error) {
	// Create state machine for sub-workflow
	subSM := NewSimplifiedStateMachine("sim-workflow", subWorkflow)

	// Inner node outputs for this iteration (local to sub-workflow for edge evaluation)
	innerOutputs := make(map[string]interface{})

	// Sub-workflow inputs include loop iteration context
	subInputs := s.assembleSubWorkflowInputs(loopID, loopNode)
	subInputs["loop"] = map[string]interface{}{
		"iteration": iteration,
	}
	subInputs["iter"] = model.BuildIterContext(iteration)

	// Initialize join state for sub-workflow
	joinState := NewJoinState()
	joinState.InitializeJoins(subWorkflow)

	workflowIdentity := s.workflowIdentityForNodePath(loopID)

	// Start with workflow-start event
	events := []*core.WorkflowEvent{{
		ID:           fmt.Sprintf("loop-%s-iter%d-start", loopID, iteration),
		WorkflowID:   "sim-workflow",
		ChatID:       "sim-chat",
		WorkflowName: workflowIdentity,
		StepID:       "", // Empty = workflow started
		Data:         subInputs,
	}}

	logger := &simLogger{}
	maxInnerIterations := 100 // Safety limit for inner execution cycles

	for innerIter := 0; innerIter < maxInnerIterations; innerIter++ {
		// Process join events
		events = processJoinEvents(events, joinState, subWorkflow, "sim-workflow", "sim-chat", workflowIdentity, innerOutputs, logger, nil, time.Now())

		// Find triggered nodes in the sub-workflow
		triggeredSteps, err := subSM.FindTriggeredNodes(events, innerOutputs, subInputs)
		if err != nil {
			return nil, fmt.Errorf("find triggered steps for loop %s iteration %d: %w", loopID, iteration, err)
		}
		events = nil

		if len(triggeredSteps) == 0 {
			break // Iteration complete
		}

		// Track which nodes we've already processed this iteration to avoid duplicates
		// (can happen when multiple events trigger the same node via different edges)
		processedThisIteration := make(map[string]bool)

		for _, triggered := range triggeredSteps {
			innerNodeID := triggered.Node.GetId()

			// Skip if already processed this iteration (dedup)
			if processedThisIteration[innerNodeID] {
				continue
			}
			processedThisIteration[innerNodeID] = true

			// Handle join steps - processJoinEvents already handles join completion
			// and emits synthetic completion events when satisfied. We just need to
			// track visitedSteps when the join is actually satisfied.
			if triggered.Node.GetType() == model.NodeTypeJoin {
				qualifiedID := loopID + "." + innerNodeID
				// Only add to visited if join is satisfied (has output from processJoinEvents)
				if output, ok := innerOutputs[innerNodeID]; ok {
					s.visitedSteps = append(s.visitedSteps, qualifiedID)
					s.nodeOutputs[qualifiedID] = output
				}
				continue
			}

			// Handle nested loops recursively
			if triggered.Node.GetType() == model.NodeTypeLoop {
				nestedQualifiedID := loopID + "." + innerNodeID
				// Create a temporary sub-simulator context for the nested loop
				nestedOutput, err := s.executeNestedLoop(nestedQualifiedID, triggered.Node, mocker)
				if err != nil {
					return nil, fmt.Errorf("execute nested loop %s: %w", nestedQualifiedID, err)
				}
				innerOutputs[innerNodeID] = nestedOutput
				s.nodeOutputs[nestedQualifiedID] = nestedOutput
				s.visitedSteps = append(s.visitedSteps, nestedQualifiedID)

				events = append(events, &core.WorkflowEvent{
					ID:           fmt.Sprintf("event-%s-%d", nestedQualifiedID, innerIter),
					WorkflowID:   "sim-workflow",
					ChatID:       "sim-chat",
					WorkflowName: workflowIdentity,
					StepID:       innerNodeID,
					Data:         nestedOutput,
				})
				continue
			}

			// Build qualified ID for this inner node
			qualifiedID := loopID + "." + innerNodeID

			// Check node condition - skip if false
			if model.ConditionExpr(triggered.Node) != "" {
				workflowContext := map[string]interface{}{
					"id":     "sim-workflow",
					"name":   workflowIdentity,
					"inputs": subInputs,
				}

				shouldExecute, err := evaluateNodeCondition(
					triggered.Node,
					innerOutputs,
					subInputs,
					workflowContext,
				)
				if err != nil {
					return nil, fmt.Errorf("node condition evaluation failed for %s: %w", qualifiedID, err)
				}
				if !shouldExecute {
					// Mark as skipped (not visited - skipped nodes don't count as "reached")
					s.nodeStates[qualifiedID] = NodeStateSkipped

					// Create skip output - skipped: true tells joins this source is done
					skippedOutput := model.SkippedOutputMap()

					// Store in outputs
					innerOutputs[innerNodeID] = skippedOutput
					s.nodeOutputs[qualifiedID] = skippedOutput

					// Create skip event
					skipEvent := &core.WorkflowEvent{
						ID:           fmt.Sprintf("skipped-%s-iter%d-%d", qualifiedID, iteration, innerIter),
						WorkflowID:   "sim-workflow",
						ChatID:       "sim-chat",
						WorkflowName: workflowIdentity,
						StepID:       innerNodeID,
						Data:         skippedOutput,
					}
					events = append(events, skipEvent)
					continue
				}
			}

			// Evaluate node config
			evalResult, err := EvaluateNodeConfig(
				triggered.Node,
				innerOutputs,
				"sim-workflow",
				workflowIdentity,
				subInputs,
				nil,             // loop context handled via subInputs
				prevIterOutputs, // previous iteration outputs for outputs.* namespace
				nil,             // no execContext in simulation
			)

			if err != nil {
				return nil, fmt.Errorf("failed to evaluate config for node %s: %w", qualifiedID, err)
			}
			evaluatedInputs, _ := model.NodeArgsAsMap(evalResult)

			// Call mocker with qualified ID
			mockOutput := mocker(qualifiedID, evaluatedInputs)
			normalizedOutput := normalizeMockOutput(mockOutput, nodeActivityName(triggered.Node))

			// Store in sub-workflow's local outputs (for edge evaluation)
			innerOutputs[innerNodeID] = normalizedOutput

			// Store in parent simulator state (for expectations)
			s.nodeOutputs[qualifiedID] = normalizedOutput
			s.visitedSteps = append(s.visitedSteps, qualifiedID)

			// Create completion event for sub-workflow edges
			events = append(events, &core.WorkflowEvent{
				ID:           fmt.Sprintf("event-%s-iter%d-%d", qualifiedID, iteration, innerIter),
				WorkflowID:   "sim-workflow",
				ChatID:       "sim-chat",
				WorkflowName: workflowIdentity,
				StepID:       innerNodeID,
				Data:         normalizedOutput,
			})
		}
	}

	// Evaluate sub-workflow outputs if declared
	if len(subWorkflow.GetOutputs()) > 0 {
		workflowContext := map[string]interface{}{
			"id":     "sim-workflow",
			"name":   workflowIdentity,
			"inputs": subInputs,
			"iter":   map[string]interface{}{"iteration": iteration}, // Pass iter context for output evaluation
		}
		evaluatedOutputs, err := EvaluateWorkflowOutputs(subWorkflow.GetOutputs(), innerOutputs, workflowContext)
		if err != nil {
			return nil, fmt.Errorf("evaluate workflow outputs for loop %s iteration %d: %w", loopID, iteration, err)
		}
		return evaluatedOutputs, nil
	}

	return innerOutputs, nil
}

// executeNestedLoop handles a loop node found inside another loop.
// The qualifiedPrefix is the dot-separated path to this nested loop (e.g., "outer_loop.inner_loop").
func (s *WorkflowSimulator) executeNestedLoop(qualifiedPrefix string, protoNode *reliantv1.Node, mocker StepMocker) (map[string]interface{}, error) {
	la := model.GetLoopArgs(protoNode)

	// Parallel loops are always treated as black boxes in simulation.
	// The mocker provides the complete output (including _iterations, _results, etc.).
	if model.CelBoolValue(la.GetParallel()) {
		mockOutput := mocker(qualifiedPrefix, s.assembleSubWorkflowInputs(qualifiedPrefix, protoNode))
		return mockOutput, nil
	}

	if s.invocationMode(qualifiedPrefix, protoNode) != core.InvocationModeInline {
		return s.executeNestedRefLoop(qualifiedPrefix, protoNode, mocker)
	}

	inlineWf := la.GetInline()
	if inlineWf == nil || len(inlineWf.GetNodes()) == 0 {
		return s.executeNestedRefLoop(qualifiedPrefix, protoNode, mocker)
	}

	whileExpr := model.DirectCelExpr(la.GetWhile())
	iterations := 0
	lastIterOutputs := make(map[string]interface{})
	const maxSimulationIterations = 1000

	for iterations < maxSimulationIterations {
		iterOutputs, err := s.executeNestedLoopIteration(qualifiedPrefix, inlineWf, protoNode, mocker, iterations, lastIterOutputs)
		if err != nil {
			return nil, err
		}
		lastIterOutputs = iterOutputs
		iterations++

		loopInputs := s.assembleSubWorkflowInputs(qualifiedPrefix, protoNode)
		shouldContinue, err := evaluateLoopWhileStrict(
			whileExpr,
			iterOutputs,
			iterations,
			loopInputs,
			s.nodeOutputs,
			fmt.Sprintf("nested loop %s", qualifiedPrefix),
		)
		if err != nil {
			return nil, err
		}
		if !shouldContinue {
			break
		}
	}

	return model.LoopOutputToMap(iterations, lastIterOutputs), nil
}

// executeNestedRefLoop handles a referenced loop inside another loop with black-box mocking.
func (s *WorkflowSimulator) executeNestedRefLoop(qualifiedPrefix string, protoNode *reliantv1.Node, mocker StepMocker) (map[string]interface{}, error) {
	la := model.GetLoopArgs(protoNode)
	whileExpr := model.DirectCelExpr(la.GetWhile())
	iterations := 0
	lastIterOutputs := make(map[string]interface{})

	mockID := model.CelStringRaw(la.GetRef())
	if contract, ok := s.subWorkflowContract(qualifiedPrefix); ok && contract.WorkflowRef != "" {
		mockID = contract.WorkflowRef
	}
	if mockID == "" {
		mockID = qualifiedPrefix
	}

	const maxSimulationIterations = 1000

	for iterations < maxSimulationIterations {
		loopInputs := s.assembleSubWorkflowInputs(qualifiedPrefix, protoNode)
		loopInputs["loop"] = map[string]interface{}{"iteration": iterations, "previous": lastIterOutputs}
		mockOutput := mocker(mockID, loopInputs)
		lastIterOutputs = mockOutput
		iterations++

		shouldContinue, err := evaluateLoopWhileStrict(
			whileExpr,
			mockOutput,
			iterations,
			loopInputs,
			s.nodeOutputs,
			fmt.Sprintf("nested ref loop %s", qualifiedPrefix),
		)
		if err != nil {
			return nil, err
		}
		if !shouldContinue {
			break
		}
	}

	return model.LoopOutputToMap(iterations, lastIterOutputs), nil
}

// executeNestedLoopIteration runs inner nodes for a nested loop iteration.
// Same pattern as executeLoopIteration but uses qualifiedPrefix for ID construction.
func (s *WorkflowSimulator) executeNestedLoopIteration(
	qualifiedPrefix string,
	subWorkflow *reliantv1.Workflow,
	loopNode *reliantv1.Node,
	mocker StepMocker,
	iteration int,
	prevIterOutputs map[string]interface{},
) (map[string]interface{}, error) {
	subSM := NewSimplifiedStateMachine("sim-workflow", subWorkflow)
	innerOutputs := make(map[string]interface{})

	subInputs := s.assembleSubWorkflowInputs(qualifiedPrefix, loopNode)
	subInputs["loop"] = map[string]interface{}{"iteration": iteration}
	subInputs["iter"] = model.BuildIterContext(iteration)

	joinState := NewJoinState()
	joinState.InitializeJoins(subWorkflow)
	workflowIdentity := s.workflowIdentityForNodePath(qualifiedPrefix)

	events := []*core.WorkflowEvent{{
		ID:           fmt.Sprintf("loop-%s-iter%d-start", qualifiedPrefix, iteration),
		WorkflowID:   "sim-workflow",
		ChatID:       "sim-chat",
		WorkflowName: workflowIdentity,
		StepID:       "",
		Data:         subInputs,
	}}

	logger := &simLogger{}
	maxInnerIterations := 100

	for innerIter := 0; innerIter < maxInnerIterations; innerIter++ {
		events = processJoinEvents(events, joinState, subWorkflow, "sim-workflow", "sim-chat", workflowIdentity, innerOutputs, logger, nil, time.Now())
		triggeredSteps, err := subSM.FindTriggeredNodes(events, innerOutputs, subInputs)
		if err != nil {
			return nil, fmt.Errorf("find triggered steps for nested loop %s iteration %d: %w", qualifiedPrefix, iteration, err)
		}
		events = nil

		if len(triggeredSteps) == 0 {
			break
		}

		// Track which nodes we've already processed this iteration to avoid duplicates
		processedThisIteration := make(map[string]bool)

		for _, triggered := range triggeredSteps {
			innerNodeID := triggered.Node.GetId()

			// Skip if already processed this iteration (dedup)
			if processedThisIteration[innerNodeID] {
				continue
			}
			processedThisIteration[innerNodeID] = true

			// Handle join steps - processJoinEvents already handles join completion
			// and emits synthetic completion events when satisfied. We just need to
			// track visitedSteps when the join is actually satisfied.
			if triggered.Node.GetType() == model.NodeTypeJoin {
				qualifiedID := qualifiedPrefix + "." + innerNodeID
				// Only add to visited if join is satisfied (has output from processJoinEvents)
				if output, ok := innerOutputs[innerNodeID]; ok {
					s.visitedSteps = append(s.visitedSteps, qualifiedID)
					s.nodeOutputs[qualifiedID] = output
				}
				continue
			}

			// Handle further nested loops recursively
			if triggered.Node.GetType() == model.NodeTypeLoop {
				nestedQualifiedID := qualifiedPrefix + "." + innerNodeID
				nestedOutput, err := s.executeNestedLoop(nestedQualifiedID, triggered.Node, mocker)
				if err != nil {
					return nil, fmt.Errorf("execute nested loop %s: %w", nestedQualifiedID, err)
				}
				innerOutputs[innerNodeID] = nestedOutput
				s.nodeOutputs[nestedQualifiedID] = nestedOutput
				s.visitedSteps = append(s.visitedSteps, nestedQualifiedID)

				events = append(events, &core.WorkflowEvent{
					ID:         fmt.Sprintf("event-%s-%d", nestedQualifiedID, innerIter),
					WorkflowID: "sim-workflow", ChatID: "sim-chat",
					WorkflowName: workflowIdentity,
					StepID:       innerNodeID, Data: nestedOutput,
				})
				continue
			}

			qualifiedID := qualifiedPrefix + "." + innerNodeID

			// Check node condition - skip if false
			if model.ConditionExpr(triggered.Node) != "" {
				workflowContext := map[string]interface{}{
					"id":     "sim-workflow",
					"name":   workflowIdentity,
					"inputs": subInputs,
				}

				shouldExecute, err := evaluateNodeCondition(
					triggered.Node,
					innerOutputs,
					subInputs,
					workflowContext,
				)
				if err != nil {
					return nil, fmt.Errorf("node condition evaluation failed for %s: %w", qualifiedID, err)
				}
				if !shouldExecute {
					// Mark as skipped (not visited - skipped nodes don't count as "reached")
					s.nodeStates[qualifiedID] = NodeStateSkipped

					// Create skip output - skipped: true tells joins this source is done
					skippedOutput := model.SkippedOutputMap()

					// Store in outputs
					innerOutputs[innerNodeID] = skippedOutput
					s.nodeOutputs[qualifiedID] = skippedOutput

					// Create skip event
					skipEvent := &core.WorkflowEvent{
						ID:           fmt.Sprintf("skipped-%s-iter%d-%d", qualifiedID, iteration, innerIter),
						WorkflowID:   "sim-workflow",
						ChatID:       "sim-chat",
						WorkflowName: workflowIdentity,
						StepID:       innerNodeID,
						Data:         skippedOutput,
					}
					events = append(events, skipEvent)
					continue
				}
			}

			evalResult, err := EvaluateNodeConfig(
				triggered.Node, innerOutputs,
				"sim-workflow", workflowIdentity, subInputs,
				nil, prevIterOutputs, nil,
			)
			if err != nil {
				return nil, fmt.Errorf("failed to evaluate config for node %s: %w", qualifiedID, err)
			}
			evaluatedInputs, _ := model.NodeArgsAsMap(evalResult)

			mockOutput := mocker(qualifiedID, evaluatedInputs)
			normalizedOutput := normalizeMockOutput(mockOutput, nodeActivityName(triggered.Node))

			innerOutputs[innerNodeID] = normalizedOutput
			s.nodeOutputs[qualifiedID] = normalizedOutput
			s.visitedSteps = append(s.visitedSteps, qualifiedID)

			events = append(events, &core.WorkflowEvent{
				ID:           fmt.Sprintf("event-%s-iter%d-%d", qualifiedID, iteration, innerIter),
				WorkflowID:   "sim-workflow",
				ChatID:       "sim-chat",
				WorkflowName: workflowIdentity,
				StepID:       innerNodeID,
				Data:         normalizedOutput,
			})
		}
	}

	// Evaluate sub-workflow outputs if declared
	if len(subWorkflow.GetOutputs()) > 0 {
		workflowContext := map[string]interface{}{
			"id":     "sim-workflow",
			"name":   workflowIdentity,
			"inputs": subInputs,
			"iter":   map[string]interface{}{"iteration": iteration}, // Pass iter context for output evaluation
		}
		evaluatedOutputs, err := EvaluateWorkflowOutputs(subWorkflow.GetOutputs(), innerOutputs, workflowContext)
		if err != nil {
			return nil, fmt.Errorf("evaluate workflow outputs for nested loop %s iteration %d: %w", qualifiedPrefix, iteration, err)
		}
		return evaluatedOutputs, nil
	}

	return innerOutputs, nil
}

// GetVisitedSteps returns the steps visited during simulation
func (s *WorkflowSimulator) GetVisitedSteps() []string {
	return s.visitedSteps
}

// GetEventSequence returns the sequence of events during simulation
func (s *WorkflowSimulator) GetEventSequence() []string {
	return s.eventSequence
}

// GetNodeOutputs returns all step outputs
func (s *WorkflowSimulator) GetNodeOutputs() map[string]interface{} {
	return s.nodeOutputs
}

// GetNodeStates returns the execution state of each node
func (s *WorkflowSimulator) GetNodeStates() map[string]NodeState {
	return s.nodeStates
}

// GetCompletedSteps returns only the nodes that completed successfully
func (s *WorkflowSimulator) GetCompletedSteps() []string {
	var completed []string
	for nodeID, state := range s.nodeStates {
		if state == NodeStateCompleted {
			completed = append(completed, nodeID)
		}
	}
	return completed
}

// GetSkippedSteps returns only the nodes that were skipped
func (s *WorkflowSimulator) GetSkippedSteps() []string {
	var skipped []string
	for nodeID, state := range s.nodeStates {
		if state == NodeStateSkipped {
			skipped = append(skipped, nodeID)
		}
	}
	return skipped
}

// executeWorkflowNode runs a sub-simulation for a workflow-type node with internal events.
// It loads the referenced workflow and simulates it, allowing scenarios to mock internal nodes
// with qualified IDs like "workflow_node.inner_node".
func (s *WorkflowSimulator) executeWorkflowNode(nodePath string, protoNode *reliantv1.Node, mocker StepMocker) (map[string]interface{}, error) {
	nodeID := protoNode.GetId()

	// Resolve sub-workflow definition (inline first, then referenced workflow).
	wfArgs := model.GetSubWorkflowArgs(protoNode)
	if wfArgs == nil {
		return nil, fmt.Errorf("workflow node %s has no workflow args", nodeID)
	}

	subWorkflow := wfArgs.GetInline()
	if subWorkflow == nil {
		if s.workflowLoader == nil {
			return nil, fmt.Errorf("workflow loader not configured, cannot simulate internal nodes for %s", nodeID)
		}

		ref := model.CelStringValue(wfArgs.GetRef())
		if ref == "" {
			if contract, ok := s.subWorkflowContract(nodePath); ok {
				ref = contract.WorkflowRef
			}
		}
		if ref == "" {
			return nil, fmt.Errorf("workflow node %s has no ref", nodeID)
		}

		loadedWorkflow, err := s.workflowLoader(ref)
		if err != nil {
			return nil, fmt.Errorf("failed to load workflow %s: %w", ref, err)
		}
		subWorkflow = loadedWorkflow
	}

	// Create prefix for qualified IDs
	prefix := nodePath

	// Create a sub-mocker that strips the prefix
	subMocker := func(stepID string, inputs map[string]interface{}) map[string]interface{} {
		qualifiedID := prefix + "." + stepID
		return mocker(qualifiedID, inputs)
	}

	// Create sub-simulator state machine
	subSM := NewSimplifiedStateMachine("sim-sub-workflow", subWorkflow)

	// Initialize sub-workflow inputs from semantic contracts
	subInputs := s.assembleSubWorkflowInputs(nodePath, protoNode)

	// Initialize join state for sub-workflow
	joinState := NewJoinState()
	joinState.InitializeJoins(subWorkflow)
	workflowIdentity := s.workflowIdentityForNodePath(nodePath)

	// Create hasInternalEvents for the sub-workflow (strip prefix)
	var subHasInternalEvents func(string) bool
	if s.hasInternalEvents != nil {
		subHasInternalEvents = func(subPrefix string) bool {
			return s.hasInternalEvents(prefix + "." + subPrefix)
		}
	}

	// Track inner outputs
	innerOutputs := make(map[string]interface{})
	logger := &simLogger{}

	// Start with workflow-start event
	events := []*core.WorkflowEvent{{
		ID:           "sim-sub-start",
		WorkflowID:   "sim-sub-workflow",
		ChatID:       "sim-chat",
		WorkflowName: workflowIdentity,
		StepID:       "",
		Data:         subInputs,
	}}

	maxInnerIterations := 100

	for innerIter := 0; innerIter < maxInnerIterations; innerIter++ {
		// Process join events
		events = processJoinEvents(events, joinState, subWorkflow, "sim-sub-workflow", "sim-chat", workflowIdentity, innerOutputs, logger, nil, time.Now())

		// Find triggered nodes
		triggeredSteps, err := subSM.FindTriggeredNodes(events, innerOutputs, subInputs)
		if err != nil {
			return nil, fmt.Errorf("find triggered steps for workflow node %s: %w", nodePath, err)
		}
		events = nil

		if len(triggeredSteps) == 0 {
			break
		}

		// Track processed nodes this iteration
		processedThisIteration := make(map[string]bool)

		for _, triggered := range triggeredSteps {
			innerNodeID := triggered.Node.GetId()
			qualifiedID := prefix + "." + innerNodeID

			// Dedupe
			if processedThisIteration[innerNodeID] {
				continue
			}
			processedThisIteration[innerNodeID] = true

			// Skip joins - handled by processJoinEvents
			if triggered.Node.GetType() == model.NodeTypeJoin {
				if _, ok := innerOutputs[innerNodeID]; ok {
					s.visitedSteps = append(s.visitedSteps, qualifiedID)
				}
				continue
			}

			// Check node condition - skip if false
			if model.ConditionExpr(triggered.Node) != "" {
				workflowContext := map[string]interface{}{
					"id":     "sim-sub-workflow",
					"name":   workflowIdentity,
					"inputs": subInputs,
				}

				shouldExecute, err := evaluateNodeCondition(
					triggered.Node,
					innerOutputs,
					subInputs,
					workflowContext,
				)
				if err != nil {
					return nil, fmt.Errorf("node condition evaluation failed for %s: %w", qualifiedID, err)
				}
				if !shouldExecute {
					s.nodeStates[qualifiedID] = NodeStateSkipped

					skippedOutput := model.SkippedOutputMap()
					innerOutputs[innerNodeID] = skippedOutput
					s.nodeOutputs[qualifiedID] = skippedOutput

					events = append(events, &core.WorkflowEvent{
						ID:           fmt.Sprintf("skipped-%s-%d", qualifiedID, innerIter),
						WorkflowID:   "sim-sub-workflow",
						ChatID:       "sim-chat",
						WorkflowName: workflowIdentity,
						StepID:       innerNodeID,
						Data:         skippedOutput,
					})
					continue
				}
			}

			// Handle loops inside the workflow
			if triggered.Node.GetType() == model.NodeTypeLoop {
				loopOutput, err := s.executeNestedLoop(qualifiedID, triggered.Node, mocker)
				if err != nil {
					return nil, fmt.Errorf("execute nested loop %s: %w", qualifiedID, err)
				}

				innerOutputs[innerNodeID] = loopOutput
				s.nodeOutputs[qualifiedID] = loopOutput
				s.visitedSteps = append(s.visitedSteps, qualifiedID)

				events = append(events, &core.WorkflowEvent{
					ID:           fmt.Sprintf("event-%s-%d", qualifiedID, innerIter),
					WorkflowID:   "sim-sub-workflow",
					ChatID:       "sim-chat",
					WorkflowName: workflowIdentity,
					StepID:       innerNodeID,
					Data:         loopOutput,
				})
				continue
			}

			// Check for nested workflow nodes.
			if triggered.Node.GetType() == model.NodeTypeWorkflow && (s.invocationMode(qualifiedID, triggered.Node) == core.InvocationModeInline || (subHasInternalEvents != nil && subHasInternalEvents(innerNodeID+"."))) {
				// Create a temporary simulator with the nested prefix
				nestedSim := &WorkflowSimulator{
					protoWorkflow:        s.protoWorkflow,
					nodeOutputs:          s.nodeOutputs,
					nodeStates:           s.nodeStates,
					visitedSteps:         s.visitedSteps,
					workflowInputs:       s.workflowInputs,
					hasInternalEvents:    subHasInternalEvents,
					workflowLoader:       s.workflowLoader,
					compiledSemantics:    s.compiledSemantics,
					canonicalWorkflowRef: s.canonicalWorkflowRef,
				}

				// Create a nested mocker with the right prefix
				nestedMocker := func(stepID string, inputs map[string]interface{}) map[string]interface{} {
					return mocker(prefix+"."+stepID, inputs)
				}

				nestedOutput, err := nestedSim.executeWorkflowNode(qualifiedID, triggered.Node, nestedMocker)
				if err != nil {
					return nil, fmt.Errorf("execute nested workflow node %s: %w", qualifiedID, err)
				}

				// Sync visited steps back
				s.visitedSteps = nestedSim.visitedSteps

				innerOutputs[innerNodeID] = nestedOutput
				s.nodeOutputs[qualifiedID] = nestedOutput
				s.visitedSteps = append(s.visitedSteps, qualifiedID)

				events = append(events, &core.WorkflowEvent{
					ID:           fmt.Sprintf("event-%s-%d", qualifiedID, innerIter),
					WorkflowID:   "sim-sub-workflow",
					ChatID:       "sim-chat",
					WorkflowName: workflowIdentity,
					StepID:       innerNodeID,
					Data:         nestedOutput,
				})
				continue
			}

			// Evaluate node config
			evalResult, err := EvaluateNodeConfig(
				triggered.Node,
				innerOutputs,
				"sim-sub-workflow",
				workflowIdentity,
				subInputs,
				nil, // iterContext
				nil, // loopOutputs
				nil, // execContext
			)
			if err != nil {
				return nil, fmt.Errorf("failed to evaluate config for node %s: %w", qualifiedID, err)
			}
			evaluatedInputs, _ := model.NodeArgsAsMap(evalResult)

			// Regular node - use sub-mocker
			mockOutput := subMocker(innerNodeID, evaluatedInputs)
			normalizedOutput := normalizeMockOutput(mockOutput, nodeActivityName(triggered.Node))

			innerOutputs[innerNodeID] = normalizedOutput
			s.nodeOutputs[qualifiedID] = normalizedOutput
			s.visitedSteps = append(s.visitedSteps, qualifiedID)

			events = append(events, &core.WorkflowEvent{
				ID:           fmt.Sprintf("event-%s-%d", qualifiedID, innerIter),
				WorkflowID:   "sim-sub-workflow",
				ChatID:       "sim-chat",
				WorkflowName: workflowIdentity,
				StepID:       innerNodeID,
				Data:         normalizedOutput,
			})
		}
	}

	// Evaluate sub-workflow outputs if declared
	if len(subWorkflow.GetOutputs()) > 0 {
		workflowContext := map[string]interface{}{
			"id":     "sim-sub-workflow",
			"name":   workflowIdentity,
			"inputs": subInputs,
		}
		evaluatedOutputs, err := EvaluateWorkflowOutputs(subWorkflow.GetOutputs(), innerOutputs, workflowContext)
		if err != nil {
			return nil, fmt.Errorf("evaluate workflow outputs for workflow node %s: %w", nodePath, err)
		}
		return evaluatedOutputs, nil
	}

	return innerOutputs, nil
}
