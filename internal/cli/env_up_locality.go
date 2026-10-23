package cli

import (
	"fmt"
	"sort"
	"strings"
)

// `forge env up` runs an environment ON THIS MACHINE. Its phases are a local
// docker build, a kubectl apply against a cluster on this host, host
// processes, and frontend dev servers. None of that is meaningful for an
// environment whose workloads live somewhere else, so an env bound to a
// non-local runtime is REFUSED here and pointed at `forge env deploy`.
//
// The refusal is the mirror image of refuseLocalEnvDeploy (see
// hosted_env_resolver.go), which stops `forge env deploy` on an env that runs
// entirely on this machine. Together they make the two verbs total: every env
// is one or the other, and whichever one you reach for names the other when
// it is the wrong one.
//
// It fires BEFORE any work. Without it, a hosted env's `env up` got as far as
// a docker build and a kubectl apply against whatever context happened to be
// current before failing on something incidental — a missing cluster, an
// unreachable registry — which reads as a broken machine rather than the
// wrong command. The locality question is answered from the env's own
// DECLARATION (its rendered runtimes and cluster targets), never from machine
// state, so the answer does not depend on what is installed or reachable.

// localityVerdict is why one env is not local: the non-local bindings found
// in its render, as "<what> (<where>)" phrases in stable order.
type localityVerdict struct {
	reasons []string
}

// local reports whether the env may be brought up on this machine.
func (v localityVerdict) local() bool { return len(v.reasons) == 0 }

// classifyEnvLocality finds every binding in a rendered env that does NOT run
// on this machine. Pure: it reads the render and nothing else.
//
// Local means host processes, docker-compose, host infra, build-only
// workloads, frontend dev servers — and a cluster that is demonstrably on
// this machine (k3d / kind / docker-desktop / minikube / rancher-desktop /
// colima / orbstack, via the same isLocalCluster the Secret-projection guard
// uses). Everything else is somewhere else:
//
//   - a `hosted` workload, database or frontend — the control plane runs it;
//   - a `cluster` workload, database or env-wide cluster target whose kubectl
//     context is not a recognized local one;
//   - a frontend bound to a bucket or Firebase — that ships to a CDN.
//
// An UNDECLARED cluster name counts as non-local, deliberately and for the
// same reason isLocalCluster treats it that way: a cluster binding forge
// cannot place must not be assumed to be the harmless case. The env says
// where it runs, so a missing name is a declaration to fix, not a default to
// guess at.
func classifyEnvLocality(e *KCLEntities) localityVerdict {
	if e == nil {
		return localityVerdict{}
	}
	seen := map[string]bool{}
	var v localityVerdict
	add := func(reason string) {
		if seen[reason] {
			return
		}
		seen[reason] = true
		v.reasons = append(v.reasons, reason)
	}

	for _, w := range e.Workloads {
		switch w.Runtime.Type {
		case RuntimeHosted:
			add(fmt.Sprintf("workload %s is bound to forge.OnHosted (the control plane runs it)", w.Name))
		case RuntimeCluster:
			if ctx := clusterContextOf(w.Runtime.Cluster); !isLocalCluster(ctx) {
				add(fmt.Sprintf("workload %s is bound to forge.OnCluster %s", w.Name, describeCluster(ctx)))
			}
		}
	}
	for _, d := range e.Databases {
		if d.Hosted() {
			add(fmt.Sprintf("database %s is hosted (the control plane runs it)", d.Name))
			continue
		}
		// A managed database that is not hosted runs in a cluster. Only its
		// own declared cluster can place it.
		if !isLocalCluster(d.Cluster) {
			add(fmt.Sprintf("database %s runs in cluster %s", d.Name, describeCluster(d.Cluster)))
		}
	}
	for _, f := range e.Frontends {
		switch {
		case frontendIsHosted(f):
			add(fmt.Sprintf("frontend %s is bound to forge.OnHosted (the control plane serves it)", f.Name))
		case f.Runtime.Ships():
			add(fmt.Sprintf("frontend %s ships to %s", f.Name, f.Runtime.Type))
		}
	}
	// The env-wide cluster target places the support resources (Namespace,
	// ConfigMaps, gateways) even when no single workload names a cluster, so
	// a remote target alone makes the env non-local.
	if t := e.ClusterTarget; t != nil && t.Cluster != "" && !isLocalCluster(t.Cluster) {
		add(fmt.Sprintf("the env's cluster_target is %s", describeCluster(t.Cluster)))
	}

	sort.Strings(v.reasons)
	return v
}

// clusterContextOf is a cluster runtime's kubectl context, "" when the
// workload declares no cluster block or no context in it.
func clusterContextOf(c *ClusterRuntime) string {
	if c == nil {
		return ""
	}
	return c.Cluster
}

// describeCluster names a cluster for the refusal, keeping an undeclared one
// legible rather than printing an empty string.
func describeCluster(name string) string {
	if strings.TrimSpace(name) == "" {
		return "(no cluster declared — forge cannot place it on this machine)"
	}
	return name
}

// refuseNonLocalEnvUp is the refusal `forge env up` returns for an env that
// does not run on this machine.
//
// The FIRST LINE is the ADR's exact contract and is depended on verbatim —
// scripts and the sibling `env deploy` refusal are written against it, so do
// not reword it. The reasons below it are what makes the message actionable:
// which binding made the env non-local, named so the author can see whether
// they reached for the wrong verb or mis-declared the env.
func refuseNonLocalEnvUp(envName string, v localityVerdict) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s is not a local environment — use 'forge env deploy %s'", envName, envName)
	if len(v.reasons) > 0 {
		sb.WriteString("\n\nwhat is not local:")
		for _, r := range v.reasons {
			fmt.Fprintf(&sb, "\n  * %s", r)
		}
	}
	sb.WriteString("\n\n`forge env up` builds, applies and runs an environment on THIS machine: " +
		"host processes, a local cluster, frontend dev servers.\nThere is nothing it can do with a " +
		"workload that runs somewhere else.")
	return fmt.Errorf("%s", sb.String())
}
