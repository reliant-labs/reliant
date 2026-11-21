// Copyright (c) 2025 Reliant Labs
package cli

import (
	internalcli "github.com/reliant-labs/forge/internal/cli"
)

// RenderProjectMemory returns the rendered forge framework memory for a
// project (the bytes that would live in the project's top-level memory
// file — reliant.md / CLAUDE.md / etc.). Out-of-process consumers
// (notably the reliant CLI) use this to inject framework context into
// the LLM session without forge having to write a stale on-disk copy
// that drifts on framework upgrades.
//
// projectRoot must be a directory containing forge.yaml. Returns an
// error when forge.yaml is missing or unreadable, or when the template
// cannot be rendered.
//
// The template is read from the same embedded source that `forge project new`
// writes for non-reliant harnesses, so the in-memory and on-disk paths
// stay byte-identical.
func RenderProjectMemory(projectRoot string) ([]byte, error) {
	return internalcli.RenderProjectMemory(projectRoot)
}
