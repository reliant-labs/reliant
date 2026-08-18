// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"strings"

	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/ptr"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/types/known/structpb"
)

// parallelIterationResult holds the outcome of a single parallel iteration.
type parallelIterationResult struct {
	Key       string
	Index     int
	Outputs   map[string]interface{}
	Error     error
	Completed bool
}

// ExecuteParallel runs all loop iterations concurrently using workflow.Go().
// Each iteration gets its own thread, node outputs, and activity ID prefix.
// Results are collected into a map keyed by the evaluated key expression.
func (e *InlineLoopExecutor) ExecuteParallel() (*reliantv1.LoopOutput, error) {
	la := model.GetLoopArgs(e.loopStep.Node)

	e.logger.Info("[InlineLoop] Starting parallel loop execution",
		"loopID", e.loopID,
		"items", model.CelStringRaw(la.GetItems()),
		"key", la.GetKey(),
		"onFailure", la.GetOnFailure(),
		"workflowIdentity", e.workflowIdentity(),
	)

	// Load sub-workflow definition (loaded once, shared across iterations)
	if err := e.loadSubWorkflow(); err != nil {
		return nil, fmt.Errorf("failed to load loop sub-workflow: %w", err)
	}

	// Evaluate the items expression to get the list/map to iterate over
	items, err := e.evaluateItems()
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate items expression: %w", err)
	}

	if len(items) == 0 {
		e.logger.Info("[InlineLoop] Parallel loop has no items, returning empty results",
			"loopID", e.loopID,
		)
		return &reliantv1.LoopOutput{
			Iterations: 0,
			Parallel:   true,
			Results:    map[string]*structpb.Struct{},
			Completed:  0,
			Failed:     0,
		}, nil
	}

	e.logger.Info("[InlineLoop] Parallel loop resolved items",
		"loopID", e.loopID,
		"itemCount", len(items),
	)

	// Determine keys for each iteration
	iterationKeys, err := e.resolveIterationKeys(items, la.GetKey())
	if err != nil {
		return nil, fmt.Errorf("failed to resolve iteration keys: %w", err)
	}

	onFailure := la.GetOnFailure()
	if onFailure == "" {
		onFailure = "continue"
	}

	// Launch all iterations in parallel using workflow.Go() + channel
	resultCh := workflow.NewChannel(e.ctx)

	for i, item := range items {
		idx := i
		iterItem := item
		iterKey := iterationKeys[idx]

		workflow.Go(e.ctx, func(gCtx workflow.Context) {
			result := e.executeParallelIteration(gCtx, idx, iterItem, iterKey)
			resultCh.Send(gCtx, result)
		})
	}

	// Collect results from all iterations
	results := make(map[string]*structpb.Struct, len(items))
	resultsMap := make(map[string]interface{}, len(items))
	var completed, failed int32

	for range items {
		var result *parallelIterationResult
		resultCh.Receive(e.ctx, &result)

		if result.Error != nil {
			failed++
			e.logger.Error("[InlineLoop] Parallel iteration failed",
				"loopID", e.loopID,
				"key", result.Key,
				"index", result.Index,
				"error", result.Error,
			)

			if onFailure == "fail_fast" {
				return nil, fmt.Errorf("parallel loop iteration %q (index %d) failed: %w", result.Key, result.Index, result.Error)
			}
			// For "continue" and "fail_all", keep collecting results
			continue
		}

		completed++

		// Convert outputs to proto Struct for the LoopOutput message
		if result.Outputs != nil {
			protoOutputs, err := structpb.NewStruct(result.Outputs)
			if err != nil {
				e.logger.Warn("[InlineLoop] Failed to convert iteration outputs to proto",
					"loopID", e.loopID,
					"key", result.Key,
					"error", err,
				)
			} else {
				results[result.Key] = protoOutputs
			}
			resultsMap[result.Key] = result.Outputs
		}
	}

	e.logger.Info("[InlineLoop] Parallel loop completed",
		"loopID", e.loopID,
		"total", len(items),
		"completed", completed,
		"failed", failed,
	)

	// Check fail_all policy
	if onFailure == "fail_all" && failed > 0 {
		return nil, fmt.Errorf("parallel loop had %d failed iterations (fail_all policy)", failed)
	}

	return &reliantv1.LoopOutput{
		Iterations: int32(len(items)),
		Parallel:   true,
		Results:    results,
		Completed:  completed,
		Failed:     failed,
	}, nil
}

// evaluateItems resolves the items CEL expression to a list of iteration items.
// Returns a []iterationItem where each item has an index, value, and optional key.
func (e *InlineLoopExecutor) evaluateItems() ([]interface{}, error) {
	la := model.GetLoopArgs(e.loopStep.Node)
	itemsExpr := model.CelStringRaw(la.GetItems())

	if itemsExpr == "" {
		return nil, fmt.Errorf("items expression is empty")
	}

	// Build CEL context for evaluating the items expression
	evalCtx := &wfcel.EdgeEvalContext{
		Nodes:  e.nodeOutputs,
		Inputs: e.workflowInputs,
	}
	rawResult, err := wfcel.EvaluateTemplate(itemsExpr, evalCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate items expression %q: %w", itemsExpr, err)
	}

	// Convert result to a list
	switch v := rawResult.(type) {
	case []interface{}:
		return v, nil
	case map[string]interface{}:
		// Convert map to list of entries, preserving keys in iter.key
		// We'll handle the key extraction separately
		items := make([]interface{}, 0, len(v))
		for key, val := range v {
			items = append(items, map[string]interface{}{
				"_map_key":   key,
				"_map_value": val,
			})
		}
		return items, nil
	default:
		return nil, fmt.Errorf("items expression must evaluate to a list or map, got %T", rawResult)
	}
}

// resolveIterationKeys determines the output key for each iteration.
// If keyExpr is provided, evaluates it per iteration. Otherwise uses string(index).
func (e *InlineLoopExecutor) resolveIterationKeys(items []interface{}, keyExpr string) ([]string, error) {
	keys := make([]string, len(items))

	for i, item := range items {
		if keyExpr == "" {
			// Check if the item is a map entry from a map iteration
			if m, ok := item.(map[string]interface{}); ok {
				if mapKey, ok := m["_map_key"].(string); ok {
					keys[i] = mapKey
					continue
				}
			}
			// Check if item is a string (common case: list of component names)
			if s, ok := item.(string); ok {
				keys[i] = s
				continue
			}
			// Default to string index
			keys[i] = fmt.Sprintf("%d", i)
		} else {
			// Evaluate key expression with iteration context
			iterCtxObj := &model.IterContext{
				Iteration: i,
				Index:     i,
				Item:      e.resolveIterItem(item),
				Key:       e.defaultKeyForItem(i, item),
			}

			evalCtx := &wfcel.LoopEvalContext{
				Iter:   iterCtxObj,
				Inputs: e.workflowInputs,
			}
			result, err := wfcel.EvaluateTemplate(keyExpr, evalCtx)
			if err != nil {
				return nil, fmt.Errorf("failed to evaluate key expression %q for iteration %d: %w", keyExpr, i, err)
			}
			keys[i] = fmt.Sprintf("%v", result)
		}
	}

	// Check for duplicate keys
	seen := make(map[string]int, len(keys))
	for i, key := range keys {
		if prevIdx, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate iteration key %q at indices %d and %d", key, prevIdx, i)
		}
		seen[key] = i
	}

	return keys, nil
}

// resolveIterItem extracts the actual iteration item value.
// For map iterations (items was a map), unwraps the _map_value.
func (e *InlineLoopExecutor) resolveIterItem(item interface{}) interface{} {
	if m, ok := item.(map[string]interface{}); ok {
		if val, hasMapValue := m["_map_value"]; hasMapValue {
			return val
		}
	}
	return item
}

// defaultKeyForItem returns a default key for an iteration item.
func (e *InlineLoopExecutor) defaultKeyForItem(index int, item interface{}) string {
	if m, ok := item.(map[string]interface{}); ok {
		if key, ok := m["_map_key"].(string); ok {
			return key
		}
	}
	if s, ok := item.(string); ok {
		return s
	}
	return fmt.Sprintf("%d", index)
}

// executeParallelIteration runs a single iteration of the parallel loop.
// It creates isolated context and executes the sub-workflow inline.
func (e *InlineLoopExecutor) executeParallelIteration(
	gCtx workflow.Context,
	index int,
	item interface{},
	key string,
) *parallelIterationResult {
	result := &parallelIterationResult{
		Key:   key,
		Index: index,
	}

	resolvedItem := e.resolveIterItem(item)

	e.logger.Info("[InlineLoop] Starting parallel iteration",
		"loopID", e.loopID,
		"index", index,
		"key", key,
	)

	// Build iteration inputs
	iterInputs, err := e.buildParallelIterationInputs(gCtx, index, resolvedItem, key)
	if err != nil {
		result.Error = fmt.Errorf("failed to build iteration inputs: %w", err)
		return result
	}

	// Create isolated node outputs for this iteration
	iterNodeOutputs := make(map[string]interface{})

	// Create state machine for sub-workflow
	iterStateMachine := NewSimplifiedStateMachine(e.workflowID, e.subWorkflow)

	// Unique activity ID prefix for this iteration (prevents collision)
	activityPrefix := fmt.Sprintf("%spar-%s-iter%d-", e.activityIDPrefix, e.loopID, index)

	// Build execution context for this iteration
	iterExecContext := e.buildParallelIterExecContext(gCtx, index, resolvedItem, key)

	// Create step executor
	iterThread := ""
	if iterExecContext != nil {
		iterThread = iterExecContext.Thread
	}
	iterExecutor := NewStepExecutor(
		gCtx,
		e.workflowID,
		e.chatID,
		e.workflowName,
		iterInputs,
		iterNodeOutputs,
		e.childTracker,
	).WithLoopContext(e.loopID, index).
		WithExecContext(iterExecContext).
		WithProjectPath(e.projectPath).
		WithWorkflow(e.subWorkflow).
		WithPauseController(e.pauseCtrl).
		WithMakeThreadPauseCtrl(e.makeThreadPauseCtrl).
		WithThreadInterrupts(resolveThreadInterrupt(e.makeThreadInterrupt, e.threadInterrupt, iterThread)).
		WithMakeThreadInterrupt(e.makeThreadInterrupt)

	// Initialize with start event
	events := []*core.WorkflowEvent{{
		ID:           fmt.Sprintf("ploop-%s-iter%d-start", e.loopID, index),
		WorkflowID:   e.workflowID,
		ChatID:       e.chatID,
		WorkflowName: e.workflowIdentity(),
		StepID:       "", // Empty = workflow started
		Data:         iterInputs,
	}}

	var runningSteps []*RunningStep

	// Initialize join state for the sub-workflow
	joinState := NewJoinState()
	joinState.InitializeJoins(e.subWorkflow)

	// Main execution loop for this iteration (same pattern as sequential)
	for {
		_ = workflow.Sleep(gCtx, 0)

		if gCtx.Err() != nil {
			result.Error = gCtx.Err()
			return result
		}

		e.pauseCtrl.DoCheckPause(gCtx)

		// Process join events
		events = processJoinEvents(events, joinState, e.subWorkflow,
			e.workflowID, e.chatID, e.workflowIdentity(),
			iterNodeOutputs, e.logger, nil, workflow.Now(gCtx))

		// Find triggered steps
		backfillNodeOutputsFromEvents(events, iterNodeOutputs)
		triggeredSteps, err := iterStateMachine.FindTriggeredNodes(events, iterNodeOutputs, iterInputs)
		if err != nil {
			result.Error = fmt.Errorf("find triggered steps for iteration %d: %w", index, err)
			return result
		}
		events = nil

		// Execute triggered steps
		for _, step := range triggeredSteps {
			if step.Node.GetType() == model.NodeTypeJoin {
				continue
			}

			// Check node condition
			skipped, skipEvt, condErr := skipNodeIfConditionFalse(
				gCtx, step.Node, iterNodeOutputs, iterInputs,
				e.workflowID, e.chatID, e.workflowIdentity(), e.logger,
				// Parallel iterations run concurrently, so there is no "previous
				// iteration" to expose as `outputs` — only `iter`.
				&LoopScope{Iter: &model.IterContext{Iteration: index, Index: index, Item: resolvedItem, Key: key}},
			)
			if condErr != nil {
				result.Error = condErr
				return result
			}
			if skipped {
				events = append(events, skipEvt)
				continue
			}

			// Handle nested loops inline (recursively)
			if step.Node.GetType() == model.NodeTypeLoop {
				nestedContract, nestedContractErr := e.subWorkflowSemantics.RequireContractForNode(step.Node.GetId(), model.NodeTypeLoop)
				if nestedContractErr != nil {
					result.Error = nestedContractErr
					return result
				}
				nestedExecutor, err := NewInlineLoopExecutor(
					gCtx, e.workflowID, e.chatID, e.workflowIdentity(),
					iterInputs, iterNodeOutputs, e.childTracker, step,
				)
				if err != nil {
					result.Error = fmt.Errorf("failed to create nested loop executor: %w", err)
					return result
				}
				nestedExecutor = nestedExecutor.
					WithActivityIDPrefix(activityPrefix).
					WithThreadTracker(e.threadTracker).
					WithExecContext(iterExecContext).
					WithProjectPath(e.projectPath).
					WithPauseController(e.pauseCtrl).
					WithMakeThreadPauseCtrl(e.makeThreadPauseCtrl).
					WithThreadInterrupts(resolveThreadInterrupt(e.makeThreadInterrupt, e.threadInterrupt, nestedExecutor.GetThread())).
					WithMakeThreadInterrupt(e.makeThreadInterrupt).
					WithInvocationContract(nestedContract)

				nestedOutput, err := nestedExecutor.Execute()
				if err != nil {
					result.Error = fmt.Errorf("nested loop %s failed: %w", step.Node.GetId(), err)
					return result
				}

				nestedOutputMap := model.ProtoLoopOutputToMap(nestedOutput)
				iterNodeOutputs[step.Node.GetId()] = nestedOutputMap
				events = append(events, &core.WorkflowEvent{
					ID:           fmt.Sprintf("ploop-%s-iter%d-nested-%s", e.loopID, index, step.Node.GetId()),
					WorkflowID:   e.workflowID,
					ChatID:       e.chatID,
					WorkflowName: e.workflowIdentity(),
					StepID:       step.Node.GetId(),
					Data:         nestedOutputMap,
				})
				continue
			}

			// Handle workflow nodes inline
			if step.Node.GetType() == model.NodeTypeWorkflow {
				iterCtx := model.BuildParallelIterContext(index, resolvedItem, key)

				evalResult, err := EvaluateNodeConfig(
					step.Node, iterNodeOutputs, e.workflowID, e.workflowIdentity(),
					iterInputs, iterCtx, nil, iterExecContext,
				)
				if err != nil {
					result.Error = fmt.Errorf("step %s config evaluation failed: %w", step.Node.GetId(), err)
					return result
				}

				inlineExecutor, err := NewInlineWorkflowExecutor(
					gCtx, e.workflowID, e.chatID, e.workflowIdentity(),
					iterInputs, iterNodeOutputs, e.childTracker,
					step.Node, evalResult, e.loopID, index,
				)
				if err != nil {
					result.Error = fmt.Errorf("failed to create executor for step %s: %w", step.Node.GetId(), err)
					return result
				}

				contract, contractErr := e.subWorkflowSemantics.RequireContractForNode(step.Node.GetId(), model.NodeTypeWorkflow)
				if contractErr != nil {
					result.Error = contractErr
					return result
				}

				// Build child execution context
				var childExecCtx *ExecutionContext
				if iterExecContext != nil {
					childExecCtx = iterExecContext.ForChild(
						step.Node.GetId(),
						model.NodeThreadMode(evalResult),
						contract.WorkflowIdentity,
						model.NodeThreadMemo(evalResult),
					)
					childExecCtx.ThreadTitle = fmt.Sprintf("%s [%s]", step.Node.GetId(), key)

					// Create child thread for non-inherit modes
					if model.NodeThreadMode(evalResult) != model.ThreadModeInherit {
						injectMsg := buildInjectMessageConfig(model.NodeInjectConfig(evalResult), e.logger)

						parentWorkflowID := ""
						if iterExecContext.Parent != nil {
							parentWorkflowID = iterExecContext.Parent.WorkflowID
						}

						loopIter := int64(index)
						if initErr := initChildWorkflow(ChildWorkflowInitOpts{
							Ctx:              gCtx,
							ChatID:           e.chatID,
							ParentWorkflowID: parentWorkflowID,
							ChildWorkflowID:  e.workflowID,
							ChildThreadID:    childExecCtx.Thread,
							WorkflowName:     contract.WorkflowIdentity,
							ThreadTitle:      ptr.StringIfNotEmpty(childExecCtx.ThreadTitle),
							ThreadMode:       model.NodeThreadMode(evalResult),
							ForkFromThread:   childExecCtx.ForkedFrom,
							ParentThread:     iterExecContext.Thread,
							SpawnedByNodeID:  step.Node.GetId(),
							LoopIteration:    &loopIter,
							InjectMessage:    injectMsg,
							Logger:           e.logger,
						}); initErr != nil {
							result.Error = fmt.Errorf("failed to init child workflow thread: %w", initErr)
							return result
						}
					} else if flatInput := buildInjectSaveMessageInput(e.chatID, childExecCtx.Thread, e.workflowID, model.NodeInjectConfig(evalResult), e.logger); flatInput != nil {
						// Inherit mode: just save inject message
						activityCtx := workflow.WithActivityOptions(gCtx, workflow.ActivityOptions{
							StartToCloseTimeout: 30 * time.Second,
							RetryPolicy: &temporal.RetryPolicy{
								InitialInterval:    time.Second,
								BackoffCoefficient: 2.0,
								MaximumInterval:    10 * time.Second,
								MaximumAttempts:    3,
							},
						})
						rtx := types.RuntimeContext{
							ChatID:     e.chatID,
							Thread:     childExecCtx.Thread,
							WorkflowID: e.workflowID,
						}
						saveInput := types.ActivityInput{Runtime: rtx, Node: buildSaveMessageNode(flatInput)}
						_ = workflow.ExecuteActivity(activityCtx, "SaveMessage", saveInput).Get(gCtx, nil)
					}

					inlineExecutor = inlineExecutor.WithExecContext(childExecCtx)
				}

				inlineExecutor = inlineExecutor.
					WithProjectPath(e.projectPath).
					WithPauseController(e.pauseCtrl).
					WithMakeThreadPauseCtrl(e.makeThreadPauseCtrl).
					WithThreadInterrupts(resolveThreadInterrupt(e.makeThreadInterrupt, e.threadInterrupt, inlineExecutor.GetThread())).
					WithMakeThreadInterrupt(e.makeThreadInterrupt).
					WithInvocationContract(contract).
					WithWorkflowContext(gCtx) // CRITICAL: use goroutine's context

				inlineOutput, err := inlineExecutor.Execute()
				if err != nil {
					result.Error = fmt.Errorf("inline workflow %s failed: %w", step.Node.GetId(), err)
					return result
				}

				iterNodeOutputs[step.Node.GetId()] = inlineOutput

				// Execute save_message if configured
				if step.Node.GetSaveMessage() != nil {
					_, _ = ExecuteSaveMessageForNode(
						gCtx, step.Node, inlineOutput, iterNodeOutputs,
						e.workflowID, e.workflowIdentity(), e.chatID,
						iterInputs, iterExecContext, e.loopID, index,
					)
				}

				events = append(events, &core.WorkflowEvent{
					ID:           fmt.Sprintf("ploop-%s-iter%d-wf-%s", e.loopID, index, step.Node.GetId()),
					WorkflowID:   e.workflowID,
					ChatID:       e.chatID,
					WorkflowName: e.workflowIdentity(),
					StepID:       step.Node.GetId(),
					Data:         inlineOutput,
				})
				continue
			}

			// Regular activity step
			running := iterExecutor.Start(step)
			runningSteps = append(runningSteps, running)
		}

		// Check completion
		if len(runningSteps) == 0 && len(events) == 0 {
			workflowContext := buildWorkflowContext(e.workflowID, e.workflowIdentity(), e.chatID, iterInputs)
			outputs, err := EvaluateWorkflowOutputs(e.subWorkflow.GetOutputs(), iterNodeOutputs, workflowContext)
			if err != nil {
				result.Error = fmt.Errorf("failed to evaluate sub-workflow outputs: %w", err)
				return result
			}
			result.Outputs = outputs
			result.Completed = true
			return result
		}

		// Wait for step completions
		if len(runningSteps) > 0 {
			completedSteps := waitForStepCompletions(gCtx, runningSteps)

			for _, running := range completedSteps {
				stepEvent := iterExecutor.HandleCompletion(running)
				runningSteps = removeRunningStep(runningSteps, running)

				if stepEvent.Error != nil {
					var canceledErr *temporal.CanceledError
					if canceledErr != nil && false {
						// placeholder
						_ = canceledErr
					}
				}

				if stepEvent.RetryExhausted {
					result.Error = fmt.Errorf("step %s exhausted retries: %w", running.StepID, stepEvent.Error)
					return result
				}

				if routingErr := EnsureStepEventRoutable(stepEvent); routingErr != nil {
					result.Error = routingErr
					return result
				}

				if stepEvent.StepID != "" && stepEvent.Data != nil {
					iterNodeOutputs[stepEvent.StepID] = stepEvent.Data
				}
				events = append(events, stepEvent.ToEvent())
			}
		}
	}
}

// buildParallelIterationInputs builds the inputs for a single parallel iteration.
func (e *InlineLoopExecutor) buildParallelIterationInputs(
	ctx workflow.Context,
	index int,
	item interface{},
	key string,
) (map[string]interface{}, error) {
	if e.inputPolicy() == core.InputPolicyInlineInheritParentInputs {
		iterInputs := make(map[string]interface{}, len(e.workflowInputs)+2)
		for k, v := range e.workflowInputs {
			iterInputs[k] = v
		}
		iterInputs["loop"] = map[string]interface{}{"iteration": index}
		iterInputs["iter"] = model.BuildParallelIterContext(index, item, key)
		return iterInputs, nil
	}

	// For ref-based loops, evaluate the node config with the iteration context
	iterCtx := model.BuildParallelIterContext(index, item, key)
	evalResult, err := EvaluateNodeConfig(
		e.loopStep.Node,
		e.nodeOutputs,
		e.workflowID,
		e.workflowIdentity(),
		e.workflowInputs,
		iterCtx,
		nil,
		e.execContext,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate loop config for iteration %d: %w", index, err)
	}

	iterInputs := make(map[string]interface{})
	if len(model.GetLoopArgs(e.loopStep.Node).GetPresets()) > 0 {
		presetEvalCtx := &wfcel.EdgeEvalContext{
			Nodes:  e.nodeOutputs,
			Inputs: e.workflowInputs,
			Iter: &model.IterContext{
				Iteration: index,
				Index:     index,
				Item:      item,
				Key:       key,
			},
		}
		if err := e.loadAndMergePresets(ctx, iterInputs, presetEvalCtx); err != nil {
			e.logger.Warn("[InlineLoop] Failed to load presets for parallel iteration",
				"loopID", e.loopID,
				"index", index,
				"error", err,
			)
		}
	}

	// Passthrough: forward specified parent inputs to the parallel loop body.
	if passthrough := model.NodePassthrough(evalResult); len(passthrough) > 0 {
		for _, name := range passthrough {
			if val, ok := e.workflowInputs[name]; ok {
				iterInputs[name] = val
			}
		}
	}

	for k, v := range model.NodeMergedSubWorkflowInputs(evalResult) {
		if k == "loop" {
			continue
		}
		iterInputs[k] = v
	}
	if len(e.subWorkflow.GetInputs()) > 0 {
		iterInputs = ApplyDefaultsForRuntime(iterInputs, e.subWorkflow.GetInputs())
	}
	// An unattended run stays unattended in every parallel branch. See unattended.go.
	propagateUnattended(e.workflowInputs, iterInputs)
	iterInputs["loop"] = map[string]interface{}{"iteration": index}
	iterInputs["iter"] = model.BuildParallelIterContext(index, item, key)
	return iterInputs, nil
}

// buildParallelIterExecContext creates an execution context for a parallel iteration.
// Each parallel iteration gets its own thread.
func (e *InlineLoopExecutor) buildParallelIterExecContext(
	gCtx workflow.Context,
	index int,
	item interface{},
	key string,
) *ExecutionContext {
	if e.execContext == nil {
		return nil
	}

	la := model.GetLoopArgs(e.loopStep.Node)

	// Determine thread mode — default to "new" for parallel loops
	threadMode := model.ThreadModeNew
	if tc := la.GetThread(); tc != nil {
		if mode := strings.TrimSpace(tc.GetMode()); mode != "" {
			threadMode = mode
		}
	}

	// Create iteration-specific exec context
	iterExecCtx := e.execContext.ForIteration(index, true).
		WithLoop(e.loopID, index)

	// For parallel iterations, override thread to create a new one per iteration
	// Use ForChild which handles thread creation based on mode
	childExecCtx := iterExecCtx.ForChild(
		fmt.Sprintf("%s-iter-%s", e.loopID, key),
		threadMode,
		e.workflowIdentity(),
		false, // memo=false for parallel iterations (each is unique)
	)
	childExecCtx.ThreadTitle = fmt.Sprintf("%s [%s]", e.loopID, key)

	// Evaluate inject message from the loop's thread config.
	// The inject content may contain CEL templates (e.g. {{iter.item.name}})
	// that need to be resolved with the iteration context.
	var injectMsg *InjectMessageConfig
	if tc := la.GetThread(); tc != nil {
		if ic := tc.GetInject(); ic != nil {
			iterCtx := model.BuildParallelIterContext(index, item, key)
			evalResult, err := EvaluateNodeConfig(
				e.loopStep.Node,
				e.nodeOutputs,
				e.workflowID,
				e.workflowIdentity(),
				e.workflowInputs,
				iterCtx,
				nil,
				e.execContext,
			)
			if err != nil {
				e.logger.Error("[InlineLoop] Failed to evaluate inject config for parallel iteration",
					"loopID", e.loopID,
					"index", index,
					"error", err,
				)
			} else {
				injectMsg = buildInjectMessageConfig(model.NodeInjectConfig(evalResult), e.logger)
			}
		}
	}

	// Initialize the child thread
	parentWorkflowID := ""
	if e.execContext.Parent != nil {
		parentWorkflowID = e.execContext.Parent.WorkflowID
	}

	loopIter := int64(index)
	if initErr := initChildWorkflow(ChildWorkflowInitOpts{
		Ctx:              gCtx,
		ChatID:           e.chatID,
		ParentWorkflowID: parentWorkflowID,
		ChildWorkflowID:  e.workflowID,
		ChildThreadID:    childExecCtx.Thread,
		WorkflowName:     e.workflowIdentity(),
		ThreadTitle:      ptr.StringIfNotEmpty(childExecCtx.ThreadTitle),
		ThreadMode:       threadMode,
		ForkFromThread:   childExecCtx.ForkedFrom,
		ParentThread:     e.execContext.Thread,
		SpawnedByNodeID:  e.loopID,
		LoopIteration:    &loopIter,
		InjectMessage:    injectMsg,
		Logger:           e.logger,
	}); initErr != nil {
		e.logger.Error("[InlineLoop] Failed to initialize parallel iteration thread",
			"loopID", e.loopID,
			"index", index,
			"key", key,
			"error", initErr,
		)
		// Continue anyway — the iteration may still work with inherited thread
	}

	return childExecCtx
}
