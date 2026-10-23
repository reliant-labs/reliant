package main

import (
	"fmt"
	"os"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cli"
	"github.com/reliant-labs/forge/internal/exitcode"
)

var (
	Version   = "dev"
	BuildDate = "unknown"
	GitCommit = "unknown"
)

func main() {
	buildinfo.Set(Version, BuildDate, GitCommit)
	cli.SetVersion(Version, BuildDate, GitCommit)

	if err := cli.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(exitcode.Of(err))
	}
}
