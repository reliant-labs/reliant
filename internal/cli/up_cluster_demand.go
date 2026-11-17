package cli

import (
	"fmt"
	"sort"
	"strings"
)

// An env NEEDS a cluster when it asks one to run or hold something, not when
// it merely says where cluster objects would go.
//
// The scaffolded dev env is the case that forced the distinction. Every one
// of its workloads runs as a host process, yet it declares a cluster_target
// (`k3d-<project>`) and the base ingress Gateway. Those are PLACEMENT and
// SUPPORT: the cluster_target says where a cluster object lands if there is
// one, the Namespace is derived from it, and a Gateway routes nothing until
// a route attaches. `forge env up dev` used to read any of them as "this env
// deploys to a cluster", so a fresh `forge project new` failed its first
// `env up` on a missing kubectl context — and, once someone had run
// `forge cluster up` by hand, on a project image nothing would ever pull.
// The README's promise ("no cluster") was false on day 0.
//
// So the dev loop derives the cluster requirement from DEMAND: the objects
// that would do work there. When there is none, `forge env up` touches no
// cluster at all and says so; the env's support objects are applied the
// moment something is bound there (or now, with `forge env deploy`, which
// still applies everything the env declares).

// envClusterDemand returns, sorted, every reason this env needs a cluster —
// "Kind/name" for each object that would run on or be held by one. Empty
// means nothing the env declares does any work in a cluster.
//
// Two sources, deduplicated by label: the rendered stream (ClusterWork, the
// ground truth of what an apply would write), and the entity graph. The
// entity half covers what forge applies OUTSIDE the stream (helm charts,
// placed Secrets, minted kubeconfigs, k3d clusters the Bundle asks forge to
// create), and keeps the answer right for a hand-built entity set that
// carries no rendered stream.
func envClusterDemand(e *KCLEntities) []string {
	if e == nil {
		return nil
	}
	seen := map[string]bool{}
	add := func(label string) {
		seen[label] = true
	}
	for _, w := range e.WorkloadsOn(RuntimeCluster) {
		add("Workload/" + w.Name)
	}
	for _, d := range e.Databases {
		if !d.Hosted() {
			add("ManagedDatabase/" + d.Name)
		}
	}
	for _, r := range e.HTTPRoutes {
		add("HTTPRoute/" + r.Name)
	}
	for _, r := range e.GRPCRoutes {
		add("GRPCRoute/" + r.Name)
	}
	for _, h := range e.HelmCharts {
		add("HelmChart/" + h.Name)
	}
	for _, s := range e.RenderedSecrets {
		add("Secret/" + s.Name)
	}
	for _, k := range e.KubeconfigSecrets {
		add("Secret/" + k.Name)
	}
	// A cluster the Bundle asks forge to CREATE is an explicit request for
	// one, whatever runs on it.
	for _, c := range e.Clusters {
		add("Cluster/" + c.Name)
	}
	for _, w := range e.ClusterWork {
		add(w)
	}
	out := make([]string, 0, len(seen))
	for label := range seen {
		out = append(out, label)
	}
	sort.Strings(out)
	return out
}

// envPhaseRequirements is targetPhaseRequirements for an untargeted run: the
// WHOLE env's demand. The cluster phases run only when the env places work
// in a cluster; the deploy phase runs when there is anything for it to bring
// up at all — that work, or the infrastructure the host processes dial
// (forge.HostInfra, compose services).
//
// A nil entity set (the render failed) keeps the full reconcile: with no
// declaration to read, refusing to decide is safer than deciding "nothing".
func envPhaseRequirements(e *KCLEntities) upPhaseRequirements {
	if e == nil {
		return upPhaseRequirements{deploy: true, cluster: true}
	}
	out := upPhaseRequirements{cluster: len(envClusterDemand(e)) > 0}
	out.deploy = out.cluster || len(e.Infra) > 0 || len(e.WorkloadsOn(RuntimeCompose)) > 0
	return out
}

// declaredButUnneededClusters names the kubectl contexts an env declares
// (its cluster_target, and any context its rendered stream is stamped for)
// when nothing it runs needs them. Empty when the env declares no cluster,
// or when it does need one.
func declaredButUnneededClusters(e *KCLEntities) []string {
	if e == nil || len(envClusterDemand(e)) > 0 {
		return nil
	}
	seen := map[string]bool{}
	if c := e.ClusterTarget.field("cluster"); c != "" {
		seen[c] = true
	}
	for _, mc := range e.ManifestClusters {
		seen[mc.Cluster] = true
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// printClusterSkip names a declared-but-unneeded cluster on an untargeted run
// that skips the cluster, so the skip is never silent. A targeted run is
// scoped by its caller and says nothing.
func printClusterSkip(env string, e *KCLEntities, required upPhaseRequirements, targets []string) {
	if required.cluster || len(targets) > 0 {
		return
	}
	if note := clusterNotNeededNote(env, declaredButUnneededClusters(e)); note != "" {
		fmt.Println(note)
	}
}

// clusterNotNeededNote is what `forge env up` prints when it leaves a
// declared cluster alone, so skipping it is a stated decision rather than a
// silence: which cluster, why, and how to apply its objects anyway.
func clusterNotNeededNote(env string, clusters []string) string {
	if len(clusters) == 0 {
		return ""
	}
	return fmt.Sprintf("[up] cluster: nothing in env %s runs in a cluster, so %s is not needed and is left alone.\n"+
		"[up]   Its Namespace and Gateway are applied once a workload, route or chart is bound there "+
		"(or now, with `forge env deploy %s`).",
		env, strings.Join(clusters, ", "), env)
}
