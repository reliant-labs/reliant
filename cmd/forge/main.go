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
	// TelemetryEndpoint is where forge's anonymous usage events go; set by
	// the release build (scripts/build-release-assets.sh). Empty: none sent.
	TelemetryEndpoint = ""
)

func main() {
	buildinfo.Set(Version, BuildDate, GitCommit)
	cli.SetVersion(Version, BuildDate, GitCommit)
	cli.SetTelemetryEndpoint(TelemetryEndpoint)

	if err := cli.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(exitcode.Of(err))
	}
}
