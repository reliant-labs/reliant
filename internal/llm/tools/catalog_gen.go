// Copyright (c) 2025 Reliant Labs
package tools

// The generator writes toolcatalog/catalog_generated.go, relative to this
// directory. It lives here rather than beside its output because it imports
// this package (to walk the registry) and toolcatalog deliberately imports
// nothing.

//go:generate go run ./cmd/toolconfiggen
