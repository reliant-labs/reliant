// Copyright (c) 2025 Reliant Labs
package analytics

import (
	"context"
	"time"
)

type contextKey string

const (
	contextKeyStartTime contextKey = "analytics:startTime"
	contextKeyOperation contextKey = "analytics:operation"
)

func WithOperationContext(ctx context.Context, operation string) context.Context {
	ctx = context.WithValue(ctx, contextKeyOperation, operation)
	ctx = context.WithValue(ctx, contextKeyStartTime, time.Now())
	return ctx
}

func GetOperationDuration(ctx context.Context) time.Duration {
	if startTime, ok := ctx.Value(contextKeyStartTime).(time.Time); ok {
		return time.Since(startTime)
	}
	return 0
}

func GetOperation(ctx context.Context) string {
	if op, ok := ctx.Value(contextKeyOperation).(string); ok {
		return op
	}
	return ""
}
