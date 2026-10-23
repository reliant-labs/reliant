// Package cli — `forge cluster info` command.
//
// Diagnostic dump of the dev-loop config: which cluster, which context,
// which namespace, which service ports. Replaces the small bash recipe
// every project would otherwise hand-write to debug "why is my
// ingress URL hitting the wrong service?"
package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

func newDevInfoCmd() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "info",
		Short: "Print static dev-loop config (cluster name, expected context, declared ports)",
		Long: `Print the static dev-loop config declared in forge.yaml + deploy/k3d.yaml.

Static means "what the project says it expects" — cluster name, expected
kubectl context, registry URL, declared service/frontend ports. It does
NOT contact the cluster or check pod state.

For dynamic state (is the cluster up? are pods running? what are the
live ingress URLs?) use ` + "`forge cluster status`" + `.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDevInfo(configPath)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", defaultK3dConfigPath, "k3d config file")
	return cmd
}

func runDevInfo(configPath string) error {
	clusterName, err := resolveClusterName(configPath)
	if err != nil {
		return err
	}
	store, err := loadProjectStore()
	if err != nil {
		return err
	}
	ns := devNamespace(clusterName)
	expectedCtx := "k3d-" + clusterName

	// The registries the dev env's images name. There is no env registry, so
	// this is a list: each workload declares its own, and a dev env that
	// re-points them onto one local registry simply reports one entry.
	var images []string
	for _, d := range declaredImageDestinationsForEnv(context.Background(), "dev") {
		images = append(images, d.repository)
	}
	declaredImages := "(no workload in deploy/kcl/dev/ declares a built image)"
	if len(images) > 0 {
		declaredImages = strings.Join(images, ", ")
	}

	fmt.Printf("Project:                    %s\n", store.Meta().Name)
	fmt.Printf("Cluster (declared):         %s\n", clusterName)
	fmt.Printf("Namespace (declared):       %s\n", ns)
	fmt.Printf("Images (declared):          %s\n", declaredImages)
	fmt.Printf("kubectl context (expected): %s\n", expectedCtx)
	fmt.Printf("k3d config:                 %s\n", configPath)
	fmt.Println()
	fmt.Println("Components:")
	// Component inventory comes from the REAL sources (proto descriptor +
	// owned worker/operator files + cmd/ binaries) — see
	// codegen.IntrospectComponents, which returns the Connect servers the
	// descriptor exposes. Every one of them mounts onto the binary's single
	// Connect mux, so in-cluster they all answer on the same port; a
	// deviation is declared per env in deploy/kcl/<env>/main.k, which this
	// static listing deliberately does not evaluate.
	projectDir := "."
	if p, ferr := findProjectConfigFile(); ferr == nil {
		projectDir = filepath.Dir(p)
	}
	printServicePorts(codegen.IntrospectComponents(projectDir))
	if len(store.Frontends()) > 0 {
		fmt.Println()
		fmt.Println("Declared frontend ports:")
		printFrontendPorts(store.Frontends())
	}
	fmt.Println()
	fmt.Println("For dynamic state (cluster up/down, pods, ingress URLs), run `forge cluster status`.")
	return nil
}

func printServicePorts(comps []config.ComponentConfig) {
	if len(comps) == 0 {
		fmt.Println("  (none)")
		return
	}
	sorted := append([]config.ComponentConfig{}, comps...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, c := range sorted {
		fmt.Printf("  %-30s serves :%d\n", c.Name, config.DefaultServePort)
	}
}

func printFrontendPorts(fes []config.FrontendConfig) {
	sorted := append([]config.FrontendConfig{}, fes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, f := range sorted {
		port := f.Port
		if port == 0 {
			fmt.Printf("  %-30s (no port declared)\n", f.Name)
			continue
		}
		fmt.Printf("  %-30s %d\n", f.Name, port)
	}
}
