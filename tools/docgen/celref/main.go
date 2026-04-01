// Copyright (c) 2025 Reliant Labs
//
// CEL reference generator - validates CEL namespace and function documentation.
//
// SOURCE OF TRUTH:
//   - internal/workflow/v2cel/types.go (namespace constants)
//   - internal/workflow/v2cel/env.go (custom functions)
//   - Proto annotations [(reliant)] for node output types
//
// Previously this tool generated cel_reference.go with hardcoded CEL type registries.
// Now the reference package populates CEL data from proto descriptors at init() time.
// This tool validates that the CEL reference data is correctly populated.
//
// Usage: go run ./tools/docgen/celref
// Regenerate with: make generate-celref

package main

import (
	"fmt"
	"os"

	"github.com/reliant-labs/reliant/internal/workflow/reference"
)

func main() {
	// Validate CEL namespaces
	if len(reference.CELNamespaces) == 0 {
		fmt.Fprintf(os.Stderr, "Error: no CEL namespaces found in reference package\n")
		os.Exit(1)
	}

	// Validate CEL functions
	if len(reference.CELFunctions) == 0 {
		fmt.Fprintf(os.Stderr, "Error: no CEL functions found in reference package\n")
		os.Exit(1)
	}

	// Validate helper types
	if len(reference.CELHelperTypes) == 0 {
		fmt.Fprintf(os.Stderr, "Error: no CEL helper types found in reference package\n")
		os.Exit(1)
	}

	fmt.Printf("CEL reference validated: %d namespaces, %d functions, %d helper types\n",
		len(reference.CELNamespaces), len(reference.CELFunctions), len(reference.CELHelperTypes))

	// Print details
	fmt.Println("\nNamespaces:")
	for _, ns := range reference.CELNamespaces {
		dynamic := ""
		if ns.IsDynamic {
			dynamic = " (dynamic)"
		}
		fmt.Printf("  %s: %s%s (%d fields)\n", ns.Name, ns.Description, dynamic, len(ns.Fields))
	}

	fmt.Println("\nFunctions:")
	for _, fn := range reference.CELFunctions {
		fmt.Printf("  %s: %s\n", fn.Name, fn.Description)
	}

	fmt.Println("\nHelper Types:")
	for _, ht := range reference.CELHelperTypes {
		fmt.Printf("  %s: %s (%d fields)\n", ht.Name, ht.Description, len(ht.Fields))
	}

	fmt.Println("\nCEL reference populated from proto descriptors. No code generation needed.")
}
