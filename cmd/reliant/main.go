// Copyright (c) 2025 Reliant Labs
package main

import (
	"github.com/reliant-labs/reliant/cmd/reliant/cmd"
	"github.com/reliant-labs/reliant/internal/credsandbox"
	"github.com/reliant-labs/reliant/internal/heavyjob"
	"github.com/reliant-labs/reliant/internal/pid1"
)

func main() {
	// The sandbox helper for `credentials: none` commands is this binary
	// re-executed. It is the init of its own PID namespace, so it runs before
	// pid1.Init (which would wrap it in another init) and before anything else.
	credsandbox.Dispatch()

	// First, before anything forks: as a container's PID 1, become a minimal
	// init that reaps orphans and run the command in a child.
	pid1.Init()

	// A daemon runs each heavy command under this binary as its slot wrapper
	// (`reliant __heavy-slot …`); that process is the wrapper, not the CLI.
	heavyjob.Dispatch()

	cmd.Execute()
}
