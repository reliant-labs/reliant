// Package cli — `forge cluster logs` command.
//
// Streams kubectl logs for one or all services in the dev namespace.
// Replaces a small bash recipe (resolve namespace, look up the right
// label selector for a service deployment) that every project would
// otherwise hand-write.
package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

func newDevLogsCmd() *cobra.Command {
	var (
		configPath string
		service    string
		tail       int
		follow     bool
	)
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Stream kubectl logs for one or all services in the dev namespace",
		Long: `Stream kubectl logs.

Without --service, streams logs from every pod managed by forge in the
dev namespace (the same label forge uses to discover deployments).
With --service, scopes to one service deployment.

Examples:
  forge cluster logs                       # tail every forge-managed pod
  forge cluster logs --service api         # tail one service
  forge cluster logs --service api --tail 200
  forge cluster logs --no-follow           # one-shot, no streaming`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDevLogs(cmd.Context(), configPath, service, tail, follow)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", defaultK3dConfigPath, "k3d config file")
	cmd.Flags().StringVar(&service, "service", "", "Scope to one service deployment")
	cmd.Flags().IntVar(&tail, "tail", 100, "Lines of recent log file to display (-1 = all)")
	cmd.Flags().BoolVar(&follow, "follow", true, "Stream new logs (kubectl logs -f)")
	return cmd
}

func runDevLogs(ctx context.Context, configPath, service string, tail int, follow bool) error {
	clusterName, err := resolveClusterName(configPath)
	if err != nil {
		return err
	}
	ns := devNamespace(clusterName)

	args := []string{"logs", "-n", ns}
	if follow {
		args = append(args, "-f")
	}
	if tail >= 0 {
		args = append(args, fmt.Sprintf("--tail=%d", tail))
	}

	if service != "" {
		// Scope to one deployment via its pod label. forge's
		// `app.kubernetes.io/name` label carries the service name on
		// the deployment's pod template by convention.
		args = append(args, "--max-log-requests=10", "-l", "app.kubernetes.io/name="+service)
	} else {
		args = append(args, "--max-log-requests=20", "-l", "app.kubernetes.io/managed-by=forge")
	}

	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
