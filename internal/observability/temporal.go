// Copyright (c) 2025 Reliant Labs
package observability

import (
	"context"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/workflow"
)

// NewMetricsWorkerInterceptor returns a Temporal WorkerInterceptor that
// records Prometheus metrics for activity and workflow execution.
//
// It opens no spans. Tracing is forge's temporalotel interceptor, installed
// once on the Temporal client (internal/temporal.NewExternalClient): it
// continues the caller's trace through the workflow header into every
// activity, which a worker-side interceptor cannot do — it never sees the
// header, so its activity spans were each a trace of their own.
func NewMetricsWorkerInterceptor() interceptor.WorkerInterceptor {
	return &metricsWorkerInterceptor{}
}

type metricsWorkerInterceptor struct {
	interceptor.WorkerInterceptorBase
}

func (o *metricsWorkerInterceptor) InterceptActivity(ctx context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return &metricsActivityInbound{ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: next}}
}

func (o *metricsWorkerInterceptor) InterceptWorkflow(ctx workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &metricsWorkflowInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}}
}

// ─── Activity Interceptor ───────────────────────────────────────────────────

type metricsActivityInbound struct {
	interceptor.ActivityInboundInterceptorBase
}

func (a *metricsActivityInbound) ExecuteActivity(ctx context.Context, in *interceptor.ExecuteActivityInput) (interface{}, error) {
	actType := activity.GetInfo(ctx).ActivityType.Name

	start := time.Now()
	result, err := a.Next.ExecuteActivity(ctx, in)
	TemporalActivityDuration.WithLabelValues(actType).Observe(time.Since(start).Seconds())

	return result, err
}

// ─── Workflow Interceptor ───────────────────────────────────────────────────

type metricsWorkflowInbound struct {
	interceptor.WorkflowInboundInterceptorBase
}

func (w *metricsWorkflowInbound) ExecuteWorkflow(ctx workflow.Context, in *interceptor.ExecuteWorkflowInput) (interface{}, error) {
	wfType := workflow.GetInfo(ctx).WorkflowType.Name

	result, err := w.Next.ExecuteWorkflow(ctx, in)

	status := "completed"
	if err != nil {
		status = "failed"
	}
	TemporalWorkflowsTotal.WithLabelValues(wfType, status).Inc()

	return result, err
}
