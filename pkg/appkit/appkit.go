package appkit

import (
	"context"

	"github.com/reliant-labs/forge/pkg/lifecyclekit"
	"github.com/reliant-labs/forge/pkg/serverkit"
)

// The worker lifecycle machinery moved to forge/pkg/lifecyclekit (the
// lib-boundary extraction — FORGE_SHAPE_REDESIGN §2). appkit now DELEGATES so
// any remaining consumer of the appkit names keeps compiling against a single
// implementation; the generated internal/app/lifecycle_gen.go uses
// lifecyclekit directly. New code should import lifecyclekit, not appkit.

// WorkerInstance is an alias for lifecyclekit.WorkerInstance.
//
// Deprecated: use lifecyclekit.WorkerInstance.
type WorkerInstance = lifecyclekit.WorkerInstance

// ContextWorkerInstance is an alias for lifecyclekit.ContextWorkerInstance.
//
// Deprecated: use lifecyclekit.ContextWorkerInstance.
type ContextWorkerInstance = lifecyclekit.ContextWorkerInstance

// WorkerLifecycle is an alias for lifecyclekit.WorkerLifecycle.
//
// Deprecated: use lifecyclekit.WorkerLifecycle.
type WorkerLifecycle = lifecyclekit.WorkerLifecycle

// NewWorkerInstance delegates to lifecyclekit.NewWorkerInstance.
//
// Deprecated: use lifecyclekit.NewWorkerInstance.
func NewWorkerInstance(name string, start, stop func(ctx context.Context) error) *WorkerInstance {
	return lifecyclekit.NewWorkerInstance(name, start, stop)
}

// NewContextWorkerInstance delegates to lifecyclekit.NewContextWorkerInstance.
//
// Deprecated: use lifecyclekit.NewContextWorkerInstance.
func NewContextWorkerInstance(name string, runContext, stop func(ctx context.Context) error) *ContextWorkerInstance {
	return lifecyclekit.NewContextWorkerInstance(name, runContext, stop)
}

// WrapWorker delegates to lifecyclekit.WrapWorker.
//
// Deprecated: use lifecyclekit.WrapWorker.
func WrapWorker(name string, w WorkerLifecycle) serverkit.Worker {
	return lifecyclekit.WrapWorker(name, w)
}
