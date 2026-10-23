// Package cli — `forge env`: the environment noun.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
)

// newEnvCmd groups every environment-scoped lifecycle command under one
// noun: `forge env <verb> <environment>`. An environment names a
// deploy/kcl/<env>/ bundle; these verbs act on its stack. Commands that
// REQUIRE an env live here with the env as a positional argument;
// commands where env is an optional modifier (e.g. `forge build [env]`)
// stay at the root.
func newEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Manage deploy environments: bring stacks up/down, deploy releases, and inspect",
	}
	cmd.AddCommand(newEnvUpCmd())
	cmd.AddCommand(newEnvBuildCmd())
	cmd.AddCommand(newEnvDownCmd())
	cmd.AddCommand(newEnvStopCmd())
	cmd.AddCommand(newEnvStartCmd())
	cmd.AddCommand(newEnvDeleteCmd())
	cmd.AddCommand(newEnvStatusCmd())
	cmd.AddCommand(newEnvPsCmd())
	cmd.AddCommand(newEnvListCmd())
	cmd.AddCommand(newEnvOptionsCmd())
	cmd.AddCommand(newEnvConfigCmd())
	cmd.AddCommand(newEnvExecCmd())
	cmd.AddCommand(newEnvRenderCmd())
	cmd.AddCommand(newEnvDiffCmd())
	cmd.AddCommand(newEnvReleasesCmd())
	cmd.AddCommand(newEnvShapeCmd())
	cmd.AddCommand(newEnvNewCmd())
	cmd.AddCommand(newDeployCmd())
	cmd.AddCommand(newSmokeCmd())
	cmd.AddCommand(newSecretsCmd())
	cmd.AddCommand(newDevStackCmd())
	return cmdutil.StrictGroup(cmd)
}

// newEnvListCmd enumerates the environments declared under deploy/kcl/ —
// one directory per env, each carrying a main.k.
func newEnvListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the environments declared in deploy/kcl/",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			base := filepath.Join(projectDirForKCL(), "deploy", "kcl")
			entries, err := os.ReadDir(base)
			if err != nil {
				return fmt.Errorf("read %s: %w (no deploy/kcl/ — does this project declare environments?)", base, err)
			}
			var envs []string
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				if _, statErr := os.Stat(filepath.Join(base, e.Name(), "main.k")); statErr == nil {
					envs = append(envs, e.Name())
				}
			}
			if len(envs) == 0 {
				fmt.Println("no environments declared under deploy/kcl/")
				return nil
			}
			sort.Strings(envs)
			for _, name := range envs {
				fmt.Println(name)
			}
			return nil
		},
	}
	return cmd
}
