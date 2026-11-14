// Copyright (c) 2025 Reliant Labs
package observability

import (
	"context"
	"encoding/json"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/log"

	"github.com/reliant-labs/reliant/internal/telemetry"
)

// NewRunScopeWorkerInterceptor returns the Temporal WorkerInterceptor that puts
// the run an activity belongs to on its context: the chat, thread and workflow
// ids its input carries, and the chat's owner (owners answers who). Every record
// the activity logs through logging.*Context then carries them (the logging
// package's scope handler), and so does every line the SDK writes through
// activity.GetLogger.
//
// This is the one place a worker stamps them. An activity's own call sites say
// nothing about who they are working for, and the worker logs they produce were
// 99.99% of error records with no user id: "affected users" of an issue could
// not be counted.
//
// A chat whose owner cannot be read is stamped without a user, never failed; the
// chat id still names the run. owners may be nil.
func NewRunScopeWorkerInterceptor(owners *telemetry.ChatOwnerCache) interceptor.WorkerInterceptor {
	return &runScopeWorkerInterceptor{owners: owners}
}

type runScopeWorkerInterceptor struct {
	interceptor.WorkerInterceptorBase
	owners *telemetry.ChatOwnerCache
}

func (o *runScopeWorkerInterceptor) InterceptActivity(_ context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return &runScopeActivityInbound{ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: next}, owners: o.owners}
}

type runScopeActivityInbound struct {
	interceptor.ActivityInboundInterceptorBase
	owners *telemetry.ChatOwnerCache
}

func (a *runScopeActivityInbound) Init(outbound interceptor.ActivityOutboundInterceptor) error {
	return a.Next.Init(&runScopeActivityOutbound{ActivityOutboundInterceptorBase: interceptor.ActivityOutboundInterceptorBase{Next: outbound}})
}

func (a *runScopeActivityInbound) ExecuteActivity(ctx context.Context, in *interceptor.ExecuteActivityInput) (interface{}, error) {
	scope := RunScopeOfActivityArgs(in.Args)
	if scope.WorkflowID == "" {
		scope.WorkflowID = activity.GetInfo(ctx).WorkflowExecution.ID
	}
	owner := a.owners.ChatOf(ctx, scope.ChatID)
	scope.UserID, scope.OrgID = owner.UserID, owner.OrgID
	ctx = telemetry.WithRunScope(ctx, scope)
	if scope.NodeID == "" {
		return a.Next.ExecuteActivity(ctx, in)
	}
	// A workflow node's activity is one execution of the node: its span
	// (workflow.node <id>) holds the agent turn, tool calls and everything
	// else the node does on this worker. Opened here, worker-side, because
	// workflow code is replayed (internal/telemetry/ai_spans.go).
	ctx, span := telemetry.StartNodeSpan(ctx)
	result, err := a.Next.ExecuteActivity(ctx, in)
	telemetry.EndAISpan(span, err)
	return result, err
}

type runScopeActivityOutbound struct {
	interceptor.ActivityOutboundInterceptorBase
}

// GetLogger is what activity.GetLogger returns: the SDK's logger with the run's
// ids on every line.
func (o *runScopeActivityOutbound) GetLogger(ctx context.Context) log.Logger {
	logger := o.Next.GetLogger(ctx)
	if kv := telemetry.RunScopeFromContext(ctx).Keyvals(); len(kv) > 0 {
		return log.With(logger, kv...)
	}
	return logger
}

// RunScopeOfActivityArgs reads the run ids out of an activity's arguments. A v3
// activity is dispatched as {runtime: {chat_id, thread, workflow_id, ...}, node};
// other inputs carry chat_id and thread_id (or thread) at the top level. Only
// the first argument is read, and only those keys: nothing is guessed.
//
// A workflow node's activity also names its node — the v3 shape by node.type
// and runtime.step_id, a run step by node_type and step_id at the top level —
// and the run's telemetry (runtime.telemetry, or telemetry at the top level).
// NodeID is set only for such an activity.
func RunScopeOfActivityArgs(args []any) telemetry.RunScope {
	if len(args) == 0 {
		return telemetry.RunScope{}
	}
	raw, err := json.Marshal(args[0])
	if err != nil {
		return telemetry.RunScope{}
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return telemetry.RunScope{}
	}
	str := func(m map[string]json.RawMessage, keys ...string) string {
		for _, k := range keys {
			var s string
			if v, ok := m[k]; ok && json.Unmarshal(v, &s) == nil && s != "" {
				return s
			}
		}
		return ""
	}
	scope := telemetry.RunScope{
		ChatID:     str(m, "chat_id", "ChatID"),
		ThreadID:   str(m, "thread", "thread_id", "Thread", "ThreadID"),
		WorkflowID: str(m, "workflow_id", "WorkflowID"),
	}
	// Where the node and telemetry fields sit: in runtime (v3), else at the
	// top level (a run step).
	fields := m
	var rt map[string]json.RawMessage
	if v, ok := m["runtime"]; ok && json.Unmarshal(v, &rt) == nil {
		fields = rt
		if scope.ChatID == "" {
			scope.ChatID = str(rt, "chat_id")
		}
		if scope.ThreadID == "" {
			scope.ThreadID = str(rt, "thread")
		}
		if scope.WorkflowID == "" {
			scope.WorkflowID = str(rt, "workflow_id")
		}
	}

	nodeType := str(m, "node_type")
	var node map[string]json.RawMessage
	if v, ok := m["node"]; ok && json.Unmarshal(v, &node) == nil {
		nodeType = str(node, "type")
	}
	if stepID := str(fields, "step_id"); stepID != "" && nodeType != "" {
		scope.NodeID, scope.NodeType = stepID, nodeType
		if str(fields, "loop_node_id") != "" {
			scope.InLoop = true
			var iteration int
			if v, ok := fields["loop_iteration"]; ok && json.Unmarshal(v, &iteration) == nil {
				scope.NodeIteration = iteration
			}
		}
	}

	var tel struct {
		WorkflowName string `json:"workflow_name"`
		WorkflowRef  string `json:"workflow_ref"`
		Trigger      string `json:"trigger"`
		RunID        string `json:"run_id"`
		ParentRunID  string `json:"parent_run_id"`
		RootRunID    string `json:"root_run_id"`
		Preset       string `json:"preset"`
	}
	if v, ok := fields["telemetry"]; ok && json.Unmarshal(v, &tel) == nil {
		scope.WorkflowName, scope.WorkflowRef, scope.Trigger = tel.WorkflowName, tel.WorkflowRef, tel.Trigger
		scope.RunID, scope.ParentRunID, scope.RootRunID = tel.RunID, tel.ParentRunID, tel.RootRunID
		scope.Preset = tel.Preset
	}
	return scope
}
