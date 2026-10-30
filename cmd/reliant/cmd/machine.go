// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/oauth2"
	"github.com/spf13/cobra"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	daemonv1 "github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1"
	"github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1/controlplanev1connect"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// `reliant machine` manages the user's own machines exactly as Settings ->
// Machines does, through the control plane's DaemonService. The control
// plane accepts the CLI login for it only when the login carries
// daemon:manage — a person-only scope no daemon's credential can hold — so a
// login made before this command existed is told to log in again.
//
// An agent's shell is refused start/stop/resize (refuseMachineFromAgentShell):
// agents use the manage_machine tool, whose start and resize need the user's
// approval card in chat.

var errAgentCannotManageMachines = errors.New("an agent cannot start, stop or resize machines from its shell: use the manage_machine tool (start and resize need the user's approval card in chat), or ask the user to do it in Settings -> Machines")

func refuseMachineFromAgentShell(*cobra.Command, []string) error {
	if os.Getenv(daemonpolicy.AgentShellEnv) != "" { //nolint:forbidigo // the marker the daemon sets on agent shells (daemonpolicy.AgentShellEnv), a process signal, not configuration
		return errAgentCannotManageMachines
	}
	return nil
}

// machineSizes maps the size names Settings -> Machines shows to the control
// plane's enum, smallest first.
var machineSizes = []struct {
	name string
	size controlplanev1.DaemonSize
}{
	{"small", controlplanev1.DaemonSize_DAEMON_SIZE_SMALL},
	{"medium", controlplanev1.DaemonSize_DAEMON_SIZE_MEDIUM},
	{"large", controlplanev1.DaemonSize_DAEMON_SIZE_LARGE},
	{"xl", controlplanev1.DaemonSize_DAEMON_SIZE_XL},
	{"2xl", controlplanev1.DaemonSize_DAEMON_SIZE_2XL},
}

func parseMachineSize(s string) (controlplanev1.DaemonSize, error) {
	names := make([]string, 0, len(machineSizes))
	for _, m := range machineSizes {
		if strings.EqualFold(strings.TrimSpace(s), m.name) {
			return m.size, nil
		}
		names = append(names, m.name)
	}
	return 0, fmt.Errorf("unknown size %q: want one of %s", s, strings.Join(names, ", "))
}

func machineSizeName(s controlplanev1.DaemonSize) string {
	for _, m := range machineSizes {
		if m.size == s {
			return m.name
		}
	}
	return ""
}

func machineStatusName(s controlplanev1.DaemonStatus) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "DAEMON_STATUS_"))
}

func machineTypeName(t controlplanev1.DaemonType) string {
	switch t {
	case controlplanev1.DaemonType_DAEMON_TYPE_MANAGED:
		return "managed"
	case controlplanev1.DaemonType_DAEMON_TYPE_EXTERNAL:
		return "connected"
	}
	return ""
}

// machineClient is the control plane's DaemonService, reached as the CLI
// login. The control plane's origin is the authorization server the API
// server names (RFC 8414), the same one `reliant auth login` signed in at.
func machineClient(ctx context.Context, cmd *cobra.Command) (controlplanev1connect.DaemonServiceClient, error) {
	conn, err := resolveConnection(cmd)
	if err != nil {
		return nil, err
	}
	meta, err := oauth2.DiscoverAuthorizationServer(ctx, conn.httpClientWithBearer(""), conn.ServerURL)
	if err != nil {
		return nil, fmt.Errorf("finding the control plane for %s: %w", conn.ServerURL, err)
	}
	issuer := strings.TrimRight(strings.TrimSpace(meta.Issuer), "/")
	if issuer == "" {
		return nil, fmt.Errorf("%s names no control plane; machines are managed only on hosted Reliant", conn.ServerURL)
	}
	return controlplanev1connect.NewDaemonServiceClient(conn.httpClient(), issuer), nil
}

// explainMachineErr turns the control plane's refusal of a login without
// daemon:manage into the fix.
func explainMachineErr(err error) error {
	if connect.CodeOf(err) == connect.CodeUnauthenticated {
		return fmt.Errorf("%w\n  hint: managing machines needs a login made by this release: run 'reliant auth login'", err)
	}
	return err
}

// resolveMachine finds one of the user's machines by id or name.
func resolveMachine(ctx context.Context, client controlplanev1connect.DaemonServiceClient, ref string) (*controlplanev1.Daemon, error) {
	resp, err := client.ListDaemons(ctx, connect.NewRequest(&daemonv1.ListDaemonsRequest{}))
	if err != nil {
		return nil, explainMachineErr(err)
	}
	var byName []*controlplanev1.Daemon
	for _, d := range resp.Msg.GetDaemons() {
		if d.GetId() == ref {
			return d, nil
		}
		if d.GetName() == ref {
			byName = append(byName, d)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return nil, fmt.Errorf("no machine %q: 'reliant machine list' shows yours", ref)
	}
	return nil, fmt.Errorf("%d machines are named %q: pass the id instead", len(byName), ref)
}

func newMachineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "machine",
		Aliases: []string{"machines"},
		Short:   "List, start, stop and resize your machines",
		Long: `Manage your own machines, exactly as Settings -> Machines does.

A machine is billed while it runs. start wakes a suspended machine; stop
suspends a running one; resize changes a SUSPENDED machine's size (small,
medium, large, xl, 2xl), which takes effect when it next starts. To resize a
running machine: stop, resize, start.

Needs a login made by this release ('reliant auth login'): it carries the
daemon:manage permission, which only a person's login can hold.`,
	}
	cmd.AddCommand(newMachineListCmd(), newMachineStartCmd(), newMachineStopCmd(), newMachineResizeCmd())
	return cmd
}

func newMachineListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List your machines",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := machineClient(cmd.Context(), cmd)
			if err != nil {
				return err
			}
			resp, err := client.ListDaemons(cmd.Context(), connect.NewRequest(&daemonv1.ListDaemonsRequest{}))
			if err != nil {
				return explainMachineErr(err)
			}
			return printMachines(cmd.OutOrStdout(), resp.Msg.GetDaemons())
		},
	}
}

func printMachines(out io.Writer, daemons []*controlplanev1.Daemon) error {
	if len(daemons) == 0 {
		_, err := fmt.Fprintln(out, "You have no machines.")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tID\tTYPE\tSTATUS\tSIZE")
	for _, d := range daemons {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.GetName(), d.GetId(), machineTypeName(d.GetDaemonType()),
			machineStatusName(d.GetStatus()), machineSizeName(d.GetSize()))
	}
	return w.Flush()
}

func newMachineStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "start <name|id>",
		Short:   "Start (wake) a suspended machine",
		Args:    cobra.ExactArgs(1),
		PreRunE: refuseMachineFromAgentShell,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := machineClient(cmd.Context(), cmd)
			if err != nil {
				return err
			}
			d, err := resolveMachine(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			if _, err := client.ResumeDaemon(cmd.Context(), connect.NewRequest(&daemonv1.ResumeDaemonRequest{DaemonId: d.GetId()})); err != nil {
				if connect.CodeOf(err) == connect.CodeFailedPrecondition {
					return fmt.Errorf("machine %s is not suspended: %w", d.GetName(), err)
				}
				return explainMachineErr(err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Starting %s (%s). It takes a minute or so to come up.\n", d.GetName(), d.GetId())
			return err
		},
	}
}

func newMachineStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "stop <name|id>",
		Short:   "Stop (suspend) a running machine",
		Args:    cobra.ExactArgs(1),
		PreRunE: refuseMachineFromAgentShell,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := machineClient(cmd.Context(), cmd)
			if err != nil {
				return err
			}
			d, err := resolveMachine(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			if _, err := client.SuspendDaemon(cmd.Context(), connect.NewRequest(&daemonv1.SuspendDaemonRequest{DaemonId: d.GetId()})); err != nil {
				return explainMachineErr(err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Stopping %s (%s).\n", d.GetName(), d.GetId())
			return err
		},
	}
}

func newMachineResizeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resize <name|id> <small|medium|large|xl|2xl>",
		Short: "Change a suspended machine's size",
		Long: `Change a SUSPENDED machine's size; it takes effect when the machine next
starts. A running machine is refused: stop it first. Disks only grow, so a
downsize keeps the larger disk.`,
		Args:    cobra.ExactArgs(2),
		PreRunE: refuseMachineFromAgentShell,
		RunE: func(cmd *cobra.Command, args []string) error {
			size, err := parseMachineSize(args[1])
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
			if _, err := client.ResizeDaemon(cmd.Context(), connect.NewRequest(&daemonv1.ResizeDaemonRequest{DaemonId: d.GetId(), Size: size})); err != nil {
				if connect.CodeOf(err) == connect.CodeFailedPrecondition {
					return fmt.Errorf("%w\n  hint: stop it first: reliant machine stop %s", err, d.GetName())
				}
				return explainMachineErr(err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s (%s) is now %s; it takes effect when it next starts.\n", d.GetName(), d.GetId(), args[1])
			return err
		},
	}
}
