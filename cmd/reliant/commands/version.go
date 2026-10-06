// Copyright (c) 2025 Reliant Labs
package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"strings"

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
				_, err := fmt.Fprintln(cmd.OutOrStdout(), build.Version)
				return err
			}

			if jsonOutput {
				info := map[string]string{
					"version": build.Version,
					"commit":  build.Commit,
					"dirty":   build.Dirty,
					"built":   build.Date,
					"branch":  build.Branch,
					"forge":   build.Forge,
					"go":      runtime.Version(),
					"os":      runtime.GOOS,
					"arch":    runtime.GOARCH,
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}

			_, err := io.WriteString(cmd.OutOrStdout(), versionText(build))
			return err
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	cmd.Flags().BoolVar(&shortOutput, "short", false, "Print only the version number")

	return cmd
}

// versionText renders the human-readable version block. `reliant version` and
// `reliant --version` both print it, so the two can never disagree.
func versionText(build version.BuildInfo) string {
	commit := build.Commit
	switch build.Dirty {
	case version.DirtyTrue:
		commit += " (dirty: uncommitted changes at build time)"
	case version.DirtyFalse:
		commit += " (clean)"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "reliant version %s\n", build.Version)
	fmt.Fprintf(&b, "  commit:  %s\n", commit)
	fmt.Fprintf(&b, "  built:   %s\n", build.Date)
	fmt.Fprintf(&b, "  forge:   %s\n", build.Forge)
	fmt.Fprintf(&b, "  go:      %s\n", runtime.Version())
	fmt.Fprintf(&b, "  os/arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	return b.String()
}

// versionAnnotation carries the rendered block to cobra's --version template.
// The block goes through an annotation rather than being the template itself,
// so nothing in a version string is ever parsed as template syntax.
const versionAnnotation = "reliant.version-text"

// enableVersionFlag gives root a --version flag that prints versionText.
// cobra registers the flag only when Version is non-empty; -v stays --verbose
// because cobra does not take a shorthand that is already bound.
func enableVersionFlag(root *cobra.Command) {
	build := version.Get()
	root.Version = build.Version
	if root.Annotations == nil {
		root.Annotations = map[string]string{}
	}
	root.Annotations[versionAnnotation] = versionText(build)
	root.SetVersionTemplate(`{{index .Annotations "` + versionAnnotation + `"}}`)
}
