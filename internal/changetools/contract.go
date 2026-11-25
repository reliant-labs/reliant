// Copyright (c) 2025 Reliant Labs

// Package changetools is the worker's tools.ChangeController: Forge Changes
// through the control plane's ChangesService.
//
// forge:outbound-io
//
// It is the outbound adapter to the control plane (the ChangesService rpcs in
// Connect JSON, and DeployService for environment names). Its contract is the
// consumer's, tools.ChangeController in internal/llm/tools: Service embeds it
// and adds nothing, so the two cannot drift. The graph node is internal/app's
// NewChangeController provider, an optional component (nil when self-hosted).
//
//forge:provided
package changetools

import "github.com/reliant-labs/reliant/internal/llm/tools"

// Service is the consumer's contract, tools.ChangeController.
type Service interface {
	tools.ChangeController
}
