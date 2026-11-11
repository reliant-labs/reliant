package cli

import (
	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cluster"
)

func newChartLockRecoverCmd() *cobra.Command {
	var kctx, owner, chart, uid, rv, confirmation string
	cmd := &cobra.Command{
		Use: "recover-chart-lock", Args: cobra.NoArgs,
		Short: "Explicitly recover an inspected chart lock after terminating its writer",
		Long: `A chart lock never expires. Before recovery, terminate/fence the holder's
forge process AND its kubectl children on every host; ensure outstanding API
writes have completed or their credentials have been revoked. A suspended writer
must never resume after recovery. Do not recover a merely slow writer.

Inspect the ConfigMap named in the deploy error in kube-system. This command
requires its owner, chart, UID, resourceVersion and literal terminated:<holder>
confirmation. It conditionally deletes ONLY that lock, never the inventory.
There is no --force, timeout-based takeover, or automatic unlock on failure.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cluster.RecoverChartLock(cmd.Context(), kctx, owner, chart, uid, rv, confirmation)
		},
	}
	cmd.Flags().StringVar(&kctx, "context", "", "Explicit kubectl context")
	cmd.Flags().StringVar(&owner, "owner", "", "Inspected forge project.env owner")
	cmd.Flags().StringVar(&chart, "chart", "", "Inspected chart name")
	cmd.Flags().StringVar(&uid, "uid", "", "Inspected lock metadata.uid")
	cmd.Flags().StringVar(&rv, "resource-version", "", "Inspected lock metadata.resourceVersion")
	cmd.Flags().StringVar(&confirmation, "confirm-terminated", "", "Explicit terminated:<holder> attestation after fencing the old writer")
	return cmd
}
