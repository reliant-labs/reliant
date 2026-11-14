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
	cmd.AddCommand(newEnvTelemetryKeeperCmd())
	cmd.AddCommand(newEnvStopCmd())
	cmd.AddCommand(newEnvStartCmd())
	cmd.AddCommand(newEnvDeleteCmd())
	cmd.AddCommand(newEnvStatusCmd())
	cmd.AddCommand(newEnvVerifyCmd())
	cmd.AddCommand(newEnvPsCmd())
	cmd.AddCommand(newEnvListCmd())
	cmd.AddCommand(newEnvOptionsCmd())
	cmd.AddCommand(newEnvConfigCmd())
	cmd.AddCommand(newEnvExecCmd())
	cmd.AddCommand(newEnvRenderCmd())
	cmd.AddCommand(newEnvDiffCmd())
	cmd.AddCommand(newEnvReleasesCmd())
	cmd.AddCommand(newEnvFloorCmd())
	cmd.AddCommand(newEnvShapeCmd())
	cmd.AddCommand(newEnvNewCmd())
	cmd.AddCommand(newEnvPreviewCmd())
	cmd.AddCommand(newEnvGCCmd())
	cmd.AddCommand(newDeployCmd())
	cmd.AddCommand(newSmokeCmd())
	cmd.AddCommand(newSecretsCmd())
	cmd.AddCommand(newDevStackCmd())
	return cmdutil.StrictGroup(cmd)
}

// envCommandGroups sorts `forge env --help` into four sections: with build,
// kcl and secret under it (deprecated_aliases.go) env has some thirty
// subcommands, and one alphabetical list of them hid the lifecycle verbs
// people come for. A subcommand missing here is listed under "Additional
// Commands"; TestEnvHelpGroupsEverySubcommand keeps that from happening.
var envCommandGroups = []struct {
	id, title string
	verbs     []string
}{
	{"lifecycle", "Lifecycle:", []string{"new", "up", "down", "start", "stop", "status", "verify", "ps", "exec", "list", "delete", "deploy", "smoke", "devstack", "preview", "gc"}},
	{"build", "Build and release:", []string{"build", "releases", "floor"}},
	{"config", "Configuration:", []string{"config", "options", "render", "diff", "shape", "kcl"}},
	{"secrets", "Secrets:", []string{"secrets", "secret"}},
}

// groupEnvCommands assigns env's subcommands to envCommandGroups. It runs
// once the whole tree is assembled, because kcl and secret are mounted
// under env after newEnvCmd returns.
func groupEnvCommands(env *cobra.Command) {
	byName := map[string]string{}
	for _, g := range envCommandGroups {
		env.AddGroup(&cobra.Group{ID: g.id, Title: g.title})
		for _, v := range g.verbs {
			byName[v] = g.id
		}
	}
	for _, c := range env.Commands() {
		if id, ok := byName[c.Name()]; ok {
			c.GroupID = id
		}
	}
}

// newEnvListCmd enumerates the environments declared under deploy/kcl/ —
// one directory per env, each carrying a main.k.
func newEnvListCmd() *cobra.Command {
	var orphaned bool
	var token, endpoint string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the environments declared in deploy/kcl/",
		Long: `List the environments declared in deploy/kcl/: one directory per env, each
carrying a main.k. Reads nothing but the checkout.

--orphaned asks the control plane instead: it lists the hosted envs of this
project that deploy/kcl/ no longer declares (an env outlives its directory),
on each control plane this project is bound to (.forge/hosted-projects.json),
or on the one --endpoint names. Each is deletable with ` + "`forge env delete <env>`" + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if orphaned {
				return runEnvListOrphaned(cmd.Context(), projectDirForKCL(), token, endpoint, cmd.OutOrStdout())
			}
			if token != "" || endpoint != "" {
				return fmt.Errorf("--token and --endpoint apply only with --orphaned, which asks a control plane\nfix: add --orphaned, or drop them")
			}
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
	cmd.Flags().BoolVar(&orphaned, "orphaned", false, "List the hosted envs on the control plane that deploy/kcl/ no longer declares")
	cmd.Flags().StringVar(&token, "token", "", "With --orphaned: credential to use, ahead of the env var and the credentials file")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "With --orphaned: the control plane to ask, one this project is bound to or Reliant cloud (default: every one it is bound to)")
	return cmd
}
