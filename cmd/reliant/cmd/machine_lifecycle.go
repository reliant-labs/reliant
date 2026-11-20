// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	daemonv1 "github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1"
)

// create, delete and grow-disk complete a cloud machine's lifecycle from the
// CLI, over the same DaemonService rpcs Settings -> Machines calls. Each costs
// money or destroys a workspace, so an agent's shell is refused all three
// (refuseMachineFromAgentShell), as it is start, stop and resize.

func newMachineCreateCmd() *cobra.Command {
	var size, idleTimeout string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a cloud machine",
		Long: `Create a Reliant cloud machine (a managed machine) of the given size, as
Settings -> Machines -> Add machine does. It is billed while it runs, and starts
at once. --idle-timeout suspends it after that long with nothing to do.

To connect a computer of your own instead, run 'reliant daemon start' on it.`,
		Example: `  reliant machine create build-box --size m
  reliant machine create gpu-runner --size xl --idle-timeout 30m`,
		Args:    cobra.ExactArgs(1),
		PreRunE: refuseMachineFromAgentShell,
		RunE: func(cmd *cobra.Command, args []string) error {
			sz, err := parseMachineSize(size)
			if err != nil {
				return err
			}
			client, err := machineClient(cmd.Context(), cmd)
			if err != nil {
				return err
			}
			resp, err := client.CreateDaemon(cmd.Context(), connect.NewRequest(&daemonv1.CreateDaemonRequest{
				Name:        strings.TrimSpace(args[0]),
				DaemonType:  controlplanev1.DaemonType_DAEMON_TYPE_MANAGED,
				Size:        sz,
				IdleTimeout: strings.TrimSpace(idleTimeout),
			}))
			if err != nil {
				if connect.CodeOf(err) == connect.CodeAlreadyExists {
					return fmt.Errorf("%w\n  hint: you already have a machine named %s: 'reliant machine list' shows it", err, args[0])
				}
				return explainMachineErr(err)
			}
			d := resp.Msg.GetDaemon()
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Created %s (%s), size %s. It takes a minute or so to come up.\n",
				d.GetName(), d.GetId(), machineSizeName(d.GetSize()))
			return err
		},
	}
	cmd.Flags().StringVar(&size, "size", "s1", "Machine size: s1, s2, m, l, xl, 2xl or 3xl")
	cmd.Flags().StringVar(&idleTimeout, "idle-timeout", "", "Suspend the machine after this long idle, e.g. 30m (default: your plan's)")
	return cmd
}

func newMachineDeleteCmd() *cobra.Command {
	var yes, snapshot bool
	cmd := &cobra.Command{
		Use:   "delete <name|id>",
		Short: "Delete a machine and its workspace",
		Long: `Delete one of your machines. A cloud machine's disk is deleted with it: every
checkout, branch workspace and file on it, including work that was never
pushed. --snapshot keeps a snapshot of the disk first. A computer of your own
is only disconnected from your account; nothing on it is deleted.

It needs --yes: there is no undo.`,
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
			if !yes {
				return fmt.Errorf("deleting %s (%s) deletes its disk and every unpushed change on it; pass --yes to delete it", d.GetName(), d.GetId())
			}
			if _, err := client.DeleteDaemon(cmd.Context(), connect.NewRequest(&daemonv1.DeleteDaemonRequest{
				DaemonId:             d.GetId(),
				SnapshotBeforeDelete: snapshot,
			})); err != nil {
				return explainMachineErr(err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Deleted %s (%s).\n", d.GetName(), d.GetId())
			return err
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the deletion")
	cmd.Flags().BoolVar(&snapshot, "snapshot", false, "Snapshot the disk before deleting the machine")
	return cmd
}

func newMachineGrowDiskCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "grow-disk <name|id> <GiB>",
		Short: "Grow a cloud machine's disk",
		Long: `Grow a cloud machine's disk to the given size in GiB (a multiple of 10, larger
than it is now). Disks only grow. A running machine uses the space after its
next restart ('reliant machine stop', then 'start'). Disk above your plan's
allowance is billed as storage, and your plan caps the size.`,
		Example: `  reliant machine grow-disk build-box 200`,
		Args:    cobra.ExactArgs(2),
		PreRunE: refuseMachineFromAgentShell,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Parsed as 32 bits, the request's width: a larger number is
			// refused, never truncated into a different, billable size.
			gib64, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(strings.ToLower(args[1])), "gib"), 10, 32)
			if err != nil || gib64 <= 0 || gib64%10 != 0 {
				return fmt.Errorf("invalid size %q: give the new size in GiB, a multiple of 10 (e.g. 200)", args[1])
			}
			gib := int32(gib64)
			client, err := machineClient(cmd.Context(), cmd)
			if err != nil {
				return err
			}
			d, err := resolveMachine(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			if _, err := client.GrowDaemonDisk(cmd.Context(), connect.NewRequest(&daemonv1.GrowDaemonDiskRequest{
				DaemonId:   d.GetId(),
				StorageGib: gib,
			})); err != nil {
				return explainMachineErr(err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s (%s) has a %d GiB disk; a running machine uses it after its next restart.\n",
				d.GetName(), d.GetId(), gib)
			return err
		},
	}
}
