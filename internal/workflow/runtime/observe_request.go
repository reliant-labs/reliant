// Copyright (c) 2026 Reliant Labs
package runtime

import (
	"encoding/json"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// ============================================================================
// Delegated observe (dev-docs/design/workflow-observe.md, section 4)
// ============================================================================
//
// A node's observe: block is evaluated by whoever executes the node, after it
// finishes. For an activity-backed node that is the ActivityWrapper
// (activity_observe.go): the workflow attaches a types.ObserveRequest at
// dispatch, as it attaches the SaveMessageRequest, and its presence is the
// whole decision. Workflow code only builds the request: a pure function of
// the definition and the run's context, so it is replay-safe, and an activity
// input is not compared on replay.

// observeRequest builds the request that hands node's effective observe block
// (the workflow's defaults merged with the node's own) to the activity
// executing it, or nil when there is none, or when it is too large to ship.
func (e *StepExecutor) observeRequest(node *reliantv1.Node) *types.ObserveRequest {
	var defaults *reliantv1.NodeObserve
	if e.workflow != nil {
		defaults = e.workflow.GetObserve()
	}
	block := model.MergeObserve(defaults, node.GetObserve())
	if observeBlockEmpty(block) {
		return nil
	}
	// The block rides in Temporal history; validation already warned about a
	// block over the cap, and here it is simply not sent.
	if b, err := json.Marshal(types.ObserveRequest{Block: block}); err != nil || len(b) > model.ObserveMaxRequestBytes {
		e.logger.Warn("[StepExecutor] Node's observe block is over its size cap; not observing it",
			"stepID", node.GetId(), "limitBytes", model.ObserveMaxRequestBytes)
		return nil
	}
	workflowContext := buildWorkflowContext(e.workflowID, e.workflowName, e.chatID, e.workflowInputs, e.execContext)
	exprs, exprErr := observeExpressions(block)
	req := &types.ObserveRequest{
		Block:    block,
		Inputs:   e.observeInputs(exprs, exprErr),
		Workflow: *workflowContextToTyped(workflowContext),
		NodeID:   node.GetId(),
		NodeType: node.GetType(),
		NodePath: joinNodePath(e.nodePathPrefix, node.GetId()),
	}
	if e.loopNodeID != "" {
		iter := iterContextFromMap(e.iterContext())
		// The item ships only when an expression reads iter: it can be
		// arbitrarily large. An unparseable expression keeps it.
		if exprErr == nil {
			if refs, err := wfcel.ReferencesOf(exprs, wfcel.CELIter); err == nil && !refs.Referenced {
				iter.Item = nil
			}
		}
		req.Iter = iter
	}
	return req
}

func observeBlockEmpty(o *reliantv1.NodeObserve) bool {
	return o == nil || (len(o.GetAttributes()) == 0 && len(o.GetEvents()) == 0 && len(o.GetMetrics()) == 0)
}

// observeInputs is the subset of workflow inputs a block reads, plus the
// reserved inputs its trigger, failure and run namespaces are derived from
// when it reads them. An expression that cannot be parsed ships the whole
// map: under-shipping would turn an expression that works into a "no such
// key" in the worker.
func (e *StepExecutor) observeInputs(exprs []string, exprErr error) map[string]interface{} {
	if exprErr != nil {
		return copyMap(e.workflowInputs)
	}
	refs, err := wfcel.ReferencesOf(exprs, wfcel.CELInputs)
	if err != nil || refs.Whole {
		return copyMap(e.workflowInputs)
	}
	inputs := make(map[string]interface{}, len(refs.Fields))
	for _, field := range refs.Fields {
		if v, ok := e.workflowInputs[field]; ok {
			inputs[field] = v
		}
	}
	for ns, key := range map[wfcel.CELNamespace]string{
		wfcel.CELTrigger: wfcel.TriggerInputKey,
		wfcel.CELFailure: wfcel.FailureInputKey,
		wfcel.CELRun:     wfcel.RunInputKey,
	} {
		v, ok := e.workflowInputs[key]
		if !ok {
			continue
		}
		if r, err := wfcel.ReferencesOf(exprs, ns); err != nil || r.Referenced {
			inputs[key] = v
		}
	}
	if len(inputs) == 0 {
		return nil
	}
	return inputs
}

// observeExpressions lists every CEL expression in a block: the {{...}}
// expressions of its templates and its raw-CEL when: conditions.
func observeExpressions(o *reliantv1.NodeObserve) ([]string, error) {
	var exprs []string
	add := func(tmpl string) error {
		got, err := wfcel.TemplateExpressions(tmpl, false)
		if err != nil {
			return err
		}
		exprs = append(exprs, got...)
		return nil
	}
	addMap := func(m map[string]string) error {
		for _, v := range m {
			if err := add(v); err != nil {
				return err
			}
		}
		return nil
	}
	if err := addMap(o.GetAttributes()); err != nil {
		return nil, err
	}
	for _, ev := range o.GetEvents() {
		if ev.GetWhen() != "" {
			exprs = append(exprs, ev.GetWhen())
		}
		if err := add(ev.GetEach()); err != nil {
			return nil, err
		}
		if err := addMap(ev.GetAttributes()); err != nil {
			return nil, err
		}
	}
	for _, m := range o.GetMetrics() {
		if m.GetWhen() != "" {
			exprs = append(exprs, m.GetWhen())
		}
		if err := add(m.GetValue()); err != nil {
			return nil, err
		}
		if err := addMap(m.GetAttributes()); err != nil {
			return nil, err
		}
	}
	return exprs, nil
}
