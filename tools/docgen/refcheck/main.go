// Copyright (c) 2025 Reliant Labs
//
// Reference Validator - validates reference data from proto descriptors.
//
// SOURCE OF TRUTH:
//   - Proto annotations [(reliant)] in workflow_v2.proto
//   - internal/workflow/reference (populated at init from proto descriptors)
//
// The reference package populates itself from proto descriptors at init() time.
// This tool validates that the reference data is correctly populated.
//
// Usage: go run ./tools/docgen/refcheck

package main

import (
	"fmt"
	"os"

	"github.com/reliant-labs/reliant/internal/workflow/reference"
)

func main() {
	// Validate that reference data is populated
	nodeTypes := reference.ListNodeTypes()
	inputTypes := reference.ListInputTypes()
	sharedTypes := reference.ListSharedTypes()

	if len(nodeTypes) == 0 {
		fmt.Fprintf(os.Stderr, "Error: no node types found in reference package\n")
		os.Exit(1)
	}
	if len(inputTypes) == 0 {
		fmt.Fprintf(os.Stderr, "Error: no input types found in reference package\n")
		os.Exit(1)
	}
	if len(sharedTypes) == 0 {
		fmt.Fprintf(os.Stderr, "Error: no shared types found in reference package\n")
		os.Exit(1)
	}

	fmt.Printf("Reference data validated: %d node types, %d input types, %d shared types\n",
		len(nodeTypes), len(inputTypes), len(sharedTypes))

	// Print summary for debugging
	fmt.Printf("\nNode types: %v\n", nodeTypes)
	fmt.Printf("Input types: %v\n", inputTypes)

	// Validate each node type has fields
	for _, name := range nodeTypes {
		info, ok := reference.GetNodeType(name)
		if !ok {
			fmt.Fprintf(os.Stderr, "Warning: node type %q not found\n", name)
			continue
		}
		if len(info.Fields) == 0 && name != "join" {
			fmt.Fprintf(os.Stderr, "Warning: node type %q has no input fields\n", name)
		}
	}

	fmt.Println("\nReference package populated from proto descriptors. No code generation needed.")
}
