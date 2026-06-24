// Copyright (c) 2025 Reliant Labs
package core

import (
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// WorkflowEvent is the state-machine input event used by runtime and simulator.
type WorkflowEvent struct {
	ID           string                 `json:"id"`
	WorkflowID   string                 `json:"workflow_id"`
	ChatID       string                 `json:"chat_id"`
	WorkflowName string                 `json:"workflow_name"`
	StepID       string                 `json:"step_id"`
	EntityID     string                 `json:"entity_id"`
	Data         map[string]interface{} `json:"data"`
}

// TriggeredNode is a node selected by edge routing for an incoming event.
type TriggeredNode struct {
	Node  *reliantv1.Node `json:"-"`
	Event *WorkflowEvent  `json:"event"`
}

// ProcessInput contains the immutable inputs for one processor step.
type ProcessInput struct {
	Events         []*WorkflowEvent
	NodeOutputs    map[string]interface{}
	WorkflowInputs map[string]interface{}
}

// WorkflowProcessorState tracks pure state-machine state between calls.
// The current edge processor is stateless, so this is intentionally empty.
type WorkflowProcessorState struct{}

// WorkflowProcessor routes workflow events to next nodes by evaluating edges.
type WorkflowProcessor struct {
	workflow *reliantv1.Workflow
	nodeByID map[string]*reliantv1.Node
}

// NewWorkflowProcessor builds a processor from a compiled workflow definition.
func NewWorkflowProcessor(workflow *reliantv1.Workflow) (*WorkflowProcessor, error) {
	if workflow == nil {
		return nil, fmt.Errorf("workflow is required")
	}

	nodeByID := make(map[string]*reliantv1.Node, len(workflow.GetNodes()))
	for _, node := range workflow.GetNodes() {
		if node == nil {
			continue
		}
		nodeByID[node.GetId()] = node
	}

	return &WorkflowProcessor{workflow: workflow, nodeByID: nodeByID}, nil
}

// Process applies one transition step from input events to triggered nodes.
func (p *WorkflowProcessor) Process(
	currentState WorkflowProcessorState,
	input ProcessInput,
) (WorkflowProcessorState, []*TriggeredNode, error) {
	if p == nil || p.workflow == nil {
		return currentState, nil, fmt.Errorf("workflow processor is not initialized")
	}

	triggered := make([]*TriggeredNode, 0)
	triggeredTransitions := make(map[string]bool)

	for _, event := range input.Events {
		if event == nil {
			continue
		}

		if event.StepID == "" {
			for _, entryNodeID := range p.workflow.GetEntry() {
				node := p.nodeByID[entryNodeID]
				if node == nil {
					continue
				}
				triggered = append(triggered, &TriggeredNode{Node: node, Event: event})
			}
			continue
		}

		for _, edge := range p.workflow.GetEdges() {
			if edge.GetFrom() != event.StepID {
				continue
			}

			targetNodeIDs, err := p.matchEdgeTargets(event, edge, input.NodeOutputs, input.WorkflowInputs)
			if err != nil {
				return currentState, nil, err
			}
			for _, targetNodeID := range targetNodeIDs {
				node := p.nodeByID[targetNodeID]
				if node == nil {
					continue
				}
				if event.EntityID == "" {
					transitionKey := event.StepID + "->" + targetNodeID
					if triggeredTransitions[transitionKey] {
						continue
					}
					triggeredTransitions[transitionKey] = true
				}
				triggered = append(triggered, &TriggeredNode{Node: node, Event: event})
			}
		}
	}

	return currentState, triggered, nil
}

func (p *WorkflowProcessor) matchEdgeTargets(
	event *WorkflowEvent,
	edge *reliantv1.Edge,
	nodeOutputs map[string]interface{},
	workflowInputs map[string]interface{},
) ([]string, error) {
	if len(edge.GetCases()) == 0 {
		return edge.GetDefault(), nil
	}

	edgeContext := &wfcel.EdgeEvalContext{
		Nodes:  nodeOutputs,
		Inputs: workflowInputs,
		Workflow: &model.WorkflowContext{
			ID:     event.WorkflowID,
			Name:   event.WorkflowName,
			Branch: branchFromInputs(workflowInputs),
		},
	}

	for _, edgeCase := range edge.GetCases() {
		condition := edgeCase.GetCondition()
		if condition == "" {
			return edgeCase.GetTo(), nil
		}
		matched, err := wfcel.EvaluateBool(condition, edgeContext)
		if err != nil {
			return nil, fmt.Errorf("evaluate edge case condition %q: %w", condition, err)
		}
		if matched {
			return edgeCase.GetTo(), nil
		}
	}

	return edge.GetDefault(), nil
}

func branchFromInputs(workflowInputs map[string]interface{}) string {
	if workflowInputs == nil {
		return ""
	}
	branch, _ := workflowInputs["current_branch"].(string)
	return branch
}
