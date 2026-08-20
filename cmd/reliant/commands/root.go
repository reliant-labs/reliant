// Copyright (c) 2025 Reliant Labs
package commands

import (
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"
)

// verbose is the value of the persistent --verbose flag.
var verbose bool

// NewRootCmd creates and returns the root Cobra command with all subcommands registered.
// Exported so tools/docgen/cli can walk the command tree for documentation generation.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "reliant",
		Short: "Reliant — AI-powered software engineering platform",
		Long: `Reliant is a multi-workflow, multi-workspace agentic assistant for
software engineering. This CLI provides commands for authentication,
project management, daemon control, workflow operations, and running
Reliant server components.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// Global persistent flags available to all subcommands. The target-selection
	// flags (--server/--gateway/--context) are owned by connection.go, which is
	// the only place their values are read.
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")
	registerConnectionFlags(root)

	// Register subcommand groups
	root.AddCommand(newServerCmd())
	root.AddCommand(newDaemonCmd())
	root.AddCommand(newAuthCmd())
	root.AddCommand(newOpenCmd())
	root.AddCommand(newProjectCmd())
	root.AddCommand(newWorkflowCmd())
	root.AddCommand(newVersionCmd())
	root.AddCommand(newForgeCmd())
	root.AddCommand(newPreviewURLCmd())
	root.AddCommand(newContextCmd())

	return root
}

// Execute runs the root command.
func Execute() error {
	root := NewRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	return nil
}

// envOrDefault returns the environment variable value or the default.
func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// envOrDefaultInt returns the environment variable as an int, or the default.
func envOrDefaultInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultVal
}

// envOrDefaultBool returns the environment variable parsed as a bool, or the
// default. Used as a flag's default value so an explicit CLI flag still wins
// over the env var — cobra only applies the default when the flag is not
// passed on the command line, so the precedence is: explicit flag > env var >
// this default.
func envOrDefaultBool(key string, defaultVal bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return defaultVal
}
