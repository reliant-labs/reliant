// Copyright (c) 2025 Reliant Labs
package commands

import (
	"github.com/spf13/cobra"

	forgecli "github.com/reliant-labs/forge/cli"
)

func newForgeCmd() *cobra.Command {
	return forgecli.NewRootCmd()
}
