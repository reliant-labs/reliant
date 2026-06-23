// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/core"
)

// RuntimeSemantics provides runtime-facing access to core compile contracts.
type RuntimeSemantics struct {
	canonicalWorkflowRef string
	nodeContracts        map[string]core.SubWorkflowContract
}

// CompileRuntimeSemantics compiles runtime semantics for a single workflow scope.
//
// The returned nodeContracts map is keyed by top-level node ID in the compiled workflow.
// Nested inline contracts remain owned by their nested executor scopes.
func CompileRuntimeSemantics(workflowDef *reliantv1.Workflow, canonicalWorkflowRef string) (*RuntimeSemantics, error) {
	trimmedCanonicalRef := strings.TrimSpace(canonicalWorkflowRef)
	program, err := core.Compile(workflowDef, core.CompileOptions{CanonicalWorkflowRef: trimmedCanonicalRef})
	if err != nil {
		return nil, fmt.Errorf("compile core semantics: %w", err)
	}

	semantics := &RuntimeSemantics{
		canonicalWorkflowRef: program.Semantics.CanonicalWorkflowRef,
		nodeContracts:        make(map[string]core.SubWorkflowContract),
	}

	for nodePath, contract := range program.Semantics.SubWorkflows {
		if strings.Contains(nodePath, "/") {
			continue
		}
		semantics.nodeContracts[nodePath] = contract
	}

	return semantics, nil
}

func (s *RuntimeSemantics) CanonicalWorkflowRef() string {
	if s == nil {
		return ""
	}
	return s.canonicalWorkflowRef
}

func (s *RuntimeSemantics) ContractForNode(nodeID string) (core.SubWorkflowContract, bool) {
	if s == nil {
		return core.SubWorkflowContract{}, false
	}
	contract, ok := s.nodeContracts[nodeID]
	return contract, ok
}

func (s *RuntimeSemantics) RequireContractForNode(nodeID, nodeType string) (core.SubWorkflowContract, error) {
	contract, ok := s.ContractForNode(nodeID)
	if !ok {
		return core.SubWorkflowContract{}, fmt.Errorf("missing core semantics contract for %s node %q", nodeType, nodeID)
	}
	return contract, nil
}
