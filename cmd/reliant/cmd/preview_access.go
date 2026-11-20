// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	daemonv1 "github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1"
)

// `reliant preview access` decides who may open a cloud machine's preview
// URLs, as the machine's Ports panel in the app does (DaemonService
// SetPortAccess, RemovePortAccess, ListPortAccessRules, SetDefaultPortAccess).
// `reliant preview-url` told the user to "make the port public", and nothing in
// the CLI could.
//
// Who can reach a machine's ports is the owner's call: an agent's shell is
// refused the writes, and may list.

var errAgentCannotChangePortAccess = errors.New("an agent cannot change who may open a machine's preview URLs from its shell: ask the user to run 'reliant preview access' or change it in the machine's Ports panel")

func refusePortAccessFromAgentShell(*cobra.Command, []string) error {
	if inAgentShell() {
		return errAgentCannotChangePortAccess
	}
	return nil
}

// portAccessModes maps the CLI's names to the control plane's modes.
var portAccessModes = map[string]controlplanev1.PortAccessMode{
	"public":        controlplanev1.PortAccessMode_PORT_ACCESS_MODE_PUBLIC,
	"authenticated": controlplanev1.PortAccessMode_PORT_ACCESS_MODE_AUTHENTICATED,
	"token":         controlplanev1.PortAccessMode_PORT_ACCESS_MODE_TOKEN,
}

func parsePortAccessMode(s string, allowed ...string) (controlplanev1.PortAccessMode, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, name := range allowed {
		if s == name {
			return portAccessModes[name], nil
		}
	}
	return 0, fmt.Errorf("unknown access %q: want one of %s", s, strings.Join(allowed, ", "))
}

func portAccessModeName(m controlplanev1.PortAccessMode) string {
	for name, mode := range portAccessModes {
		if mode == m {
			return name
		}
	}
	return "-"
}

func parsePort(s string) (int32, error) {
	port, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("invalid port %q: must be an integer in 1..65535", s)
	}
	return int32(port), nil //nolint:gosec // bounded to 1..65535 above
}

func newPreviewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "preview",
		Short: "Control who may open your cloud machines' preview URLs",
		Long: `A cloud machine serves each listening port at a preview URL ('reliant
preview-url <port>' prints it). By default only you, signed in, may open one.
'reliant preview access' changes that, per port or for the whole machine.`,
	}
	cmd.AddCommand(newPreviewAccessCmd())
	return cmd
}

func newPreviewAccessCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "access",
		Short: "List and change who may open a machine's preview URLs",
		Long: `Who may open a cloud machine's preview URLs, per port:

  authenticated  you, signed in to Reliant (the default)
  public         anyone with the URL
  token          a caller presenting the port's bearer token

A port with no rule of its own follows the machine's default, which is
authenticated or public ('reliant preview access default').`,
		Example: `  reliant preview access list build-box
  reliant preview access set build-box 5173 public
  reliant preview access remove build-box 5173
  reliant preview access default build-box authenticated`,
	}
	cmd.AddCommand(newPreviewAccessListCmd(), newPreviewAccessSetCmd(), newPreviewAccessRemoveCmd(), newPreviewAccessDefaultCmd())
	return cmd
}

func newPreviewAccessListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list <machine>",
		Aliases: []string{"ls"},
		Short:   "List a machine's port access rules and its default",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := machineClient(cmd.Context(), cmd)
			if err != nil {
				return err
			}
			d, err := resolveMachine(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			resp, err := client.ListPortAccessRules(cmd.Context(), connect.NewRequest(&daemonv1.ListPortAccessRulesRequest{DaemonId: d.GetId()}))
			if err != nil {
				return explainMachineErr(err)
			}
			return printPortAccess(cmd.OutOrStdout(), d, resp.Msg.GetRules())
		},
	}
}

func printPortAccess(out io.Writer, d *controlplanev1.Daemon, rules []*controlplanev1.PortAccessRule) error {
	def := portAccessModeName(d.GetDefaultPortAccess())
	if def == "-" {
		def = "authenticated"
	}
	fmt.Fprintf(out, "%s (%s): ports with no rule are %s\n", d.GetName(), d.GetId(), def)
	if len(rules) == 0 {
		_, err := fmt.Fprintln(out, "No port has a rule of its own.")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PORT\tACCESS\tURL")
	for _, r := range rules {
		fmt.Fprintf(w, "%d\t%s\t%s\n", r.GetPort(), portAccessModeName(r.GetAccessMode()), r.GetUrl())
	}
	return w.Flush()
}

func newPreviewAccessSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "set <machine> <port> <public|authenticated|token>",
		Short:   "Set who may open one port's preview URL",
		Args:    cobra.ExactArgs(3),
		PreRunE: refusePortAccessFromAgentShell,
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := parsePort(args[1])
			if err != nil {
				return err
			}
			mode, err := parsePortAccessMode(args[2], "public", "authenticated", "token")
			if err != nil {
				return err
			}
			client, err := machineClient(cmd.Context(), cmd)
			if err != nil {
				return err
			}
			d, err := resolveMachine(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			if _, err := client.SetPortAccess(cmd.Context(), connect.NewRequest(&daemonv1.SetPortAccessRequest{
				DaemonId: d.GetId(), Port: port, AccessMode: mode,
			})); err != nil {
				return explainMachineErr(err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Port %d on %s is now %s.\n", port, d.GetName(), portAccessModeName(mode))
			return err
		},
	}
}

func newPreviewAccessRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <machine> <port>",
		Aliases: []string{"rm"},
		Short:   "Remove one port's rule, so it follows the machine's default",
		Args:    cobra.ExactArgs(2),
		PreRunE: refusePortAccessFromAgentShell,
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := parsePort(args[1])
			if err != nil {
				return err
			}
			client, err := machineClient(cmd.Context(), cmd)
			if err != nil {
				return err
			}
			d, err := resolveMachine(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			if _, err := client.RemovePortAccess(cmd.Context(), connect.NewRequest(&daemonv1.RemovePortAccessRequest{
				DaemonId: d.GetId(), Port: port,
			})); err != nil {
				return explainMachineErr(err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Port %d on %s follows the machine's default.\n", port, d.GetName())
			return err
		},
	}
}

func newPreviewAccessDefaultCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "default <machine> <authenticated|public>",
		Short: "Set who may open the ports that have no rule of their own",
		Long: `Set the machine's default: who may open a port that has no rule of its own.
public makes every such listening port reachable by anyone with its URL.`,
		Args:    cobra.ExactArgs(2),
		PreRunE: refusePortAccessFromAgentShell,
		RunE: func(cmd *cobra.Command, args []string) error {
			mode, err := parsePortAccessMode(args[1], "authenticated", "public")
			if err != nil {
				return err
			}
			client, err := machineClient(cmd.Context(), cmd)
			if err != nil {
				return err
			}
			d, err := resolveMachine(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			if _, err := client.SetDefaultPortAccess(cmd.Context(), connect.NewRequest(&daemonv1.SetDefaultPortAccessRequest{
				DaemonId: d.GetId(), DefaultAccessMode: mode,
			})); err != nil {
				return explainMachineErr(err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Ports on %s with no rule of their own are now %s.\n", d.GetName(), portAccessModeName(mode))
			return err
		},
	}
}
