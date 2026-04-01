// Copyright (c) 2025 Reliant Labs
package main

import (
	"os"

	"github.com/reliant-labs/reliant/cmd/reliant/commands"
)

func main() {
	if err := commands.Execute(); err != nil {
		os.Exit(1)
	}
}
