package service

import (
	"context"

	"github.com/reliant-labs/reliant/internal/skills"
)

// Service is a compatibility orchestration boundary for the skills pipeline.
type Service struct{}

// ResolveInput captures the current end-to-end resolution inputs.
type ResolveInput = skills.ResolveTurnInput

// ResolveOutput captures the current end-to-end resolution outputs.
type ResolveOutput = skills.ResolveTurnResult

// New returns a service instance.
func New() Service {
	return Service{}
}

// Resolve delegates to the current runtime while providing a stable service boundary
// for future migration of catalog, activation, materialization, policy, and prompt rendering.
func (Service) Resolve(ctx context.Context, input ResolveInput) ResolveOutput {
	return skills.DefaultRuntime().ResolveTurn(ctx, input)
}
