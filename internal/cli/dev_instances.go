// Package cli — `forge cluster instances` command.
//
// Lists every forge-managed dev namespace on every reachable k3d
// cluster. Supports the multi-worktree workflow where each worktree
// runs in its own namespace and we need a host-wide view to find what's
// running.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

func newDevInstancesCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "instances",
		Short: "List every forge dev namespace on every reachable cluster",
		Long: `List every forge-managed dev namespace on the host.

Inspects each k3d cluster's kubeconfig context and reports the
namespaces labelled app.kubernetes.io/managed-by=forge. Useful for the
multi-worktree workflow: many worktrees, each with its own namespace,
all sharing one cluster (or one per worktree).

Examples:
  forge cluster instances
  forge cluster instances --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDevInstances(cmd.Context(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit machine-readable JSON")
	return cmd
}

type devInstance struct {
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	PodCount  int    `json:"pod_count"`
}

func runDevInstances(ctx context.Context, jsonOut bool) error {
	clusters, err := listK3dClustersFn(ctx)
	if err != nil {
		return err
	}
	if len(clusters) == 0 {
		if jsonOut {
			_, _ = os.Stdout.WriteString("[]\n")
			return nil
		}
		fmt.Println("No k3d clusters found.")
		return nil
	}

	var instances []devInstance
	for _, c := range clusters {
		kubeCtx := "k3d-" + c.Name
		nsList := listForgeNamespaces(ctx, kubeCtx)
		for _, ns := range nsList {
			pc := podCount(ctx, kubeCtx, ns)
			instances = append(instances, devInstance{
				Cluster:   c.Name,
				Namespace: ns,
				PodCount:  pc,
			})
		}
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(instances)
	}

	if len(instances) == 0 {
		fmt.Println("No forge-managed dev namespaces found.")
		return nil
	}
	fmt.Printf("%-20s %-35s %s\n", "CLUSTER", "NAMESPACE", "PODS")
	for _, ins := range instances {
		fmt.Printf("%-20s %-35s %d\n", ins.Cluster, ins.Namespace, ins.PodCount)
	}
	return nil
}

// listForgeNamespaces queries a specific kubectl context for all
// forge-managed namespaces. Failures (cluster down, context missing)
// return nil so the caller can keep walking remaining clusters.
func listForgeNamespaces(ctx context.Context, kubeCtx string) []string {
	cmd := exec.CommandContext(ctx, "kubectl", "--context", kubeCtx,
		"get", "namespaces",
		"-l", "app.kubernetes.io/managed-by=forge",
		"-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var nss []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			nss = append(nss, line)
		}
	}
	return nss
}

// podCount returns the number of pods in a namespace via the given
// context. Best-effort: failures return 0.
func podCount(ctx context.Context, kubeCtx, namespace string) int {
	cmd := exec.CommandContext(ctx, "kubectl", "--context", kubeCtx, "get", "pods",
		"-n", namespace, "--no-headers",
		"-o", "name")
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}
