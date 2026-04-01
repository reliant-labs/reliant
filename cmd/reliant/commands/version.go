// Copyright (c) 2025 Reliant Labs
package commands

import (
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// Build-time variables injected via ldflags:
//
//	go build -ldflags "-X ...commands.Version=1.0.0 -X ...commands.Commit=abc123 -X ...commands.BuildDate=2025-01-01"
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

func newVersionCmd() *cobra.Command {
	var (
		jsonOutput  bool
		shortOutput bool
	)

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(cmd *cobra.Command, args []string) error {
			if shortOutput {
				fmt.Println(Version)
				return nil
			}

			if jsonOutput {
				info := map[string]string{
					"version": Version,
					"commit":  Commit,
					"built":   BuildDate,
					"go":      runtime.Version(),
					"os":      runtime.GOOS,
					"arch":    runtime.GOARCH,
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "reliant version %s\n", Version)
			fmt.Fprintf(cmd.OutOrStdout(), "  commit:  %s\n", Commit)
			fmt.Fprintf(cmd.OutOrStdout(), "  built:   %s\n", BuildDate)
			fmt.Fprintf(cmd.OutOrStdout(), "  go:      %s\n", runtime.Version())
			fmt.Fprintf(cmd.OutOrStdout(), "  os/arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	cmd.Flags().BoolVar(&shortOutput, "short", false, "Print only the version number")

	return cmd
}
