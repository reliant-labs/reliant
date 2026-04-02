// Copyright (c) 2025 Reliant Labs
package observability

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/workflow"
)

var temporalTracer = otel.Tracer("reliant.temporal")

// NewOTelWorkerInterceptor returns a Temporal WorkerInterceptor that creates
// OTel spans and records Prometheus metrics for activity and workflow execution.
func NewOTelWorkerInterceptor() interceptor.WorkerInterceptor {
	return &otelWorkerInterceptor{}
}

type otelWorkerInterceptor struct {
	interceptor.WorkerInterceptorBase
}

func (o *otelWorkerInterceptor) InterceptActivity(ctx context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return &otelActivityInbound{ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: next}}
}

func (o *otelWorkerInterceptor) InterceptWorkflow(ctx workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &otelWorkflowInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}}
}

// ─── Activity Interceptor ───────────────────────────────────────────────────

type otelActivityInbound struct {
	interceptor.ActivityInboundInterceptorBase
}

func (a *otelActivityInbound) ExecuteActivity(ctx context.Context, in *interceptor.ExecuteActivityInput) (interface{}, error) {
	actInfo := activity.GetInfo(ctx)
	actType := actInfo.ActivityType.Name

	ctx, span := temporalTracer.Start(ctx, "temporal.activity."+actType,
		trace.WithAttributes(
			attribute.String("temporal.activity.type", actType),
			attribute.String("temporal.workflow.id", actInfo.WorkflowExecution.ID),
			attribute.String("temporal.workflow.run_id", actInfo.WorkflowExecution.RunID),
		),
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer span.End()

	start := time.Now()
	result, err := a.Next.ExecuteActivity(ctx, in)
	duration := time.Since(start).Seconds()

	TemporalActivityDuration.WithLabelValues(actType).Observe(duration)

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}

	return result, err
}

// ─── Workflow Interceptor ───────────────────────────────────────────────────

type otelWorkflowInbound struct {
	interceptor.WorkflowInboundInterceptorBase
}

func (w *otelWorkflowInbound) ExecuteWorkflow(ctx workflow.Context, in *interceptor.ExecuteWorkflowInput) (interface{}, error) {
	wfType := workflow.GetInfo(ctx).WorkflowType.Name

	// Workflow code runs in a deterministic sandbox — real OTel spans (which
	// perform I/O) are forbidden. We record Prometheus counters only.
	result, err := w.Next.ExecuteWorkflow(ctx, in)

	status := "completed"
	if err != nil {
		status = "failed"
	}
	TemporalWorkflowsTotal.WithLabelValues(wfType, status).Inc()

	return result, err
}
