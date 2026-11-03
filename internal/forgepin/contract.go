// Copyright (c) 2025 Reliant Labs

//forge:not-a-component: the process-wide pinned-forge source of internal/skills/catalog and internal/toolexec/daemonruntime (Default) — never a node of the composition graph
package forgepin

import (
	"context"
	"io"

	forgecli "github.com/reliant-labs/forge/cli"
)

// Service asks the forge a project pins: which forge that is, and what it
// prints.
type Service interface {
	// Pinned reports the forge the project at root pins when it is not the
	// embedded one (ok), or why it could not be obtained (err). command is the
	// forge command the caller will run, which the decision depends on.
	Pinned(root, command string) (forgecli.PinnedForge, bool, error)
	// Output is what the pinned forge pf prints for the first of tries (each
	// one a command line) that succeeds, run in dir, kept under name.
	Output(pf forgecli.PinnedForge, dir, name string, tries ...[]string) ([]byte, error)
}

// Deps are the Service's collaborators: RealDeps for the process, fake funcs
// in tests.
type Deps struct {
	// Resolve is forge's decision of which forge runs a command in a project.
	Resolve func(ctx context.Context, projectRoot, command string, log io.Writer) (forgecli.PinnedForge, bool, error)
	// Run runs the pinned forge bin with args in dir and returns its stdout.
	Run func(bin, dir string, args ...string) ([]byte, error)
	// CacheDir is where a module version's output is kept ("" keeps nothing
	// on disk).
	CacheDir func() string
}
