// Copyright (c) 2025 Reliant Labs
package commands

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/cliconfig"
)

func newContextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Manage CLI contexts (server + token pairs)",
		Long: `Contexts pair a server URL with an API token (rlnt_pat_...) so the CLI can
target multiple Reliant environments. Commands resolve the context via:

  --context flag > RELIANT_CONTEXT env > current_context > legacy auth file

Config file locations:
  macOS:   ~/Library/Application Support/reliant/cli-config.json
  Linux:   ~/.config/reliant/cli-config.json
  Windows: %APPDATA%\reliant\cli-config.json`,
	}

	cmd.AddCommand(newContextListCmd())
	cmd.AddCommand(newContextUseCmd())
	cmd.AddCommand(newContextSetCmd())

	return cmd
}

func newContextListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured contexts",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cliconfig.Load()
			if err != nil {
				return err
			}
			if len(cfg.Contexts) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No contexts configured")
				fmt.Fprintln(cmd.OutOrStdout(), "Create one with: reliant context set <name> --server <url>")
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "CURRENT\tNAME\tSERVER\tTOKEN\tHOOKS")
			for _, name := range cfg.ContextNames() {
				c := cfg.Contexts[name]
				marker := ""
				if name == cfg.CurrentContext {
					marker = "*"
				}
				token := "-"
				if c.Token != "" {
					token = truncateToken(c.Token)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n", marker, name, c.Server, token, len(c.Hooks))
			}
			return w.Flush()
		},
	}
}

func truncateToken(token string) string {
	if len(token) <= 12 {
		return token
	}
	return token[:12] + "..."
}

func newContextUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Switch the current context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := cliconfig.Load()
			if err != nil {
				return err
			}
			if _, ok := cfg.Contexts[name]; !ok {
				return fmt.Errorf("context %q not found — create it with 'reliant context set %s --server <url>'", name, name)
			}
			cfg.CurrentContext = name
			if err := cliconfig.Save(cfg); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Switched to context %q\n", name)
			return nil
		},
	}
}

func newContextSetCmd() *cobra.Command {
	var (
		ctxServer string
		ctxToken  string
		use       bool
	)

	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Create or update a context",
		Long: `Creates the named context if it does not exist, then applies the given
--server / --token values. The first context created becomes the current
context automatically.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := cliconfig.Load()
			if err != nil {
				return err
			}

			c := cfg.Contexts[name]
			created := c == nil
			if created {
				c = &cliconfig.Context{}
				cfg.Contexts[name] = c
			}
			if cmd.Flags().Changed("server") {
				c.Server = ctxServer
			}
			if cmd.Flags().Changed("token") {
				c.Token = ctxToken
			}
			if use || cfg.CurrentContext == "" {
				cfg.CurrentContext = name
			}

			if err := cliconfig.Save(cfg); err != nil {
				return err
			}

			verb := "Updated"
			if created {
				verb = "Created"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s context %q (server: %s)\n", verb, name, c.Server)
			if cfg.CurrentContext == name {
				fmt.Fprintf(cmd.OutOrStdout(), "Current context: %s\n", name)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&ctxServer, "server", "", "Server URL for this context")
	cmd.Flags().StringVar(&ctxToken, "token", "", "API token (rlnt_pat_...) for this context")
	cmd.Flags().BoolVar(&use, "use", false, "Also switch the current context to this one")

	return cmd
}
