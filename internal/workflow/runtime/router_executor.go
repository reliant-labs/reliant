package runtime

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/types/known/structpb"
)

// routerDecision is the parsed result from the routing LLM call.
type routerDecision struct {
	Workflow  string `json:"workflow"`
	Preset    string `json:"preset"`
	Prompt    string `json:"prompt"`
	Reasoning string `json:"reasoning"`
}

// nodeRouterDecision is the parsed result from a node routing LLM call.
type nodeRouterDecision struct {
	SelectedNode string `json:"selected_node"`
	Reasoning    string `json:"reasoning"`
}

// RouterExecutor handles the two-phase execution of a router node:
// 1. Dispatch a CallLLM activity to make the routing decision
// 2. Execute the selected workflow via InlineWorkflowExecutor
type RouterExecutor struct {
	ctx            workflow.Context
	workflowID     string
	chatID         string
	workflowName   string
	workflowInputs map[string]interface{}
	nodeOutputs    map[string]interface{}
	childTracker   *ChildWorkflowTracker
	logger         log.Logger

	node       *reliantv1.Node // Original node (pre-evaluation)
	evalResult *reliantv1.Node // CEL-resolved node

	// Set after routing decision
	decision   *routerDecision
	candidates []routerWorkflowInfo

	// Execution context
	execContext *ExecutionContext
	projectPath string
	pauseCtrl   *PauseController

	// Thread tracker
	threadTracker       *ThreadTracker
	makeThreadPauseCtrl func(string) *PauseController
}

// NewRouterExecutor creates a new router executor.
func NewRouterExecutor(
	ctx workflow.Context,
	workflowID string,
	chatID string,
	workflowName string,
	workflowInputs map[string]interface{},
	nodeOutputs map[string]interface{},
	childTracker *ChildWorkflowTracker,
	node *reliantv1.Node,
	evalResult *reliantv1.Node,
) *RouterExecutor {
	return &RouterExecutor{
		ctx:            ctx,
		workflowID:     workflowID,
		chatID:         chatID,
		workflowName:   workflowName,
		workflowInputs: workflowInputs,
		nodeOutputs:    nodeOutputs,
		childTracker:   childTracker,
		logger:         workflow.GetLogger(ctx),
		node:           node,
		evalResult:     evalResult,
	}
}

// WithExecContext sets the execution context.
func (r *RouterExecutor) WithExecContext(ctx *ExecutionContext) *RouterExecutor {
	r.execContext = ctx
	return r
}

// WithProjectPath sets the project path.
func (r *RouterExecutor) WithProjectPath(path string) *RouterExecutor {
	r.projectPath = path
	return r
}

// WithPauseController sets the pause controller.
func (r *RouterExecutor) WithPauseController(pc *PauseController) *RouterExecutor {
	r.pauseCtrl = pc
	return r
}

// WithThreadTracker sets the thread tracker.
func (r *RouterExecutor) WithThreadTracker(tracker *ThreadTracker) *RouterExecutor {
	r.threadTracker = tracker
	return r
}

// WithMakeThreadPauseCtrl sets the per-thread pause controller factory.
func (r *RouterExecutor) WithMakeThreadPauseCtrl(fn func(string) *PauseController) *RouterExecutor {
	r.makeThreadPauseCtrl = fn
	return r
}

// WithWorkflowContext overrides the workflow context (needed for workflow.Go() goroutines).
func (r *RouterExecutor) WithWorkflowContext(ctx workflow.Context) *RouterExecutor {
	r.ctx = ctx
	r.logger = workflow.GetLogger(ctx)
	return r
}

// Execute runs the router execution.
// If node candidates are configured, it runs node routing (simple LLM call + parse).
// Otherwise, it runs the two-phase workflow routing (load candidates, decide, execute).
func (r *RouterExecutor) Execute() (map[string]interface{}, error) {
	args := model.GetRouterArgs(r.evalResult)
	if args == nil {
		return nil, fmt.Errorf("router node %s has no router args", r.node.GetId())
	}

	// Branch: node routing vs workflow routing
	if len(args.GetNodes()) > 0 {
		return r.executeNodeRouting(args)
	}

	// Phase 1: Load candidates and make routing decision
	if err := r.loadCandidates(args); err != nil {
		return nil, fmt.Errorf("failed to load router candidates: %w", err)
	}

	// Check for pause between loading candidates and making routing decision
	r.pauseCtrl.DoCheckPause(r.ctx)

	if err := r.makeRoutingDecision(args); err != nil {
		return nil, fmt.Errorf("routing decision failed: %w", err)
	}

	r.logger.Info("[Router] Routing decision made",
		"nodeID", r.node.GetId(),
		"selectedWorkflow", r.decision.Workflow,
		"selectedPreset", r.decision.Preset,
		"reasoning", r.decision.Reasoning,
	)

	// Update the thread title and routing metadata now that we know the selected workflow/preset.
	// The InlineWorkflowExecutor will pick this up when it emits thread_created.
	if r.execContext != nil {
		r.execContext.ThreadTitle = routerThreadTitle(r.decision)
		r.execContext.RouterDecision = &RouterDecisionMeta{
			Workflow: strings.TrimPrefix(r.decision.Workflow, "builtin://"),
			Preset:   r.decision.Preset,
		}
	}

	// Check for pause between routing decision and workflow execution
	r.pauseCtrl.DoCheckPause(r.ctx)

	// Phase 2: Execute the selected workflow
	childOutputs, err := r.executeSelectedWorkflow()
	if err != nil {
		return nil, fmt.Errorf("selected workflow execution failed: %w", err)
	}

	output := map[string]interface{}{
		"selected_workflow": r.decision.Workflow,
		"selected_preset":   r.decision.Preset,
		"prompt":            r.decision.Prompt,
		"reasoning":         r.decision.Reasoning,
		"outputs":           childOutputs,
	}

	// Evaluate declared outputs if present
	if declaredOutputs := args.GetOutputs(); len(declaredOutputs) > 0 {
		evaluated, err := evaluateOutputsMap(declaredOutputs, output, r.logger)
		if err != nil {
			return nil, fmt.Errorf("failed to evaluate router outputs: %w", err)
		}
		for k, v := range evaluated {
			output[k] = v
		}
	}

	return output, nil
}

// executeNodeRouting runs the simplified node routing path:
// 1. Build system prompt and response schema from node candidates
// 2. Dispatch a CallLLM activity
// 3. Parse and validate the response
// 4. Return the selected node ID and reasoning
func (r *RouterExecutor) executeNodeRouting(args *reliantv1.RouterArgs) (map[string]interface{}, error) {
	nodeCandidates := args.GetNodes()

	// Build routing context
	customPrompt := model.CelStringValue(args.GetSystemPrompt())
	systemPrompt := buildNodeRoutingSystemPrompt(nodeCandidates, customPrompt)
	schema := buildNodeRoutingResponseSchema(nodeCandidates)
	schemaStruct, err := structpb.NewStruct(schema)
	if err != nil {
		return nil, fmt.Errorf("failed to build node routing response schema: %w", err)
	}

	// Build a synthetic CallLLM node for the routing decision
	callLLMNode := &reliantv1.Node{
		Id:   r.node.GetId() + "__node_routing_decision",
		Type: model.NodeTypeCallLLM,
		Args: &reliantv1.Node_CallLlm{
			CallLlm: &reliantv1.CallLLMArgs{
				SystemPrompt: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: systemPrompt}},
				Model:        args.GetModel(),
				ResponseTool: &reliantv1.ResponseTool{
					Name:        &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "node_routing_decision"}},
					Description: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "Select the node to route to"}},
					Schema:      schemaStruct,
				},
				Messages: []*reliantv1.CallLLMMessageInput{
					{
						Role:    "user",
						Content: "Route this request to the most appropriate node based on the conversation history.",
					},
				},
			},
		},
	}

	// Use the parent thread for loading conversation history (same pattern as workflow routing)
	routerThread := ""
	if r.execContext != nil {
		if r.execContext.ParentThread != "" {
			routerThread = r.execContext.ParentThread
		} else {
			routerThread = r.execContext.Thread
		}
	}

	// Delta identity: routers call CallLLM directly (a second phantom
	// source), so allocate an id under the same GetVersion gate and finalize
	// after the call — completed on success, aborted on failure.
	preallocatedID := preallocateAssistantMessageID(r.ctx, r.chatID, routerThread)

	rtx := types.RuntimeContext{
		ChatID:             r.chatID,
		Thread:             routerThread,
		WorkflowID:         r.workflowID,
		StepID:             callLLMNode.GetId(),
		AssistantMessageID: preallocatedID,
	}

	input := types.ActivityInput{
		Runtime: rtx,
		Node:    callLLMNode,
	}

	// Dispatch the CallLLM activity
	baseCtx := r.pauseCtrl.GetActivityCtx(r.ctx)
	activityCtx := workflow.WithActivityOptions(baseCtx, workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		HeartbeatTimeout:    activityHeartbeatTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    3,
		},
	})

	var output reliantv1.CallLLMOutput
	if err := workflow.ExecuteActivity(activityCtx, "CallLLM", input).Get(r.ctx, &output); err != nil {
		emitStreamFinalized(r.ctx, r.chatID, preallocatedID, routerThread, streamReasonAborted, 0)
		return nil, fmt.Errorf("node routing CallLLM failed: %w", err)
	}
	emitStreamFinalized(r.ctx, r.chatID, preallocatedID, routerThread, streamReasonCompleted, output.GetLastStreamSeq())

	// Parse the decision
	decision, err := r.parseNodeRoutingDecision(&output)
	if err != nil {
		return nil, fmt.Errorf("node routing decision parse failed: %w", err)
	}

	// Validate selected node is in the candidate list
	valid := false
	for _, c := range nodeCandidates {
		if c.GetId() == decision.SelectedNode {
			valid = true
			break
		}
	}
	if !valid {
		fallback := strings.TrimSpace(args.GetFallback())
		if fallback == "" {
			return nil, fmt.Errorf("node routing selected unknown node %q", decision.SelectedNode)
		}
		// Validate fallback is also in the candidate list
		fallbackValid := false
		for _, c := range nodeCandidates {
			if c.GetId() == fallback {
				fallbackValid = true
				break
			}
		}
		if !fallbackValid {
			return nil, fmt.Errorf("node routing fallback %q is not a valid candidate node", fallback)
		}
		r.logger.Info("[Router] Invalid node selection, using fallback",
			"selectedNode", decision.SelectedNode,
			"fallback", fallback,
		)
		decision.SelectedNode = fallback
	}

	r.logger.Info("[Router] Node routing decision made",
		"nodeID", r.node.GetId(),
		"selectedNode", decision.SelectedNode,
		"reasoning", decision.Reasoning,
	)

	return map[string]interface{}{
		"selected_node": decision.SelectedNode,
		"reasoning":     decision.Reasoning,
	}, nil
}

// parseNodeRoutingDecision extracts the structured node routing decision from CallLLM output.
func (r *RouterExecutor) parseNodeRoutingDecision(output *reliantv1.CallLLMOutput) (*nodeRouterDecision, error) {
	var decision nodeRouterDecision
	if rd := output.GetResponseData(); rd != nil {
		rdJSON, err := rd.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("failed to marshal response_data: %w", err)
		}
		if err := json.Unmarshal(rdJSON, &decision); err != nil {
			return nil, fmt.Errorf("failed to parse node routing decision from response_data: %w", err)
		}
	} else if responseText := output.GetResponseText(); responseText != "" {
		if err := json.Unmarshal([]byte(responseText), &decision); err != nil {
			if recoveredDecision, ok := r.parseNodeRoutingDecisionFromProse(responseText); ok {
				return recoveredDecision, nil
			}
			return nil, fmt.Errorf("failed to parse node routing decision JSON: %w (raw: %s)", err, responseText)
		}
	} else {
		return nil, fmt.Errorf("node routing decision has no response_data or response_text")
	}

	if decision.SelectedNode == "" {
		return nil, fmt.Errorf("node routing decision has empty selected_node")
	}

	return &decision, nil
}

func (r *RouterExecutor) parseNodeRoutingDecisionFromProse(responseText string) (*nodeRouterDecision, bool) {
	args := model.GetRouterArgs(r.evalResult)
	if args == nil {
		args = model.GetRouterArgs(r.node)
	}
	if args == nil || len(args.GetNodes()) == 0 {
		return nil, false
	}

	selectedNode := ""
	for _, candidate := range args.GetNodes() {
		candidateID := strings.TrimSpace(candidate.GetId())
		if candidateID == "" || !nodeRoutingProseMentionsCandidate(responseText, candidateID) {
			continue
		}
		if selectedNode != "" && selectedNode != candidateID {
			return nil, false
		}
		selectedNode = candidateID
	}
	if selectedNode == "" {
		return nil, false
	}

	return &nodeRouterDecision{
		SelectedNode: selectedNode,
		Reasoning:    "Recovered from unstructured router response mentioning a single candidate node.",
	}, true
}

func nodeRoutingProseMentionsCandidate(responseText string, candidateID string) bool {
	pattern := fmt.Sprintf(`(^|[^A-Za-z0-9_-])%s([^A-Za-z0-9_-]|$)`, regexp.QuoteMeta(candidateID))
	return regexp.MustCompile(pattern).MatchString(responseText)
}

// loadCandidates loads workflow metadata for all configured candidates.
func (r *RouterExecutor) loadCandidates(args *reliantv1.RouterArgs) error {
	candidates := args.GetWorkflows()
	if len(candidates) == 0 {
		return fmt.Errorf("router node %s has no candidate workflows", r.node.GetId())
	}

	// Use pause-aware activity context so activities get cancelled on pause signal
	baseCtx := r.pauseCtrl.GetActivityCtx(r.ctx)
	activityCtx := workflow.WithActivityOptions(baseCtx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
	})

	presetLoader := preset.NewLoaderForProject(r.projectPath)

	r.candidates = make([]routerWorkflowInfo, 0, len(candidates))
	for _, candidate := range candidates {
		ref := candidate.GetRef()
		if ref == "" {
			return fmt.Errorf("router candidate has empty ref")
		}

		// Load workflow via ActivityLoadWorkflow
		loadInput := map[string]string{
			"chat_id":       r.chatID,
			"workflow_name": ref,
		}
		var loadedWf LoadedWorkflow
		if err := workflow.ExecuteActivity(activityCtx, "ActivityLoadWorkflow", loadInput).Get(r.ctx, &loadedWf); err != nil {
			return fmt.Errorf("failed to load workflow %q: %w", ref, err)
		}

		wf, err := LoadWorkflow(loadedWf.WorkflowJSON)
		if err != nil {
			return fmt.Errorf("failed to parse workflow %q: %w", ref, err)
		}

		// Load valid presets for this workflow
		validPresets, presetValidationErrors := presetLoader.LoadForWorkflow(wf)
		if len(presetValidationErrors) > 0 {
			return fmt.Errorf("failed to load presets for workflow %q: %s", ref, presetValidationErrors[0].Message)
		}
		filteredPresets := filterPresetsByAllowed(validPresets, candidate.GetPresets())

		info := routerWorkflowInfo{
			Ref:         ref,
			Workflow:    wf,
			RawYAML:     string(loadedWf.YAML),
			Presets:     filteredPresets,
			Description: candidate.GetDescription(),
		}
		r.candidates = append(r.candidates, info)
	}

	return nil
}

// makeRoutingDecision dispatches a CallLLM activity with the routing context and response schema.
func (r *RouterExecutor) makeRoutingDecision(args *reliantv1.RouterArgs) error {
	// Build the system prompt with workflow metadata
	customPrompt := model.CelStringValue(args.GetSystemPrompt())
	systemPrompt := buildRoutingSystemPrompt(r.candidates, customPrompt)

	// Fixed routing instruction — history provides all the context the LLM needs
	userPrompt := "Route this conversation to the most appropriate workflow and preset based on the conversation history."

	// Build the response schema
	schema := buildRoutingResponseSchema(r.candidates)
	schemaStruct, err := structpb.NewStruct(schema)
	if err != nil {
		return fmt.Errorf("failed to build response schema: %w", err)
	}

	// Build a synthetic CallLLM node for the routing decision
	callLLMNode := &reliantv1.Node{
		Id:   r.node.GetId() + "__routing_decision",
		Type: model.NodeTypeCallLLM,
		Args: &reliantv1.Node_CallLlm{
			CallLlm: &reliantv1.CallLLMArgs{
				SystemPrompt: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: systemPrompt}},
				Model:        args.GetModel(),
				ResponseTool: &reliantv1.ResponseTool{
					Name:        &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "routing_decision"}},
					Description: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "Select the workflow and preset to route to"}},
					Schema:      schemaStruct,
				},
			},
		},
	}

	// Build messages for the routing LLM
	// If include_history is true, the CallLLM activity will load conversation history
	// from the thread. We also inject the user prompt as an additional message.
	messages := []*reliantv1.CallLLMMessageInput{
		{
			Role:    "user",
			Content: userPrompt,
		},
	}
	callLLMNode.GetCallLlm().Messages = messages

	// Use the PARENT thread for loading conversation history.
	// The router's exec context has a child thread (new/fork) for the selected
	// workflow to execute on, but the routing decision needs the conversation
	// history which lives on the parent thread.
	routerThread := ""
	if r.execContext != nil {
		if r.execContext.ParentThread != "" {
			routerThread = r.execContext.ParentThread
		} else {
			routerThread = r.execContext.Thread
		}
	}

	// Build runtime context for the CallLLM activity
	// Delta identity: same phantom-source treatment as node routing above.
	preallocatedID := preallocateAssistantMessageID(r.ctx, r.chatID, routerThread)

	rtx := types.RuntimeContext{
		ChatID:             r.chatID,
		Thread:             routerThread,
		WorkflowID:         r.workflowID,
		StepID:             callLLMNode.GetId(),
		AssistantMessageID: preallocatedID,
	}

	input := types.ActivityInput{
		Runtime: rtx,
		Node:    callLLMNode,
	}

	// Dispatch the CallLLM activity using pause-aware context
	baseCtx := r.pauseCtrl.GetActivityCtx(r.ctx)
	activityCtx := workflow.WithActivityOptions(baseCtx, workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute, // Routing should be fast but give it some room
		HeartbeatTimeout:    activityHeartbeatTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    3,
		},
	})

	var output reliantv1.CallLLMOutput
	if err := workflow.ExecuteActivity(activityCtx, "CallLLM", input).Get(r.ctx, &output); err != nil {
		emitStreamFinalized(r.ctx, r.chatID, preallocatedID, routerThread, streamReasonAborted, 0)
		return fmt.Errorf("routing CallLLM failed: %w", err)
	}
	emitStreamFinalized(r.ctx, r.chatID, preallocatedID, routerThread, streamReasonCompleted, output.GetLastStreamSeq())

	// Parse the routing decision from the response
	return r.parseRoutingDecision(&output)
}

// parseRoutingDecision extracts the structured routing decision from CallLLM output.
func (r *RouterExecutor) parseRoutingDecision(output *reliantv1.CallLLMOutput) error {
	// Prefer structured response_data (populated by CallLLM when a response_tool
	// is configured). Fall back to response_text for plain-text responses.
	var decision routerDecision
	if rd := output.GetResponseData(); rd != nil {
		rdJSON, err := rd.MarshalJSON()
		if err != nil {
			return fmt.Errorf("failed to marshal response_data: %w", err)
		}
		if err := json.Unmarshal(rdJSON, &decision); err != nil {
			return fmt.Errorf("failed to parse routing decision from response_data: %w", err)
		}
	} else if responseText := output.GetResponseText(); responseText != "" {
		if err := json.Unmarshal([]byte(responseText), &decision); err != nil {
			return fmt.Errorf("failed to parse routing decision JSON: %w (raw: %s)", err, responseText)
		}
	} else {
		return fmt.Errorf("routing decision has no response_data or response_text")
	}

	// Validate the decision
	if decision.Workflow == "" {
		return fmt.Errorf("routing decision has empty workflow selection")
	}
	if decision.Preset == "" {
		return fmt.Errorf("routing decision has empty preset selection")
	}

	selectedCandidate, ok := r.candidateForWorkflow(decision.Workflow)
	if !ok {
		return fmt.Errorf("routing decision selected unknown workflow %q", decision.Workflow)
	}

	if !presetAllowedForCandidate(selectedCandidate, decision.Preset) {
		args := model.GetRouterArgs(r.evalResult)
		fallback := ""
		if args != nil {
			fallback = strings.TrimSpace(args.GetFallback())
		}
		if fallback == "" {
			return fmt.Errorf("routing decision selected invalid preset %q for workflow %q", decision.Preset, decision.Workflow)
		}
		if !presetAllowedForCandidate(selectedCandidate, fallback) {
			return fmt.Errorf("router fallback preset %q is invalid for workflow %q", fallback, decision.Workflow)
		}
		r.logger.Info("[Router] Invalid preset selection, using fallback",
			"selectedPreset", decision.Preset,
			"fallback", fallback,
		)
		decision.Preset = fallback
	}

	r.decision = &decision
	return nil
}

// executeSelectedWorkflow runs the workflow selected by the routing decision.
func (r *RouterExecutor) executeSelectedWorkflow() (map[string]interface{}, error) {
	if r.decision == nil {
		return nil, fmt.Errorf("no routing decision available")
	}

	args := model.GetRouterArgs(r.evalResult)

	// Build a synthetic workflow node for the InlineWorkflowExecutor.
	// The router's rewritten prompt is injected into the child thread as a user message
	// so the selected workflow receives the routed request even if it does not expose
	// a dedicated string input for it.
	syntheticThread := args.GetThread()
	if syntheticThread == nil {
		syntheticThread = &reliantv1.ThreadConfig{}
	}
	if prompt := strings.TrimSpace(r.decision.Prompt); prompt != "" {
		syntheticThread = &reliantv1.ThreadConfig{
			Mode: syntheticThread.GetMode(),
			Inject: &reliantv1.InjectConfig{
				Role:    &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "user"}},
				Content: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: prompt}},
			},
		}

		// The parent workflow already created the child thread (in workflow.go initChildWorkflow)
		// before the routing decision was available, so no inject message was saved.
		// Save the routed prompt as a user message now so the selected workflow has history.
		if r.execContext != nil && r.execContext.Thread != "" {
			activityCtx := workflow.WithActivityOptions(r.ctx, workflow.ActivityOptions{
				StartToCloseTimeout: 30 * time.Second,
				RetryPolicy: &temporal.RetryPolicy{
					InitialInterval:    time.Second,
					BackoffCoefficient: 2.0,
					MaximumInterval:    10 * time.Second,
					MaximumAttempts:    3,
				},
			})
			flatInput := &types.SaveMessageInput{
				ChatID:     r.chatID,
				Thread:     r.execContext.Thread,
				Role:       "user",
				Content:    prompt,
				WorkflowID: r.workflowID,
			}
			rtx := types.RuntimeContext{
				ChatID:     r.chatID,
				Thread:     r.execContext.Thread,
				WorkflowID: r.workflowID,
			}
			saveInput := types.ActivityInput{Runtime: rtx, Node: buildSaveMessageNode(flatInput)}
			if err := workflow.ExecuteActivity(activityCtx, "SaveMessage", saveInput).Get(r.ctx, nil); err != nil {
				return nil, fmt.Errorf("failed to save router inject message: %w", err)
			}
			r.logger.Info("[Router] Saved routed prompt to child thread",
				"thread", r.execContext.Thread,
				"promptLength", len(prompt),
			)
		}
	}
	syntheticNode := &reliantv1.Node{
		Id:   r.node.GetId(),
		Type: model.NodeTypeWorkflow,
		Args: &reliantv1.Node_Workflow{
			Workflow: &reliantv1.SubWorkflowArgs{
				Ref: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: r.decision.Workflow}},
				Presets: map[string]string{
					DefaultPresetGroup: r.decision.Preset,
				},
				Thread:  syntheticThread,
				Project: args.GetProject(),
			},
		},
	}

	// Create InlineWorkflowExecutor
	inlineExecutor, err := NewInlineWorkflowExecutor(
		r.ctx,
		r.workflowID,
		r.chatID,
		r.workflowName,
		r.workflowInputs,
		r.nodeOutputs,
		r.childTracker,
		syntheticNode,
		syntheticNode, // evalResult = same since we already resolved everything
		"",            // No parent loop
		-1,            // No loop iteration
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create inline executor for selected workflow: %w", err)
	}

	// Build a runtime contract for the selected workflow
	contract := core.SubWorkflowContract{
		NodePath:         r.node.GetId(),
		NodeID:           r.node.GetId(),
		NodeType:         model.NodeTypeRouter,
		WorkflowIdentity: r.decision.Workflow,
		WorkflowRef:      r.decision.Workflow,
		InputPolicy:      core.InputPolicyRefPresetsArgsDefaults,
		LoadStrategy:     core.LoadStrategyLoadByWorkflowRef,
		InputAssembly: []core.InputAssemblyStage{
			core.InputAssemblyStagePresets,
			core.InputAssemblyStageArgs,
			core.InputAssemblyStageDefaults,
		},
		Presets: map[string]string{
			DefaultPresetGroup: r.decision.Preset,
		},
	}

	// The parent workflow already initialized the router child execution context/thread.
	// Re-deriving here would incorrectly produce a fork-of-a-fork or new-of-a-new thread.
	childExecCtx := r.execContext

	// Wire up the executor
	inlineExecutor = inlineExecutor.
		WithInvocationContract(contract).
		WithExecContext(childExecCtx).
		WithProjectPath(r.projectPath)
	if r.threadTracker != nil {
		inlineExecutor = inlineExecutor.WithThreadTracker(r.threadTracker)
	}
	if r.pauseCtrl != nil {
		inlineExecutor = inlineExecutor.WithPauseController(r.pauseCtrl)
	}
	if r.makeThreadPauseCtrl != nil {
		inlineExecutor = inlineExecutor.WithMakeThreadPauseCtrl(r.makeThreadPauseCtrl)
	}

	// Execute the workflow
	return inlineExecutor.Execute()
}

// routerThreadMode extracts the thread mode from router args, defaulting to "new".
func routerThreadMode(evalResult *reliantv1.Node) string {
	args := model.GetRouterArgs(evalResult)
	if args == nil || args.GetThread() == nil || args.GetThread().GetMode() == "" {
		return model.ThreadModeNew
	}
	return args.GetThread().GetMode()
}

// routerWorkflowIdentity returns a placeholder identity for the router node.
// The actual identity is determined at runtime after the LLM routing decision.
func routerWorkflowIdentity(node *reliantv1.Node) string {
	args := model.GetRouterArgs(node)
	if args == nil {
		return "router"
	}
	// Use first candidate workflow ref as a hint, or a generic identity
	candidates := args.GetWorkflows()
	if len(candidates) > 0 {
		refs := make([]string, 0, len(candidates))
		for _, c := range candidates {
			refs = append(refs, c.GetRef())
		}
		return "router[" + strings.Join(refs, ",") + "]"
	}
	return "router"
}

func (r *RouterExecutor) candidateForWorkflow(workflowRef string) (routerWorkflowInfo, bool) {
	for _, candidate := range r.candidates {
		if candidate.Ref == workflowRef {
			return candidate, true
		}
	}
	return routerWorkflowInfo{}, false
}

func presetAllowedForCandidate(candidate routerWorkflowInfo, presetName string) bool {
	for _, candidatePreset := range candidate.Presets {
		if candidatePreset.Name == presetName {
			return true
		}
	}
	return false
}

// routerThreadTitle builds a human-readable thread title from the routing decision.
// Examples: "agent / code-review", "builtin://agent / general"
func routerThreadTitle(decision *routerDecision) string {
	if decision == nil {
		return ""
	}
	// Strip builtin:// prefix for cleaner display
	workflow := decision.Workflow
	workflow = strings.TrimPrefix(workflow, "builtin://")

	if decision.Preset != "" {
		return workflow + " / " + decision.Preset
	}
	return workflow
}
