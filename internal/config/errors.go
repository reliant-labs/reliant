// Copyright (c) 2025 Reliant Labs

// Package config provides configuration loading and validation.
//
// Error handling has moved to the validation package.
// Use validation.Error, validation.LocalCollector, and validation.Global() instead.
//
// Example usage:
//
//	import "github.com/reliant-labs/reliant/internal/validation"
//
//	// Create errors
//	err := validation.NewError(validation.CategoryConfig, "message").Source("source").Build()
//
//	// Use the global collector
//	validation.Global().AddError(err)
//
//	// Create a local collector for validation passes
//	collector := validation.NewLocalCollector()
package config
