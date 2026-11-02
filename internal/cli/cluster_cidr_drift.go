package cli

// CIDR DRIFT GUARD for `forge env up`.
//
// `forge.Cluster` can declare `cluster_cidr` / `service_cidr`, and
// k3sCIDRArgs projects them onto `k3d cluster create`. But k3s fixes both at
// CREATE time: declaring them against an already-running cluster changes
// nothing at all. Without a guard that is the worst shape of bug — an
// operator declares disjoint CIDRs to fix cross-cluster pod routing, every
// cluster comes up green, and the routing is still broken because the live
// clusters still hold k3s's defaults. They then debug the networking rather
// than the fact that their declaration never took effect.
//
// So this mirrors checkClusterPortDrift exactly: compare DECLARED against
// LIVE, treat a read failure as a warning and a no-op (it must never block
// the warm-run fast path), and make real drift a hard error that prints the
// literal recreate command. Detection and refusal only — forge never
// recreates a cluster on the operator's behalf.

import (
	"context"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
)

// liveClusterCIDRs is what the LIVE cluster actually uses, as opposed to
// what the env declares. Both fields are read from the cluster itself, never
// from a forge-side cache — the cache records what forge asked for, which is
// precisely the thing under suspicion.
type liveClusterCIDRs struct {
	// PodCIDRs are the per-node `.spec.podCIDR` slices the controller
	// manager handed out. Each is a SUBNET of the cluster CIDR (k3s's
	// default carves 10.42.0.0/24 out of 10.42.0.0/16), so the comparison
	// below is containment, not equality.
	PodCIDRs []string
	// ServiceIP is the `kubernetes` Service's ClusterIP — the first usable
	// address of the Service CIDR, and therefore inside it by construction.
	ServiceIP string
}

// liveClusterCIDRsFn is the read seam, so the drift decision is unit-testable
// without a live cluster (matching the rest of cluster_phase.go).
var liveClusterCIDRsFn = readLiveClusterCIDRs

// readLiveClusterCIDRs reads both values with kubectl, which this package
// already shells out to elsewhere (readCoreDNSNodeHosts, kubectlWaitNodesReady).
//
// Why these two sources rather than the k3s server flags on the node
// container: both are plain Kubernetes API objects, so one kubectl invocation
// each works identically on every k3d cluster regardless of how it was
// created — a cluster created from a deploy/k3d.yaml config file carries its
// CIDRs in the config, not on a command line we can scrape, and `docker
// inspect`-ing the server process's argv would read the DECLARATION forge
// passed at create time rather than what k3s actually allocated. The API
// objects are the allocation itself.
func readLiveClusterCIDRs(ctx context.Context, kctx string) (liveClusterCIDRs, error) {
	var live liveClusterCIDRs

	out, err := exec.CommandContext(ctx, "kubectl", "--context", kctx,
		"get", "nodes", "-o", `jsonpath={range .items[*]}{.spec.podCIDR}{"\n"}{end}`).CombinedOutput()
	if err != nil {
		return live, fmt.Errorf("kubectl read %s node podCIDRs: %w: %s", kctx, err, strings.TrimSpace(string(out)))
	}
	for _, line := range strings.Split(string(out), "\n") {
		if cidr := strings.TrimSpace(line); cidr != "" {
			live.PodCIDRs = append(live.PodCIDRs, cidr)
		}
	}

	out, err = exec.CommandContext(ctx, "kubectl", "--context", kctx,
		"get", "svc", "kubernetes", "-n", "default", "-o", "jsonpath={.spec.clusterIP}").CombinedOutput()
	if err != nil {
		return live, fmt.Errorf("kubectl read %s default/kubernetes ClusterIP: %w: %s", kctx, err, strings.TrimSpace(string(out)))
	}
	live.ServiceIP = strings.TrimSpace(string(out))

	return live, nil
}

// clusterCIDRDrift returns one human-readable line per declared CIDR the live
// cluster does not honour, and nil when everything declared is in effect.
//
// DECLARED-EMPTY MEANS "I DON'T CARE" and compares equal to anything: a
// cluster that does not use this capability behaves exactly as it did before
// the capability existed.
//
// Comparison is CONTAINMENT, not string equality, because the live values are
// not the declared ones even when the declaration took effect: a node's
// podCIDR is a slice of the cluster CIDR, and the `kubernetes` ClusterIP is
// one address out of the Service CIDR. A live value we cannot parse, or a
// declaration we cannot parse, yields no drift — this guard only ever reports
// what it can prove, since a false positive here refuses to start a cluster
// that is fine.
func clusterCIDRDrift(c ClusterEntity, live liveClusterCIDRs) []string {
	var drift []string

	if declared, err := netip.ParsePrefix(c.ClusterCIDR); err == nil {
		for _, raw := range live.PodCIDRs {
			livePrefix, err := netip.ParsePrefix(raw)
			if err != nil {
				continue
			}
			// A node slice inside the declared block is honoured; one
			// outside it (or wider than it) is not.
			if declared.Contains(livePrefix.Addr()) && livePrefix.Bits() >= declared.Bits() {
				continue
			}
			drift = append(drift, fmt.Sprintf(
				"cluster_cidr declares %s but a live node allocates pods from %s", c.ClusterCIDR, raw))
		}
	}

	if declared, err := netip.ParsePrefix(c.ServiceCIDR); err == nil {
		if liveIP, err := netip.ParseAddr(live.ServiceIP); err == nil && !declared.Contains(liveIP) {
			drift = append(drift, fmt.Sprintf(
				"service_cidr declares %s but the live default/kubernetes Service holds ClusterIP %s", c.ServiceCIDR, live.ServiceIP))
		}
	}

	return drift
}

// checkClusterCIDRDrift errors when the LIVE cluster does not use the pod /
// Service CIDRs the env declares. k3s fixes both at cluster-create time — no
// flag, patch or restart moves a running cluster onto a different address
// block — so the only fix is a recreate, and we say so explicitly rather than
// let the operator's disjoint-CIDR declaration sit there inert while they
// debug the routing it was supposed to fix.
//
// Declaring nothing, or failing to read the live cluster, is a silent no-op
// (a warning at most): this guard must not block the warm-run fast path.
func checkClusterCIDRDrift(ctx context.Context, c ClusterEntity, declared []ClusterEntity, env string) error {
	if c.ClusterCIDR == "" && c.ServiceCIDR == "" {
		return nil
	}
	live, err := liveClusterCIDRsFn(ctx, "k3d-"+c.Name)
	if err != nil {
		// Best-effort, exactly like checkClusterPortDrift: a read failure
		// must not turn a healthy warm run into a hard failure.
		fmt.Printf("  warning: could not verify pod/Service CIDRs for %q: %v\n", c.Name, err)
		return nil
	}
	drift := clusterCIDRDrift(c, live)
	if len(drift) == 0 {
		return nil
	}
	return fmt.Errorf(
		"cluster %q does not use the pod/Service CIDRs env %q declares:\n"+
			"    %s\n"+
			"k3s fixes both at cluster-create time, so declaring them against an already-running "+
			"cluster has no effect — the live cluster keeps the address blocks it was created with. "+
			"Recreate it to pick up the declared CIDRs:\n"+
			"    %s",
		c.Name, env, strings.Join(drift, "\n    "), recreateClusterCommand(c, declared, env))
}
