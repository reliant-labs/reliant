// Copyright (c) 2025 Reliant Labs
package core

import reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"

// Program is the compiled, pure semantic representation consumed by core engine/runtime bridges.
type Program struct {
	Workflow  *reliantv1.Workflow
	Semantics *CompiledSemantics
}

// CompileOptions configures semantic compilation.
type CompileOptions struct {
	// CanonicalWorkflowRef is the loadable workflow reference used for workflow.name identity
	// (for example: builtin://agent). Inline child workflows inherit this identity.
	CanonicalWorkflowRef string

	// WorkflowLoader optionally loads external workflow refs for semantic expansion/default extraction.
	WorkflowLoader WorkflowLoader
}

// WorkflowLoader resolves a loadable workflow reference into a workflow definition.
type WorkflowLoader func(workflowRef string) (*reliantv1.Workflow, error)
