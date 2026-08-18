// Copyright (c) 2025 Reliant Labs
package commands

import (
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/version"
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
			build := version.Get()

			if shortOutput {
				fmt.Println(build.Version)
				return nil
			}

			if jsonOutput {
				info := map[string]string{
					"version": build.Version,
					"commit":  build.Commit,
					"built":   build.Date,
					"branch":  build.Branch,
					"go":      runtime.Version(),
					"os":      runtime.GOOS,
					"arch":    runtime.GOARCH,
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "reliant version %s\n", build.Version)
			fmt.Fprintf(cmd.OutOrStdout(), "  commit:  %s\n", build.Commit)
			fmt.Fprintf(cmd.OutOrStdout(), "  built:   %s\n", build.Date)
			fmt.Fprintf(cmd.OutOrStdout(), "  go:      %s\n", runtime.Version())
			fmt.Fprintf(cmd.OutOrStdout(), "  os/arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	cmd.Flags().BoolVar(&shortOutput, "short", false, "Print only the version number")

	return cmd
}
