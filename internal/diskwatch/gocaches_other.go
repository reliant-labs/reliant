// Copyright (c) 2026 Reliant Labs

//go:build !linux

package diskwatch

// scanGoCacheRoots has no /proc to read here: only the shared cache and the
// policy's go_caches are trimmed.
func scanGoCacheRoots() []string { return nil }
