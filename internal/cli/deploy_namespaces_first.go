package cli

// NAMESPACES BEFORE ANYTHING IS WRITTEN INTO THEM.
//
// The cluster apply sends a release's Namespaces first (cluster.Apply's
// config-first pass), but a deploy writes into namespaces BEFORE that apply:
// the minted kubeconfig credentials (a ServiceAccount's Roles in every
// namespace it is granted), the provider-projected Secrets and the bootstrap
// Secrets. A release that introduces a namespace and grants something in it
// therefore failed in that earlier phase: control-plane's prod v1.7.89-rc.2
// minted the prerequisite observer's Roles into claimable-dns, a namespace the
// same release creates, and the deploy stopped with `namespaces
// "claimable-dns" not found` after the promotion was recorded (forge bug
// ac69bea3).
//
// So the deploy first creates every Namespace the release renders that does not
// exist yet, on its own cluster, exactly as rendered (labels and annotations:
// a pod-security label must hold from the first pod). An existing Namespace is
// left alone: the cluster apply converges it, and re-applying it here with a
// subset of the fields the apply stamps would strip them until that apply ran.

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"
)

// namespaceExistsFn and applyNamespaceFn are the two cluster calls the step
// makes, as seams for its test.
var (
	namespaceExistsFn = kubectlNamespaceExists
	applyNamespaceFn  = cluster.KubectlApply
)

func kubectlNamespaceExists(ctx context.Context, kctx, name string) (bool, error) {
	cmd := exec.CommandContext(ctx, "kubectl", cluster.KubectlArgs(kctx, "get", "namespace", name, "-o", "name", "--ignore-not-found")...)
	scrubSubprocessLogEnv(cmd)
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("read namespace %s on %s: %w", name, kctx, err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// applyMissingRenderedNamespaces creates, on each cluster, the rendered
// Namespaces that do not exist yet. Under dryRun it contacts no cluster and
// prints what a real run would check.
func applyMissingRenderedNamespaces(ctx context.Context, entities *KCLEntities, dryRun bool) error {
	if entities == nil {
		return nil
	}
	for _, ns := range entities.ManifestNamespaces {
		if ns.Cluster == deploytarget.HostedProviderID {
			continue
		}
		if dryRun {
			fmt.Printf("  [dry-run] Namespace %s on %s: created before anything is written into it, if absent\n", ns.Name, ns.Cluster)
			continue
		}
		exists, err := namespaceExistsFn(ctx, ns.Cluster, ns.Name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		doc, err := renderedNamespaceYAML(ns)
		if err != nil {
			return err
		}
		fmt.Printf("  creating Namespace %s on %s (the release creates it; something is written into it before the apply)\n", ns.Name, ns.Cluster)
		if err := applyNamespaceFn(ctx, ns.Cluster, doc); err != nil {
			return fmt.Errorf("create namespace %s on %s: %w", ns.Name, ns.Cluster, err)
		}
	}
	return nil
}

// renderedNamespaceYAML is the Namespace manifest, as rendered.
func renderedNamespaceYAML(ns RenderedNamespaceEntity) (string, error) {
	meta := map[string]any{"name": ns.Name}
	if len(ns.Labels) > 0 {
		meta["labels"] = ns.Labels
	}
	if len(ns.Annotations) > 0 {
		meta["annotations"] = ns.Annotations
	}
	b, err := yaml.Marshal(map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": meta})
	if err != nil {
		return "", fmt.Errorf("render namespace %s: %w", ns.Name, err)
	}
	return string(b), nil
}
