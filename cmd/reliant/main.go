// Copyright (c) 2025 Reliant Labs
package main

import (
	"os"

	"github.com/reliant-labs/reliant/cmd/reliant/cmd"
	"github.com/reliant-labs/reliant/internal/heavyjob"
	"github.com/reliant-labs/reliant/internal/pid1"
)

func main() {
	// First, before anything forks: as a container's PID 1, become a minimal
	// init that reaps orphans and run the command in a child.
	pid1.Init()

	// A daemon runs each heavy command under this binary as its slot wrapper
	// (`reliant __heavy-slot …`); that process is the wrapper, not the CLI.
	heavyjob.Dispatch()

	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
