// Copyright (c) 2025 Reliant Labs

package replaytest

import (
	"go.temporal.io/sdk/interceptor"
	sdkworkflow "go.temporal.io/sdk/workflow"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// panicWorkflowName is the user-draft workflow the workflow_panic scenario
// runs. Runs of it, and only them, panic where they would schedule their
// first CallLLM.
const panicWorkflowName = "replay-panic"

// injectedWorkflowPanic is the value they panic with: to the runtime, an
// ordinary bug in our workflow code.
const injectedWorkflowPanic = "replay fixture: injected workflow panic"

// workflowPanicInjector makes a workflow_panic run panic inside its root
// coroutine, exactly where a bug in step dispatch would. The generator's
// worker runs it to record the fixtures, and every replayer runs it so those
// histories replay the panic they were recorded with; every other workflow is
// untouched, so the rest of the corpus replays as before.
type workflowPanicInjector struct {
	interceptor.WorkerInterceptorBase
}

func (*workflowPanicInjector) InterceptWorkflow(_ sdkworkflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &panicInjectorInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}}
}

type panicInjectorInbound struct {
	interceptor.WorkflowInboundInterceptorBase
	armed bool
}

func (i *panicInjectorInbound) Init(outbound interceptor.WorkflowOutboundInterceptor) error {
	return i.Next.Init(&panicInjectorOutbound{
		WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: outbound},
		inbound:                         i,
	})
}

func (i *panicInjectorInbound) ExecuteWorkflow(ctx sdkworkflow.Context, in *interceptor.ExecuteWorkflowInput) (interface{}, error) {
	if len(in.Args) > 0 {
		switch input := in.Args[0].(type) {
		case v2.WorkflowInput:
			i.armed = input.WorkflowName == panicWorkflowName
		case *v2.WorkflowInput:
			i.armed = input != nil && input.WorkflowName == panicWorkflowName
		}
	}
	return i.Next.ExecuteWorkflow(ctx, in)
}

type panicInjectorOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
	inbound *panicInjectorInbound
}

func (o *panicInjectorOutbound) ExecuteActivity(ctx sdkworkflow.Context, activityType string, args ...interface{}) sdkworkflow.Future {
	if o.inbound.armed && activityType == "CallLLM" {
		panic(injectedWorkflowPanic)
	}
	return o.Next.ExecuteActivity(ctx, activityType, args...)
}
